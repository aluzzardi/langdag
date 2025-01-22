// GitHub Toolkit
package main

import (
	"context"
)

// Add a comment to a GitHub pull request.
func (m *Github) PullRequestComment(
	ctx context.Context,
	// Select a repository using the [HOST/]OWNER/REPO format
	repo string,
	// The PR number, URL or branch
	pr string,

	// The comment body text
	body string,
) (string, error) {
	args := []string{
		"gh", "pr", "comment",
		"-R", repo,
		pr,
		"--body", body,
	}

	return m.base().WithExec(args).Stdout(ctx)
}

// Close a pull request
func (m *Github) PullRequestClose(
	ctx context.Context,
	// Select a repository using the [HOST/]OWNER/REPO format
	repo string,
	// The PR number, URL or branch
	pr string,

	// Leave a closing comment
	comment string,

	// Delete the local and remote branch after close (true or false)
	// +optional
	deleteBranch string,
) (string, error) {
	args := []string{
		"gh", "pr", "close",
		"-R", repo,
		pr,
	}

	if comment != "" {
		args = append(args, "--comment", comment)
	}

	if deleteBranch == "true" {
		args = append(args, "--delete-branch")
	}

	return m.base().WithExec(args).Stdout(ctx)
}
