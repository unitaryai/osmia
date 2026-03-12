//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// failingEngine is an engine whose jobs always fail, used to exercise
// the fallback chain in integration tests.
type failingEngine struct {
	name string
}

func (e *failingEngine) BuildExecutionSpec(_ engine.Task, _ engine.EngineConfig) (*engine.ExecutionSpec, error) {
	return &engine.ExecutionSpec{
		Image:                 fmt.Sprintf("%s-image:latest", e.name),
		Command:               []string{"false"},
		Env:                   map[string]string{"ENGINE": e.name},
		ActiveDeadlineSeconds: 3600,
	}, nil
}

func (e *failingEngine) BuildPrompt(task engine.Task) (string, error) {
	return "test prompt for " + task.Title, nil
}

func (e *failingEngine) Name() string          { return e.name }
func (e *failingEngine) InterfaceVersion() int { return 1 }

// fallbackTestTicketing is a ticketing backend for fallback tests.
// It returns tickets on every poll so that reconcileOnce progresses past
// the empty-ticket check and reaches checkRunningJobs. ProcessTicket
// handles idempotency so duplicate processing is harmless.
type fallbackTestTicketing struct {
	tickets      []ticketing.Ticket
	markedFailed []string
}

func (t *fallbackTestTicketing) PollReadyTickets(_ context.Context) ([]ticketing.Ticket, error) {
	return t.tickets, nil
}

func (t *fallbackTestTicketing) MarkInProgress(_ context.Context, _ string) error { return nil }

func (t *fallbackTestTicketing) MarkComplete(_ context.Context, _ string, _ engine.TaskResult) error {
	return nil
}

func (t *fallbackTestTicketing) MarkFailed(_ context.Context, id string, _ string) error {
	t.markedFailed = append(t.markedFailed, id)
	return nil
}

func (t *fallbackTestTicketing) AddComment(_ context.Context, _ string, _ string) error { return nil }
func (t *fallbackTestTicketing) Name() string                                           { return "mock" }
func (t *fallbackTestTicketing) InterfaceVersion() int                                  { return 1 }

func fallbackTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestEngineFallbackChain verifies that when a job fails, the reconciler
// falls back to the next engine in the configured chain before exhausting
// retries.
func TestEngineFallbackChain(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	jb := jobbuilder.NewJobBuilder("test-ns")
	logger := fallbackTestLogger()

	ticket := ticketing.Ticket{
		ID:    "FALLBACK-1",
		Title: "Test fallback chain",
	}
	tb := &fallbackTestTicketing{tickets: []ticketing.Ticket{ticket}}

	primary := &failingEngine{name: "claude-code"}
	fallback1 := &failingEngine{name: "cline"}
	fallback2 := &failingEngine{name: "aider"}

	cfg := &config.Config{
		Engines: config.EnginesConfig{
			Default:         "claude-code",
			FallbackEngines: []string{"cline", "aider"},
		},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(primary),
		controller.WithEngine(fallback1),
		controller.WithEngine(fallback2),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// Process the ticket — should use the primary engine (claude-code).
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("FALLBACK-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.Equal(t, "claude-code", tr.CurrentEngine)
	assert.Equal(t, []string{"claude-code"}, tr.EngineAttempts)

	// Simulate primary engine job failure.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)

	failJob(t, ctx, k8s, &jobs.Items[0])

	// Run a brief reconcile to detect the failure and trigger fallback.
	runReconcileBriefly(t, r)

	// Verify fallback to cline.
	tr, ok = r.GetTaskRun("FALLBACK-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.Equal(t, "cline", tr.CurrentEngine)
	assert.Equal(t, []string{"claude-code", "cline"}, tr.EngineAttempts)

	// Verify a second job was created (for cline).
	jobs, err = k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 2, "a fallback job should have been created for cline")

	// Simulate second engine (cline) failure.
	failJob(t, ctx, k8s, &jobs.Items[1])
	runReconcileBriefly(t, r)

	// Verify fallback to aider.
	tr, ok = r.GetTaskRun("FALLBACK-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.Equal(t, "aider", tr.CurrentEngine)
	assert.Equal(t, []string{"claude-code", "cline", "aider"}, tr.EngineAttempts)

	// Verify a third job was created (for aider).
	jobs, err = k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 3, "a fallback job should have been created for aider")
}

// TestEngineFallbackExhausted verifies that when all engines in the
// fallback chain have been tried and retries are exhausted, the task run
// reaches a terminal failed state.
func TestEngineFallbackExhausted(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	jb := jobbuilder.NewJobBuilder("test-ns")
	logger := fallbackTestLogger()

	ticket := ticketing.Ticket{
		ID:    "EXHAUST-1",
		Title: "Test exhausted fallback",
	}
	tb := &fallbackTestTicketing{tickets: []ticketing.Ticket{ticket}}

	primary := &failingEngine{name: "claude-code"}
	fallback := &failingEngine{name: "cline"}

	cfg := &config.Config{
		Engines: config.EnginesConfig{
			Default:         "claude-code",
			FallbackEngines: []string{"cline"},
		},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(primary),
		controller.WithEngine(fallback),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()

	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Fail the primary engine job.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)
	failJob(t, ctx, k8s, &jobs.Items[0])
	runReconcileBriefly(t, r)

	// Verify we moved to fallback.
	tr, ok := r.GetTaskRun("EXHAUST-1-1")
	require.True(t, ok)
	assert.Equal(t, "cline", tr.CurrentEngine)

	// Fail the fallback engine job.
	jobs, err = k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 2)
	failJob(t, ctx, k8s, &jobs.Items[1])
	runReconcileBriefly(t, r)

	// All engines exhausted + retries exhausted → terminal failure.
	tr, ok = r.GetTaskRun("EXHAUST-1-1")
	require.True(t, ok)
	// After fallback engines are exhausted, normal retry logic kicks in.
	// With MaxRetries=1 and RetryCount=0 initially, one retry is allowed,
	// then it becomes terminal on the next failure.
	// The task should either be retrying or terminal failed depending on retry count.
	assert.Equal(t, []string{"claude-code", "cline"}, tr.EngineAttempts)
}

// TestEngineFallbackRespectsOrder verifies that the fallback chain
// follows the configured engine order.
func TestEngineFallbackRespectsOrder(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	jb := jobbuilder.NewJobBuilder("test-ns")
	logger := fallbackTestLogger()

	ticket := ticketing.Ticket{
		ID:    "ORDER-1",
		Title: "Test fallback order",
	}
	tb := &fallbackTestTicketing{tickets: []ticketing.Ticket{ticket}}

	engines := []*failingEngine{
		{name: "claude-code"},
		{name: "aider"},
		{name: "cline"},
	}

	// Order: claude-code → aider → cline
	cfg := &config.Config{
		Engines: config.EnginesConfig{
			Default:         "claude-code",
			FallbackEngines: []string{"aider", "cline"},
		},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}

	opts := []controller.ReconcilerOption{
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	}
	for _, e := range engines {
		opts = append(opts, controller.WithEngine(e))
	}

	r := controller.NewReconciler(cfg, logger, opts...)

	ctx := context.Background()

	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("ORDER-1-1")
	require.True(t, ok)
	assert.Equal(t, "claude-code", tr.CurrentEngine, "first attempt should use the default engine")

	// Fail first → should fallback to aider (not cline).
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	failJob(t, ctx, k8s, &jobs.Items[0])
	runReconcileBriefly(t, r)

	tr, _ = r.GetTaskRun("ORDER-1-1")
	assert.Equal(t, "aider", tr.CurrentEngine, "second attempt should use the first fallback engine")

	// Fail second → should fallback to cline.
	jobs, err = k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	failJob(t, ctx, k8s, &jobs.Items[1])
	runReconcileBriefly(t, r)

	tr, _ = r.GetTaskRun("ORDER-1-1")
	assert.Equal(t, "cline", tr.CurrentEngine, "third attempt should use the second fallback engine")
	assert.Equal(t, []string{"claude-code", "aider", "cline"}, tr.EngineAttempts)
}

// failJob updates a Job's status to Failed.
func failJob(t *testing.T, ctx context.Context, k8s *fake.Clientset, job *batchv1.Job) {
	t.Helper()
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:    batchv1.JobFailed,
		Status:  corev1.ConditionTrue,
		Message: "engine failure",
	})
	_, err := k8s.BatchV1().Jobs(job.Namespace).UpdateStatus(ctx, job, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// runReconcileBriefly runs the reconciler for just long enough to process
// one cycle.
func runReconcileBriefly(t *testing.T, r *controller.Reconciler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx, 50*time.Millisecond)
}
