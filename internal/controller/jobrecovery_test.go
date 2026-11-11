package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

const recoveryNamespace = "osmia"

func runningRun(id, key, job string, updated time.Time) *taskrun.TaskRun {
	return &taskrun.TaskRun{
		ID:                id,
		IdempotencyKey:    key,
		TicketID:          "ticket-" + key,
		TicketTitle:       "Title of " + key,
		TicketDescription: "Description of " + key,
		RepoURL:           "https://github.com/acme/widgets",
		State:             taskrun.StateRunning,
		JobName:           job,
		CreatedAt:         updated.Add(-time.Minute),
		UpdatedAt:         updated,
	}
}

func agentJob(name string) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: recoveryNamespace}}
}

func TestRunningTaskRunsToRestore(t *testing.T) {
	now := time.Now()
	older := runningRun("tr-old", "k1", "job-old", now.Add(-time.Hour))
	newer := runningRun("tr-new", "k1", "job-new", now)
	succeeded := runningRun("tr-done", "k2", "job-done", now)
	succeeded.State = taskrun.StateSucceeded
	noJob := runningRun("tr-nojob", "k3", "", now)
	other := runningRun("tr-other", "k4", "job-other", now.Add(-2*time.Hour))

	tests := []struct {
		name string
		runs []*taskrun.TaskRun
		want []string
	}{
		{name: "nothing persisted", runs: nil, want: []string{}},
		{name: "only running runs with a job are picked", runs: []*taskrun.TaskRun{succeeded, noJob, other}, want: []string{"tr-other"}},
		{name: "the newest run per key wins", runs: []*taskrun.TaskRun{newer, older}, want: []string{"tr-new"}},
		{name: "oldest first", runs: []*taskrun.TaskRun{newer, other}, want: []string{"tr-other", "tr-new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runningTaskRunsToRestore(tt.runs)
			ids := make([]string, 0, len(got))
			for _, tr := range got {
				ids = append(ids, tr.ID)
			}
			assert.Equal(t, tt.want, ids)
		})
	}
}

// TestRestoreRunningJobs_SurvivesRestart covers the motivating case: the
// controller is replaced while an agent Job runs, and the new process must
// track that Job again, count it against the concurrency limit, and have the
// ticket details completion handling needs.
func TestRestoreRunningJobs_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	store := taskrun.NewMemoryStore()
	live := runningRun("tr-live", "k-live", "job-live", time.Now())
	require.NoError(t, store.Save(ctx, live))

	r := NewReconciler(&config.Config{}, testLogger(),
		WithTaskRunStore(store),
		WithK8sClient(fake.NewSimpleClientset(agentJob("job-live"))),
		WithNamespace(recoveryNamespace),
	)
	require.Zero(t, r.activeJobCount(), "a fresh process starts with nothing tracked")

	r.restoreRunningJobs(ctx)

	got, ok := r.GetTaskRun("k-live")
	require.True(t, ok)
	assert.Equal(t, "tr-live", got.ID)
	assert.Equal(t, 1, r.activeJobCount())
	assert.Equal(t, ticketing.Ticket{
		ID:          "ticket-k-live",
		Title:       "Title of k-live",
		Description: "Description of k-live",
		RepoURL:     "https://github.com/acme/widgets",
	}, r.ticketCache["ticket-k-live"])
}

func TestAdoptRunningJobs(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name        string
		run         *taskrun.TaskRun
		jobs        []*batchv1.Job
		preTracked  bool
		wantTracked bool
		want        jobRecoveryCounts
	}{
		{
			name:        "a run whose job still exists is restored",
			run:         runningRun("tr-1", "k1", "job-1", now),
			jobs:        []*batchv1.Job{agentJob("job-1")},
			wantTracked: true,
			want:        jobRecoveryCounts{restored: 1},
		},
		{
			name: "a run whose job is gone is left alone",
			run:  runningRun("tr-1", "k1", "job-1", now),
			want: jobRecoveryCounts{jobGone: 1},
		},
		{
			name: "a tournament run is skipped",
			run: func() *taskrun.TaskRun {
				tr := runningRun("tr-1", "k1", "job-1", now)
				tr.TournamentID = "tournament-1"
				return tr
			}(),
			jobs: []*batchv1.Job{agentJob("job-1")},
			want: jobRecoveryCounts{tournament: 1},
		},
		{
			name:        "a key this process already tracks is not replaced",
			run:         runningRun("tr-1", "k1", "job-1", now),
			jobs:        []*batchv1.Job{agentJob("job-1")},
			preTracked:  true,
			wantTracked: true,
			want:        jobRecoveryCounts{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, j := range tt.jobs {
				_, err := client.BatchV1().Jobs(recoveryNamespace).Create(context.Background(), j, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			r := NewReconciler(&config.Config{}, testLogger(),
				WithK8sClient(client),
				WithNamespace(recoveryNamespace),
			)
			current := &taskrun.TaskRun{ID: "tr-current", IdempotencyKey: "k1", State: taskrun.StateRunning}
			if tt.preTracked {
				r.taskRuns["k1"] = current
			}

			got := r.adoptRunningJobs(context.Background(), []*taskrun.TaskRun{tt.run})

			assert.Equal(t, tt.want, got)
			tracked, ok := r.GetTaskRun("k1")
			assert.Equal(t, tt.wantTracked, ok)
			if tt.preTracked {
				assert.Same(t, current, tracked, "the run this process started must be kept")
			}
		})
	}
}
