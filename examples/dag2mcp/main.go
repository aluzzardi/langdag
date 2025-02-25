package main

import (
	"context"
	"fmt"
	"os"

	"dagger.io/dagger"
	"github.com/aluzzardi/langdag/tool"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <modules...>\n", os.Args[0])
		os.Exit(1)
	}
	if err := dag2mcp(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func dag2mcp(ctx context.Context, mods []string) error {
	dag, err := dagger.Connect(ctx)
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

	s := server.NewMCPServer(
		"Demo 🚀",
		"1.0.0",
	)
	for _, tool := range tools {
		s.AddTool(tool.ToMCP(), tool.MCPHandler)

	}

	// Start the stdio server
	return server.ServeStdio(s)
}
