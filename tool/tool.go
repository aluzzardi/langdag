package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"dagger.io/dagger"
	"dagger.io/dagger/querybuilder"
	"github.com/Khan/genqlient/graphql"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openai/openai-go"
)

func Load(ctx context.Context, dag *dagger.Client, ref string, args map[string]any) (Tools, error) {
	mod, err := initializeModule(ctx, dag, ref, false)
	if err != nil {
		return nil, fmt.Errorf("unable to load %s: %w", ref, err)
	}

	var o functionProvider = mod.MainObject.AsFunctionProvider()

	fns, _ := GetSupportedFunctions(o)

	tools := make(Tools, 0, len(fns))

	for _, fn := range fns {
		tools = append(tools, NewTool(dag, mod, fn, args))
	}

	return tools, nil
}

func LoadAll(ctx context.Context, dag *dagger.Client, refs []string) (Tools, error) {
	tools := Tools{}
	for _, ref := range refs {
		t, err := Load(ctx, dag, ref, nil)
		if err != nil {
			return nil, err
		}
		tools = append(tools, t...)
	}

	return tools, nil
}

type Tool struct {
	dag  *dagger.Client
	mod  *moduleDef
	fn   *modFunction
	args map[string]any
}

func NewTool(dag *dagger.Client, mod *moduleDef, fn *modFunction, args map[string]any) *Tool {
	if args == nil {
		args = make(map[string]any)
	}
	return &Tool{
		dag:  dag,
		mod:  mod,
		fn:   fn,
		args: args,
	}
}

func (t *Tool) Name() string {
	return t.mod.Name + "_" + t.fn.CmdName()
}

func (t *Tool) Description() string {
	return t.mod.Description + "\n" + t.fn.Short()
}

func (t *Tool) Params() openai.ChatCompletionToolParam {
	properties := map[string]any{}
	required := []string{}
	for _, arg := range t.fn.Args {
		props := map[string]any{
			"type":        arg.TypeDef.String(),
			"description": arg.Description,
		}
		switch arg.TypeDef.Kind {
		case dagger.TypeDefKindStringKind:
			props["type"] = "string"
		case dagger.TypeDefKindIntegerKind:
			props["type"] = "integer"
		case dagger.TypeDefKindBooleanKind:
			props["type"] = "boolean"
		case dagger.TypeDefKindVoidKind:
			props["type"] = "null"
		// case dagger.TypeDefKindScalarKind:
		// 	return t.AsScalar.Name
		// case dagger.TypeDefKindEnumKind:
		// 	return t.AsEnum.Name
		// case dagger.TypeDefKindInputKind:
		// 	return t.AsInput.Name
		// case dagger.TypeDefKindObjectKind:
		// 	return t.AsObject.Name
		// case dagger.TypeDefKindInterfaceKind:
		// 	return t.AsInterface.Name
		case dagger.TypeDefKindListKind:
			props["type"] = "array"
			props["items"] = map[string]string{
				"type": arg.TypeDef.AsList.ElementTypeDef.String(),
			}
		default:
			panic(fmt.Sprintf("unsupported type: %s", arg.TypeDef.Kind))
		}

		properties[arg.Name] = props

		if !arg.TypeDef.Optional {
			required = append(required, arg.Name)
		}
	}
	return openai.ChatCompletionToolParam{
		Type: openai.F(openai.ChatCompletionToolTypeFunction),
		Function: openai.F(openai.FunctionDefinitionParam{
			Name:        openai.String(t.Name()),
			Description: openai.String(t.Description()),
			// Strict:      openai.Bool(true),
			Parameters: openai.F(openai.FunctionParameters{
				"type":       "object",
				"properties": properties,
				"required":   required,
				// "additionalProperties": false,
			}),
		}),
	}
}

func (t *Tool) InitFromEnv() error {
	ctor := t.mod.MainObject.AsObject.Constructor

	for _, arg := range ctor.Args {
		k := strings.ToUpper(t.mod.Name) + "_" + strings.ToUpper(arg.Name)
		fmt.Fprintf(os.Stderr, "Loading option %s::%s from %s\n", t.mod.Name, arg.Name, k)
		v := os.Getenv(k)
		if v == "" {
			return fmt.Errorf("%q not set", k)
		}

		switch arg.TypeDef.Kind {
		case dagger.TypeDefKindStringKind:
			t.args[arg.Name] = v
		case dagger.TypeDefKindObjectKind:
			if arg.TypeDef.AsObject.Name != "Secret" {
				return fmt.Errorf("unsupported type: %s", arg.TypeDef.AsObject.Name)
			}

			t.args[arg.Name] = t.dag.SetSecret(k, v)
		// case dagger.TypeDefKindIntegerKind:
		// case dagger.TypeDefKindBooleanKind:
		// case dagger.TypeDefKindVoidKind:
		// case dagger.TypeDefKindListKind:
		default:
			panic(fmt.Sprintf("unsupported type: %s", arg.TypeDef.Kind))
		}
	}

	return nil
}

