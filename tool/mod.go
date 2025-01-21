package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dagger.io/dagger"
	"github.com/aluzzardi/langdag/pathutil"
	"github.com/dagger/dagger/core/modules"
	"github.com/iancoleman/strcase"
)

//go:embed typedefs.graphql
var loadTypeDefsQuery string

//go:embed modconf.graphql
var loadModConfQuery string

const (
	moduleURLDefault = "."
)

func loadModule(ctx context.Context, dag *dagger.Client, mod string) (*moduleDef, error) {
	conf := &configuredModule{}

	conf.Source = dag.ModuleSource(mod)
	var err error
	conf.SourceKind, err = conf.Source.Kind(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get module ref kind: %w", err)
	}

	if conf.SourceKind != dagger.ModuleSourceKindGitSource {
		return nil, fmt.Errorf("unsupported source kind %s", conf.SourceKind)
	}

	conf.ModuleSourceConfigExists, err = conf.Source.ConfigExists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check if module config exists: %w", err)
	}

	err = conf.Source.AsModule().Initialize().Serve(ctx)
	if err != nil {
		return nil, err
	}

	def, err := inspectModule(ctx, dag, conf.Source)
	if err != nil {
		return nil, err
	}

	err = def.loadTypeDefs(ctx, dag)
	if err != nil {
		return nil, err
	}

	return def, nil
}

func inspectModule(ctx context.Context, dag *dagger.Client, source *dagger.ModuleSource) (rdef *moduleDef, rerr error) {
	// NB: All we need most of the time is the name of the dependencies.
	// We need the descriptions when listing the dependencies, and the source
	// ref if we need to load a specific dependency. However getting the refs
	// and descriptions here, at module load, doesn't add much overhead and
	// makes it easier (and faster) later.

	var res struct {
		Source struct {
			AsString string
			Module   struct {
				Name       string
				Initialize struct {
					Description string
				}
				Dependencies []struct {
					Name        string
					Description string
					Source      struct {
						AsString string
						Pin      string
					}
				}
			}
		}
	}

	id, err := source.ID(ctx)
	if err != nil {
		return nil, err
	}

	err = dag.Do(ctx, &dagger.Request{
		Query: loadModConfQuery,
		Variables: map[string]any{
			"source": id,
		},
	}, &dagger.Response{
		Data: &res,
	})
	if err != nil {
		return nil, fmt.Errorf("query module metadata: %w", err)
	}

	deps := make([]*moduleDependency, 0, len(res.Source.Module.Dependencies))
	for _, dep := range res.Source.Module.Dependencies {
		deps = append(deps, &moduleDependency{
			Name:        dep.Name,
			Description: dep.Description,
			ModRef:      dep.Source.AsString,
			RefPin:      dep.Source.Pin,
		})
	}

	def := &moduleDef{
		Source:       source,
		ModRef:       res.Source.AsString,
		Name:         res.Source.Module.Name,
		Description:  res.Source.Module.Initialize.Description,
		Dependencies: deps,
	}

	return def, nil
}

type moduleDef struct {
	Name        string
	Description string
	MainObject  *modTypeDef
	Objects     []*modTypeDef
	Interfaces  []*modTypeDef
	Enums       []*modTypeDef
	Inputs      []*modTypeDef

	// the ModuleSource definition for the module, needed by some arg types
	// applying module-specific configs to the arg value.
	Source *dagger.ModuleSource

	// ModRef is the human readable module source reference as returned by the API
	ModRef string

	Dependencies []*moduleDependency
}

