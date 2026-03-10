//go:build integration

// Package integration_test contains Tier 2 integration tests that verify
// the full reconciler pipeline with real engines, real job builders, and
// fake Kubernetes clients.
package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
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
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// reconcilerTestLogger returns a quiet logger for test use.
func reconcilerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// mockReconcilerTicketing implements ticketing.Backend for integration tests,
// with thread-safe tracking of all calls made by the reconciler.
type mockReconcilerTicketing struct {
	mu             sync.Mutex
	tickets        []ticketing.Ticket
	pollCount      int
	maxPolls       int // return tickets for this many polls, then empty (0 = unlimited)
	markedProgress []string
	markedComplete []string
	markedFailed   []string
}

func (m *mockReconcilerTicketing) PollReadyTickets(_ context.Context) ([]ticketing.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pollCount++
	if m.maxPolls > 0 && m.pollCount > m.maxPolls {
		return nil, nil
	}
	return m.tickets, nil
}

func (m *mockReconcilerTicketing) MarkInProgress(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedProgress = append(m.markedProgress, id)
	return nil
}

func (m *mockReconcilerTicketing) MarkComplete(_ context.Context, id string, _ engine.TaskResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedComplete = append(m.markedComplete, id)
	return nil
}

func (m *mockReconcilerTicketing) MarkFailed(_ context.Context, id string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedFailed = append(m.markedFailed, id)
	return nil
}

func (m *mockReconcilerTicketing) AddComment(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockReconcilerTicketing) Name() string          { return "mock" }
func (m *mockReconcilerTicketing) InterfaceVersion() int { return ticketing.InterfaceVersion }

// reconcilerTestConfig returns a standard config for reconciler integration tests.
func reconcilerTestConfig(maxConcurrent int) *config.Config {
	return &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     maxConcurrent,
			MaxJobDurationMinutes: 120,
			AllowedRepos:          []string{"https://github.com/org/*"},
			AllowedTaskTypes:      []string{"issue", "bug", "feature"},
		},
	}
}

// standardTicket returns a ticket that passes all default guard rails.
func standardTicket(id string) ticketing.Ticket {
	return ticketing.Ticket{
		ID:          id,
		Title:       fmt.Sprintf("Test ticket %s", id),
		Description: "Integration test ticket",
		TicketType:  "issue",
		RepoURL:     "https://github.com/org/repo",
		Labels:      []string{"osmia"},
	}
}

// TestReconcilerFullCycle verifies that a ticket flows through the entire
// pipeline: ticketing → engine → job builder → K8s Job → state tracking.
func TestReconcilerFullCycle(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	tb := &mockReconcilerTicketing{tickets: []ticketing.Ticket{standardTicket("42")}, maxPolls: 1}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := reconcilerTestConfig(5)
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, standardTicket("42"))
	require.NoError(t, err)

	// Verify a Job was created in the fake K8s.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1, "expected exactly one Job")

	job := jobs.Items[0]

	// Verify labels.
	assert.Equal(t, "osmia-agent", job.Labels["app"], "Job must carry app=osmia-agent label")
	assert.Equal(t, "claude-code", job.Labels["osmia.io/engine"], "Job must carry engine label")
	assert.NotEmpty(t, job.Labels["osmia.io/task-run-id"], "Job must have task-run-id label")

	// Verify container security context.
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, sc)
	assert.True(t, *sc.RunAsNonRoot)
	assert.True(t, *sc.ReadOnlyRootFilesystem)
	assert.False(t, *sc.AllowPrivilegeEscalation)

	// Verify TaskRun state.
	tr, ok := r.GetTaskRun("42-1")
	require.True(t, ok, "TaskRun must exist for idempotency key")
	assert.Equal(t, taskrun.StateRunning, tr.State)

	// Verify ticketing was notified.
	tb.mu.Lock()
	assert.Contains(t, tb.markedProgress, "42", "ticket must be marked in progress")
	tb.mu.Unlock()
}

// TestReconcilerMultipleTicketsRespectsConcurrencyLimit verifies that the
// reconciler does not exceed the configured concurrent job limit.
func TestReconcilerMultipleTicketsRespectsConcurrencyLimit(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	tb := &mockReconcilerTicketing{
		tickets:  []ticketing.Ticket{standardTicket("1"), standardTicket("2"), standardTicket("3")},
		maxPolls: 1,
	}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := reconcilerTestConfig(2) // limit to 2
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// Process tickets one at a time (as the reconciler does).
	for _, ticket := range tb.tickets {
		_ = r.ProcessTicket(ctx, ticket)
	}

	// Verify only 2 Jobs were created (third should be rejected at engine
	// selection since activeJobCount is checked in reconcileOnce, but
	// ProcessTicket itself does not enforce the limit — so all 3 get created
	// when called directly). Let's verify via the actual reconcile loop instead.

	// Reset with fresh state.
	k8s2 := fake.NewSimpleClientset()
	tb2 := &mockReconcilerTicketing{
		tickets:  []ticketing.Ticket{standardTicket("10"), standardTicket("11"), standardTicket("12")},
		maxPolls: 1,
	}

	r2 := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb2),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s2),
		controller.WithNamespace("test-ns"),
	)

	// Run reconciler for a short time to process via reconcileOnce.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	_ = r2.Run(ctx2, 100*time.Millisecond)

	jobs, err := k8s2.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(jobs.Items), 2,
		"should not exceed concurrency limit of 2, got %d", len(jobs.Items))
}

