package gitlab

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

func TestGitLabSCMBackend_Name(t *testing.T) {
	b := NewGitLabSCMBackend("tok", testLogger())
	assert.Equal(t, "gitlab", b.Name())
}

func TestGitLabSCMBackend_InterfaceVersion(t *testing.T) {
	b := NewGitLabSCMBackend("tok", testLogger())
	assert.Equal(t, scm.InterfaceVersion, b.InterfaceVersion())
}

func TestParseProjectPath(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantPath string
		wantErr  bool
	}{
		{
			name:     "https URL with group and project",
			url:      "https://gitlab.com/acme/widgets",
			wantPath: "acme/widgets",
		},
		{
			name:     "https URL with .git suffix",
			url:      "https://gitlab.com/acme/widgets.git",
			wantPath: "acme/widgets",
		},
		{
			name:     "https URL with subgroup",
			url:      "https://gitlab.com/acme/platform/widgets",
			wantPath: "acme/platform/widgets",
		},
		{
			name:     "ssh URL",
			url:      "git@gitlab.com:acme/widgets.git",
			wantPath: "acme/widgets",
		},
		{
			name:     "ssh URL without .git",
			url:      "git@gitlab.com:acme/widgets",
			wantPath: "acme/widgets",
		},
		{
			name:     "self-managed instance",
			url:      "https://gitlab.example.com/team/project",
			wantPath: "team/project",
		},
		{
			name:    "empty string",
			url:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := parseProjectPath(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPath, path)
		})
	}
}

func TestParseMRURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantPath string
		wantIID  int
		wantErr  bool
	}{
		{
			name:     "standard MR URL",
			url:      "https://gitlab.com/acme/widgets/-/merge_requests/42",
			wantPath: "acme/widgets",
			wantIID:  42,
		},
		{
			name:     "subgroup MR URL",
			url:      "https://gitlab.com/acme/platform/widgets/-/merge_requests/7",
			wantPath: "acme/platform/widgets",
			wantIID:  7,
		},
		{
			name:     "self-managed MR URL",
			url:      "https://gitlab.example.com/team/repo/-/merge_requests/1",
			wantPath: "team/repo",
			wantIID:  1,
		},
		{
			name:    "invalid URL - not a merge request",
			url:     "https://gitlab.com/acme/widgets/-/issues/42",
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
			path, iid, err := parseMRURL(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPath, path)
			assert.Equal(t, tt.wantIID, iid)
		})
	}
}

func TestGitLabSCMBackend_CreateBranch(t *testing.T) {
	var receivedPayload map[string]string
	var receivedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.EscapedPath()
		require.Equal(t, http.MethodPost, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(glBranch{
			Name:   "feature/fix-bug",
			Commit: glCommit{ID: "abc123"},
		})
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	err := b.CreateBranch(context.Background(), "https://gitlab.com/acme/widgets", "feature/fix-bug", "main")
	require.NoError(t, err)

	assert.Equal(t, "/projects/acme%2Fwidgets/repository/branches", receivedPath)
	assert.Equal(t, "feature/fix-bug", receivedPayload["branch"])
	assert.Equal(t, "main", receivedPayload["ref"])
}

func TestGitLabSCMBackend_CreateBranch_DefaultBase(t *testing.T) {
	var receivedPayload map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(glBranch{Name: "my-branch"})
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	err := b.CreateBranch(context.Background(), "https://gitlab.com/acme/widgets", "my-branch", "")
	require.NoError(t, err)
	assert.Equal(t, "main", receivedPayload["ref"])
}

func TestGitLabSCMBackend_CreatePullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/projects/acme%2Fwidgets/merge_requests", r.URL.EscapedPath())

		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		assert.Equal(t, "Fix login bug", payload["title"])
		assert.Equal(t, "feature/fix", payload["source_branch"])
		assert.Equal(t, "main", payload["target_branch"])

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glMR{
			IID:          10,
			Title:        "Fix login bug",
			Description:  "Fixes the crash",
			WebURL:       "https://gitlab.com/acme/widgets/-/merge_requests/10",
			State:        "opened",
			SourceBranch: "feature/fix",
			TargetBranch: "main",
		})
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	pr, err := b.CreatePullRequest(context.Background(), scm.CreatePullRequestInput{
		RepoURL:     "https://gitlab.com/acme/widgets",
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
	assert.Equal(t, "https://gitlab.com/acme/widgets/-/merge_requests/10", pr.URL)
}

func TestGitLabSCMBackend_GetPullRequestStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/projects/acme%2Fwidgets/merge_requests/42", r.URL.EscapedPath())

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glMR{
			IID:          42,
			Title:        "Feature MR",
			WebURL:       "https://gitlab.com/acme/widgets/-/merge_requests/42",
			State:        "merged",
			SourceBranch: "feature/x",
			TargetBranch: "main",
		})
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	pr, err := b.GetPullRequestStatus(context.Background(), "https://gitlab.com/acme/widgets/-/merge_requests/42")
	require.NoError(t, err)

	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "merged", pr.State)
}

