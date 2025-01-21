package main

import (
	"context"
	"fmt"
	"os"

	"dagger.io/dagger"
	"github.com/aluzzardi/langdag/tool"
	"github.com/openai/openai-go"
)

func run(ctx context.Context) error {
	dag, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}

	tools, err := tool.Load(ctx, dag, "github.com/aluzzardi/daggerverse/trufflehog")
	if err != nil {
		return err
	}

	for _, t := range tools {
		fmt.Fprintf(os.Stderr, "Tool %s: %s\n", t.Name(), t.Description())
	}

	client := openai.NewClient()

	question := "Are there any leaked secrets in https://github.com/trufflesecurity/test_keys?"

	print("> ")
	println(question)

	params := openai.ChatCompletionNewParams{
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(question),
		}),
		Tools: openai.F(tools.Functions()),
		Seed:  openai.Int(0),
		Model: openai.F(openai.ChatModelGPT4o),
	}

	// Make initial chat completion request
	completion, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		panic(err)
	}

	toolCalls := completion.Choices[0].Message.ToolCalls

	// Abort early if there are no tool calls
	if len(toolCalls) == 0 {
		return fmt.Errorf("no function call")
	}

	// If there is a was a function call, continue the conversation
	params.Messages.Value = append(params.Messages.Value, completion.Choices[0].Message)
	for _, toolCall := range toolCalls {
		fmt.Fprintf(os.Stderr, "invoking tool: %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)
		response, err := tools.Dispatch(ctx, toolCall.Function.Name, toolCall.Function.Arguments)
		if err != nil {
			return err
		}
		params.Messages.Value = append(params.Messages.Value, openai.ToolMessage(toolCall.ID, response))
	}

	completion, err = client.Chat.Completions.New(ctx, params)
	if err != nil {
		return err
	}

	println(completion.Choices[0].Message.Content)
	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		panic(err)
	}
}
