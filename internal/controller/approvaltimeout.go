package controller

import (
	"context"
	"fmt"

	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// failUnansweredApproval fails a run whose approval gate timed out, without
// retry: a retry would launch a job nobody approved. Using up its retry budget
// makes the run terminal, which frees its MaxConcurrentJobs slot.
func (r *Reconciler) failUnansweredApproval(ctx context.Context, tr *taskrun.TaskRun, reason string) {
	r.mu.Lock()
	if tr.State != taskrun.StateNeedsHuman {
		// Answered since the watchdog check: the approval wins.
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

	if r.approvalBackend != nil {
		if err := r.approvalBackend.CancelPending(ctx, tr.ID); err != nil {
			r.logger.WarnContext(ctx, "failed to cancel pending approval", "task_run_id", tr.ID, "error", err)
		}
	}
	if r.ticketing != nil {
		if err := r.ticketing.MarkFailed(ctx, tr.TicketID, "approval not given: "+reason); err != nil {
			r.logger.ErrorContext(ctx, "failed to mark ticket failed", "ticket_id", tr.TicketID, "error", err)
		}
	}
	r.updateNotificationStatus(ctx, threadRef,
		fmt.Sprintf("\u274C *Osmia failed:* %s", tr.TicketID))

	r.logger.WarnContext(ctx, "approval gate not answered in time; task run failed without retry",
		"task_run_id", tr.ID,
		"gate_type", gateType,
	)
}
