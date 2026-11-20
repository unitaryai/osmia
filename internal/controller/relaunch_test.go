package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// TestRelaunchClearsPreviousResult checks that each way of starting a new
// Job for a task run drops the result of the attempt it replaces. A kept
// failed result would be read as the new attempt's own when that attempt
// ends without one, and handleJobComplete would then fail a run that may
// have succeeded.
func TestRelaunchClearsPreviousResult(t *testing.T) {
	tests := []struct {
		name   string
		launch func(r *Reconciler, tr *taskrun.TaskRun)
	}{
		{
			name: "retry",
			launch: func(r *Reconciler, tr *taskrun.TaskRun) {
				r.launchRetryJob(context.Background(), tr, "")
			},
		},
		{
			name: "fallback",
			launch: func(r *Reconciler, tr *taskrun.TaskRun) {
				r.launchFallbackJob(context.Background(), tr, "codex")
			},
		},
		{
			name: "continuation",
			launch: func(r *Reconciler, tr *taskrun.TaskRun) {
				r.launchContinuationJob(context.Background(), tr)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := retryingTaskRunWithFailedResult()
			r := relaunchReconciler(tr, fake.NewSimpleClientset())

			tt.launch(r, tr)

			require.NotEmpty(t, tr.JobName, "the new Job was not launched")
			assert.Nil(t, tr.Result)
			assert.Equal(t, priorBranch, tr.PriorBranchName)
			assert.Equal(t, priorMR, tr.PriorMergeRequestURL)
		})
	}
}

// TestLaunchRetryJobKeepsResultWhenJobCreateFails checks that the previous
// result survives a launch that never created a Job, since nothing replaced
// that attempt.
func TestLaunchRetryJobKeepsResultWhenJobCreateFails(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	k8s.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server unavailable")
	})
	tr := retryingTaskRunWithFailedResult()
	previous := tr.Result
	r := relaunchReconciler(tr, k8s)

	r.launchRetryJob(context.Background(), tr, "")

	assert.Same(t, previous, tr.Result)
}

const (
	priorBranch = "sc-TICKET-1"
	priorMR     = "https://gitlab.com/org/repo/-/merge_requests/7"
)

// TestSecondRetryKeepsPriorWork reproduces review on #111: the first retry
// clears the result, ends without one of its own, and is retried again.
// The second retry must still be told about the branch and merge request
// the first attempt opened, or the agent can open a second request.
func TestSecondRetryKeepsPriorWork(t *testing.T) {
	tr := retryingTaskRunWithFailedResult()
	tr.MaxRetries = 2
	r := relaunchReconciler(tr, fake.NewSimpleClientset())
	eng := &capturingEngine{mockEngine: mockEngine{name: "claude-code"}}
	r.engines["claude-code"] = eng

	r.launchRetryJob(context.Background(), tr, "")
	require.Nil(t, tr.Result)
	// The retry ends without a result and is retried again.
	require.NoError(t, tr.Transition(taskrun.StateFailed))
	require.NoError(t, tr.Transition(taskrun.StateRetrying))
	tr.RetryCount++
	r.launchRetryJob(context.Background(), tr, "")

	assert.Equal(t, priorBranch, eng.lastTask.PriorBranchName)
	assert.Equal(t, priorMR, eng.lastTask.PriorMergeRequestURL)
}

// TestHandleJobFailedFailsRunWhenRetryCannotStart checks that a retry whose
// Job is never created fails the run and its ticket, rather than leaving it
// in Retrying, a state nothing polls.
func TestHandleJobFailedFailsRunWhenRetryCannotStart(t *testing.T) {
	tb := newMockTicketing(nil)
	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	tr.CurrentEngine = "claude-code"
	// Retries remain after this one, so only marking them spent makes the
	// failed run terminal.
	tr.MaxRetries = 2
	_ = tr.Transition(taskrun.StateRunning)
	k8s := fake.NewSimpleClientset()
	k8s.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server unavailable")
	})
	r := relaunchReconciler(tr, k8s)
	r.ticketing = tb

	r.handleJobFailed(context.Background(), tr, "tests did not pass")

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.Equal(t, []string{"TICKET-1"}, tb.markedFailed)
	assert.Equal(t, []string{"retry could not be started"}, tb.failReasons)
	assert.True(t, tr.IsTerminal(), "a run whose retry never started must not block new triggers")
}

