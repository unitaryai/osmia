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
	"github.com/unitaryai/osmia/pkg/plugin/notifications"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// Given a task run waiting for pre_start approval,
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

	ticket := ticketing.Ticket{ID: "TICKET-APPROVAL", Title: "Fix the thing", RepoURL: "https://github.com/org/repo"}
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
	tr, ok := r.GetTaskRun("TICKET-APPROVAL-1")
	require.True(t, ok)
	require.Equal(t, taskrun.StateNeedsHuman, tr.State)
	tr.UpdatedAt = time.Now().Add(-time.Hour)

	r.runWatchdogChecks(ctx)

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.True(t, tr.IsTerminal(), "a timed-out approval must not be retried")
	assert.Contains(t, tb.markedFailed, "TICKET-APPROVAL")
	assert.Equal(t, []string{tr.ID}, approvals.cancelled, "the pending approval request is withdrawn")
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items, "no job may run without approval")
}

// Given a task run waiting for pre_merge approval, counted as an active job,
// when the approval times out,
// then the ActiveJobs gauge is released exactly once.
func TestUnansweredPreMergeApprovalReleasesActiveJob(t *testing.T) {
	tr := taskrun.New("tr-merge", "key-merge", "TICKET-MERGE", "claude-code")
	require.NoError(t, tr.Transition(taskrun.StateRunning))
	require.NoError(t, tr.Transition(taskrun.StateNeedsHuman))
	tr.ApprovalGateType = "pre_merge"
	r := &Reconciler{logger: testLogger(), taskRunStore: taskrun.NewMemoryStore()}

	// Count the run as active first, as launching it would have, so the
	// release below returns the shared gauge to where it started.
	metrics.ActiveJobs.Inc()
	before := testutil.ToFloat64(metrics.ActiveJobs)
	r.failUnansweredApproval(context.Background(), tr, "timed out")

	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.InDelta(t, before-1, testutil.ToFloat64(metrics.ActiveJobs), 0)
}

// hangingApprovalBackend never finishes cancelling, like a Slack request that
// never gets a response, until its context ends.
type hangingApprovalBackend struct {
	stubApprovalBackend
}

func (h *hangingApprovalBackend) CancelPending(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// Given an approval backend that never answers,
// when an unanswered approval is failed,
// then the cancel call gives up after the cleanup timeout and the ticket is
// still marked failed, so one stuck request cannot stall the watchdog.
func TestUnansweredApprovalDoesNotWaitForeverOnCancel(t *testing.T) {
	tr := taskrun.New("tr-hang", "key-hang", "TICKET-HANG", "claude-code")
	require.NoError(t, tr.Transition(taskrun.StateNeedsHuman))
	tb := newMockTicketing(nil)
	r := &Reconciler{
		logger:                 testLogger(),
		taskRunStore:           taskrun.NewMemoryStore(),
		approvalBackend:        &hangingApprovalBackend{},
		ticketing:              tb,
		approvalCleanupTimeout: 50 * time.Millisecond,
	}

	done := make(chan struct{})
	go func() {
		r.failUnansweredApproval(context.Background(), tr, "timed out")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failing an unanswered approval waited on the approval backend without a limit")
	}
	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.Contains(t, tb.markedFailed, "TICKET-HANG")
}

// hangingNotifier never finishes updating a message, like a Slack request
// that never gets a response, until its context ends.
type hangingNotifier struct {
	spyNotifier
}

func (h *hangingNotifier) UpdateMessage(ctx context.Context, _ string, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// Given a notification channel that never answers,
// when an unanswered approval is failed,
// then the notification update gives up after the cleanup timeout, so one
// stuck request cannot stall the watchdog.
func TestUnansweredApprovalDoesNotWaitForeverOnNotification(t *testing.T) {
	tr := taskrun.New("tr-notify", "key-notify", "TICKET-NOTIFY", "claude-code")
	require.NoError(t, tr.Transition(taskrun.StateNeedsHuman))
	tr.NotificationThreadRef = "thread-ref"
	r := &Reconciler{
		logger:                 testLogger(),
		taskRunStore:           taskrun.NewMemoryStore(),
		notifiers:              []notifications.Channel{&hangingNotifier{}},
		approvalCleanupTimeout: 50 * time.Millisecond,
	}

	done := make(chan struct{})
	go func() {
		r.failUnansweredApproval(context.Background(), tr, "timed out")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failing an unanswered approval waited on the notification channel without a limit")
	}
	assert.Equal(t, taskrun.StateFailed, tr.State)
}