func (t *Tool) ToMCP() mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription(t.Description()),
	}
	for _, arg := range t.fn.Args {
		props := []mcp.PropertyOption{
			mcp.Description(arg.Description),
		}

		if !arg.TypeDef.Optional {
			props = append(props, mcp.Required())
		}

		switch arg.TypeDef.Kind {
		case dagger.TypeDefKindStringKind:
			opts = append(opts, mcp.WithString(arg.Name, props...))
		case dagger.TypeDefKindIntegerKind:
			opts = append(opts, mcp.WithNumber(arg.Name, props...))
		case dagger.TypeDefKindBooleanKind:
			opts = append(opts, mcp.WithBoolean(arg.Name, props...))
		// case dagger.TypeDefKindVoidKind:
		// 	props["type"] = "null"
		// case dagger.TypeDefKindScalarKind:
		// 	return t.AsScalar.Name
		// case dagger.TypeDefKindEnumKind:
		// 	return t.AsEnum.Name
		// case dagger.TypeDefKindInputKind:
		// 	return t.AsInput.Name
		// case dagger.TypeDefKindObjectKind:
		// 	return t.AsObject.Name
		// case dagger.TypeDefKindInterfaceKind:
		// 	return t.AsInterface.Name
		case dagger.TypeDefKindListKind:
			// props["type"] = "array"
			// props["items"] = map[string]string{
			// 	"type": arg.TypeDef.AsList.ElementTypeDef.String(),
			// }
		default:
			panic(fmt.Sprintf("unsupported type: %s", arg.TypeDef.Kind))
		}
	}

	tool := mcp.NewTool(t.Name(), opts...)

	return tool
}

func (t *Tool) MCPHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q := querybuilder.Query().Client(t.dag.GraphQLClient())

	// Select module
	q = q.Select(t.mod.Name)
	// Bind top-level args
	for k, v := range t.args {
		q = q.Arg(k, v)
	}

	// Select function
	q = q.Select(t.fn.Name)
	// Extract the location from the function call arguments
	for arg, v := range request.Params.Arguments {
		q = q.Arg(arg, v)
	}

	gql, err := q.Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	fmt.Fprintf(os.Stderr, "sending query: %s\n", gql)

	var response graphql.Response

	err = t.dag.GraphQLClient().MakeRequest(ctx,
		&graphql.Request{
			Query: gql,
		},
		&response,
	)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(response.Data)
	if err != nil {
		return nil, err
	}

	return mcp.NewToolResultText(string(data)), err
}

func (t *Tool) Call(ctx context.Context, arguments string) (string, error) {
	q := querybuilder.Query().Client(t.dag.GraphQLClient())

	// Select module
	q = q.Select(t.mod.Name)
	// Bind top-level args
	for k, v := range t.args {
		q = q.Arg(k, v)
	}

	// Select function
	q = q.Select(t.fn.Name)
	// Extract the location from the function call arguments
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		panic(err)
	}
	for arg, v := range args {
		q = q.Arg(arg, v)
	}

	gql, err := q.Build(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to build query: %w", err)
	}

	fmt.Fprintf(os.Stderr, "sending query: %s\n", gql)

	var response graphql.Response

	err = t.dag.GraphQLClient().MakeRequest(ctx,
		&graphql.Request{
			Query: gql,
		},
		&response,
	)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(response.Data)
	if err != nil {
		return "", err
	}

	return string(data), err
}

type Tools []*Tool

func (t Tools) Functions() []openai.ChatCompletionToolParam {
	params := make([]openai.ChatCompletionToolParam, 0, len(t))
	for _, tool := range t {
		params = append(params, tool.Params())
	}
	return params
}

func (t Tools) Get(name string) *Tool {
	for _, tool := range t {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}

func (t Tools) Dispatch(ctx context.Context, name, arguments string) (string, error) {
	tool := t.Get(name)
	if tool == nil {
		return "", fmt.Errorf("tool not found: %s", name)
	}
	return tool.Call(ctx, arguments)
}

func (t Tools) InitFromEnv() error {
	for _, tool := range t {
		if err := tool.InitFromEnv(); err != nil {
			return err
		}
	}
	return nil
}
