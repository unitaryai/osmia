package controller

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/internal/watchdog"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// Given a task held at the pre_start approval gate,
// when the watchdog's unanswered-human timeout passes,
// then the run fails without retry and no agent job is created.
func TestUnansweredPreStartApprovalFailsClosed(t *testing.T) {
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
			ApprovalGates:         []string{"pre_start"},
		},
	}
	wdCfg := watchdog.DefaultConfig()
	wdCfg.MinConsecutiveTicks = 1
	wdCfg.Rules.UnansweredHumanTimeoutMinutes = 30

	ticket := ticketing.Ticket{ID: "TICKET-GATE", Title: "Fix the thing", RepoURL: "https://github.com/org/repo"}
	tb := newMockTicketing([]ticketing.Ticket{ticket})
	k8s := fake.NewSimpleClientset()
	approvals := &stubApprovalBackend{}
	r := NewReconciler(cfg, testLogger(),
		WithEngine(&mockEngine{name: "claude-code"}),
		WithTicketing(tb),
		WithJobBuilder(&mockJobBuilder{}),
		WithK8sClient(k8s),
		WithNamespace("test-ns"),
		WithWatchdog(watchdog.New(wdCfg, testLogger())),
		WithApprovalBackend(approvals),
	)

	ctx := context.Background()
	require.NoError(t, r.ProcessTicket(ctx, ticket))
	tr, ok := r.GetTaskRun("TICKET-GATE-1")
	require.True(t, ok)
	require.Equal(t, taskrun.StateNeedsHuman, tr.State)
	tr.UpdatedAt = time.Now().Add(-time.Hour)

	r.runWatchdogChecks(ctx)

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.True(t, tr.IsTerminal(), "an unanswered gate must not be retried")
	assert.Contains(t, tb.markedFailed, "TICKET-GATE")
	assert.Equal(t, []string{tr.ID}, approvals.cancelled, "the pending approval request is withdrawn")
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items, "no job may run without approval")
}

// Given a task run at the pre_merge gate, counted as an active job,
// when the gate times out,
// then the ActiveJobs gauge is released exactly once.
func TestUnansweredPreMergeApprovalReleasesActiveJob(t *testing.T) {
	tr := taskrun.New("tr-merge", "key-merge", "TICKET-MERGE", "claude-code")
	require.NoError(t, tr.Transition(taskrun.StateRunning))
	require.NoError(t, tr.Transition(taskrun.StateNeedsHuman))
	tr.ApprovalGateType = "pre_merge"
	r := &Reconciler{logger: testLogger(), taskRunStore: taskrun.NewMemoryStore()}

	before := testutil.ToFloat64(metrics.ActiveJobs)
	r.failUnansweredApproval(context.Background(), tr, "timed out")

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.InDelta(t, before-1, testutil.ToFloat64(metrics.ActiveJobs), 0)
}
