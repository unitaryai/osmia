package controller

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

func TestExtractRepoURL(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "GitHub URL in plain text",
			text: "Please fix the bug in https://github.com/unitaryai/osmia",
			want: "https://github.com/unitaryai/osmia",
		},
		{
			name: "GitLab URL in plain text",
			text: "The repo is at https://gitlab.com/unitaryai/backend/goldsmith",
			want: "https://gitlab.com/unitaryai/backend/goldsmith",
		},
		{
			name: "URL with trailing period from prose",
			text: "Check https://github.com/org/repo.",
			want: "https://github.com/org/repo",
		},
		{
			name: "URL in markdown link",
			text: "See [the repo](https://github.com/org/repo) for details.",
			want: "https://github.com/org/repo",
		},
		{
			name: "URL with path segments",
			text: "Bug is in https://github.com/unitaryai/RoboDev/tree/main/src",
			want: "https://github.com/unitaryai/RoboDev/tree/main/src",
		},
		{
			name: "multiple URLs returns first",
			text: "See https://github.com/org/first and https://gitlab.com/org/second",
			want: "https://github.com/org/first",
		},
		{
			name: "no URL returns empty",
			text: "Fix the login bug. The auth service is broken.",
			want: "",
		},
		{
			name: "non-GitHub/GitLab URL ignored",
			text: "See https://bitbucket.org/org/repo for the code",
			want: "",
		},
		{
			name: "URL in angle brackets",
			text: "Repo: <https://github.com/org/repo>",
			want: "https://github.com/org/repo",
		},
		{
			name: "URL with trailing comma",
			text: "Repos: https://github.com/org/repo, and others",
			want: "https://github.com/org/repo",
		},
		{
			name: "empty text",
			text: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRepoURL(tt.text)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveRepoURL_ExtractsFromDescription(t *testing.T) {
	r := &Reconciler{
		logger: slog.Default(),
	}
	ticket := ticketing.Ticket{
		ID:          "T-1",
		Description: "Fix the bug in https://github.com/unitaryai/osmia please",
	}

	ok := r.resolveRepoURL(context.Background(), &ticket)
	assert.True(t, ok)
	assert.Equal(t, "https://github.com/unitaryai/osmia", ticket.RepoURL)
}

func TestResolveRepoURL_ReturnsFalseWithoutURL(t *testing.T) {
	r := &Reconciler{
		logger: slog.Default(),
	}
	ticket := ticketing.Ticket{
		ID:          "T-2",
		Description: "Fix the login bug",
	}

	ok := r.resolveRepoURL(context.Background(), &ticket)
	assert.False(t, ok)
	assert.Empty(t, ticket.RepoURL)
}
