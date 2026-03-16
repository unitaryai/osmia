//go:build e2e

package e2e

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/memory"
	"github.com/unitaryai/osmia/internal/prm"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/internal/tournament"
	"github.com/unitaryai/osmia/internal/watchdog"
)

// TestWorkflowHappyPath verifies the basic ticket → K8s Job → NDJSON stream →
// StateSucceeded cycle with a real kind cluster.
func TestWorkflowHappyPath(t *testing.T) {
	k8s := newK8sClient(t)
	restCfg := newRestConfig(t)
	ns := workflowNamespace()
	ensureNamespace(t, k8s, ns)

	mock := &mockWorkflowTicketing{}
	eng := &workflowFakeEngine{scenario: "success"}

	r := newWorkflowReconciler(t, k8s, restCfg, ns, mock, eng)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	runReconcilerInBackground(t, r, ctx)

	err := r.ProcessTicket(ctx, workflowTicket("happy-1"))
	require.NoError(t, err)

	waitForTicketComplete(t, mock, "happy-1", 3*time.Minute)

	tr, ok := r.GetTaskRun("happy-1-1")
	require.True(t, ok, "TaskRun must exist for happy-1-1")
	assert.Equal(t, taskrun.StateSucceeded, tr.State)
	require.NotNil(t, tr.Result)
	assert.True(t, tr.Result.Success)
	assert.Equal(t, "Fixed the bug", tr.Result.Summary)

	// Exactly one K8s Job should have been created for this task run.
	// Filter by the task run ID label to avoid picking up stale jobs from
	// previous runs that share the same namespace.
	jobs, err := k8s.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{
		LabelSelector: jobbuilder.LabelTaskRunID + "=" + tr.ID,
	})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "expected exactly one K8s Job for this task run")
}

// TestWorkflowJobFailure verifies that a job whose container exits non-zero
// eventually reaches StateFailed and calls MarkFailed on the ticketing backend.
func TestWorkflowJobFailure(t *testing.T) {
	k8s := newK8sClient(t)
	restCfg := newRestConfig(t)
	ns := workflowNamespace()
	ensureNamespace(t, k8s, ns)

	mock := &mockWorkflowTicketing{}
	eng := &workflowFakeEngine{scenario: "fail"}

	r := newWorkflowReconciler(t, k8s, restCfg, ns, mock, eng)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	runReconcilerInBackground(t, r, ctx)

	err := r.ProcessTicket(ctx, workflowTicket("fail-1"))
	require.NoError(t, err)

	// The "fail" scenario exits with code 1. With MaxRetries=1 (default), the
	// controller retries once and then calls MarkFailed.
	waitForTicketFailed(t, mock, "fail-1", 3*time.Minute)

	tr, ok := r.GetTaskRun("fail-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateFailed, tr.State)

	mock.mu.Lock()
	assert.Contains(t, mock.markedFailed, "fail-1")
	assert.Empty(t, mock.markedComplete)
	mock.mu.Unlock()
}

// TestWorkflowEngineChainFallback verifies that when the primary engine fails,
// the controller falls back to the secondary engine and marks the ticket
// complete once the fallback job succeeds.
func TestWorkflowEngineChainFallback(t *testing.T) {
	k8s := newK8sClient(t)
	restCfg := newRestConfig(t)
	ns := workflowNamespace()
	ensureNamespace(t, k8s, ns)

	mock := &mockWorkflowTicketing{}
	claudeEng := &workflowFakeEngine{scenario: "fail"}                  // "claude-code" — will fail
	aiderEng := &workflowFakeEngine{name: "aider", scenario: "success"} // fallback — will succeed

	// Build the reconciler directly so we can supply a custom config with
	// FallbackEngines set.
	cfg := workflowConfig(ns)
	cfg.Engines.FallbackEngines = []string{"aider"}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	jb := jobbuilder.NewJobBuilder(ns)
	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(mock),
		controller.WithEngine(claudeEng),
		controller.WithEngine(aiderEng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace(ns),
		controller.WithRestConfig(restCfg),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runReconcilerInBackground(t, r, ctx)

	err := r.ProcessTicket(ctx, workflowTicket("fallback-1"))
	require.NoError(t, err)

	// Wait for the ticket to be marked complete (fallback job succeeds).
	waitForTicketComplete(t, mock, "fallback-1", 4*time.Minute)

	tr, ok := r.GetTaskRun("fallback-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateSucceeded, tr.State)

	// Expect 2 K8s Jobs: one for claude-code (failed), one for aider (succeeded).
	jobs, err := k8s.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(jobs.Items), 2, "expected at least 2 K8s Jobs (primary + fallback)")

	mock.mu.Lock()
	assert.Contains(t, mock.markedComplete, "fallback-1")
	mock.mu.Unlock()
}

// TestWorkflowPRMHintDelivery verifies that the PRM subsystem fires for an
// agent that loops, and that the controller logs at least one hint/PRM message.
func TestWorkflowPRMHintDelivery(t *testing.T) {
	k8s := newK8sClient(t)
	restCfg := newRestConfig(t)
	ns := workflowNamespace()
	ensureNamespace(t, k8s, ns)

	mock := &mockWorkflowTicketing{}
	eng := &workflowFakeEngine{scenario: "loop"}

	// Use a capturing log writer so we can assert on PRM messages.
	logWriter := &testLogWriter{}
	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelDebug}))
	jb := jobbuilder.NewJobBuilder(ns)
	cfg := workflowConfig(ns)
	cfg.PRM = config.PRMConfig{
		Enabled:             true,
		EvaluationInterval:  5,
		WindowSize:          10,
		ScoreThresholdNudge: 9, // nudge whenever score < 9 (virtually always for loop)
		HintFilePath:        "/tmp/osmia-hint.md",
	}

	prmCfg := prm.Config{
		Enabled:             true,
		EvaluationInterval:  5,
		WindowSize:          10,
		ScoreThresholdNudge: 9,
		HintFilePath:        "/tmp/osmia-hint.md",
	}

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(mock),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace(ns),
		controller.WithRestConfig(restCfg),
		controller.WithPRMConfig(prmCfg),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	runReconcilerInBackground(t, r, ctx)

	err := r.ProcessTicket(ctx, workflowTicket("loop-1"))
	require.NoError(t, err)

	// The loop scenario still exits 0 with a "Done" result, so the ticket
	// completes eventually.
	waitForTicketComplete(t, mock, "loop-1", 3*time.Minute)

	tr, ok := r.GetTaskRun("loop-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateSucceeded, tr.State)

	// The PRM evaluator should have fired at least once (at tool call 5/10/…)
	// and logged a "prm nudge recorded" message, or a hint write failure.
	assert.True(t,
		logWriter.hasAny("prm", "hint"),
		"expected at least one PRM/hint log message to confirm the subsystem was triggered",
	)
}

