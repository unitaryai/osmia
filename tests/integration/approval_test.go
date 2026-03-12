//go:build integration

// Package integration_test contains integration tests that verify the
// approval gate workflow: pre-start gates block job creation, pre-merge
// gates hold completion, and TaskRunStore persistence works correctly.
package integration_test

import (
	"context"
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
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// approvalTestConfig returns a config with the specified approval gates enabled.
func approvalTestConfig(gates ...string) *config.Config {
	return &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
			AllowedRepos:          []string{"https://github.com/org/*"},
			AllowedTaskTypes:      []string{"issue", "bug", "feature"},
			ApprovalGates:         gates,
		},
	}
}

// approvalTicket returns a ticket that passes guard rails for approval tests.
func approvalTicket(id string) ticketing.Ticket {
	return ticketing.Ticket{
		ID:         id,
		Title:      "Approval test ticket",
		TicketType: "issue",
		RepoURL:    "https://github.com/org/repo",
	}
}

// TestApprovalPreStartGateBlocksExecution verifies that when a "pre_start"
// approval gate is configured, processing a ticket results in a TaskRun in
// NeedsHuman state with no Kubernetes Job created.
func TestApprovalPreStartGateBlocksExecution(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	tb := &mockReconcilerTicketing{
		tickets:  []ticketing.Ticket{approvalTicket("APPROVE-1")},
		maxPolls: 1,
	}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := approvalTestConfig("pre_start")
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, approvalTicket("APPROVE-1"))
	require.NoError(t, err)

	// Verify TaskRun is in NeedsHuman state.
	tr, ok := r.GetTaskRun("APPROVE-1-1")
	require.True(t, ok, "TaskRun must exist")
	assert.Equal(t, taskrun.StateNeedsHuman, tr.State,
		"TaskRun must be in NeedsHuman state when pre_start gate is active")

	// Verify no Job was created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items, "no Job should be created when pre_start gate blocks execution")
}

// TestApprovalNoGateAllowsExecution verifies that without any approval gates
// configured, processing a ticket creates a Job and transitions the TaskRun
// to Running state.
func TestApprovalNoGateAllowsExecution(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	tb := &mockReconcilerTicketing{
		tickets:  []ticketing.Ticket{approvalTicket("NOAPPROVE-1")},
		maxPolls: 1,
	}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := approvalTestConfig() // no gates
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, approvalTicket("NOAPPROVE-1"))
	require.NoError(t, err)

	// Verify TaskRun is in Running state.
	tr, ok := r.GetTaskRun("NOAPPROVE-1-1")
	require.True(t, ok, "TaskRun must exist")
	assert.Equal(t, taskrun.StateRunning, tr.State,
		"TaskRun must be in Running state when no gates are configured")

	// Verify a Job was created.
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "one Job should be created")
}

// TestApprovalPreMergeGateHoldsCompletion verifies that when a "pre_merge"
// approval gate is configured and a Job completes, the TaskRun transitions
// to NeedsHuman state instead of Succeeded.
func TestApprovalPreMergeGateHoldsCompletion(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	ticket := approvalTicket("MERGE-1")
	tb := &mockReconcilerTicketing{
		tickets:  []ticketing.Ticket{ticket},
		maxPolls: 1,
	}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := approvalTestConfig("pre_merge")
	logger := reconcilerTestLogger()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	ctx := context.Background()

	// Process ticket to create a Job.
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("MERGE-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)

	// Mark the Job as Complete.
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

	// Run a reconcile cycle to detect the completed Job.
	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.Run(ctx2, 50*time.Millisecond)

	// Verify TaskRun is held in NeedsHuman, not Succeeded.
	tr, ok = r.GetTaskRun("MERGE-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateNeedsHuman, tr.State,
		"TaskRun must be in NeedsHuman state when pre_merge gate holds completion")

	// Verify ticket was NOT marked complete (it should be held).
	tb.mu.Lock()
	assert.NotContains(t, tb.markedComplete, "MERGE-1",
		"ticket must not be marked complete when held by pre_merge gate")
	tb.mu.Unlock()
}

// TestApprovalTaskRunStorePersistence verifies that processing a ticket
// saves the TaskRun to the store and it can be retrieved by ID and ticket ID.
func TestApprovalTaskRunStorePersistence(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	ticket := approvalTicket("STORE-1")
	tb := &mockReconcilerTicketing{
		tickets:  []ticketing.Ticket{ticket},
		maxPolls: 1,
	}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := approvalTestConfig() // no gates, so we get a Running task run
	logger := reconcilerTestLogger()
	store := taskrun.NewMemoryStore()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		controller.WithTaskRunStore(store),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Retrieve the TaskRun via the reconciler's in-memory map.
	tr, ok := r.GetTaskRun("STORE-1-1")
	require.True(t, ok)
	require.NotEmpty(t, tr.ID)

	// Verify the store has the TaskRun persisted by ID.
	stored, err := store.Get(ctx, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, tr.ID, stored.ID, "stored TaskRun ID must match")
	assert.Equal(t, taskrun.StateRunning, stored.State, "stored TaskRun must be Running")

	// Verify retrieval by ticket ID.
	byTicket, err := store.ListByTicketID(ctx, "STORE-1")
	require.NoError(t, err)
	require.Len(t, byTicket, 1, "store must contain exactly one TaskRun for ticket STORE-1")
	assert.Equal(t, tr.ID, byTicket[0].ID)
}

// TestApprovalPreStartStoresPersisted verifies that a TaskRun held by a
// pre_start gate is also persisted to the TaskRunStore.
func TestApprovalPreStartStoresPersisted(t *testing.T) {
	t.Parallel()

	k8s := fake.NewSimpleClientset()
	ticket := approvalTicket("PRESTORE-1")
	tb := &mockReconcilerTicketing{
		tickets:  []ticketing.Ticket{ticket},
		maxPolls: 1,
	}
	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")
	cfg := approvalTestConfig("pre_start")
	logger := reconcilerTestLogger()
	store := taskrun.NewMemoryStore()

	r := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		controller.WithTaskRunStore(store),
	)

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Verify the held TaskRun is persisted.
	tr, ok := r.GetTaskRun("PRESTORE-1-1")
	require.True(t, ok)

	stored, err := store.Get(ctx, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, taskrun.StateNeedsHuman, stored.State,
		"persisted TaskRun must be in NeedsHuman state")
}
