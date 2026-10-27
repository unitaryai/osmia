package controller

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/unitaryai/osmia/internal/reviewpoller"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// reviewRestoreWindow bounds how far back restoreReviewTracking looks for
// PRs/MRs to resume watching. A PR opened longer ago than this and still open
// is almost certainly abandoned; without a bound every restart would re-add
// every open PR Osmia has ever opened, each costing a status request on every
// poll. Merged and closed PRs drop out on their first poll regardless.
const reviewRestoreWindow = 30 * 24 * time.Hour

// registerForReview starts review monitoring for the PR/MR a task opened.
//
// The ticket's title, description and repository are taken from the ticket
// cache when present and from the TaskRun otherwise. Before the TaskRun
// carried them, registration was skipped entirely on a cache miss, which is
// exactly what a controller restart during a running job produces: that job's
// PR was then never watched.
func (r *Reconciler) registerForReview(tr *taskrun.TaskRun, prURL string) {
	if r.reviewPoller == nil || prURL == "" {
		return
	}

	title, description, repoURL := tr.TicketTitle, tr.TicketDescription, tr.RepoURL

	r.mu.RLock()
	cached, ok := r.ticketCache[tr.TicketID]
	r.mu.RUnlock()
	if ok {
		title, description, repoURL = cached.Title, cached.Description, cached.RepoURL
	}
	if repoURL == "" {
		repoURL = repoURLFromPRURL(prURL)
	}

	r.reviewPoller.Register(prURL, tr.TicketID, title, description, repoURL)
}

// restoreReviewTracking resumes review monitoring after a restart.
//
// The review poller keeps its list of watched PRs in memory, so before this
// any restart, including the autoscaler moving the controller to another
// node, silently stopped Osmia following up on every PR it had opened. The
// TaskRun store does persist which PR each task opened, so the list is
// rebuilt from it. With the in-memory store this finds nothing, which is
// correct: there is nothing to restore from.
func (r *Reconciler) restoreReviewTracking(ctx context.Context) {
	if r.reviewPoller == nil || r.taskRunStore == nil {
		return
	}

	runs, err := r.taskRunStore.List(ctx)
	if err != nil {
		r.logger.WarnContext(ctx, "could not restore review monitoring; PRs opened before this restart will not be followed up",
			"error", err,
		)
		return
	}

	restored := reviewTrackingFromTaskRuns(runs, time.Now())
	for _, pr := range restored {
		r.reviewPoller.Restore(pr)
	}

	r.logger.InfoContext(ctx, "restored review monitoring from persisted task runs",
		"prs", len(restored),
	)
}

// reviewTrackingFromTaskRuns works out which PRs to resume watching from the
// persisted TaskRuns, and how much of each PR's history was already handled.
//
// A PR is restored when a succeeded, non-follow-up TaskRun opened it within
// reviewRestoreWindow. Follow-up TaskRuns (those with ReviewPRURL set) supply
// the follow-up count, so a restart does not refill MaxFollowUpJobs, and the
// handled-until point: each follow-up was created from the comments the
// poller had seen by then, so comments before the newest follow-up's creation
// are not acted on again. When no follow-up has run, the point is when the
// originating task finished, since nothing on the PR had been read before
// it was registered.
func reviewTrackingFromTaskRuns(runs []*taskrun.TaskRun, now time.Time) []reviewpoller.RestoredPR {
	origins := make(map[string]*taskrun.TaskRun)
	followUps := make(map[string]int)
	lastFollowUp := make(map[string]time.Time)

	for _, tr := range runs {
		if tr.ReviewPRURL != "" {
			followUps[tr.ReviewPRURL]++
			if tr.CreatedAt.After(lastFollowUp[tr.ReviewPRURL]) {
				lastFollowUp[tr.ReviewPRURL] = tr.CreatedAt
			}
			continue
		}
		if tr.State != taskrun.StateSucceeded || tr.Result == nil || tr.Result.MergeRequestURL == "" {
			continue
		}
		if now.Sub(tr.UpdatedAt) > reviewRestoreWindow {
			continue
		}
		prURL := tr.Result.MergeRequestURL
		// A ticket retried to success can report the same PR more than once;
		// keep the most recent run, whose metadata is freshest.
		if existing, ok := origins[prURL]; !ok || tr.UpdatedAt.After(existing.UpdatedAt) {
			origins[prURL] = tr
		}
	}

	restored := make([]reviewpoller.RestoredPR, 0, len(origins))
	for prURL, tr := range origins {
		handledUntil := tr.UpdatedAt
		if last := lastFollowUp[prURL]; last.After(handledUntil) {
			handledUntil = last
		}
		repoURL := tr.RepoURL
		if repoURL == "" {
			repoURL = repoURLFromPRURL(prURL)
		}
		restored = append(restored, reviewpoller.RestoredPR{
			PRURL:               prURL,
			TicketID:            tr.TicketID,
			OriginalTitle:       tr.TicketTitle,
			OriginalDescription: tr.TicketDescription,
			RepoURL:             repoURL,
			FollowUpCount:       followUps[prURL],
			HandledUntil:        handledUntil,
		})
	}
	return restored
}

// repoURLFromPRURL derives a repository URL from a GitLab merge request or
// GitHub pull request URL, for TaskRuns recorded before the repository was
// persisted. It returns the input unchanged if it matches neither shape,
// which still routes to the right SCM backend since routing is by host.
func repoURLFromPRURL(prURL string) string {
	u, err := url.Parse(prURL)
	if err != nil {
		return prURL
	}
	path := u.Path
	switch {
	case strings.Contains(path, "/-/merge_requests/"):
		path = path[:strings.Index(path, "/-/merge_requests/")]
	case strings.Contains(path, "/pull/"):
		path = path[:strings.Index(path, "/pull/")]
	default:
		return prURL
	}
	u.Path = path
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}
