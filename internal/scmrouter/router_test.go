package scmrouter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// stubBackend is a minimal scm.Backend implementation used in tests.
// It only implements Name and InterfaceVersion; all other methods panic.
type stubBackend struct {
	name string
}

// Compile-time check that stubBackend implements scm.Backend.
var _ scm.Backend = (*stubBackend)(nil)

func (s *stubBackend) Name() string { return s.name }

func (s *stubBackend) InterfaceVersion() int { return scm.InterfaceVersion }

func (s *stubBackend) CreateBranch(_ context.Context, _, _, _ string) error {
	panic("stubBackend.CreateBranch not implemented")
}

func (s *stubBackend) CreatePullRequest(_ context.Context, _ scm.CreatePullRequestInput) (*scm.PullRequest, error) {
	panic("stubBackend.CreatePullRequest not implemented")
}

func (s *stubBackend) GetPullRequestStatus(_ context.Context, _ string) (*scm.PullRequest, error) {
	panic("stubBackend.GetPullRequestStatus not implemented")
}

func (s *stubBackend) ListReviewComments(_ context.Context, _ string) ([]scm.ReviewComment, error) {
	panic("stubBackend.ListReviewComments not implemented")
}

func (s *stubBackend) ReplyToComment(_ context.Context, _, _, _ string) error {
	panic("stubBackend.ReplyToComment not implemented")
}

func (s *stubBackend) ResolveThread(_ context.Context, _, _ string) error {
	panic("stubBackend.ResolveThread not implemented")
}

func (s *stubBackend) GetDiff(_ context.Context, _, _, _ string) (string, error) {
	panic("stubBackend.GetDiff not implemented")
}

func TestRouter_For(t *testing.T) {
	githubBackend := &stubBackend{name: "github"}
	gitlabBackend := &stubBackend{name: "gitlab"}

	router := NewRouter(
		Entry{Match: "github.com", Backend: githubBackend},
		Entry{Match: "gitlab.com", Backend: gitlabBackend},
		Entry{Match: "*.internal.example.com", Backend: gitlabBackend},
	)

	tests := []struct {
		name        string
		repoURL     string
		wantBackend scm.Backend
		wantErr     bool
	}{
		{
			name:        "exact match github.com",
			repoURL:     "https://github.com/acme/widgets",
			wantBackend: githubBackend,
		},
		{
			name:        "exact match gitlab.com",
			repoURL:     "https://gitlab.com/acme/widgets",
			wantBackend: gitlabBackend,
		},
		{
			name:        "glob match *.internal.example.com",
			repoURL:     "https://git.internal.example.com/acme/widgets",
			wantBackend: gitlabBackend,
		},
		{
			name:        "no match falls back to first backend",
			repoURL:     "https://bitbucket.org/acme/widgets",
			wantBackend: githubBackend,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := router.For(tt.repoURL)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantBackend, got)
		})
	}
}

func TestRouter_For_NoBackends(t *testing.T) {
	router := NewRouter()
	_, err := router.For("https://github.com/acme/widgets")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no SCM backends configured")
}

func TestRouter_For_NoSchemeURL(t *testing.T) {
	router := NewRouter(
		Entry{Match: "github.com", Backend: &stubBackend{name: "github"}},
	)
	// A URL without a scheme has no host after parsing, so For should
	// return an error about the missing host.
	_, err := router.For("github.com/acme/widgets")
	require.Error(t, err)
}

func TestRouter_Len(t *testing.T) {
	router := NewRouter(
		Entry{Match: "github.com", Backend: &stubBackend{name: "github"}},
		Entry{Match: "gitlab.com", Backend: &stubBackend{name: "gitlab"}},
	)
	assert.Equal(t, 2, router.Len())
}

func TestRouter_Len_Empty(t *testing.T) {
	router := NewRouter()
	assert.Equal(t, 0, router.Len())
}
