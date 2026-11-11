package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

const testHeadSHA = "1111111111111111111111111111111111111111"

func checkRun(id int64, name, status, conclusion string) ghCheckRun {
	run := ghCheckRun{ID: id, Name: name, Status: status, Conclusion: conclusion}
	run.App.Slug = githubActionsApp
	return run
}

func TestCheckRunsStatus(t *testing.T) {
	tests := []struct {
		name    string
		runs    []ghCheckRun
		want    scm.PipelineStatus
		wantErr string
	}{
		{name: "all passed", runs: []ghCheckRun{checkRun(1, "a", "completed", "success"), checkRun(2, "b", "completed", "skipped")}, want: scm.PipelineSucceeded},
		{name: "one failed", runs: []ghCheckRun{checkRun(1, "a", "completed", "success"), checkRun(2, "b", "completed", "failure")}, want: scm.PipelineFailed},
		{name: "timed out counts as failed", runs: []ghCheckRun{checkRun(1, "a", "completed", "timed_out")}, want: scm.PipelineFailed},
		{name: "running wins over failed", runs: []ghCheckRun{checkRun(1, "a", "completed", "failure"), checkRun(2, "b", "in_progress", "")}, want: scm.PipelineRunning},
		{name: "cancelled", runs: []ghCheckRun{checkRun(1, "a", "completed", "cancelled")}, want: scm.PipelineOther},
		{name: "unknown status", runs: []ghCheckRun{checkRun(1, "a", "exploded", "")}, wantErr: "unknown GitHub check run status"},
		{name: "unknown conclusion", runs: []ghCheckRun{checkRun(1, "a", "completed", "exploded")}, wantErr: "unknown GitHub check run conclusion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := checkRunsStatus(tt.runs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLatestPipelineFromCheckRuns(t *testing.T) {
	lint := checkRun(11, "lint", "completed", "failure")
	lint.HTMLURL = "https://github.com/acme/widgets/actions/runs/1/job/11"
	external := checkRun(12, "external", "completed", "failure")
	external.App.Slug = "some-ci"
	external.Output.Title = "2 tests failed"
	external.Output.Summary = "TestFoo failed"
	passing := checkRun(13, "unit", "completed", "success")

	logServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"), "the token must not follow the redirect to the log host")
		_, _ = w.Write([]byte("\x1b[31mlint failed\x1b[0m\n"))
	}))
	defer logServer.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/7":
			writeJSON(ghPR{Number: 7, Head: ghHead{SHA: testHeadSHA}})
		case "/repos/acme/widgets/commits/" + testHeadSHA + "/check-runs":
			writeJSON(map[string]any{"check_runs": []ghCheckRun{lint, external, passing}})
		case "/repos/acme/widgets/actions/jobs/11/logs":
			http.Redirect(w, r, logServer.URL+"/signed-log", http.StatusFound)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	b := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(api.URL))
	got, err := b.LatestPipeline(context.Background(), "https://github.com/acme/widgets/pull/7")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, testHeadSHA+":13", got.ID)
	assert.Equal(t, scm.PipelineFailed, got.Status)
	require.Len(t, got.FailedJobs, 2)
	assert.Equal(t, "lint", got.FailedJobs[0].Name)
	assert.Equal(t, "lint failed\n", got.FailedJobs[0].LogTail)
	assert.Equal(t, "external", got.FailedJobs[1].Name)
	assert.Equal(t, "2 tests failed\n\nTestFoo failed", got.FailedJobs[1].LogTail, "a non-Actions check falls back to its output")
}

func TestLatestPipelineNoCheckRuns(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/acme/widgets/pulls/7" {
			_ = json.NewEncoder(w).Encode(ghPR{Number: 7, Head: ghHead{SHA: testHeadSHA}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": []ghCheckRun{}})
	}))
	defer api.Close()

	got, err := NewGitHubSCMBackend("tok", testLogger(), WithBaseURL(api.URL)).
		LatestPipeline(context.Background(), "https://github.com/acme/widgets/pull/7")
	require.NoError(t, err)
	assert.Nil(t, got)
}
