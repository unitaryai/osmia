package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestGitHubSCMBackend_Name(t *testing.T) {
	b := NewGitHubSCMBackend("tok", testLogger())
	assert.Equal(t, "github", b.Name())
}

func TestGitHubSCMBackend_InterfaceVersion(t *testing.T) {
	b := NewGitHubSCMBackend("tok", testLogger())
	assert.Equal(t, scm.InterfaceVersion, b.InterfaceVersion())
}

func TestParseOwnerRepo(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "https URL",
			url:       "https://github.com/acme/widgets",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
		{
			name:      "https URL with .git suffix",
			url:       "https://github.com/acme/widgets.git",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
		{
			name:      "ssh URL",
			url:       "git@github.com:acme/widgets.git",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
		{
			name:      "ssh URL without .git",
			url:       "git@github.com:acme/widgets",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
		{
			name:      "https with trailing path",
			url:       "https://github.com/acme/widgets/tree/main",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
		{
			name:    "unsupported URL",
			url:     "https://gitlab.com/acme/widgets",
			wantErr: true,
		},
		{
			name:    "empty string",
			url:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := parseOwnerRepo(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantRepo, repo)
		})
	}
}

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantOwner  string
		wantRepo   string
		wantNumber int
		wantErr    bool
	}{
		{
			name:       "standard PR URL",
			url:        "https://github.com/acme/widgets/pull/42",
			wantOwner:  "acme",
			wantRepo:   "widgets",
			wantNumber: 42,
		},
		{
			name:    "invalid URL",
			url:     "https://github.com/acme/widgets/issues/42",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, number, err := parsePRURL(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantRepo, repo)
			assert.Equal(t, tt.wantNumber, number)
		})
	}
}

func TestGitHubSCMBackend_CreateBranch(t *testing.T) {
	var createdRef map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/git/ref/heads/main":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ghRef{
				Ref:    "refs/heads/main",
				Object: ghObject{SHA: "abc123"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/git/refs":
			_ = json.NewDecoder(r.Body).Decode(&createdRef)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	err := b.CreateBranch(context.Background(), "https://github.com/acme/widgets", "feature/fix-bug", "main")
	require.NoError(t, err)

	assert.Equal(t, "refs/heads/feature/fix-bug", createdRef["ref"])
	assert.Equal(t, "abc123", createdRef["sha"])
}

func TestGitHubSCMBackend_CreateBranch_DefaultBase(t *testing.T) {
	var fetchedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fetchedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ghRef{
				Ref:    "refs/heads/main",
				Object: ghObject{SHA: "def456"},
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	err := b.CreateBranch(context.Background(), "https://github.com/acme/widgets", "my-branch", "")
	require.NoError(t, err)
	assert.Equal(t, "/repos/acme/widgets/git/ref/heads/main", fetchedPath)
}

func TestGitHubSCMBackend_CreatePullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repos/acme/widgets/pulls", r.URL.Path)

		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		assert.Equal(t, "Fix login bug", payload["title"])
		assert.Equal(t, "feature/fix", payload["head"])
		assert.Equal(t, "main", payload["base"])

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghPR{
			Number:  10,
			Title:   "Fix login bug",
			Body:    "Fixes the crash",
			HTMLURL: "https://github.com/acme/widgets/pull/10",
			State:   "open",
			Head:    ghHead{Ref: "feature/fix"},
			Base:    ghHead{Ref: "main"},
		})
	}))
	defer srv.Close()

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	pr, err := b.CreatePullRequest(context.Background(), scm.CreatePullRequestInput{
		RepoURL:     "https://github.com/acme/widgets",
		Title:       "Fix login bug",
		Description: "Fixes the crash",
		BranchName:  "feature/fix",
		BaseBranch:  "main",
	})
	require.NoError(t, err)

	assert.Equal(t, 10, pr.Number)
	assert.Equal(t, "10", pr.ID)
	assert.Equal(t, "Fix login bug", pr.Title)
	assert.Equal(t, "open", pr.State)
	assert.Equal(t, "feature/fix", pr.BranchName)
	assert.Equal(t, "main", pr.BaseBranch)
	assert.Equal(t, "https://github.com/acme/widgets/pull/10", pr.URL)
}

func TestGitHubSCMBackend_GetPullRequestStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/acme/widgets/pulls/42", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghPR{
			Number:  42,
			Title:   "Feature PR",
			HTMLURL: "https://github.com/acme/widgets/pull/42",
			State:   "closed",
			Merged:  true,
			Head:    ghHead{Ref: "feature/x"},
			Base:    ghHead{Ref: "main"},
		})
	}))
	defer srv.Close()

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	pr, err := b.GetPullRequestStatus(context.Background(), "https://github.com/acme/widgets/pull/42")
	require.NoError(t, err)

	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "merged", pr.State)
}

func TestGitHubSCMBackend_GetPullRequestStatus_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	_, err := b.GetPullRequestStatus(context.Background(), "https://github.com/acme/widgets/pull/999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 404")
}

func TestGitHubSCMBackend_GetDiff(t *testing.T) {
	tests := []struct {
		name           string
		baseBranch     string
		wantCompareSeg string // expected "base...head" segment in the URL path
	}{
		{
			name:           "explicit base branch",
			baseBranch:     "develop",
			wantCompareSeg: "develop...feature/x",
		},
		{
			name:           "empty base defaults to HEAD",
			baseBranch:     "",
			wantCompareSeg: "HEAD...feature/x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const fakeDiff = "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n@@ -1 +1 @@\n-old\n+new\n"
			var capturedPath string
			var capturedAccept string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				capturedAccept = r.Header.Get("Accept")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(fakeDiff))
			}))
			defer srv.Close()

			b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
			diff, err := b.GetDiff(context.Background(), "https://github.com/acme/widgets", tt.baseBranch, "feature/x")
			require.NoError(t, err)

			assert.Equal(t, "/repos/acme/widgets/compare/"+tt.wantCompareSeg, capturedPath)
			assert.Equal(t, "application/vnd.github.v3.diff", capturedAccept)
			assert.Equal(t, fakeDiff, diff)
		})
	}
}

func TestGitHubSCMBackend_GetDiff_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	_, err := b.GetDiff(context.Background(), "https://github.com/acme/widgets", "main", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 404")
}

func TestGitHubSCMBackend_AuthHeader(t *testing.T) {
	var authHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghPR{Number: 1, State: "open"})
	}))
	defer srv.Close()

	b := NewGitHubSCMBackend("my-secret-token", testLogger(), WithBaseURL(srv.URL))
	_, _ = b.GetPullRequestStatus(context.Background(), "https://github.com/o/r/pull/1")
	assert.Equal(t, "Bearer my-secret-token", authHeader)
}
