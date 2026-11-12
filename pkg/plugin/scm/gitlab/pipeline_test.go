package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

const (
	testMRURL = "https://gitlab.com/acme/widgets/-/merge_requests/7"
	testAPI   = "/api/v4/projects/acme%2Fwidgets"
	headSHA   = "1111111111111111111111111111111111111111"
	mergeSHA  = "3333333333333333333333333333333333333333"
)

// pipelineServer serves a merge request with the given head pipeline, and
// that pipeline's failed jobs.
func pipelineServer(t *testing.T, head *glPipeline, jobs []glJob, traces map[int]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		writeJSON := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch {
		case path == testAPI+"/merge_requests/7":
			writeJSON(glMR{IID: 7, SHA: headSHA, HeadPipeline: head})
		case path == testAPI+"/pipelines/42/jobs":
			assert.Equal(t, "failed", r.URL.Query().Get("scope[]"))
			writeJSON(jobs)
		case strings.HasPrefix(path, testAPI+"/jobs/") && strings.HasSuffix(path, "/trace"):
			var id int
			_, _ = fmt.Sscanf(strings.TrimPrefix(path, testAPI+"/jobs/"), "%d/trace", &id)
			trace, ok := traces[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(trace))
		default:
			t.Errorf("unexpected request %s", path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLatestPipelineFailed(t *testing.T) {
	srv := pipelineServer(t,
		// A merged-results pipeline: its SHA is the merge commit, not the
		// source branch head, and it must still be found.
		&glPipeline{ID: 42, SHA: mergeSHA, Status: "failed", WebURL: "https://gitlab.com/acme/widgets/-/pipelines/42", CreatedAt: "2026-11-11T10:00:00Z"},
		[]glJob{
			{ID: 1, Name: "lint", Stage: "test", WebURL: "https://gitlab.com/acme/widgets/-/jobs/1", FinishedAt: "2026-11-11T10:05:00Z"},
			{ID: 9, Name: "flaky", Stage: "test", AllowFailure: true, FinishedAt: "2026-11-11T11:00:00Z"},
			{ID: 3, Name: "unit", Stage: "test", FinishedAt: "2026-11-11T10:07:00Z"},
		},
		map[int]string{1: "\x1b[31mERROR\x1b[0m gofmt: main.go is not formatted\n"},
	)
	defer srv.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL))
	got, err := b.LatestPipeline(context.Background(), testMRURL)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "42:3", got.ID, "the newest required failed job is part of the ID, so a retry is a new failure")
	assert.Equal(t, mergeSHA, got.SHA)
	assert.Equal(t, scm.PipelineFailed, got.Status)
	assert.Equal(t, 2026, got.CreatedAt.Year())
	assert.Equal(t, 7, got.FailedAt.Minute(), "the latest required failed job sets FailedAt; allowed failures do not")
	require.Len(t, got.FailedJobs, 2, "a job allowed to fail is not reported")
	assert.Equal(t, "lint", got.FailedJobs[0].Name)
	assert.Equal(t, "ERROR gofmt: main.go is not formatted\n", got.FailedJobs[0].LogTail, "colour codes are stripped")
	assert.Equal(t, "unit", got.FailedJobs[1].Name)
	assert.Empty(t, got.FailedJobs[1].LogTail, "an unreadable log leaves the tail empty")
}

func TestLatestPipelineStatuses(t *testing.T) {
	tests := []struct {
		name    string
		head    *glPipeline
		want    *scm.PipelineStatus
		wantErr string
	}{
		{name: "no head pipeline"},
		{name: "running", head: &glPipeline{ID: 42, SHA: headSHA, Status: "running"}, want: ptr(scm.PipelineRunning)},
		{name: "passed", head: &glPipeline{ID: 42, SHA: headSHA, Status: "success"}, want: ptr(scm.PipelineSucceeded)},
		{name: "cancelled", head: &glPipeline{ID: 42, SHA: headSHA, Status: "canceled"}, want: ptr(scm.PipelineOther)},
		{name: "unknown status", head: &glPipeline{ID: 42, SHA: headSHA, Status: "exploded"}, wantErr: `unknown GitLab pipeline status "exploded"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := pipelineServer(t, tt.head, nil, nil)
			defer srv.Close()

			got, err := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL)).LatestPipeline(context.Background(), testMRURL)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, got.Status)
			assert.Empty(t, got.FailedJobs)
			assert.Equal(t, "42", got.ID)
		})
	}
}

func TestLatestPipelineCapsFailedJobs(t *testing.T) {
	var jobs []glJob
	for i := 1; i <= scm.MaxFailedJobs+3; i++ {
		jobs = append(jobs, glJob{ID: i, Name: fmt.Sprintf("job-%d", i)})
	}
	srv := pipelineServer(t, &glPipeline{ID: 42, SHA: headSHA, Status: "failed"}, jobs, nil)
	defer srv.Close()

	got, err := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(srv.URL)).LatestPipeline(context.Background(), testMRURL)
	require.NoError(t, err)
	assert.Len(t, got.FailedJobs, scm.MaxFailedJobs)
}

// TestJobLogRedirectDropsToken checks that a job log served through a
// redirect, as GitLab does for logs in object storage, is fetched without the
// API token, which travels in a custom header Go would otherwise copy.
func TestJobLogRedirectDropsToken(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("PRIVATE-TOKEN"), "the token must not reach the redirect target")
		_, _ = w.Write([]byte("job failed\n"))
	}))
	defer storage.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "tok", r.Header.Get("PRIVATE-TOKEN"))
		http.Redirect(w, r, storage.URL+"/trace", http.StatusFound)
	}))
	defer api.Close()

	b := NewGitLabSCMBackend("tok", testLogger(), WithBaseURL(api.URL))
	assert.Equal(t, "job failed\n", b.jobLogTail(context.Background(), b.baseURL+"/projects/x", 1))
}

func ptr[T any](v T) *T { return &v }
