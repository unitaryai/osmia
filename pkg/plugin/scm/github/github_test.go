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

func TestGitHubSCMBackend_AuthenticatedUser(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantUser string
		wantErr  string
	}{
		{
			name:     "returns the token's login",
			status:   http.StatusOK,
			body:     `{"id": 7, "login": "osmia-bot", "type": "User"}`,
			wantUser: "osmia-bot",
		},
		{
			// GitHub App installation tokens cannot call GET /user.
			name:    "installation token is rejected with an error the poller can fall back from",
			status:  http.StatusForbidden,
			body:    `{"message": "Resource not accessible by integration"}`,
			wantErr: "unexpected status 403",
		},
		{
			name:    "missing login is an error rather than an empty identity",
			status:  http.StatusOK,
			body:    `{"id": 7}`,
			wantErr: "no login",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				assert.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
			user, err := b.AuthenticatedUser(context.Background())

			assert.Equal(t, "/user", gotPath)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUser, user)
		})
	}
}

// TestGitHubSCMBackend_ListReviewComments_ReadsEveryPage serves both comment
// listings over two pages and checks the second page of each is read.
func TestGitHubSCMBackend_ListReviewComments_ReadsEveryPage(t *testing.T) {
	comment := func(id int, user string) map[string]any {
		return map[string]any{
			"id": id, "body": "c", "user": map[string]any{"login": user},
			"created_at": "2026-10-28T13:00:00Z",
		}
	}
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		w.Header().Set("Content-Type", "application/json")
		base := 100
		if r.URL.Path == "/repos/o/r/issues/7/comments" {
			base = 200
		}
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode([]map[string]any{comment(base+2, "reviewer")})
			return
		}
		w.Header().Set("Link", "<"+srvURL+r.URL.Path+`?per_page=100&page=2>; rel="next"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{comment(base+1, "bot")})
	}))
	defer srv.Close()
	srvURL = srv.URL

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	comments, err := b.ListReviewComments(context.Background(), "https://github.com/o/r/pull/7")
	require.NoError(t, err)

	var ids []string
	for _, c := range comments {
		ids = append(ids, c.ID)
	}
	assert.ElementsMatch(t, []string{"101", "102", "201", "202"}, ids)
}
