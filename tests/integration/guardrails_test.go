//go:build integration

package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockEngine implements engine.ExecutionEngine, returning a fixed ExecutionSpec
// suitable for unit and integration testing.
type mockEngine struct {
	name    string
	specErr error
}

func (m *mockEngine) BuildExecutionSpec(_ engine.Task, _ engine.EngineConfig) (*engine.ExecutionSpec, error) {
	if m.specErr != nil {
		return nil, m.specErr
	}
	return &engine.ExecutionSpec{
		Image:                 "test-image:latest",
		Command:               []string{"echo", "hello"},
		Env:                   map[string]string{"TEST": "true"},
		ActiveDeadlineSeconds: 3600,
	}, nil
}

func (m *mockEngine) BuildPrompt(task engine.Task) (string, error) {
	return "test prompt for " + task.Title, nil
}

func (m *mockEngine) Name() string          { return m.name }
func (m *mockEngine) InterfaceVersion() int { return 1 }

// mockTicketing implements ticketing.Backend, tracking all state-change calls
// so that tests can assert which ticket transitions occurred.
type mockTicketing struct {
	tickets        []ticketing.Ticket
	pollErr        error
	markedProgress []string
	markedComplete []string
	markedFailed   []string
	comments       map[string][]string
}

func newMockTicketing(tickets []ticketing.Ticket) *mockTicketing {
	return &mockTicketing{
		tickets:  tickets,
		comments: make(map[string][]string),
	}
}

func (m *mockTicketing) PollReadyTickets(_ context.Context) ([]ticketing.Ticket, error) {
	if m.pollErr != nil {
		return nil, m.pollErr
	}
	return m.tickets, nil
}

func (m *mockTicketing) MarkInProgress(_ context.Context, ticketID string) error {
	m.markedProgress = append(m.markedProgress, ticketID)
	return nil
}

func (m *mockTicketing) MarkComplete(_ context.Context, ticketID string, _ engine.TaskResult) error {
	m.markedComplete = append(m.markedComplete, ticketID)
	return nil
}

func (m *mockTicketing) MarkFailed(_ context.Context, ticketID string, _ string) error {
	m.markedFailed = append(m.markedFailed, ticketID)
	return nil
}

func (m *mockTicketing) AddComment(_ context.Context, ticketID string, comment string) error {
	m.comments[ticketID] = append(m.comments[ticketID], comment)
	return nil
}

func (m *mockTicketing) Name() string          { return "mock" }
func (m *mockTicketing) InterfaceVersion() int { return 1 }

// mockJobBuilder implements controller.JobBuilder, returning a minimal
// batch/v1 Job without any real container configuration.
type mockJobBuilder struct {
	buildErr error
}

func (m *mockJobBuilder) Build(taskRunID string, _ string, spec *engine.ExecutionSpec) (*batchv1.Job, error) {
	if m.buildErr != nil {
		return nil, m.buildErr
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "job-" + taskRunID,
			Namespace: "test-ns",
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "agent",
							Image:   spec.Image,
							Command: spec.Command,
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}, nil
}

// testLogger returns a logger that suppresses output below WARN level, keeping
// test output clean while preserving visibility of genuine warnings.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// ---------------------------------------------------------------------------
// Guard-rail tests
// ---------------------------------------------------------------------------

// TestGuardRailsRejectDisallowedRepoViaReconciler verifies that a ticket whose
// repository URL does not match the AllowedRepos glob is rejected with a
// MarkFailed call and no Kubernetes Job is created.
func TestGuardRailsRejectDisallowedRepoViaReconciler(t *testing.T) {
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "test-engine"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
			AllowedRepos:          []string{"https://github.com/allowed/*"},
		},
	}

	ticket := ticketing.Ticket{
		ID:      "TICKET-forbidden",
		Title:   "Fix something",
		RepoURL: "https://github.com/forbidden/repo",
	}

	tb := newMockTicketing([]ticketing.Ticket{ticket})
	eng := &mockEngine{name: "test-engine"}
	jb := &mockJobBuilder{}
	k8s := fake.NewSimpleClientset()

	r := controller.NewReconciler(cfg, testLogger(),
		controller.WithEngine(eng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	// ProcessTicket returns nil after marking the ticket as failed via the backend.
	require.NoError(t, err)

	// The ticket should have been rejected by the guard rails.
	assert.Contains(t, tb.markedFailed, "TICKET-forbidden",
		"ticket with disallowed repo should be marked as failed")
	assert.Empty(t, tb.markedProgress,
		"disallowed ticket should not be marked in-progress")

	// No Kubernetes Job should have been created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items, "no k8s job should be created for a rejected ticket")
}

// TestGuardRailsConcurrentLimitSkipsPolling verifies that when the active job
// count equals MaxConcurrentJobs, the reconciliation loop skips polling the
// ticketing backend so no additional jobs are created.
func TestGuardRailsConcurrentLimitSkipsPolling(t *testing.T) {
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "test-engine"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     1,
			MaxJobDurationMinutes: 120,
		},
	}

	ticket1 := ticketing.Ticket{
		ID:    "T-concurrent-1",
		Title: "First ticket",
	}
	ticket2 := ticketing.Ticket{
		ID:    "T-concurrent-2",
		Title: "Second ticket",
	}

	// Ticketing initially returns only ticket-2; ticket-1 is processed directly.
	tb := newMockTicketing([]ticketing.Ticket{ticket2})
	eng := &mockEngine{name: "test-engine"}
	jb := &mockJobBuilder{}
	k8s := fake.NewSimpleClientset()

	r := controller.NewReconciler(cfg, testLogger(),
		controller.WithEngine(eng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// Process ticket-1 to fill the single available concurrent slot.
	err := r.ProcessTicket(ctx, ticket1)
	require.NoError(t, err)

	// Run the reconciliation loop briefly. Every tick should detect that the
	// active job count equals the limit and skip polling for ticket-2.
	runCtx, runCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer runCancel()

	errCh := make(chan error, 1)
	go func() { errCh <- r.Run(runCtx, time.Millisecond) }()
	<-errCh // Wait for the loop to exit after context cancellation.

	// Only the job for ticket-1 should exist; ticket-2 must not have been polled.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "only the first job should exist when at the concurrent limit")
}

// TestGuardRailsWildcardRepoMatch verifies that a ticket whose repository URL
// matches an AllowedRepos glob pattern is accepted and progressed normally.
func TestGuardRailsWildcardRepoMatch(t *testing.T) {
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "test-engine"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
			AllowedRepos:          []string{"https://github.com/org/*"},
		},
	}

	ticket := ticketing.Ticket{
		ID:      "TICKET-wildcard",
		Title:   "Implement feature",
		RepoURL: "https://github.com/org/myrepo",
	}

	tb := newMockTicketing([]ticketing.Ticket{ticket})
	eng := &mockEngine{name: "test-engine"}
	jb := &mockJobBuilder{}
	k8s := fake.NewSimpleClientset()

	r := controller.NewReconciler(cfg, testLogger(),
		controller.WithEngine(eng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// The ticket matched the wildcard pattern so it should proceed.
	assert.Contains(t, tb.markedProgress, "TICKET-wildcard",
		"ticket matching wildcard repo pattern should be marked in-progress")
	assert.Empty(t, tb.markedFailed,
		"ticket matching allowed repo pattern should not be marked failed")

	// A Kubernetes Job should have been created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "one job should be created for the matched ticket")
}