func (m *moduleDef) loadTypeDefs(ctx context.Context, dag *dagger.Client) (rerr error) {
	var res struct {
		TypeDefs []*modTypeDef
	}

	err := dag.Do(ctx, &dagger.Request{
		Query: loadTypeDefsQuery,
	}, &dagger.Response{
		Data: &res,
	})
	if err != nil {
		return fmt.Errorf("query module objects: %w", err)
	}

	name := gqlObjectName(m.Name)
	if name == "" {
		name = "Query"
	}

	for _, typeDef := range res.TypeDefs {
		switch typeDef.Kind {
		case dagger.TypeDefKindObjectKind:
			obj := typeDef.AsObject
			// FIXME: we could get the real constructor's name through the field
			// in Query which would avoid the need to convert the module name,
			// but the Query TypeDef is loaded before the module so the module
			// isn't available in its functions list.
			if name == gqlObjectName(obj.Name) {
				m.MainObject = typeDef

				// There's always a constructor, even if the SDK didn't define one.
				// Make sure one always exists to make it easier to reuse code while
				// building out Cobra.
				if obj.Constructor == nil {
					obj.Constructor = &modFunction{ReturnType: typeDef}
				}

				if name != "Query" {
					// Constructors have an empty function name in ObjectTypeDef.
					obj.Constructor.Name = gqlFieldName(obj.Name)
				}
			}
			m.Objects = append(m.Objects, typeDef)
		case dagger.TypeDefKindInterfaceKind:
			m.Interfaces = append(m.Interfaces, typeDef)
		case dagger.TypeDefKindEnumKind:
			m.Enums = append(m.Enums, typeDef)
		case dagger.TypeDefKindInputKind:
			m.Inputs = append(m.Inputs, typeDef)
		}
	}

	if m.MainObject == nil {
		return fmt.Errorf("main object not found, check that your module's name and main object match")
	}

	m.LoadFunctionTypeDefs(m.MainObject.AsObject.Constructor)

	// FIXME: the API doesn't return the module constructor in the Query object
	rootObj := m.GetObject("Query")
	if !rootObj.HasFunction(m.MainObject.AsObject.Constructor) {
		rootObj.Functions = append(rootObj.Functions, m.MainObject.AsObject.Constructor)
	}

	return nil
}

// HasFunction checks if an object has a function with the given name.
func (m *moduleDef) HasFunction(fp functionProvider, name string) bool {
	if fp == nil {
		return false
	}
	fn, _ := m.GetFunction(fp, name)
	return fn != nil
}

type functionProvider interface {
	ProviderName() string
	GetFunctions() []*modFunction
	IsCore() bool
}

func (m *moduleDef) GetFunction(fp functionProvider, functionName string) (*modFunction, error) {
	// This avoids an issue with module constructors overriding core functions.
	// See https://github.com/dagger/dagger/issues/9122
	if m.HasModule() && fp.ProviderName() == "Query" && m.MainObject.AsObject.Constructor.CmdName() == functionName {
		return m.MainObject.AsObject.Constructor, nil
	}
	for _, fn := range fp.GetFunctions() {
		if fn.Name == functionName || fn.CmdName() == functionName {
			m.LoadFunctionTypeDefs(fn)
			return fn, nil
		}
	}
	return nil, fmt.Errorf("no function %q in type %q", functionName, fp.ProviderName())
}

func (f *modFunction) CmdName() string {
	if f.cmdName == "" {
		f.cmdName = cliName(f.Name)
	}
	return f.cmdName
}

// HasModule checks if a module's definitions are loaded
func (m *moduleDef) HasModule() bool {
	return m.Name != ""
}

func (m *moduleDef) LoadFunctionTypeDefs(fn *modFunction) {
	// We need to load references to types with their type definitions because
	// the introspection doesn't recursively add them, just their names.
	m.LoadTypeDef(fn.ReturnType)
	for _, arg := range fn.Args {
		m.LoadTypeDef(arg.TypeDef)
	}
}

// GetObject retrieves a saved object type definition from the module.
func (m *moduleDef) GetObject(name string) *modObject {
	for _, obj := range m.AsObjects() {
		// Normalize name in case an SDK uses a different convention for object names.
		if gqlObjectName(obj.Name) == gqlObjectName(name) {
			return obj
		}
	}
	return nil
}

// AsObjects returns the module's object type definitions.
func (m *moduleDef) AsObjects() []*modObject {
	var defs []*modObject
	for _, typeDef := range m.Objects {
		if typeDef.AsObject != nil {
			defs = append(defs, typeDef.AsObject)
		}
	}
	return defs
}

