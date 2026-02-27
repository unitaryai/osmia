package controller

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

	"github.com/unitaryai/robodev/internal/config"
	"github.com/unitaryai/robodev/internal/taskrun"
	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// mockEngine implements engine.ExecutionEngine for testing.
type mockEngine struct {
	name    string
	specErr error
}

func (m *mockEngine) BuildExecutionSpec(task engine.Task, cfg engine.EngineConfig) (*engine.ExecutionSpec, error) {
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

func (m *mockEngine) Name() string         { return m.name }
func (m *mockEngine) InterfaceVersion() int { return 1 }

// mockTicketing implements ticketing.Backend for testing.
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

func (m *mockTicketing) Name() string         { return "mock" }
func (m *mockTicketing) InterfaceVersion() int { return 1 }

// mockJobBuilder implements JobBuilder for testing.
type mockJobBuilder struct {
	buildErr error
}

func (m *mockJobBuilder) Build(taskRunID string, engineName string, spec *engine.ExecutionSpec) (*batchv1.Job, error) {
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

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func testConfig() *config.Config {
	return &config.Config{
		Engines: config.EnginesConfig{
			Default: "claude-code",
		},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}
}

func TestNewReconciler(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()

	r := NewReconciler(cfg, logger)
	require.NotNil(t, r)
	assert.Equal(t, cfg, r.config)
	assert.NotNil(t, r.engines)
	assert.NotNil(t, r.taskRuns)
}

func TestReconcilerOptions(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()

	eng := &mockEngine{name: "test-engine"}
	tb := newMockTicketing(nil)
	jb := &mockJobBuilder{}
	k8s := fake.NewSimpleClientset()

	r := NewReconciler(cfg, logger,
		WithEngine(eng),
		WithTicketing(tb),
		WithJobBuilder(jb),
		WithK8sClient(k8s),
		WithNamespace("test-ns"),
	)

	assert.NotNil(t, r.engines["test-engine"])
	assert.NotNil(t, r.ticketing)
	assert.NotNil(t, r.jobBuilder)
	assert.NotNil(t, r.k8sClient)
	assert.Equal(t, "test-ns", r.namespace)
}

func TestProcessTicket(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()
	k8s := fake.NewSimpleClientset()

	ticket := ticketing.Ticket{
		ID:          "TICKET-1",
		Title:       "Fix the bug",
		Description: "There is a bug in the login flow",
		RepoURL:     "https://github.com/org/repo",
		Labels:      []string{"robodev"},
	}

	tb := newMockTicketing([]ticketing.Ticket{ticket})
	eng := &mockEngine{name: "claude-code"}
	jb := &mockJobBuilder{}

	r := NewReconciler(cfg, logger,
		WithEngine(eng),
		WithTicketing(tb),
		WithJobBuilder(jb),
		WithK8sClient(k8s),
		WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.processTicket(ctx, ticket)
	require.NoError(t, err)

	// Verify ticket was marked in progress.
	assert.Contains(t, tb.markedProgress, "TICKET-1")

	// Verify a TaskRun was created.
	tr, ok := r.GetTaskRun("TICKET-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.Equal(t, "claude-code", tr.Engine)
	assert.NotEmpty(t, tr.JobName)

	// Verify the K8s Job was created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1)
}

func TestProcessTicketIdempotency(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()
	k8s := fake.NewSimpleClientset()

	ticket := ticketing.Ticket{
		ID:    "TICKET-2",
		Title: "Another bug",
	}

	tb := newMockTicketing([]ticketing.Ticket{ticket})
	eng := &mockEngine{name: "claude-code"}
	jb := &mockJobBuilder{}

	r := NewReconciler(cfg, logger,
		WithEngine(eng),
		WithTicketing(tb),
		WithJobBuilder(jb),
		WithK8sClient(k8s),
		WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// First processing.
	err := r.processTicket(ctx, ticket)
	require.NoError(t, err)

	// Second processing should be skipped (idempotency).
	err = r.processTicket(ctx, ticket)
	require.NoError(t, err)

	// Only one job should exist.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1)
}

func TestValidateGuardRails(t *testing.T) {
	tests := []struct {
		name      string
		config    config.GuardRailsConfig
		ticket    ticketing.Ticket
		expectErr bool
	}{
		{
			name:   "no guard rails configured",
			config: config.GuardRailsConfig{},
			ticket: ticketing.Ticket{
				ID:      "T-1",
				RepoURL: "https://github.com/any/repo",
			},
			expectErr: false,
		},
		{
			name: "allowed repo passes",
			config: config.GuardRailsConfig{
				AllowedRepos: []string{"https://github.com/org/*"},
			},
			ticket: ticketing.Ticket{
				ID:      "T-2",
				RepoURL: "https://github.com/org/repo",
			},
			expectErr: false,
		},
		{
			name: "disallowed repo fails",
			config: config.GuardRailsConfig{
				AllowedRepos: []string{"https://github.com/org/*"},
			},
			ticket: ticketing.Ticket{
				ID:      "T-3",
				RepoURL: "https://github.com/other/repo",
			},
			expectErr: true,
		},
		{
			name: "allowed task type passes",
			config: config.GuardRailsConfig{
				AllowedTaskTypes: []string{"bug_fix", "test_fix"},
			},
			ticket: ticketing.Ticket{
				ID:         "T-4",
				TicketType: "bug_fix",
			},
			expectErr: false,
		},
		{
			name: "disallowed task type fails",
			config: config.GuardRailsConfig{
				AllowedTaskTypes: []string{"bug_fix"},
			},
			ticket: ticketing.Ticket{
				ID:         "T-5",
				TicketType: "deployment",
			},
			expectErr: true,
		},
		{
			name: "empty ticket type with restrictions passes",
			config: config.GuardRailsConfig{
				AllowedTaskTypes: []string{"bug_fix"},
			},
			ticket: ticketing.Ticket{
				ID:         "T-6",
				TicketType: "",
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				config: &config.Config{GuardRails: tt.config},
				logger: testLogger(),
			}
			err := r.validateGuardRails(tt.ticket)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestActiveJobCount(t *testing.T) {
	r := &Reconciler{
		taskRuns: map[string]*taskrun.TaskRun{
			"key-1": {State: taskrun.StateRunning},
			"key-2": {State: taskrun.StateQueued},
			"key-3": {State: taskrun.StateSucceeded},
			"key-4": {State: taskrun.StateNeedsHuman},
			"key-5": {State: taskrun.StateFailed, RetryCount: 1, MaxRetries: 1},
		},
	}

	// Running and NeedsHuman count as active; Queued, Succeeded, and terminal Failed do not.
	count := r.activeJobCount()
	assert.Equal(t, 2, count)
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		expect  bool
	}{
		{"*", "anything", true},
		{"org/*", "org/repo", true},
		{"org/*", "other/repo", false},
		{"*/repo", "org/repo", true},
		{"*/repo", "org/other", false},
		{"exact", "exact", true},
		{"exact", "other", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.value, func(t *testing.T) {
			assert.Equal(t, tt.expect, matchGlob(tt.pattern, tt.value))
		})
	}
}

func TestReconcileOnce_ConcurrentJobLimit(t *testing.T) {
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs: 1,
		},
	}
	logger := testLogger()

	ticket := ticketing.Ticket{ID: "T-1", Title: "Test"}
	tb := newMockTicketing([]ticketing.Ticket{ticket})
	eng := &mockEngine{name: "claude-code"}
	jb := &mockJobBuilder{}
	k8s := fake.NewSimpleClientset()

	r := NewReconciler(cfg, logger,
		WithEngine(eng),
		WithTicketing(tb),
		WithJobBuilder(jb),
		WithK8sClient(k8s),
		WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// First reconcile should process the ticket.
	err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Len(t, tb.markedProgress, 1)

	// Second reconcile should skip due to concurrent limit.
	tb.tickets = []ticketing.Ticket{{ID: "T-2", Title: "Another"}}
	err = r.reconcileOnce(ctx)
	require.NoError(t, err)
	// T-2 should not be processed because T-1 is still running.
	assert.Len(t, tb.markedProgress, 1)
}

func TestHandleJobComplete(t *testing.T) {
	logger := testLogger()
	tb := newMockTicketing(nil)

	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)

	r := &Reconciler{
		config:    testConfig(),
		logger:    logger,
		ticketing: tb,
		taskRuns:  map[string]*taskrun.TaskRun{"key-1": tr},
	}

	ctx := context.Background()
	r.handleJobComplete(ctx, tr)

	assert.Equal(t, taskrun.StateSucceeded, tr.State)
	assert.NotNil(t, tr.Result)
	assert.True(t, tr.Result.Success)
	assert.Contains(t, tb.markedComplete, "TICKET-1")
}

func TestHandleJobFailed_WithRetry(t *testing.T) {
	logger := testLogger()
	tb := newMockTicketing(nil)

	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)

	r := &Reconciler{
		config:    testConfig(),
		logger:    logger,
		ticketing: tb,
		taskRuns:  map[string]*taskrun.TaskRun{"key-1": tr},
	}

	ctx := context.Background()
	r.handleJobFailed(ctx, tr, "out of memory")

	// Should be retrying, not terminal failed.
	assert.Equal(t, taskrun.StateRetrying, tr.State)
	assert.Equal(t, 1, tr.RetryCount)
	assert.Empty(t, tb.markedFailed)
}

func TestHandleJobFailed_NoRetries(t *testing.T) {
	logger := testLogger()
	tb := newMockTicketing(nil)

	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	tr.RetryCount = 1 // Already used the retry.

	r := &Reconciler{
		config:    testConfig(),
		logger:    logger,
		ticketing: tb,
		taskRuns:  map[string]*taskrun.TaskRun{"key-1": tr},
	}

	ctx := context.Background()
	r.handleJobFailed(ctx, tr, "permanent failure")

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.Contains(t, tb.markedFailed, "TICKET-1")
}

func TestRunContextCancellation(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()
	tb := newMockTicketing(nil)

	r := NewReconciler(cfg, logger, WithTicketing(tb))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := r.Run(ctx, 10*time.Second)
	assert.ErrorIs(t, err, context.Canceled)
}
