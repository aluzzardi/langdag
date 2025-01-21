package main

import (
	"context"
	"fmt"
	"os"

	"dagger.io/dagger"
	"github.com/aluzzardi/langdag/tool"
	prompt "github.com/c-bata/go-prompt"
	"github.com/openai/openai-go"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <module>\n", os.Args[0])
		os.Exit(1)
	}
	mod := os.Args[1]
	if err := chat(context.Background(), mod); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func chat(ctx context.Context, mod string) error {
	dag, err := dagger.Connect(ctx) //, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer dag.Close()

	tools, err := tool.Load(ctx, dag, mod, nil)
	if err != nil {
		return err
	}
	if err := tools.InitFromEnv(); err != nil {
		return err
	}

	client := openai.NewClient()

	params := openai.ChatCompletionNewParams{
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{}),
		Tools:    openai.F(tools.Functions()),
		Seed:     openai.Int(0),
		Model:    openai.F(openai.ChatModelGPT4o),
	}

	history := []string{}
	for {
		question := prompt.Input("> ", func(in prompt.Document) []prompt.Suggest {
			return []prompt.Suggest{}
		}, prompt.OptionHistory(history))
		history = append(history, question)
		if question == "exit" {
			break
		}
		fmt.Fprintf(os.Stderr, "\n")

		params.Messages.Value = append(params.Messages.Value, openai.UserMessage(question))

		// Make initial chat completion request
		completion, err := client.Chat.Completions.New(ctx, params)
		if err != nil {
			return err
		}

		params.Messages.Value = append(params.Messages.Value, completion.Choices[0].Message)

		for len(completion.Choices[0].Message.ToolCalls) > 0 {
			toolCalls := completion.Choices[0].Message.ToolCalls
			for _, toolCall := range toolCalls {
				fmt.Fprintf(os.Stderr, "=> invoking tool: %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)
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
			params.Messages.Value = append(params.Messages.Value, completion.Choices[0].Message)
		}

		fmt.Printf("%s\n", completion.Choices[0].Message.Content)
	}

	return nil
}