// LoadTypeDef attempts to replace a function's return object type or argument's
// object type with with one from the module's object type definitions, to
// recover missing function definitions in those places when chaining functions.
func (m *moduleDef) LoadTypeDef(typeDef *modTypeDef) {
	if typeDef.AsObject != nil && typeDef.AsObject.Functions == nil && typeDef.AsObject.Fields == nil {
		obj := m.GetObject(typeDef.AsObject.Name)
		if obj != nil {
			typeDef.AsObject = obj
		}
	}
	if typeDef.AsInterface != nil && typeDef.AsInterface.Functions == nil {
		iface := m.GetInterface(typeDef.AsInterface.Name)
		if iface != nil {
			typeDef.AsInterface = iface
		}
	}
	if typeDef.AsEnum != nil {
		enum := m.GetEnum(typeDef.AsEnum.Name)
		if enum != nil {
			typeDef.AsEnum = enum
		}
	}
	if typeDef.AsInput != nil && typeDef.AsInput.Fields == nil {
		input := m.GetInput(typeDef.AsInput.Name)
		if input != nil {
			typeDef.AsInput = input
		}
	}
	if typeDef.AsList != nil {
		m.LoadTypeDef(typeDef.AsList.ElementTypeDef)
	}
}

// GetInterface retrieves a saved interface type definition from the module.
func (m *moduleDef) GetInterface(name string) *modInterface {
	for _, iface := range m.AsInterfaces() {
		// Normalize name in case an SDK uses a different convention for interface names.
		if gqlObjectName(iface.Name) == gqlObjectName(name) {
			return iface
		}
	}
	return nil
}

func (m *moduleDef) AsInputs() []*modInput {
	var defs []*modInput
	for _, typeDef := range m.Inputs {
		if typeDef.AsInput != nil {
			defs = append(defs, typeDef.AsInput)
		}
	}
	return defs
}

// GetInput retrieves a saved input type definition from the module.
func (m *moduleDef) GetInput(name string) *modInput {
	for _, input := range m.AsInputs() {
		// Normalize name in case an SDK uses a different convention for input names.
		if gqlObjectName(input.Name) == gqlObjectName(name) {
			return input
		}
	}
	return nil
}

func (m *moduleDef) AsInterfaces() []*modInterface {
	var defs []*modInterface
	for _, typeDef := range m.Interfaces {
		if typeDef.AsInterface != nil {
			defs = append(defs, typeDef.AsInterface)
		}
	}
	return defs
}

// GetEnum retrieves a saved enum type definition from the module.
func (m *moduleDef) GetEnum(name string) *modEnum {
	for _, enum := range m.AsEnums() {
		// Normalize name in case an SDK uses a different convention for object names.
		if gqlObjectName(enum.Name) == gqlObjectName(name) {
			return enum
		}
	}
	return nil
}

func (m *moduleDef) AsEnums() []*modEnum {
	var defs []*modEnum
	for _, typeDef := range m.Enums {
		if typeDef.AsEnum != nil {
			defs = append(defs, typeDef.AsEnum)
		}
	}
	return defs
}

type moduleDependency struct {
	Name        string
	Description string
	Source      *dagger.ModuleSource

	// ModRef is the human readable module source reference as returned by the API
	ModRef string

	// RefPin is the module source pin for this dependency, if any
	RefPin string
}

func (m *moduleDependency) Short() string {
	s := m.Description
	if s == "" {
		s = "-"
	}
	return strings.SplitN(s, "\n", 2)[0]
}

// modTypeDef is a representation of dagger.TypeDef.
type modTypeDef struct {
	Kind        dagger.TypeDefKind
	Optional    bool
	AsObject    *modObject
	AsInterface *modInterface
	AsInput     *modInput
	AsList      *modList
	AsScalar    *modScalar
	AsEnum      *modEnum
}

func (t *modTypeDef) String() string {
	switch t.Kind {
	case dagger.TypeDefKindStringKind:
		return "string"
	case dagger.TypeDefKindIntegerKind:
		return "int"
	case dagger.TypeDefKindBooleanKind:
		return "bool"
	case dagger.TypeDefKindVoidKind:
		return "void"
	case dagger.TypeDefKindScalarKind:
		return t.AsScalar.Name
	case dagger.TypeDefKindEnumKind:
		return t.AsEnum.Name
	case dagger.TypeDefKindInputKind:
		return t.AsInput.Name
	case dagger.TypeDefKindObjectKind:
		return t.AsObject.Name
	case dagger.TypeDefKindInterfaceKind:
		return t.AsInterface.Name
	case dagger.TypeDefKindListKind:
		return "[]" + t.AsList.ElementTypeDef.String()
	default:
		// this should never happen because all values for kind are covered,
		// unless a new one is added and this code isn't updated
		return ""
	}
}

