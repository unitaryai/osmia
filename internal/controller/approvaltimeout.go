package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// defaultApprovalCleanupTimeout bounds each call to an external system made
// while failing an unanswered approval. failUnansweredApproval runs inside the
// watchdog check, so without a bound one slow Slack or ticketing request would
// hold up the checks of every other task run until the controller stops.
const defaultApprovalCleanupTimeout = 30 * time.Second

// failUnansweredApproval fails the task run when its approval request times out.
// It uses up the retry budget so there is no retry, and frees the concurrency slot.
func (r *Reconciler) failUnansweredApproval(ctx context.Context, tr *taskrun.TaskRun, reason string) {
	r.mu.Lock()
	if tr.State != taskrun.StateNeedsHuman {
		// The approval was resolved after the watchdog check; keep that result.
		r.mu.Unlock()
		return
	}
	if err := tr.Transition(taskrun.StateFailed); err != nil {
		r.mu.Unlock()
		r.logger.ErrorContext(ctx, "failed to fail unanswered approval", "task_run_id", tr.ID, "error", err)
		return
	}
	tr.RetryCount = tr.MaxRetries
	gateType := tr.ApprovalGateType
	threadRef := tr.NotificationThreadRef
	r.mu.Unlock()

	r.saveTaskRunOrLog(ctx, tr)
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateFailed)).Inc()
	// Only a pre_merge run still counts as an active job.
	if gateType == "pre_merge" {
		metrics.ActiveJobs.Dec()
	}
	r.cleanupHeartbeat(tr.ID)

	r.cancelPendingApproval(ctx, tr.ID)
	r.markTicketFailed(ctx, tr.TicketID, "approval not given: "+reason)
	r.updateNotificationStatus(ctx, threadRef,
		fmt.Sprintf("\u274C *Osmia failed:* %s", tr.TicketID))

	r.logger.WarnContext(ctx, "approval not answered in time; task run failed without retry",
		"task_run_id", tr.ID,
		"gate_type", gateType,
	)
}

// cancelPendingApproval withdraws the approval request, giving up after the
// cleanup timeout.
func (r *Reconciler) cancelPendingApproval(ctx context.Context, taskRunID string) {
	if r.approvalBackend == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.approvalCleanupTimeoutOrDefault())
	defer cancel()
	if err := r.approvalBackend.CancelPending(ctx, taskRunID); err != nil {
		r.logger.WarnContext(ctx, "failed to cancel pending approval", "task_run_id", taskRunID, "error", err)
	}
}

// markTicketFailed marks the ticket failed, giving up after the cleanup
// timeout.
func (r *Reconciler) markTicketFailed(ctx context.Context, ticketID, reason string) {
	if r.ticketing == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.approvalCleanupTimeoutOrDefault())
	defer cancel()
	if err := r.ticketing.MarkFailed(ctx, ticketID, reason); err != nil {
		r.logger.ErrorContext(ctx, "failed to mark ticket failed", "ticket_id", ticketID, "error", err)
	}
}

// approvalCleanupTimeoutOrDefault returns the configured cleanup timeout, or
// the default when none is set.
func (r *Reconciler) approvalCleanupTimeoutOrDefault() time.Duration {
	if r.approvalCleanupTimeout > 0 {
		return r.approvalCleanupTimeout
	}
	return defaultApprovalCleanupTimeout
}
