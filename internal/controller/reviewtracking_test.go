package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/reviewpoller"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	mr256 = "https://gitlab.com/acme/widgets/-/merge_requests/256"
	repo  = "https://gitlab.com/acme/widgets"
)

var restoreNow = time.Date(2026, 10, 27, 11, 16, 0, 0, time.UTC)

// succeeded builds a persisted, succeeded originating TaskRun that opened prURL.
func succeeded(id, prURL string, finished time.Time) *taskrun.TaskRun {
	tr := taskrun.New(id, id, "T-1", "claude-code")
	tr.State = taskrun.StateSucceeded
	tr.CreatedAt = finished.Add(-10 * time.Minute)
	tr.UpdatedAt = finished
	tr.Result = &engine.TaskResult{Success: true, MergeRequestURL: prURL}
	return tr
}

// followUp builds a review follow-up TaskRun for prURL created at created.
func followUp(id, prURL string, created time.Time) *taskrun.TaskRun {
	tr := taskrun.New(id, id, "T-1", "claude-code")
	tr.State = taskrun.StateSucceeded
	tr.CreatedAt = created
	tr.UpdatedAt = created.Add(5 * time.Minute)
	tr.ReviewPRURL = prURL
	return tr
}

func TestReviewTrackingFromTaskRuns(t *testing.T) {
	finished := time.Date(2026, 10, 27, 10, 53, 0, 0, time.UTC)

	t.Run("MR opened before the restart is restored, from when it finished", func(t *testing.T) {
		origin := succeeded("tr-1", mr256, finished)
		origin.TicketTitle = "UC2: fail a policy"
		origin.TicketDescription = "## Problem"
		origin.RepoURL = repo

		got := reviewTrackingFromTaskRuns([]*taskrun.TaskRun{origin}, restoreNow)

		require.Len(t, got, 1)
		assert.Equal(t, reviewpoller.RestoredPR{
			PRURL:               mr256,
			TicketID:            "T-1",
			OriginalTitle:       "UC2: fail a policy",
			OriginalDescription: "## Problem",
			RepoURL:             repo,
			FollowUpCount:       0,
			HandledUntil:        finished,
		}, got[0])
	})

	t.Run("follow-ups carry over the budget and move the handled point forward", func(t *testing.T) {
		second := finished.Add(40 * time.Minute)
		runs := []*taskrun.TaskRun{
			succeeded("tr-1", mr256, finished),
			followUp("tr-r1", mr256, finished.Add(20*time.Minute)),
			followUp("tr-r2", mr256, second),
		}

		got := reviewTrackingFromTaskRuns(runs, restoreNow)

		require.Len(t, got, 1)
		assert.Equal(t, 2, got[0].FollowUpCount)
		assert.Equal(t, second, got[0].HandledUntil)
	})

	t.Run("a follow-up run is never itself treated as opening a PR", func(t *testing.T) {
		fu := followUp("tr-r1", mr256, finished)
		fu.Result = &engine.TaskResult{Success: true, MergeRequestURL: mr256}

		assert.Empty(t, reviewTrackingFromTaskRuns([]*taskrun.TaskRun{fu}, restoreNow))
	})

	t.Run("runs that did not succeed, or opened no PR, are ignored", func(t *testing.T) {
		failed := succeeded("tr-f", mr256, finished)
		failed.State = taskrun.StateFailed
		noPR := succeeded("tr-n", "", finished)
		noResult := succeeded("tr-x", mr256, finished)
		noResult.Result = nil

		assert.Empty(t, reviewTrackingFromTaskRuns([]*taskrun.TaskRun{failed, noPR, noResult}, restoreNow))
	})

	t.Run("PRs older than the restore window are not resurrected", func(t *testing.T) {
		old := succeeded("tr-old", mr256, restoreNow.Add(-reviewRestoreWindow-time.Hour))

		assert.Empty(t, reviewTrackingFromTaskRuns([]*taskrun.TaskRun{old}, restoreNow))
	})

	t.Run("a PR reported by two successful runs is restored once, from the newer", func(t *testing.T) {
		older := succeeded("tr-1", mr256, finished)
		older.TicketTitle = "old title"
		newer := succeeded("tr-2", mr256, finished.Add(time.Hour))
		newer.TicketTitle = "new title"

		got := reviewTrackingFromTaskRuns([]*taskrun.TaskRun{newer, older}, restoreNow)

		require.Len(t, got, 1)
		assert.Equal(t, "new title", got[0].OriginalTitle)
		assert.Equal(t, finished.Add(time.Hour), got[0].HandledUntil)
	})

	t.Run("a run recorded before the repo was persisted gets it from the PR URL", func(t *testing.T) {
		got := reviewTrackingFromTaskRuns([]*taskrun.TaskRun{succeeded("tr-1", mr256, finished)}, restoreNow)

		require.Len(t, got, 1)
		assert.Equal(t, repo, got[0].RepoURL)
	})
}

func TestRepoURLFromPRURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{mr256, repo},
		{"https://gitlab.com/acme/widgets/-/merge_requests/7#note_1", "https://gitlab.com/acme/widgets"},
		{"https://github.com/acme/widgets/pull/42", "https://github.com/acme/widgets"},
		{"https://example.com/not-a-pr", "https://example.com/not-a-pr"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, repoURLFromPRURL(tt.in), tt.in)
	}
}

// TestRestoreReviewTracking_SurvivesRestart covers the motivating case: a
// task opens an MR and finishes, the controller is replaced (for example by
// an autoscaler), and the new process must still be watching the MR.
func TestRestoreReviewTracking_SurvivesRestart(t *testing.T) {
	store := taskrun.NewMemoryStore()
	ctx := context.Background()
	require.NoError(t, store.Save(ctx, succeeded("tr-t1", mr256, time.Now().Add(-20*time.Minute))))

	poller := reviewpoller.New(config.ReviewResponseConfig{Enabled: true}, nil, testLogger())
	r := NewReconciler(&config.Config{}, testLogger(),
		WithTaskRunStore(store),
		WithReviewPoller(poller),
	)

	require.False(t, poller.IsTracked(mr256), "a fresh process starts with nothing tracked")

	r.restoreReviewTracking(ctx)

	assert.True(t, poller.IsTracked(mr256))
}

// TestRegisterForReview_FallsBackToTaskRun covers a restart while the job was
// still running: the ticket cache is empty when it finishes, and previously
// its MR was then never registered at all.
func TestRegisterForReview_FallsBackToTaskRun(t *testing.T) {
	poller := reviewpoller.New(config.ReviewResponseConfig{Enabled: true}, nil, testLogger())
	r := NewReconciler(&config.Config{}, testLogger(), WithReviewPoller(poller))

	tr := succeeded("tr-t1", mr256, time.Now())
	tr.TicketTitle = "UC2: fail a policy"
	tr.RepoURL = repo

	r.registerForReview(tr, mr256)

	assert.True(t, poller.IsTracked(mr256), "an empty ticket cache must not stop registration")
}
