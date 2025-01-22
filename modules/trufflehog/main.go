// Scan for secrets in a directory using Trufflehog
package main

import (
	"context"
	"dagger/trufflehog/internal/dagger"
)

// Scan for secrets in a directory using Trufflehog
type Trufflehog struct {
}

func (m *Trufflehog) base() *dagger.Container {
	return dag.Container().
		From("trufflesecurity/trufflehog")
}

// Find credentials in git repositories. Returns the JSON output of the scan
func (m *Trufflehog) Git(
	ctx context.Context,
	// Git repository URL. https:// or ssh:// schema expected
	uri string,

	// Commit (or branch) to start scan from
	// +optional
	sinceCommit string,

	// Branch to scan
	// +optional
	branch string,
) (string, error) {
	args := []string{"trufflehog", "--json", "git", uri}
	if sinceCommit != "" {
		args = append(args, "--since-commit", sinceCommit)
	}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	return m.base().WithExec(args).Stdout(ctx)
}