func (t *modTypeDef) Name() string {
	if fp := t.AsFunctionProvider(); fp != nil {
		return fp.ProviderName()
	}
	return ""
}

func (t *modTypeDef) AsFunctionProvider() functionProvider {
	if t.AsList != nil {
		t = t.AsList.ElementTypeDef
	}
	if t.AsObject != nil {
		return t.AsObject
	}
	if t.AsInterface != nil {
		return t.AsInterface
	}
	return nil
}

func (t *modTypeDef) KindDisplay() string {
	switch t.Kind {
	case dagger.TypeDefKindStringKind,
		dagger.TypeDefKindIntegerKind,
		dagger.TypeDefKindBooleanKind:
		return "Scalar"
	case dagger.TypeDefKindScalarKind,
		dagger.TypeDefKindVoidKind:
		return "Custom scalar"
	case dagger.TypeDefKindEnumKind:
		return "Enum"
	case dagger.TypeDefKindInputKind:
		return "Input"
	case dagger.TypeDefKindObjectKind:
		return "Object"
	case dagger.TypeDefKindInterfaceKind:
		return "Interface"
	case dagger.TypeDefKindListKind:
		return "List of " + strings.ToLower(t.AsList.ElementTypeDef.KindDisplay()) + "s"
	default:
		return ""
	}
}

func (t *modTypeDef) Description() string {
	switch t.Kind {
	case dagger.TypeDefKindStringKind,
		dagger.TypeDefKindIntegerKind,
		dagger.TypeDefKindBooleanKind:
		return "Primitive type."
	case dagger.TypeDefKindVoidKind:
		return ""
	case dagger.TypeDefKindScalarKind:
		return t.AsScalar.Description
	case dagger.TypeDefKindEnumKind:
		return t.AsEnum.Description
	case dagger.TypeDefKindInputKind:
		return t.AsInput.Description
	case dagger.TypeDefKindObjectKind:
		return t.AsObject.Description
	case dagger.TypeDefKindInterfaceKind:
		return t.AsInterface.Description
	case dagger.TypeDefKindListKind:
		return t.AsList.ElementTypeDef.Description()
	default:
		// this should never happen because all values for kind are covered,
		// unless a new one is added and this code isn't updated
		return ""
	}
}

func (t *modTypeDef) Short() string {
	s := t.String()
	if d := t.Description(); d != "" {
		return s + " - " + strings.SplitN(d, "\n", 2)[0]
	}
	return s
}

func (t *modTypeDef) Long() string {
	s := t.String()
	if d := t.Description(); d != "" {
		return s + "\n\n" + d
	}
	return s
}

// modObject is a representation of dagger.ObjectTypeDef.
type modObject struct {
	Name             string
	Description      string
	Functions        []*modFunction
	Fields           []*modField
	Constructor      *modFunction
	SourceModuleName string
}

func (o *modObject) ProviderName() string {
	return o.Name
}

func (o *modObject) IsCore() bool {
	return o.SourceModuleName == ""
}

// GetFunctions returns the object's function definitions including the fields,
// which are treated as functions with no arguments.
func (o *modObject) GetFunctions() []*modFunction {
	return append(o.GetFieldFunctions(), o.Functions...)
}

func (o *modObject) GetFieldFunctions() []*modFunction {
	fns := make([]*modFunction, 0, len(o.Fields))
	for _, f := range o.Fields {
		fns = append(fns, f.AsFunction())
	}
	return fns
}

func (o *modObject) HasFunction(f *modFunction) bool {
	for _, fn := range o.Functions {
		if fn.Name == f.Name {
			return true
		}
	}
	return false
}

type modInterface struct {
	Name             string
	Description      string
	Functions        []*modFunction
	SourceModuleName string
}