// TestReconcilerIdempotencyAcrossPolls verifies that the same ticket processed
// twice only results in a single Job.
func TestReconcilerIdempotencyAcrossPolls(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	ticket := standardTicket("99")
	tb := &mockReconcilerTicketing{tickets: []ticketing.Ticket{ticket}, maxPolls: 1}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := reconcilerTestConfig(5)
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// Process the same ticket twice.
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	err = r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Only one Job should exist.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "idempotency should prevent duplicate Jobs")
}

// TestReconcilerJobCompletionTransition verifies that when a Job completes,
// the corresponding TaskRun transitions to Succeeded.
func TestReconcilerJobCompletionTransition(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	ticket := standardTicket("50")
	// Return the ticket for 1 poll only. The initial job is created via direct
	// ProcessTicket call. The Run loop's first poll returns the same ticket
	// (ProcessTicket skips, idempotent), reaches checkRunningJobs which detects
	// the Complete condition. After that, no more tickets prevents re-creation.
	tb := &mockReconcilerTicketing{tickets: []ticketing.Ticket{ticket}, maxPolls: 1}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := reconcilerTestConfig(5)
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// Process the ticket to create a Job.
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("50-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)

	// Find the created Job and update its status to Complete.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)

	job := &jobs.Items[0]
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	})
	_, err = k8s.BatchV1().Jobs("test-ns").UpdateStatus(ctx, job, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Run a short reconcile cycle to pick up the completed Job.
	// Use a very short poll interval so we get at least one reconcileOnce call.
	ctx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = r.Run(ctx2, 50*time.Millisecond)

	// Verify TaskRun reached Succeeded.
	tr, ok = r.GetTaskRun("50-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateSucceeded, tr.State, "TaskRun should transition to Succeeded")

	// Verify ticket was marked complete.
	tb.mu.Lock()
	assert.Contains(t, tb.markedComplete, "50")
	tb.mu.Unlock()
}

// TestReconcilerJobFailureAndRetry verifies that a failed Job triggers
// a retry transition on the TaskRun.
func TestReconcilerJobFailureAndRetry(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	ticket := standardTicket("60")
	// Return ticket for 1 poll only — enough for checkRunningJobs to detect failure.
	tb := &mockReconcilerTicketing{tickets: []ticketing.Ticket{ticket}, maxPolls: 1}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := reconcilerTestConfig(5)
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Find the Job and mark it Failed.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)

	job := &jobs.Items[0]
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:    batchv1.JobFailed,
		Status:  corev1.ConditionTrue,
		Message: "out of memory",
	})
	_, err = k8s.BatchV1().Jobs("test-ns").UpdateStatus(ctx, job, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Run reconcile cycle to detect the failure.
	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.Run(ctx2, 100*time.Millisecond)

	// TaskRun should be in Retrying state (first failure, MaxRetries=1).
	tr, ok := r.GetTaskRun("60-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRetrying, tr.State, "TaskRun should be retrying after first failure")
	assert.Equal(t, 1, tr.RetryCount)
}

// TestReconcilerJobFailureExhaustsRetries verifies that after all retries
// are exhausted, the TaskRun reaches a terminal Failed state.
func TestReconcilerJobFailureExhaustsRetries(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	ticket := standardTicket("70")
	// Return ticket for 1 poll only — enough for checkRunningJobs to detect failure.
	tb := &mockReconcilerTicketing{tickets: []ticketing.Ticket{ticket}, maxPolls: 1}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := reconcilerTestConfig(5)
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Manually set RetryCount to MaxRetries so next failure is terminal.
	tr, ok := r.GetTaskRun("70-1")
	require.True(t, ok)
	tr.RetryCount = tr.MaxRetries // exhaust retries

	// Mark the Job as failed.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)

	job := &jobs.Items[0]
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:    batchv1.JobFailed,
		Status:  corev1.ConditionTrue,
		Message: "permanent failure",
	})
	_, err = k8s.BatchV1().Jobs("test-ns").UpdateStatus(ctx, job, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Run reconcile to detect the failure.
	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.Run(ctx2, 100*time.Millisecond)

	// TaskRun should be terminal Failed.
	tr, ok = r.GetTaskRun("70-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateFailed, tr.State, "TaskRun should be terminal Failed")
	assert.True(t, tr.IsTerminal(), "TaskRun should be terminal")

	// Ticket should be marked failed.
	tb.mu.Lock()
	assert.Contains(t, tb.markedFailed, "70")
	tb.mu.Unlock()
}
