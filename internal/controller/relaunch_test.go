package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func retryingTaskRunWithFailedResult() *taskrun.TaskRun {
	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	tr.CurrentEngine = "claude-code"
	tr.EngineAttempts = []string{"claude-code"}
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)
	_ = tr.Transition(taskrun.StateRetrying)
	tr.Result = &engine.TaskResult{Success: false, Summary: "could not clone the repository"}
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
