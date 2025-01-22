package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"dagger.io/dagger"
	"github.com/aluzzardi/langdag/tool"
	"github.com/openai/openai-go"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <mission> <modules...>\n", os.Args[0])
		os.Exit(1)
	}
	if err := serve(context.Background(), os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, mission string, mods []string) error {
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

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}

		params := openai.ChatCompletionNewParams{
			Messages: openai.F([]openai.ChatCompletionMessageParamUnion{}),
			Tools:    openai.F(tools.Functions()),
			Seed:     openai.Int(0),
			Model:    openai.F(openai.ChatModelGPT4o),
		}

		params.Messages.Value = append(params.Messages.Value, openai.SystemMessage("You are an agent that reacts to GitHub webhooks. Your goal is to comply to the user provided mission and then process incoming webhooks and take actions according to the request."))
		params.Messages.Value = append(params.Messages.Value, openai.UserMessage(mission))

		event := r.Header.Get("X-GitHub-Event")
		payload, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}

		fmt.Fprintf(os.Stderr, "==> processing incoming %s\n", event)

		params.Messages.Value = append(params.Messages.Value, openai.UserMessage(fmt.Sprintf("incoming webhook of type %s: %s", event, payload)))

		completion, err := client.Chat.Completions.New(ctx, params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			http.Error(w, "fail", http.StatusInternalServerError)
		}
		params.Messages.Value = append(params.Messages.Value, completion.Choices[0].Message)

		for len(completion.Choices[0].Message.ToolCalls) > 0 {
			toolCalls := completion.Choices[0].Message.ToolCalls
			for _, toolCall := range toolCalls {
				fmt.Fprintf(os.Stderr, "=> invoking tool: %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)
				response, err := tools.Dispatch(ctx, toolCall.Function.Name, toolCall.Function.Arguments)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
					return
				}
				params.Messages.Value = append(params.Messages.Value, openai.ToolMessage(toolCall.ID, response))
			}

			completion, err = client.Chat.Completions.New(ctx, params)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
				return
			}
			params.Messages.Value = append(params.Messages.Value, completion.Choices[0].Message)
		}

		fmt.Printf("=> %s\n", completion.Choices[0].Message.Content)
	})

	fmt.Fprintf(os.Stderr, "\n\n\n==> Agent Started.\n")
	fmt.Fprintf(os.Stderr, "My mission is to %s\n", mission)

	fmt.Fprintf(os.Stderr, "==> Listening on :9000\n")

	return http.ListenAndServe(":9000", nil)
}
