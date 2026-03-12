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

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/memory"
	"github.com/unitaryai/osmia/internal/prm"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
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

func (m *mockEngine) Name() string          { return m.name }
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

func (m *mockTicketing) Name() string          { return "mock" }
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
		Labels:      []string{"osmia"},
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
	err := r.ProcessTicket(ctx, ticket)
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
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Second processing should be skipped (idempotency).
	err = r.ProcessTicket(ctx, ticket)
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
		config:       testConfig(),
		logger:       logger,
		ticketing:    tb,
		taskRuns:     map[string]*taskrun.TaskRun{"key-1": tr},
		taskRunStore: taskrun.NewMemoryStore(),
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
		config:       testConfig(),
		logger:       logger,
		ticketing:    tb,
		taskRuns:     map[string]*taskrun.TaskRun{"key-1": tr},
		taskRunStore: taskrun.NewMemoryStore(),
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
		config:       testConfig(),
		logger:       logger,
		ticketing:    tb,
		taskRuns:     map[string]*taskrun.TaskRun{"key-1": tr},
		taskRunStore: taskrun.NewMemoryStore(),
	}

	ctx := context.Background()
	r.handleJobFailed(ctx, tr, "permanent failure")

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.Contains(t, tb.markedFailed, "TICKET-1")
}