var _ functionProvider = (*modInterface)(nil)

func (o *modInterface) ProviderName() string {
	return o.Name
}

func (o *modInterface) IsCore() bool {
	return o.SourceModuleName == ""
}

func (o *modInterface) GetFunctions() []*modFunction {
	return o.Functions
}

type modEnumValue struct {
	Name        string
	Description string
}

type modInput struct {
	Name        string
	Description string
	Fields      []*modField
}

// modFunction is a representation of dagger.Function.
type modFunction struct {
	Name        string
	Description string
	ReturnType  *modTypeDef
	Args        []*modFunctionArg
	cmdName     string
}

func (f *modFunction) Short() string {
	s := strings.SplitN(f.Description, "\n", 2)[0]
	if s == "" {
		s = "-"
	}
	return s
}

// GetArg returns the argument definition corresponding to the given name.
func (f *modFunction) GetArg(name string) (*modFunctionArg, error) {
	for _, a := range f.Args {
		if a.FlagName() == name {
			return a, nil
		}
	}
	return nil, fmt.Errorf("no argument %q in function %q", name, f.CmdName())
}

func (f *modFunction) HasRequiredArgs() bool {
	for _, arg := range f.Args {
		if arg.IsRequired() {
			return true
		}
	}
	return false
}

func (f *modFunction) RequiredArgs() []*modFunctionArg {
	args := make([]*modFunctionArg, 0, len(f.Args))
	for _, arg := range f.Args {
		if arg.IsRequired() {
			args = append(args, arg)
		}
	}
	return args
}

func (f *modFunction) OptionalArgs() []*modFunctionArg {
	args := make([]*modFunctionArg, 0, len(f.Args))
	for _, arg := range f.Args {
		if !arg.IsRequired() {
			args = append(args, arg)
		}
	}
	return args
}

func (f *modFunction) SupportedArgs() []*modFunctionArg {
	args := make([]*modFunctionArg, 0, len(f.Args))
	for _, arg := range f.Args {
		if !arg.IsUnsupportedFlag() {
			args = append(args, arg)
		}
	}
	return args
}

func (f *modFunction) HasUnsupportedFlags() bool {
	for _, arg := range f.Args {
		if arg.IsRequired() && arg.IsUnsupportedFlag() {
			return true
		}
	}
	return false
}

// modFunctionArg is a representation of dagger.FunctionArg.
type modFunctionArg struct {
	Name         string
	Description  string
	TypeDef      *modTypeDef
	DefaultValue dagger.JSON
	DefaultPath  string
	Ignore       []string
	flagName     string
}

// FlagName returns the name of the argument using CLI naming conventions.
func (r *modFunctionArg) FlagName() string {
	if r.flagName == "" {
		r.flagName = cliName(r.Name)
	}
	return r.flagName
}

func (r *modFunctionArg) Usage() string {
	return fmt.Sprintf("--%s %s", r.FlagName(), r.TypeDef.String())
}

func (r *modFunctionArg) Short() string {
	return strings.SplitN(r.Description, "\n", 2)[0]
}

func (r *modFunctionArg) Long() string {
	sb := new(strings.Builder)
	multiline := strings.Contains(r.Description, "\n")

	if r.Description != "" {
		sb.WriteString(r.Description)
	}

	if defVal := r.defValue(); defVal != "" {
		if multiline {
			sb.WriteString("\n\n")
		} else if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(fmt.Sprintf("(default: %s)", defVal))
	}

	if r.TypeDef.Kind == dagger.TypeDefKindEnumKind {
		names := strings.Join(r.TypeDef.AsEnum.ValueNames(), ", ")
		if multiline {
			sb.WriteString("\n\n")
		} else if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(fmt.Sprintf("(possible values: %s)", names))
	}

	return sb.String()
}

func (r *modFunctionArg) IsRequired() bool {
	return !r.TypeDef.Optional && r.DefaultValue == ""
}

func (r *modFunctionArg) IsUnsupportedFlag() bool {
	return false
	// flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	// err := r.AddFlag(flags)
	// var e *UnsupportedFlagError
	// return errors.As(err, &e)
}

