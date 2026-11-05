package controller

import (
	"context"

	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// failUnansweredApproval ends a TaskRun whose approval gate (pre_start,
// pre_merge, continuation or missing_repo_url) was not answered in time.
//
// The gate must fail closed. The generic failure path retries or falls back
// to another engine, which for a run held at a gate means launching a job
// that no human approved. So the run goes straight to Failed with its retry
// budget used up. That also makes it terminal, so it no longer counts
// against MaxConcurrentJobs.
func (r *Reconciler) failUnansweredApproval(ctx context.Context, tr *taskrun.TaskRun, reason string) {
	r.mu.Lock()
	if tr.State != taskrun.StateNeedsHuman {
		// The gate was answered between the watchdog check and now, so the
		// approval wins and the timeout is dropped.
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
	r.mu.Unlock()

	r.saveTaskRunOrLog(ctx, tr)
	metrics.TaskRunsTotal.WithLabelValues(string(taskrun.StateFailed)).Inc()
	// A pre_merge run still counts as an active job: its job finished, but the
	// gauge is only decremented once the gate is resolved. The other gates
	// either never incremented it or decremented it on entry.
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

	r.logger.WarnContext(ctx, "approval gate not answered in time; task run failed without retry",
		"task_run_id", tr.ID,
		"gate_type", gateType,
	)
}
