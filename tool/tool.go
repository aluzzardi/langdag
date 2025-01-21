package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"dagger.io/dagger"
	"dagger.io/dagger/querybuilder"
	"github.com/Khan/genqlient/graphql"
	"github.com/openai/openai-go"
)

func Load(ctx context.Context, dag *dagger.Client, ref string) (Tools, error) {
	mod, err := loadModule(ctx, dag, ref)
	if err != nil {
		return nil, fmt.Errorf("unable to load %s: %w", err)
	}

	var o functionProvider = mod.MainObject.AsFunctionProvider()

	fns, _ := GetSupportedFunctions(o)

	tools := make(Tools, 0, len(fns))

	for _, fn := range fns {
		tools = append(tools, NewTool(mod, fn))
	}

	return tools, nil
}

type Tool struct {
	mod *moduleDef
	fn  *modFunction
}

func NewTool(mod *moduleDef, fn *modFunction) *Tool {
	return &Tool{
		mod: mod,
		fn:  fn,
	}
}

func (t *Tool) Name() string {
	return t.mod.Name + "__" + t.fn.CmdName()
}

func (t *Tool) Description() string {
	return t.mod.Description + "\n" + t.fn.Short()
}

func (t *Tool) Params() openai.ChatCompletionToolParam {
	properties := map[string]any{}
	required := []string{}
	for _, arg := range t.fn.Args {
		properties[arg.Name] = map[string]string{
			"type":        arg.TypeDef.String(),
			"description": arg.Description,
		}
		if !arg.TypeDef.Optional {
			required = append(required, arg.Name)
		}
	}
	return openai.ChatCompletionToolParam{
		Type: openai.F(openai.ChatCompletionToolTypeFunction),
		Function: openai.F(openai.FunctionDefinitionParam{
			Name:        openai.String(t.Name()),
			Description: openai.String(t.Description()),
			Parameters: openai.F(openai.FunctionParameters{
				"type":       "object",
				"properties": properties,
				"required":   required,
			}),
		}),
	}
}

func (t *Tool) Call(ctx context.Context, dag *dagger.Client, args map[string]interface{}) (string, error) {
	q := querybuilder.Query().Client(dag.GraphQLClient())

	q = q.Select(t.mod.Name).Select(t.fn.Name)

	for arg, v := range args {
		q = q.Arg(arg, v)
	}

	gql, err := q.Build(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to build query: %w", err)
	}

	fmt.Fprintf(os.Stderr, "sending query: %s\n", gql)

	var response graphql.Response

	err = dag.GraphQLClient().MakeRequest(ctx,
		&graphql.Request{
			Query: gql,
		},
		&response,
	)
	if err != nil {
		return "", err
	}

	fmt.Fprintf(os.Stderr, "RESPONSE: %+v\n", response)

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