func TestGitLabSCMBackend_GetPullRequestStatus_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	_, err := b.GetPullRequestStatus(context.Background(), "https://gitlab.com/acme/widgets/-/merge_requests/999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 404")
}

func TestGitLabSCMBackend_GetPullRequestStatus_HTMLResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>Sign in</body></html>"))
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("bad-token", testLogger(), WithBaseURL(srv.URL))
	_, err := b.GetPullRequestStatus(context.Background(), "https://gitlab.com/acme/widgets/-/merge_requests/42")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected content-type")
	assert.Contains(t, err.Error(), "token may lack access")
}

func TestGitLabSCMBackend_AuthHeader(t *testing.T) {
	var authHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(glMR{IID: 1, State: "opened"})
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("my-secret-token", testLogger(), WithBaseURL(srv.URL))
	_, _ = b.GetPullRequestStatus(context.Background(), "https://gitlab.com/o/r/-/merge_requests/1")
	assert.Equal(t, "my-secret-token", authHeader)
}

func TestGitLabSCMBackend_GetDiff(t *testing.T) {
	tests := []struct {
		name             string
		baseBranch       string
		wantProjectFetch bool // whether the test server should expect a project API call
		wantFrom         string
	}{
		{
			name:             "explicit base branch",
			baseBranch:       "develop",
			wantProjectFetch: false,
			wantFrom:         "develop",
		},
		{
			name:             "empty base fetches default branch",
			baseBranch:       "",
			wantProjectFetch: true,
			wantFrom:         "trunk", // returned by the mock project endpoint
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var projectFetched bool
			var capturedFrom string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.EscapedPath() == "/projects/acme%2Fwidgets" && r.Method == http.MethodGet:
					projectFetched = true
					_ = json.NewEncoder(w).Encode(map[string]string{"default_branch": "trunk"})
				case r.URL.EscapedPath() == "/projects/acme%2Fwidgets/repository/compare":
					capturedFrom = r.URL.Query().Get("from")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"diffs": []map[string]string{
							{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1 @@\n-old\n+new"},
						},
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
			diff, err := b.GetDiff(context.Background(), "https://gitlab.com/acme/widgets", tt.baseBranch, "feature/x")
			require.NoError(t, err)

			assert.Equal(t, tt.wantProjectFetch, projectFetched)
			assert.Equal(t, tt.wantFrom, capturedFrom)
			assert.Contains(t, diff, "--- a/a.go")
			assert.Contains(t, diff, "+++ b/a.go")
		})
	}
}

func TestGitLabSCMBackend_GetDiff_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	_, err := b.GetDiff(context.Background(), "https://gitlab.com/acme/widgets", "main", "nonexistent")
	require.Error(t, err)
}

func TestGitLabSCMBackend_SubgroupProject(t *testing.T) {
	var receivedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(glBranch{Name: "test-branch"})
	}))
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	err := b.CreateBranch(context.Background(), "https://gitlab.com/acme/platform/widgets", "test-branch", "main")
	require.NoError(t, err)

	assert.Equal(t, "/projects/acme%2Fplatform%2Fwidgets/repository/branches", receivedPath)
}