func TestProcessTicket_PreStartApprovalGate(t *testing.T) {
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
			ApprovalGates:         []string{"pre_start"},
		},
	}
	logger := testLogger()
	k8s := fake.NewSimpleClientset()

	ticket := ticketing.Ticket{
		ID:    "TICKET-GATE",
		Title: "Fix the thing",
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
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Verify the TaskRun is held in NeedsHuman, not Running.
	tr, ok := r.GetTaskRun("TICKET-GATE-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateNeedsHuman, tr.State)
	assert.Equal(t, "approve task start?", tr.HumanQuestion)

	// Verify no K8s Job was created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items)

	// Verify ticket was NOT marked in progress (no job started).
	assert.Empty(t, tb.markedProgress)
}

func TestProcessTicket_NoApprovalGate(t *testing.T) {
	cfg := testConfig() // No approval gates.
	logger := testLogger()
	k8s := fake.NewSimpleClientset()

	ticket := ticketing.Ticket{
		ID:    "TICKET-NO-GATE",
		Title: "Normal ticket",
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
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Verify the TaskRun is Running (not held).
	tr, ok := r.GetTaskRun("TICKET-NO-GATE-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)

	// Verify a K8s Job was created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1)
}

func TestProcessTicket_TaskRunStorePersistence(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()
	k8s := fake.NewSimpleClientset()
	store := taskrun.NewMemoryStore()

	ticket := ticketing.Ticket{
		ID:    "TICKET-STORE",
		Title: "Test store persistence",
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
		WithTaskRunStore(store),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Verify the TaskRun was persisted to the store.
	runs, err := store.ListByTicketID(ctx, "TICKET-STORE")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, taskrun.StateRunning, runs[0].State)
}

func TestHandleJobComplete_PreMergeGate(t *testing.T) {
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
			ApprovalGates:         []string{"pre_merge"},
		},
	}
	logger := testLogger()
	tb := newMockTicketing(nil)
	store := taskrun.NewMemoryStore()

	tr := taskrun.New("tr-merge", "key-merge", "TICKET-MERGE", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)

	r := &Reconciler{
		config:       cfg,
		logger:       logger,
		ticketing:    tb,
		taskRuns:     map[string]*taskrun.TaskRun{"key-merge": tr},
		taskRunStore: store,
	}

	ctx := context.Background()
	r.handleJobComplete(ctx, tr)

	// Should be in NeedsHuman for pre-merge approval, not Succeeded.
	assert.Equal(t, taskrun.StateNeedsHuman, tr.State)
	assert.Equal(t, "approve merge of completed task?", tr.HumanQuestion)

	// Ticket should NOT be marked complete yet.
	assert.Empty(t, tb.markedComplete)
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

func TestWithPRMConfig(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()

	prmCfg := prm.Config{
		Enabled:                true,
		EvaluationInterval:     5,
		WindowSize:             10,
		ScoreThresholdNudge:    7,
		ScoreThresholdEscalate: 3,
		HintFilePath:           "/workspace/.hint.md",
		MaxTrajectoryLength:    50,
	}

	r := NewReconciler(cfg, logger, WithPRMConfig(prmCfg))
	require.NotNil(t, r)
	assert.True(t, r.prmConfig.Enabled)
	assert.Equal(t, 5, r.prmConfig.EvaluationInterval)
	assert.NotNil(t, r.prmEvaluators)
}

func TestWithPRMConfigDisabled(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()

	r := NewReconciler(cfg, logger)
	assert.False(t, r.prmConfig.Enabled)
	assert.NotNil(t, r.prmEvaluators)
}

func TestWithMemory(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()

	graph := memory.NewGraph(nil, logger)
	extractor := memory.NewExtractor(logger)
	qe := memory.NewQueryEngine(graph, logger)

	r := NewReconciler(cfg, logger, WithMemory(graph, extractor, qe))
	require.NotNil(t, r)
	assert.NotNil(t, r.memoryGraph)
	assert.NotNil(t, r.memoryExtractor)
	assert.NotNil(t, r.memoryQuery)
}

func TestWithMemoryNotSet(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()

	r := NewReconciler(cfg, logger)
	assert.Nil(t, r.memoryGraph)
	assert.Nil(t, r.memoryExtractor)
	assert.Nil(t, r.memoryQuery)
}

func TestProcessTicketWithMemoryContext(t *testing.T) {
	cfg := testConfig()
	logger := testLogger()
	k8s := fake.NewSimpleClientset()

	// Set up memory with a prior fact.
	graph := memory.NewGraph(nil, logger)
	ctx := context.Background()
	require.NoError(t, graph.AddNode(ctx, &memory.Fact{
		ID:         "test-fact",
		Content:    "known issue with authentication",
		FactKind:   memory.FactTypeFailurePattern,
		Confidence: 0.9,
		DecayRate:  0.01,
		ValidFrom:  time.Now(),
	}))

	extractor := memory.NewExtractor(logger)
	qe := memory.NewQueryEngine(graph, logger)

	ticket := ticketing.Ticket{
		ID:          "MEM-1",
		Title:       "Fix auth issue",
		Description: "Authentication is broken",
	}

	tb := newMockTicketing(nil)
	eng := &mockEngine{name: "claude-code"}
	jb := &mockJobBuilder{}

	r := NewReconciler(cfg, logger,
		WithEngine(eng),
		WithTicketing(tb),
		WithJobBuilder(jb),
		WithK8sClient(k8s),
		WithNamespace("test-ns"),
		WithMemory(graph, extractor, qe),
	)

	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("MEM-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)
}

func TestHandleJobCompleteWithMemory(t *testing.T) {
	logger := testLogger()
	tb := newMockTicketing(nil)

	graph := memory.NewGraph(nil, logger)
	extractor := memory.NewExtractor(logger)

	tr := taskrun.New("tr-mem", "key-mem", "TICKET-MEM", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)

	r := &Reconciler{
		config:          testConfig(),
		logger:          logger,
		ticketing:       tb,
		taskRuns:        map[string]*taskrun.TaskRun{"key-mem": tr},
		taskRunStore:    taskrun.NewMemoryStore(),
		prmEvaluators:   make(map[string]*prm.Evaluator),
		memoryGraph:     graph,
		memoryExtractor: extractor,
	}

	ctx := context.Background()
	r.handleJobComplete(ctx, tr)

	assert.Equal(t, taskrun.StateSucceeded, tr.State)

	// Give the background goroutine time to extract.
	time.Sleep(50 * time.Millisecond)

	// Memory extraction runs in a goroutine; verify it added nodes.
	assert.Greater(t, graph.NodeCount(), 0,
		"memory should have extracted at least one node from completed task")
}

func TestHandleJobFailedWithMemory(t *testing.T) {
	logger := testLogger()
	tb := newMockTicketing(nil)

	graph := memory.NewGraph(nil, logger)
	extractor := memory.NewExtractor(logger)

	tr := taskrun.New("tr-fail-mem", "key-fail-mem", "TICKET-FAIL-MEM", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	tr.RetryCount = 1 // Exhaust retries.

	r := &Reconciler{
		config:          testConfig(),
		logger:          logger,
		ticketing:       tb,
		taskRuns:        map[string]*taskrun.TaskRun{"key-fail-mem": tr},
		taskRunStore:    taskrun.NewMemoryStore(),
		prmEvaluators:   make(map[string]*prm.Evaluator),
		memoryGraph:     graph,
		memoryExtractor: extractor,
	}

	ctx := context.Background()
	r.handleJobFailed(ctx, tr, "test failure")

	assert.Equal(t, taskrun.StateFailed, tr.State)

	// Give the background goroutine time to extract.
	time.Sleep(50 * time.Millisecond)

	assert.Greater(t, graph.NodeCount(), 0,
		"memory should have extracted at least one node from failed task")
}

func TestResolveApproval_UnknownTaskRun(t *testing.T) {
	r := &Reconciler{
		logger:   testLogger(),
		taskRuns: map[string]*taskrun.TaskRun{},
	}

	err := r.ResolveApproval(context.Background(), "nonexistent", true, "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveApproval_WrongState(t *testing.T) {
	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)

	r := &Reconciler{
		logger:   testLogger(),
		taskRuns: map[string]*taskrun.TaskRun{"key-1": tr},
	}

	err := r.ResolveApproval(context.Background(), "tr-1", true, "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not NeedsHuman")
}

func TestResolveApproval_Rejection(t *testing.T) {
	tr := taskrun.New("tr-rej", "key-rej", "TICKET-REJ", "claude-code")
	_ = tr.Transition(taskrun.StateNeedsHuman)
	tr.ApprovalGateType = "pre_start"

	tb := newMockTicketing(nil)
	store := taskrun.NewMemoryStore()

	r := &Reconciler{
		config:       testConfig(),
		logger:       testLogger(),
		ticketing:    tb,
		taskRuns:     map[string]*taskrun.TaskRun{"key-rej": tr},
		taskRunStore: store,
	}

	err := r.ResolveApproval(context.Background(), "tr-rej", false, "bob")
	require.NoError(t, err)
	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.Contains(t, tb.markedFailed, "TICKET-REJ")
}

// capturingEngine wraps mockEngine and captures the last task passed to
// BuildExecutionSpec, enabling assertion on PriorBranchName in retry tests.
type capturingEngine struct {
	mockEngine
	lastTask engine.Task
}

func (c *capturingEngine) BuildExecutionSpec(task engine.Task, cfg engine.EngineConfig) (*engine.ExecutionSpec, error) {
	c.lastTask = task
	return c.mockEngine.BuildExecutionSpec(task, cfg)
}

func TestLaunchRetryJob_SetsProirBranchFromResult(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	eng := &capturingEngine{mockEngine: mockEngine{name: "claude-code"}}
	jb := &mockJobBuilder{}

	tr := taskrun.New("tr-1", "key-1", "TICKET-1", "claude-code")
	tr.CurrentEngine = "claude-code"
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)
	_ = tr.Transition(taskrun.StateRetrying)
	tr.RetryCount = 1
	tr.Result = &engine.TaskResult{
		Success:    false,
		BranchName: "osmia/TICKET-1",
	}

	ticket := ticketing.Ticket{
		ID:      "TICKET-1",
		Title:   "Fix something",
		RepoURL: "https://github.com/org/repo",
	}

	r := &Reconciler{
		config:        testConfig(),
		logger:        testLogger(),
		k8sClient:     k8s,
		engines:       map[string]engine.ExecutionEngine{"claude-code": eng},
		jobBuilder:    jb,
		taskRuns:      map[string]*taskrun.TaskRun{"key-1": tr},
		ticketCache:   map[string]ticketing.Ticket{"TICKET-1": ticket},
		taskRunStore:  taskrun.NewMemoryStore(),
		namespace:     "test-ns",
		streamReaders: make(map[string]context.CancelFunc),
	}

	ctx := context.Background()
	r.launchRetryJob(ctx, tr, "")

	assert.Equal(t, "osmia/TICKET-1", eng.lastTask.PriorBranchName)
}

func TestLaunchRetryJob_FallbackBranchWhenNoResult(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	eng := &capturingEngine{mockEngine: mockEngine{name: "claude-code"}}
	jb := &mockJobBuilder{}

	tr := taskrun.New("tr-2", "key-2", "TICKET-42", "claude-code")
	tr.CurrentEngine = "claude-code"
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)
	_ = tr.Transition(taskrun.StateRetrying)
	tr.RetryCount = 1
	// No Result set — simulates pod killed before on-complete.sh ran.

	ticket := ticketing.Ticket{
		ID:      "TICKET-42",
		Title:   "Add feature",
		RepoURL: "https://github.com/org/repo",
	}

	r := &Reconciler{
		config:        testConfig(),
		logger:        testLogger(),
		k8sClient:     k8s,
		engines:       map[string]engine.ExecutionEngine{"claude-code": eng},
		jobBuilder:    jb,
		taskRuns:      map[string]*taskrun.TaskRun{"key-2": tr},
		ticketCache:   map[string]ticketing.Ticket{"TICKET-42": ticket},
		taskRunStore:  taskrun.NewMemoryStore(),
		namespace:     "test-ns",
		streamReaders: make(map[string]context.CancelFunc),
	}

	ctx := context.Background()
	r.launchRetryJob(ctx, tr, "")

	assert.Equal(t, "osmia/TICKET-42", eng.lastTask.PriorBranchName)
}

func TestLaunchRetryJob_NoPriorBranchOnFirstRun(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	eng := &capturingEngine{mockEngine: mockEngine{name: "claude-code"}}
	jb := &mockJobBuilder{}

	tr := taskrun.New("tr-3", "key-3", "TICKET-7", "claude-code")
	tr.CurrentEngine = "claude-code"
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)
	_ = tr.Transition(taskrun.StateRetrying)
	// RetryCount is 0 — first attempt failed with no result written.

	ticket := ticketing.Ticket{
		ID:      "TICKET-7",
		Title:   "Fix tests",
		RepoURL: "https://github.com/org/repo",
	}

	r := &Reconciler{
		config:        testConfig(),
		logger:        testLogger(),
		k8sClient:     k8s,
		engines:       map[string]engine.ExecutionEngine{"claude-code": eng},
		jobBuilder:    jb,
		taskRuns:      map[string]*taskrun.TaskRun{"key-3": tr},
		ticketCache:   map[string]ticketing.Ticket{"TICKET-7": ticket},
		taskRunStore:  taskrun.NewMemoryStore(),
		namespace:     "test-ns",
		streamReaders: make(map[string]context.CancelFunc),
	}

	ctx := context.Background()
	r.launchRetryJob(ctx, tr, "")

	assert.Empty(t, eng.lastTask.PriorBranchName)
}

// stubApprovalBackend records RequestApproval calls for assertion in tests.
type stubApprovalBackend struct {
	requests []stubApprovalRequest
}

type stubApprovalRequest struct {
	taskRunID string
	question  string
	options   []string
}

func (s *stubApprovalBackend) RequestApproval(_ context.Context, question string, _ ticketing.Ticket, taskRunID string, options []string) error {
	s.requests = append(s.requests, stubApprovalRequest{taskRunID: taskRunID, question: question, options: options})
	return nil
}

func (s *stubApprovalBackend) CancelPending(_ context.Context, _ string) error { return nil }
func (s *stubApprovalBackend) Name() string                                     { return "stub" }
func (s *stubApprovalBackend) InterfaceVersion() int                            { return 1 }

func TestShouldPromptContinuation(t *testing.T) {
	// A base TaskRun with turns exhausted and all conditions met.
	makeTR := func() *taskrun.TaskRun {
		tr := taskrun.New("tr-1", "key-1", "T-1", "claude-code")
		_ = tr.Transition(taskrun.StateRunning)
		tr.ToolCallsTotal = 50
		tr.ConfiguredMaxTurns = 50
		tr.MaxContinuations = 3
		tr.Result = &engine.TaskResult{Success: false}
		return tr
	}

	tests := []struct {
		desc        string
		cfgEnabled  bool
		hasApproval bool
		modify      func(*taskrun.TaskRun)
		want        bool
	}{
		{
			desc:        "all conditions met",
			cfgEnabled:  true,
			hasApproval: true,
			modify:      func(_ *taskrun.TaskRun) {},
			want:        true,
		},
		{
			desc:        "continuation_prompt disabled",
			cfgEnabled:  false,
			hasApproval: true,
			modify:      func(_ *taskrun.TaskRun) {},
			want:        false,
		},
		{
			desc:        "no approval backend",
			cfgEnabled:  true,
			hasApproval: false,
			modify:      func(_ *taskrun.TaskRun) {},
			want:        false,
		},
		{
			desc:        "turns not yet exhausted",
			cfgEnabled:  true,
			hasApproval: true,
			modify:      func(tr *taskrun.TaskRun) { tr.ToolCallsTotal = 49 },
			want:        false,
		},
		{
			desc:        "agent declared success",
			cfgEnabled:  true,
			hasApproval: true,
			modify:      func(tr *taskrun.TaskRun) { tr.Result = &engine.TaskResult{Success: true} },
			want:        false,
		},
		{
			desc:        "max continuations already reached",
			cfgEnabled:  true,
			hasApproval: true,
			modify:      func(tr *taskrun.TaskRun) { tr.ContinuationCount = 3 },
			want:        false,
		},
		{
			desc:        "max continuations is zero (not configured)",
			cfgEnabled:  true,
			hasApproval: true,
			modify:      func(tr *taskrun.TaskRun) { tr.MaxContinuations = 0 },
			want:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			tr := makeTR()
			tc.modify(tr)

			r := &Reconciler{
				config: &config.Config{
					Engines: config.EnginesConfig{
						ClaudeCode: &config.ClaudeCodeEngineConfig{
							ContinuationPrompt: tc.cfgEnabled,
							MaxContinuations:   3,
						},
					},
				},
			}
			if tc.hasApproval {
				r.approvalBackend = &stubApprovalBackend{}
			}

			got := r.shouldPromptContinuation(tr)
			assert.Equal(t, tc.want, got, tc.desc)
		})
	}
}

