package controller

import (
	"context"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/unitaryai/osmia/internal/metrics"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// jobRecoveryCounts summarises what restoreRunningJobs did, for its log line.
type jobRecoveryCounts struct {
	restored   int
	jobGone    int
	tournament int
	failed     int
}

// restoreRunningJobs re-adopts agent Jobs that were still running when the
// controller last stopped.
//
// The controller tracks running TaskRuns in memory only, and checks Job
// status only for the runs it is tracking. Before this, a restart (an
// eviction, a rollout, the autoscaler moving the pod) left every running Job
// unwatched: the agent finished its work, but the result was never
// collected, the ticket was never completed, and the concurrency limit no
// longer counted the Job. Each run whose Job still exists is put back under
// tracking and its log is read again from the start, so the final result
// event, which carries the merge request URL, is captured as usual.
//
// A run whose Job no longer exists is left as it is. The store can hold
// Running rows from long-past crashes, and failing them here would post a
// failure on tickets that were dealt with long ago.
//
// It reports whether recovery is complete. It is not when the store or a Job
// lookup failed, which is usually a passing API error; Run then calls it
// again on later ticks until it completes. Runs already adopted are skipped
// on those passes, because adoption never replaces a tracked key.
func (r *Reconciler) restoreRunningJobs(ctx context.Context) bool {
	if r.taskRunStore == nil || r.k8sClient == nil {
		return true
	}

	runs, err := r.taskRunStore.List(ctx)
	if err != nil {
		r.logger.WarnContext(ctx, "could not read task runs to restore running jobs; will try again",
			"error", err,
		)
		return false
	}

	counts := r.adoptRunningJobs(ctx, runs)

	r.logger.InfoContext(ctx, "restored running jobs from persisted task runs",
		"restored", counts.restored,
		"job_gone", counts.jobGone,
		"skipped_tournament", counts.tournament,
		"lookup_failed", counts.failed,
	)
	return counts.failed == 0
}

// adoptRunningJobs re-adopts each restorable run whose Job still exists and
// starts its stream reader.
func (r *Reconciler) adoptRunningJobs(ctx context.Context, runs []*taskrun.TaskRun) jobRecoveryCounts {
	var counts jobRecoveryCounts
	for _, tr := range runningTaskRunsToRestore(runs) {
		if tr.TournamentID != "" {
			// Tournament progress is held by the in-memory coordinator, which
			// a restart has lost; a lone candidate cannot be completed.
			r.logger.WarnContext(ctx, "not restoring tournament task run; tournament state does not survive a restart",
				"task_run_id", tr.ID,
				"tournament_id", tr.TournamentID,
			)
			counts.tournament++
			continue
		}

		exists, err := r.jobExists(ctx, tr.JobName)
		switch {
		case err != nil:
			r.logger.WarnContext(ctx, "could not check job of running task run; not restoring it",
				"task_run_id", tr.ID,
				"job", tr.JobName,
				"error", err,
			)
			counts.failed++
			continue
		case !exists:
			counts.jobGone++
			continue
		}

		if r.adoptTaskRun(tr) {
			// The gauge starts at zero in a new process; the adopted Job is
			// decremented when it finishes, like any other.
			metrics.ActiveJobs.Inc()
			r.startStreamReader(ctx, tr)
			counts.restored++
		}
	}
	return counts
}

// jobExists reports whether the named Job is still in the cluster.
func (r *Reconciler) jobExists(ctx context.Context, jobName string) (bool, error) {
	_, err := r.k8sClient.BatchV1().Jobs(r.namespace).Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// adoptTaskRun puts a persisted TaskRun back under tracking, together with
// its engine fallback chain and the ticket details completion handling reads
// from the ticket cache. It
// returns false when the idempotency key is already tracked, so a run this
// process has started since is never replaced by a stale copy.
func (r *Reconciler) adoptTaskRun(tr *taskrun.TaskRun) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, tracked := r.taskRuns[tr.IdempotencyKey]; tracked {
		return false
	}
	r.taskRuns[tr.IdempotencyKey] = tr
	if len(tr.EngineChain) > 0 {
		// Runs persisted before the chain was recorded have none, and so
		// cannot fall back to another engine if they fail.
		r.engineChains[tr.IdempotencyKey] = tr.EngineChain
	}

	if _, cached := r.ticketCache[tr.TicketID]; !cached {
		r.ticketCache[tr.TicketID] = ticketing.Ticket{
			ID:          tr.TicketID,
			Title:       tr.TicketTitle,
			Description: tr.TicketDescription,
			RepoURL:     tr.RepoURL,
		}
	}
	return true
}

// runningTaskRunsToRestore picks the persisted runs that may still have a
// live Job: those in Running with a Job name. When several share an
// idempotency key, only the most recently updated is kept, because the
// controller tracks one run per key.
func runningTaskRunsToRestore(runs []*taskrun.TaskRun) []*taskrun.TaskRun {
	latest := make(map[string]*taskrun.TaskRun)
	for _, tr := range runs {
		if tr.State != taskrun.StateRunning || tr.JobName == "" {
			continue
		}
		if prev, ok := latest[tr.IdempotencyKey]; ok && !tr.UpdatedAt.After(prev.UpdatedAt) {
			continue
		}
		latest[tr.IdempotencyKey] = tr
	}

	out := make([]*taskrun.TaskRun, 0, len(latest))
	for _, tr := range latest {
		out = append(out, tr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