func getDefaultValue[T any](r *modFunctionArg) (T, error) {
	var val T
	err := json.Unmarshal([]byte(r.DefaultValue), &val)
	return val, err
}

// DefValue is the default value (as text); for the usage message
func (r *modFunctionArg) defValue() string {
	if r.DefaultPath != "" {
		return fmt.Sprintf("%q", r.DefaultPath)
	}
	if r.DefaultValue == "" {
		return ""
	}
	t := r.TypeDef
	switch t.Kind {
	case dagger.TypeDefKindStringKind:
		v, err := getDefaultValue[string](r)
		if err == nil {
			return fmt.Sprintf("%q", v)
		}
	default:
		v, err := getDefaultValue[any](r)
		if err == nil {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// modList is a representation of dagger.ListTypeDef.
type modList struct {
	ElementTypeDef *modTypeDef
}

// modField is a representation of dagger.FieldTypeDef.
type modField struct {
	Name        string
	Description string
	TypeDef     *modTypeDef
}

func (f *modField) AsFunction() *modFunction {
	return &modFunction{
		Name:        f.Name,
		Description: f.Description,
		ReturnType:  f.TypeDef,
	}
}

type modScalar struct {
	Name        string
	Description string
}

type modEnum struct {
	Name        string
	Description string
	Values      []*modEnumValue
}

func (e *modEnum) ValueNames() []string {
	values := make([]string, 0, len(e.Values))
	for _, v := range e.Values {
		values = append(values, v.Name)
	}
	return values
}

// cliName converts casing to the CLI convention (kebab)
func cliName(name string) string {
	return strcase.ToKebab(name)
}

// gqlObjectName converts casing to a GraphQL object  name
func gqlObjectName(name string) string {
	return strcase.ToCamel(name)
}

// gqlFieldName converts casing to a GraphQL object field name
func gqlFieldName(name string) string {
	return strcase.ToLowerCamel(name)
}

func getModuleConfigurationForSourceRef(
	ctx context.Context,
	dag *dagger.Client,
	srcRefStr string,
	doFindUp bool,
	resolveFromCaller bool,
	srcOpts ...dagger.ModuleSourceOpts,
) (*configuredModule, error) {
	conf := &configuredModule{}

	conf.Source = dag.ModuleSource(srcRefStr, srcOpts...)
	var err error
	conf.SourceKind, err = conf.Source.Kind(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get module ref kind: %w", err)
	}

	if conf.SourceKind == dagger.ModuleSourceKindGitSource {
		conf.ModuleSourceConfigExists, err = conf.Source.ConfigExists(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to check if module config exists: %w", err)
		}
		return conf, nil
	}

	if doFindUp {
		// need to check if this is a named module from the *default* dagger.json found-up from the cwd
		defaultFindupConfigDir, defaultFindupExists, err := findUp(moduleURLDefault)
		if err != nil {
			return nil, fmt.Errorf("error trying to find default config path for: %w", err)
		}
		if defaultFindupExists {
			configPath := filepath.Join(defaultFindupConfigDir, modules.Filename)
			contents, err := os.ReadFile(configPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read %s: %w", configPath, err)
			}
			var modCfg modules.ModuleConfig
			if err := json.Unmarshal(contents, &modCfg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal %s: %w", configPath, err)
			}

			namedDep, ok := modCfg.DependencyByName(srcRefStr)
			if ok {
				opts := dagger.ModuleSourceOpts{RefPin: namedDep.Pin}
				depSrc := dag.ModuleSource(namedDep.Source, opts)
				depKind, err := depSrc.Kind(ctx)
				if err != nil {
					return nil, err
				}
				depSrcRef := namedDep.Source
				if depKind == dagger.ModuleSourceKindLocalSource {
					depSrcRef = filepath.Join(defaultFindupConfigDir, namedDep.Source)
				}
				return getModuleConfigurationForSourceRef(ctx, dag, depSrcRef, false, resolveFromCaller, opts)
			}
		}

		findupConfigDir, findupExists, err := findUp(srcRefStr)
		if err != nil {
			return nil, fmt.Errorf("error trying to find config path for %s: %w", srcRefStr, err)
		}
		if !findupExists {
			return nil, fmt.Errorf("no %s found in directory %s or any parents up to git root", modules.Filename, srcRefStr)
		}
		srcRefStr = findupConfigDir
	}

	conf.LocalRootSourcePath, err = pathutil.Abs(srcRefStr)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for %s: %w", srcRefStr, err)
	}
	if filepath.IsAbs(srcRefStr) {
		cwd, err := pathutil.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current working directory: %w", err)
		}
		srcRefStr, err = filepath.Rel(cwd, srcRefStr)
		if err != nil {
			return nil, fmt.Errorf("failed to get relative path for %s: %w", srcRefStr, err)
		}
	}
	if err := os.MkdirAll(srcRefStr, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory for %s: %w", srcRefStr, err)
	}

	conf.Source = dag.ModuleSource(srcRefStr)

	conf.LocalContextPath, err = conf.Source.ResolveContextPathFromCaller(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get local root path: %w", err)
	}
	_, err = os.Lstat(filepath.Join(conf.LocalRootSourcePath, modules.Filename))
	conf.ModuleSourceConfigExists = err == nil

	if resolveFromCaller {
		conf.Source = conf.Source.ResolveFromCaller()
	}

	return conf, nil
}

// FIXME: huge refactor needed to remove this function - it shares a lot of
// similarity with the engine-side callerHostFindUpContext, and it would be a
// big simplification
func findUp(curDirPath string) (string, bool, error) {
	_, err := os.Lstat(curDirPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to lstat %s: %w", curDirPath, err)
	}

	configPath := filepath.Join(curDirPath, modules.Filename)
	stat, err := os.Lstat(configPath)
	switch {
	case os.IsNotExist(err):

	case err == nil:
		// make sure it's a file
		if !stat.Mode().IsRegular() {
			return "", false, fmt.Errorf("expected %s to be a file", configPath)
		}
		return curDirPath, true, nil

	default:
		return "", false, fmt.Errorf("failed to lstat %s: %w", configPath, err)
	}

	// didn't exist, try parent unless we've hit the root or a git repo checkout root
	curDirAbsPath, err := pathutil.Abs(curDirPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to get absolute path for %s: %w", curDirPath, err)
	}
	if curDirAbsPath[len(curDirAbsPath)-1] == os.PathSeparator {
		// path ends in separator, we're at root
		return "", false, nil
	}

	_, err = os.Lstat(filepath.Join(curDirPath, ".git"))
	if err == nil {
		return "", false, nil
	}

	parentDirPath := filepath.Join(curDirPath, "..")
	return findUp(parentDirPath)
}

type configuredModule struct {
	Source     *dagger.ModuleSource
	SourceKind dagger.ModuleSourceKind

	LocalContextPath    string
	LocalRootSourcePath string

	// whether the dagger.json in the module source dir exists yet
	ModuleSourceConfigExists bool
}

func (c *configuredModule) FullyInitialized() bool {
	return c.ModuleSourceConfigExists
}

func GetSupportedFunctions(fp functionProvider) ([]*modFunction, []string) {
	allFns := fp.GetFunctions()
	fns := make([]*modFunction, 0, len(allFns))
	skipped := make([]string, 0, len(allFns))
	for _, fn := range allFns {
		if skipFunction(fp.ProviderName(), fn.Name) || fn.HasUnsupportedFlags() {
			skipped = append(skipped, fn.CmdName())
		} else {
			fns = append(fns, fn)
		}
	}
	return fns, skipped
}

func skipFunction(obj, field string) bool {
	// TODO: make this configurable in the API but may not be easy to
	// generalize because an "internal" field may still need to exist in
	// codegen, for example. Could expose if internal via the TypeDefs though.
	skip := map[string][]string{
		"Query": {
			// for SDKs only
			"builtinContainer",
			"generatedCode",
			"currentFunctionCall",
			"currentModule",
			"typeDef",
			// not useful until the CLI accepts ID inputs
			"cacheVolume",
			"setSecret",
			// for tests only
			"secret",
			// deprecated
			"pipeline",
		},
	}
	if fields, ok := skip[obj]; ok {
		return slices.Contains(fields, field)
	}
	return false
}