func TestPromptContinuation(t *testing.T) {
	// Verify NeedsHuman transition, gate type, and approval request are sent.
	tr := taskrun.New("tr-cont", "key-cont", "T-CONT", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	tr.ToolCallsTotal = 50
	tr.ConfiguredMaxTurns = 50
	tr.MaxContinuations = 3
	tr.Result = &engine.TaskResult{Success: false, Summary: "Halfway done"}

	approvalBackend := &stubApprovalBackend{}
	store := taskrun.NewMemoryStore()

	r := &Reconciler{
		config:          &config.Config{},
		logger:          testLogger(),
		approvalBackend: approvalBackend,
		taskRunStore:    store,
		taskRuns:        map[string]*taskrun.TaskRun{"key-cont": tr},
		ticketCache:     map[string]ticketing.Ticket{"T-CONT": {ID: "T-CONT"}},
	}

	r.promptContinuation(context.Background(), tr)

	assert.Equal(t, taskrun.StateNeedsHuman, tr.State)
	assert.Equal(t, "continuation", tr.ApprovalGateType)
	assert.NotEmpty(t, tr.HumanQuestion)
	require.Len(t, approvalBackend.requests, 1)
	req := approvalBackend.requests[0]
	assert.Equal(t, "tr-cont", req.taskRunID)
	assert.Equal(t, []string{"continue", "stop"}, req.options)
	assert.Contains(t, req.question, "50/50")
	assert.Contains(t, req.question, "Halfway done")
}

func TestResolveContinuationApproval_Approved(t *testing.T) {
	// Verify job is launched with session ID, ContinuationCount incremented,
	// RetryCount unchanged.
	k8s := fake.NewSimpleClientset()
	eng := &capturingEngine{mockEngine: mockEngine{name: "claude-code"}}
	jb := &mockJobBuilder{}
	tb := newMockTicketing(nil)
	store := taskrun.NewMemoryStore()

	tr := taskrun.New("tr-ca", "key-ca", "T-CA", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateNeedsHuman)
	tr.CurrentEngine = "claude-code"
	tr.ApprovalGateType = "continuation"
	tr.ContinuationCount = 0
	tr.MaxContinuations = 3
	tr.SessionID = "sess-abc"

	ticket := ticketing.Ticket{ID: "T-CA", Title: "Fix it", Description: "Details"}

	r := &Reconciler{
		config:        testConfig(),
		logger:        testLogger(),
		k8sClient:     k8s,
		ticketing:     tb,
		engines:       map[string]engine.ExecutionEngine{"claude-code": eng},
		jobBuilder:    jb,
		taskRuns:      map[string]*taskrun.TaskRun{"key-ca": tr},
		taskRunStore:  store,
		ticketCache:   map[string]ticketing.Ticket{"T-CA": ticket},
		namespace:     "test-ns",
		streamReaders: make(map[string]context.CancelFunc),
	}

	err := r.ResolveApproval(context.Background(), "tr-ca", true, "alice")
	require.NoError(t, err)

	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.Equal(t, 1, tr.ContinuationCount)
	assert.Equal(t, 0, tr.RetryCount, "RetryCount must not be incremented for continuations")
	assert.NotEmpty(t, tr.JobName)
	assert.Equal(t, "sess-abc", eng.lastTask.SessionID)

	jobs, listErr := k8s.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.Len(t, jobs.Items, 1)
}

func TestResolveContinuationApproval_Rejected(t *testing.T) {
	// Verify Failed transition and ticket marked with progress summary.
	tb := newMockTicketing(nil)
	store := taskrun.NewMemoryStore()

	tr := taskrun.New("tr-cr", "key-cr", "T-CR", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateNeedsHuman)
	tr.ApprovalGateType = "continuation"
	tr.Result = &engine.TaskResult{Success: false, Summary: "Refactored auth module"}

	r := &Reconciler{
		config:       testConfig(),
		logger:       testLogger(),
		ticketing:    tb,
		taskRuns:     map[string]*taskrun.TaskRun{"key-cr": tr},
		taskRunStore: store,
	}

	err := r.ResolveApproval(context.Background(), "tr-cr", false, "bob")
	require.NoError(t, err)

	assert.Equal(t, taskrun.StateFailed, tr.State)
	require.Len(t, tb.markedFailed, 1)
	assert.Contains(t, tb.markedFailed[0], "T-CR")
}

func TestHandleJobComplete_ContinuationBeforePreMerge(t *testing.T) {
	// Verify that the continuation check fires (and holds the TaskRun in
	// NeedsHuman) before reaching the pre-merge gate.
	approvalBackend := &stubApprovalBackend{}
	store := taskrun.NewMemoryStore()
	tb := newMockTicketing(nil)

	tr := taskrun.New("tr-cbp", "key-cbp", "T-CBP", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	tr.ToolCallsTotal = 50
	tr.ConfiguredMaxTurns = 50
	tr.MaxContinuations = 3
	tr.Result = &engine.TaskResult{Success: false}

	cfg := &config.Config{
		GuardRails: config.GuardRailsConfig{ApprovalGates: []string{"pre_merge"}},
		Engines: config.EnginesConfig{
			ClaudeCode: &config.ClaudeCodeEngineConfig{
				ContinuationPrompt: true,
				MaxContinuations:   3,
			},
		},
	}

	r := &Reconciler{
		config:              cfg,
		logger:              testLogger(),
		approvalBackend:     approvalBackend,
		taskRunStore:        store,
		ticketing:           tb,
		taskRuns:            map[string]*taskrun.TaskRun{"key-cbp": tr},
		ticketCache:         map[string]ticketing.Ticket{"T-CBP": {ID: "T-CBP"}},
		taskRunRole:         map[string]string{},
		taskRunToTournament: map[string]string{},
		streamReaders:       make(map[string]context.CancelFunc),
		podNames:            map[string]string{},
	}

	r.handleJobComplete(context.Background(), tr)

	// Continuation should have fired, not pre-merge gate.
	assert.Equal(t, taskrun.StateNeedsHuman, tr.State)
	assert.Equal(t, "continuation", tr.ApprovalGateType)
	require.Len(t, approvalBackend.requests, 1)
	assert.Equal(t, []string{"continue", "stop"}, approvalBackend.requests[0].options)
	// Ticket should NOT be marked complete (pre-merge gate not reached).
	assert.Empty(t, tb.markedComplete)
}

func TestResolveApproval_PreStartApproval(t *testing.T) {
	cfg := testConfig()
	k8s := fake.NewSimpleClientset()
	tb := newMockTicketing(nil)
	eng := &mockEngine{name: "claude-code"}
	jb := &mockJobBuilder{}
	store := taskrun.NewMemoryStore()

	tr := taskrun.New("tr-approve", "key-approve", "TICKET-APPROVE", "claude-code")
	_ = tr.Transition(taskrun.StateNeedsHuman)
	tr.ApprovalGateType = "pre_start"

	ticket := ticketing.Ticket{
		ID:          "TICKET-APPROVE",
		Title:       "Fix something",
		Description: "Details here",
	}

	r := &Reconciler{
		config:        cfg,
		logger:        testLogger(),
		k8sClient:     k8s,
		ticketing:     tb,
		engines:       map[string]engine.ExecutionEngine{"claude-code": eng},
		jobBuilder:    jb,
		taskRuns:      map[string]*taskrun.TaskRun{"key-approve": tr},
		engineChains:  map[string][]string{"key-approve": {"claude-code"}},
		ticketCache:   map[string]ticketing.Ticket{"TICKET-APPROVE": ticket},
		taskRunStore:  store,
		namespace:     "test-ns",
		streamReaders: make(map[string]context.CancelFunc),
	}

	ctx := context.Background()
	err := r.ResolveApproval(ctx, "tr-approve", true, "alice")
	require.NoError(t, err)
	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.NotEmpty(t, tr.JobName)
	assert.Contains(t, tb.markedProgress, "TICKET-APPROVE")

	// Verify the K8s Job was created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1)
}
