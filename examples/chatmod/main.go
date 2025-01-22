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
		fmt.Fprintf(os.Stderr, "usage: %s <modules...>\n", os.Args[0])
		os.Exit(1)
	}
	if err := chat(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func chat(ctx context.Context, mods []string) error {
	dag, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer dag.Close()

	tools, err := tool.LoadAll(ctx, dag, mods)
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
		if question == "" {
			continue
		}
		if question == "exit" {
			break
		}

		history = append(history, question)
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
