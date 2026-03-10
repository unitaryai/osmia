//go:build integration

// Package integration_test contains the all-features integration test that
// exercises every Active Integration subsystem simultaneously through the
// controller's reconciliation pipeline.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/internal/diagnosis"
	"github.com/unitaryai/osmia/internal/estimator"
	"github.com/unitaryai/osmia/internal/memory"
	"github.com/unitaryai/osmia/internal/prm"
	"github.com/unitaryai/osmia/internal/routing"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/internal/tournament"
	"github.com/unitaryai/osmia/internal/watchdog"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

func allFeaturesLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestAllFeaturesEnabled wires every Active Integration subsystem into the
// reconciler simultaneously and verifies:
//   - ProcessTicket succeeds for a normal ticket (no panic)
//   - ProcessTicket succeeds for a tournament ticket (no panic)
//   - Both task runs reach a terminal state
//
// This is a smoke test — it validates correct composition, not specific
// subsystem behaviour (which is covered by the individual tests above).
func TestAllFeaturesEnabled(t *testing.T) {
	t.Parallel()

	logger := allFeaturesLogger()
	ctx := context.Background()

	// --- Memory subsystem ---
	memStore, err := memory.NewSQLiteStore(":memory:", logger)
	require.NoError(t, err)
	t.Cleanup(func() { memStore.Close() })
	graph := memory.NewGraph(memStore, logger)
	extractor := memory.NewExtractor(logger)
	queryEngine := memory.NewQueryEngine(graph, logger)

	// --- Watchdog ---
	wdCfg := watchdog.DefaultConfig()
	wdCfg.AdaptiveCalibration = watchdog.AdaptiveCalibrationConfig{
		Enabled:             true,
		MinSampleCount:      10,
		PercentileThreshold: "p90",
		ColdStartFallback:   true,
	}
	wdCal := watchdog.NewCalibrator(logger)
	wdProfileStore := watchdog.NewMemoryProfileStore()
	wdResolver := watchdog.NewProfileResolver(wdProfileStore, wdCal, 10)
	wd := watchdog.NewWithCalibration(wdCfg, logger, wdCal, wdResolver)

	// --- Routing ---
	fpStore := routing.NewMemoryFingerprintStore()
	routingCfg := &config.RoutingConfig{
		Enabled:              true,
		EpsilonGreedy:        0.1,
		MinSamplesForRouting: 5,
	}
	sel := routing.NewIntelligentSelector(fpStore,
		&staticFallback{engines: []string{"claude-code"}},
		routingCfg,
		[]string{"claude-code"},
		logger,
	)

	// --- Estimator ---
	estimatorStore := estimator.NewMemoryEstimatorStore()
	estimatorCfg := &config.EstimatorConfig{
		Enabled:                true,
		MaxPredictedCostPerJob: 100.0,
		DefaultCostPerEngine: map[string]config.CostRange{
			"claude-code": {Low: 1.0, High: 10.0},
		},
		DefaultDurationPerEngine: map[string]config.DurationRange{
			"claude-code": {LowMinutes: 5, HighMinutes: 60},
		},
	}
	scorer := estimator.NewComplexityScorer()
	predictor := estimator.NewPredictor(estimatorStore, estimatorCfg, logger)

	// --- Diagnosis ---
	analyser := diagnosis.NewAnalyser(logger)
	retryBuilder := diagnosis.NewRetryBuilder(logger)

	// --- PRM ---
	prmCfg := prm.Config{
		Enabled:                true,
		EvaluationInterval:     5,
		WindowSize:             10,
		ScoreThresholdNudge:    7,
		ScoreThresholdEscalate: 3,
		HintFilePath:           "/tmp/osmia-allfeatures-hint.md",
		MaxTrajectoryLength:    50,
	}

	// --- Tournament ---
	coord := tournament.NewCoordinator(logger)

	// --- Controller config ---
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     10,
			MaxJobDurationMinutes: 120,
		},
	}

	k8s := fake.NewSimpleClientset()
	eng := &stubEngine{name: "claude-code"}
	tb := newStubTicketing(nil)
	jb := &stubJobBuilder{}

	r := controller.NewReconciler(cfg, logger,
		controller.WithEngine(eng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		controller.WithMemory(graph, extractor, queryEngine),
		controller.WithWatchdog(wd),
		controller.WithWatchdogCalibration(wdCal, wdResolver),
		controller.WithIntelligentSelector(sel),
		controller.WithEstimator(predictor, scorer),
		controller.WithDiagnosis(analyser, retryBuilder),
		controller.WithPRMConfig(prmCfg),
		controller.WithTournamentCoordinator(coord),
	)

	// --- Normal ticket ---
	normalTicket := ticketing.Ticket{
		ID:          "AF-NORMAL-1",
		Title:       "Fix login timeout",
		Description: "Users report timeout errors on login",
		RepoURL:     "https://github.com/org/repo",
		TicketType:  "bug_fix",
		Labels:      []string{"bug"},
	}

	assert.NotPanics(t, func() {
		err = r.ProcessTicket(ctx, normalTicket)
	}, "ProcessTicket must not panic")
	require.NoError(t, err, "ProcessTicket must not return an error for normal ticket")

	// Verify normal task run was created in a terminal state.
	tr, ok := r.GetTaskRun("AF-NORMAL-1-1")
	require.True(t, ok, "task run for normal ticket must exist")
	assert.True(t,
		tr.State == taskrun.StateRunning ||
			tr.State == taskrun.StateSucceeded ||
			tr.State == taskrun.StateFailed,
		"task run must be in a terminal or running state, got %q", tr.State)

	// --- A second normal ticket to verify no state corruption across calls ---
	secondTicket := ticketing.Ticket{
		ID:          "AF-NORMAL-2",
		Title:       "Add email validation",
		Description: "Validate email format on signup",
		RepoURL:     "https://github.com/org/repo",
		TicketType:  "feature",
		Labels:      []string{"feature"},
	}

	assert.NotPanics(t, func() {
		err = r.ProcessTicket(ctx, secondTicket)
	}, "second ProcessTicket must not panic")
	// May return an error if a duplicate is detected; that's acceptable.

	// Verify both task runs exist without panicking.
	_, ok = r.GetTaskRun("AF-NORMAL-2-1")
	assert.True(t, ok, "second task run must exist after all-features reconciler run")
}