// TestHandleJobCompleteSavesPriorMergeRequest checks that a run which
// succeeds without reporting a merge request is saved with the one an
// earlier attempt opened. The review poller reads it from the store after a
// restart.
func TestHandleJobCompleteSavesPriorMergeRequest(t *testing.T) {
	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	tr.PriorBranchName = priorBranch
	tr.PriorMergeRequestURL = priorMR
	store := &mergeRequestSaveRecorder{MemoryStore: taskrun.NewMemoryStore()}
	r := relaunchReconciler(tr, fake.NewSimpleClientset())
	r.taskRunStore = store

	r.handleJobComplete(context.Background(), tr)

	require.Equal(t, taskrun.StateSucceeded, tr.State)
	require.NotEmpty(t, store.savedMRs)
	assert.Equal(t, priorMR, store.savedMRs[0], "the first save after success must hold the merge request")
}

// mergeRequestSaveRecorder records the merge request URL each Save sees, since
// MemoryStore keeps the pointer and later changes would show through.
type mergeRequestSaveRecorder struct {
	*taskrun.MemoryStore
	savedMRs []string
}

func (s *mergeRequestSaveRecorder) Save(ctx context.Context, tr *taskrun.TaskRun) error {
	mr := ""
	if tr.Result != nil {
		mr = tr.Result.MergeRequestURL
	}
	s.savedMRs = append(s.savedMRs, mr)
	return s.MemoryStore.Save(ctx, tr)
}

// TestCompletedResultCarriesPriorWork checks that a run which succeeds
// without reporting a merge request is recorded with the one an earlier
// attempt opened, so the review poller still watches it.
func TestCompletedResultCarriesPriorWork(t *testing.T) {
	tests := []struct {
		name   string
		result *engine.TaskResult
		wantMR string
	}{
		{name: "no result", result: nil, wantMR: priorMR},
		{name: "result without a merge request", result: &engine.TaskResult{Success: true}, wantMR: priorMR},
		{
			name:   "result with its own merge request",
			result: &engine.TaskResult{Success: true, MergeRequestURL: "https://gitlab.com/org/repo/-/merge_requests/8"},
			wantMR: "https://gitlab.com/org/repo/-/merge_requests/8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
			tr.PriorBranchName = priorBranch
			tr.PriorMergeRequestURL = priorMR
			tr.Result = tt.result

			result := completedResult(tr)

			assert.True(t, result.Success)
			assert.Equal(t, tt.wantMR, result.MergeRequestURL)
			assert.Equal(t, priorBranch, result.BranchName)
		})
	}
}

func retryingTaskRunWithFailedResult() *taskrun.TaskRun {
	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	tr.CurrentEngine = "claude-code"
	tr.EngineAttempts = []string{"claude-code"}
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)
	_ = tr.Transition(taskrun.StateRetrying)
	tr.Result = &engine.TaskResult{
		Success:         false,
		Summary:         "tests still fail",
		BranchName:      priorBranch,
		MergeRequestURL: priorMR,
	}
	return tr
}

func relaunchReconciler(tr *taskrun.TaskRun, k8s *fake.Clientset) *Reconciler {
	return &Reconciler{
		config:    testConfig(),
		logger:    testLogger(),
		k8sClient: k8s,
		engines: map[string]engine.ExecutionEngine{
			"claude-code": &mockEngine{name: "claude-code"},
			"codex":       &mockEngine{name: "codex"},
		},
		jobBuilder:    &mockJobBuilder{},
		taskRuns:      map[string]*taskrun.TaskRun{tr.IdempotencyKey: tr},
		ticketCache:   map[string]ticketing.Ticket{tr.TicketID: {ID: tr.TicketID, Title: "Fix something"}},
		taskRunStore:  taskrun.NewMemoryStore(),
		namespace:     "test-ns",
		streamReaders: make(map[string]context.CancelFunc),
	}
}

// TestFailedRelaunchDeletesStoredJob checks that when a create call errors
// after the API server stored the Job, as a timed-out call can, that Job is
// deleted. The run never records it, so nothing else would stop it once the
// run has failed.
func TestFailedRelaunchDeletesStoredJob(t *testing.T) {
	tests := []struct {
		name   string
		launch func(r *Reconciler, tr *taskrun.TaskRun)
	}{
		{name: "retry", launch: func(r *Reconciler, tr *taskrun.TaskRun) { r.launchRetryJob(context.Background(), tr, "") }},
		{name: "fallback", launch: func(r *Reconciler, tr *taskrun.TaskRun) { r.launchFallbackJob(context.Background(), tr, "codex") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := fake.NewSimpleClientset()
			k8s.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
				require.NoError(t, k8s.Tracker().Add(action.(k8stesting.CreateAction).GetObject()))
				return true, nil, errors.New("context deadline exceeded")
			})
			tr := retryingTaskRunWithFailedResult()
			r := relaunchReconciler(tr, k8s)

			tt.launch(r, tr)

			jobs, err := k8s.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
			require.NoError(t, err)
			assert.Empty(t, jobs.Items)
			assert.Empty(t, tr.JobName)
		})
	}
}
