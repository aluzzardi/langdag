// GitHub Toolkit
package main

import (
	"dagger/github/internal/dagger"
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
