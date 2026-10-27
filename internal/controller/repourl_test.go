package controller

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
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

// stubRepoURLPoller answers AskForRepoURL with a fixed URL.
type stubRepoURLPoller struct {
	url string
}

func (s stubRepoURLPoller) AskForRepoURL(_ context.Context, _, _, _ string) (string, error) {
	return s.url, nil
}

func TestPollRepoURL_AppliesGuardRails(t *testing.T) {
	tests := []struct {
		name      string
		slackURL  string
		wantState taskrun.State
		wantJobs  int
	}{
		{
			name:      "allowed repository launches the job",
			slackURL:  "https://github.com/org/repo",
			wantState: taskrun.StateRunning,
			wantJobs:  1,
		},
		{
			name:      "repository outside the allowed list is rejected",
			slackURL:  "https://github.com/other/repo",
			wantState: taskrun.StateFailed,
			wantJobs:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.GuardRails.AllowedRepos = []string{"https://github.com/org/*"}
			k8s := fake.NewSimpleClientset()
			tb := newMockTicketing(nil)
			store := taskrun.NewMemoryStore()

			tr := taskrun.New("tr-slack", "key-slack", "TICKET-SLACK", "claude-code")
			require.NoError(t, tr.Transition(taskrun.StateNeedsHuman))
			tr.ApprovalGateType = "missing_repo_url"

			ticket := ticketing.Ticket{ID: "TICKET-SLACK", Title: "Fix something"}

			r := &Reconciler{
				config:        cfg,
				logger:        testLogger(),
				k8sClient:     k8s,
				ticketing:     tb,
				engines:       map[string]engine.ExecutionEngine{"claude-code": &mockEngine{name: "claude-code"}},
				jobBuilder:    &mockJobBuilder{},
				taskRuns:      map[string]*taskrun.TaskRun{"key-slack": tr},
				engineChains:  map[string][]string{"key-slack": {"claude-code"}},
				ticketCache:   map[string]ticketing.Ticket{"TICKET-SLACK": ticket},
				taskRunStore:  store,
				repoURLPoller: stubRepoURLPoller{url: tt.slackURL},
				namespace:     "test-ns",
				streamReaders: make(map[string]context.CancelFunc),
			}

			ctx := context.Background()
			r.pollRepoURL(ctx, tr.ID, ticket, "")

			assert.Equal(t, tt.wantState, tr.State)
			jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			assert.Len(t, jobs.Items, tt.wantJobs)

			if tt.wantState == taskrun.StateFailed {
				assert.True(t, tr.IsTerminal(), "a rejected repository must not be retried")
				assert.Zero(t, r.activeJobCount(), "a rejected repository must not hold a concurrency slot")
				assert.Contains(t, tb.markedFailed, "TICKET-SLACK")
				saved, err := store.Get(ctx, tr.ID)
				require.NoError(t, err)
				assert.Equal(t, taskrun.StateFailed, saved.State)
			}
		})
	}
}