// TestWorkflowWatchdogTermination verifies that an agent emitting high token
// counts without file progress is detected and terminated by the watchdog,
// eventually resulting in StateFailed.
func TestWorkflowWatchdogTermination(t *testing.T) {
	k8s := newK8sClient(t)
	restCfg := newRestConfig(t)
	ns := workflowNamespace()
	ensureNamespace(t, k8s, ns)

	mock := &mockWorkflowTicketing{}
	eng := &workflowFakeEngine{scenario: "thrash"}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	jb := jobbuilder.NewJobBuilder(ns)
	cfg := workflowConfig(ns)

	wdCfg := watchdog.DefaultConfig()
	wdCfg.CheckIntervalSeconds = 2
	wdCfg.MinConsecutiveTicks = 1
	wdCfg.ResearchGracePeriodMinutes = 0
	wdCfg.Rules.ThrashingDetection.TokensWithoutProgressThreshold = 50000
	wdCfg.Rules.ThrashingDetection.Action = watchdog.ActionWarn
	wdCfg.Rules.ThrashingDetection.EscalationAction = watchdog.ActionTerminateWithFeedback
	wd := watchdog.New(wdCfg, logger)

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(mock),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace(ns),
		controller.WithRestConfig(restCfg),
		controller.WithWatchdog(wd),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runReconcilerInBackground(t, r, ctx)

	err := r.ProcessTicket(ctx, workflowTicket("thrash-1"))
	require.NoError(t, err)

	waitForTicketFailed(t, mock, "thrash-1", 4*time.Minute)

	tr, ok := r.GetTaskRun("thrash-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateFailed, tr.State)

	mock.mu.Lock()
	assert.Contains(t, mock.markedFailed, "thrash-1")
	mock.mu.Unlock()

	// Result may be nil (watchdog termination) or have Success=false.
	if tr.Result != nil {
		assert.False(t, tr.Result.Success)
	}
}

// TestWorkflowSequentialTasksMemory verifies that episodic memory is populated
// after the first task, and that the second task receives memory context in its
// prompt.
func TestWorkflowSequentialTasksMemory(t *testing.T) {
	k8s := newK8sClient(t)
	restCfg := newRestConfig(t)
	ns := workflowNamespace()
	ensureNamespace(t, k8s, ns)

	mock := &mockWorkflowTicketing{}
	eng := &workflowFakeEngine{scenario: "success"}

	memLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	memGraph := memory.NewGraph(nil, memLogger)
	memExtractor := memory.NewExtractor(memLogger)
	memQuery := memory.NewQueryEngine(memGraph, memLogger)

	r := newWorkflowReconciler(t, k8s, restCfg, ns, mock, eng,
		controller.WithMemory(memGraph, memExtractor, memQuery),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	runReconcilerInBackground(t, r, ctx)

	// --- Task 1 ---
	err := r.ProcessTicket(ctx, workflowTicket("mem-1"))
	require.NoError(t, err)
	waitForTicketComplete(t, mock, "mem-1", 3*time.Minute)

	// Allow the asynchronous memory extraction goroutine to complete.
	time.Sleep(3 * time.Second)
	assert.Positive(t, memGraph.NodeCount(),
		"at least one memory node should have been extracted after the first task")

	// Capture the prompt used for task 1 to compare later.
	task1 := eng.getLastTask()

	// --- Task 2 ---
	err = r.ProcessTicket(ctx, workflowTicket("mem-2"))
	require.NoError(t, err)
	waitForTicketComplete(t, mock, "mem-2", 3*time.Minute)

	task2 := eng.getLastTask()

	// The second task should have memory context injected.
	assert.NotEmpty(t, task2.MemoryContext,
		"second task should have non-empty MemoryContext injected from episodic memory")
	assert.NotEqual(t, task1.MemoryContext, task2.MemoryContext,
		"second task MemoryContext should differ from (or extend beyond) first task's")

	mock.mu.Lock()
	assert.Contains(t, mock.markedComplete, "mem-1")
	assert.Contains(t, mock.markedComplete, "mem-2")
	mock.mu.Unlock()
}

// TestWorkflowTournamentEndToEnd verifies that competitive execution creates
// two candidate jobs and one judge job, the judge selects a winner, and the
// ticket is marked complete.
func TestWorkflowTournamentEndToEnd(t *testing.T) {
	k8s := newK8sClient(t)
	restCfg := newRestConfig(t)
	ns := workflowNamespace()
	ensureNamespace(t, k8s, ns)

	mock := &mockWorkflowTicketing{}
	// Primary engine: "claude-code" — handles candidate_a and judge roles.
	primaryEng := &workflowFakeEngine{scenario: "tournament_a"}
	// Secondary engine: "fake-code" — handles candidate_b role.
	secondaryEng := &workflowSecondEngine{}

	cfg := workflowConfig(ns)
	cfg.Engines.FallbackEngines = []string{"fake-code"}
	cfg.CompetitiveExecution = config.CompetitiveExecutionConfig{
		Enabled:                   true,
		DefaultCandidates:         2,
		JudgeEngine:               "claude-code",
		EarlyTerminationThreshold: 1.0,
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	jb := jobbuilder.NewJobBuilder(ns)
	tc := tournament.NewCoordinator(logger)

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(mock),
		controller.WithEngine(primaryEng),
		controller.WithEngine(secondaryEng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace(ns),
		controller.WithRestConfig(restCfg),
		controller.WithTournamentCoordinator(tc),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	runReconcilerInBackground(t, r, ctx)

	err := r.ProcessTicket(ctx, workflowTicket("tourn-1"))
	require.NoError(t, err)

	waitForTicketComplete(t, mock, "tourn-1", 5*time.Minute)

	// Three K8s Jobs should have been created: 2 candidates + 1 judge.
	// Use GreaterOrEqual because candidate and judge jobs carry distinct
	// LabelTaskRunID values, making a single-selector filter impractical;
	// stale jobs from earlier test runs may also be present in the namespace.
	jobs, err := k8s.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(jobs.Items), 3,
		"expected at least 3 K8s Jobs for tournament (2 candidates + 1 judge)")

	mock.mu.Lock()
	assert.Contains(t, mock.markedComplete, "tourn-1")
	mock.mu.Unlock()

	// The tournament ticket should have a succeeded task run.
	tr, ok := r.GetTaskRun("tourn-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateSucceeded, tr.State)
}
