// GitHub Toolkit
package main

import (
	"context"
	"dagger/github/internal/dagger"
	"strings"
)

type Github struct {
	// +private
	Token *dagger.Secret
}

func New(
	token *dagger.Secret,
) *Github {
	return &Github{
		Token: token,
	}
}

func (m *Github) base() *dagger.Container {
	return dag.Container().
		From("debian").
		WithExec([]string{"sh", "-c",
			`
		(type -p wget >/dev/null || (apt update && apt-get install wget -y)) \
	&& mkdir -p -m 755 /etc/apt/keyrings \
        && out=$(mktemp) && wget -nv -O$out https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        && cat $out | tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
	&& chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
	&& echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
	&& apt update \
	&& apt install gh -y
	`,
		}).
		WithSecretVariable("GITHUB_TOKEN", m.Token)
}

// Create an issue on GitHub.
func (m *Github) IssueCreate(
	ctx context.Context,
	// Select another repository using the [HOST/]OWNER/REPO format
	repo string,

	// Assign people by their login. Use "@me" to self-assign.
	// +optional
	assignee string,

	// Supply a title.
	// +optional
	title string,

	// Supply a body.
	// +optional
	body string,

	// Add labels by name
	// +optional
	// label []string,

	// Add the issue to a milestone by name
	// +optional
	// milestone string,
) (string, error) {
	args := []string{"gh", "issue", "create", "-R", repo}

	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}
	if title != "" {
		args = append(args, "--title", title)
	}
	if body != "" {
		args = append(args, "--body", body)
	}

	// if len(label) > 0 {
	// 	args = append(args, "--label", strings.Join(label, ","))
	// }

	// if milestone != "" {
	// 	args = append(args, "--milestone", milestone)
	// }

	return m.base().WithExec(args).Stdout(ctx)
}

// List issues in a GitHub repository.
func (m *Github) IssueList(
	ctx context.Context,
	// Select another repository using the [HOST/]OWNER/REPO format
	repo string,
	// Filter by assignee
	// +optional
	assignee string,
	// Filter by author
	// +optional
	author string,
	// Filter by label
	// +optional
	label []string,
	// Maximum number of issues to fetch (default 30)
	// +optional
	// FIXME: int
	limit string,
	// Search issues with query
	// +optional
	query string,
	// Filter by state: {open|closed|all} (default "open")
	// +optional
	state string,
) (string, error) {
	args := []string{"gh", "issue", "list", "-R", repo}

	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}
	if author != "" {
		args = append(args, "--author", author)
	}
	if len(label) > 0 {
		args = append(args, "--label", strings.Join(label, ","))
	}
	if limit != "" {
		args = append(args, "--limit", limit)
	}
	if query != "" {
		args = append(args, "--search", query)
	}
	if state != "" {
		args = append(args, "--state", state)
	}

	fields := []string{
		"assignees",
		"author",
		"body",
		"closed",
		"closedAt",
		"comments",
		"createdAt",
		"id",
		// "isPinned",
		"labels",
		// "milestone",
		"number",
		// "projectCards",
		// "projectItems",
		// "reactionGroups",
		"state",
		"stateReason",
		"title",
		// "updatedAt",
		"url",
	}

	args = append(args, "--json", strings.Join(fields, ","))

	return m.base().WithExec(args).Stdout(ctx)
}

// Add a comment to a GitHub issue.
func (m *Github) IssueComment(
	ctx context.Context,
	// Select another repository using the [HOST/]OWNER/REPO format
	repo string,
	// The issue number or URL
	issue string,

	// The comment body text
	body string,
) (string, error) {
	args := []string{
		"gh", "issue", "comment",
		"-R", repo,
		issue,
		"--body", body,
	}

	return m.base().WithExec(args).Stdout(ctx)
}

// Close issue
func (m *Github) IssueClose(
	ctx context.Context,
	// Select another repository using the [HOST/]OWNER/REPO format
	repo string,
	// The issue number or URL
	issue string,

	// Leave a closing comment
	// +optional
	comment string,

	// Reason for closing: {completed|not planned}
	reason string,
	// +optional
) (string, error) {
	args := []string{
		"gh", "issue", "close",
		"-R", repo,
		issue,
	}

	if comment != "" {
		args = append(args, "--comment", comment)
	}
	if reason != "" {
		args = append(args, "--reason", reason)
	}

	return m.base().WithExec(args).Stdout(ctx)
}
