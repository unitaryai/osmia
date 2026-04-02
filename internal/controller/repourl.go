package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// repoURLPattern matches GitHub and GitLab repository URLs in free text.
// It captures URLs like https://github.com/org/repo or
// https://gitlab.com/group/subgroup/project — stopping at whitespace,
// quotes, angle brackets, or closing parentheses.
var repoURLPattern = regexp.MustCompile(
	`https?://(?:github\.com|gitlab\.com)/[^\s)"'<>]+`,
)

// RepoURLPoller asks a human for a repository URL and polls for the
// answer. Implementations may use Slack, Teams, or any other channel.
type RepoURLPoller interface {
	// AskForRepoURL posts a question and blocks until the human replies
	// with a URL or the timeout expires. threadTS, when non-empty, causes
	// the question to be posted as a threaded reply.
	AskForRepoURL(ctx context.Context, ticketID, ticketTitle, threadTS string) (string, error)
}

// extractRepoURL scans text for a GitHub or GitLab repository URL and
// returns the first match. Returns empty string if none found.
func extractRepoURL(text string) string {
	match := repoURLPattern.FindString(text)
	if match == "" {
		return ""
	}
	// Strip common trailing punctuation that may have been captured
	// from markdown or prose (e.g. trailing period, comma, colon).
	match = strings.TrimRight(match, ".,;:!?")
	return match
}

// resolveRepoURL attempts to fill in a missing RepoURL on the ticket
// by extracting it from the description. This only handles the
// synchronous extraction step — Slack polling is handled asynchronously
// by startRepoURLPoll.
func (r *Reconciler) resolveRepoURL(ctx context.Context, ticket *ticketing.Ticket) bool {
	if url := extractRepoURL(ticket.Description); url != "" {
		r.logger.InfoContext(ctx, "extracted repo URL from ticket description",
			"ticket_id", ticket.ID,
			"repo_url", url,
		)
		ticket.RepoURL = url
		return true
	}
	return false
}

// startRepoURLPoll creates a TaskRun in NeedsHuman state and starts a
// background goroutine that polls the configured RepoURLPoller. When a
// URL is received the ticket cache is updated and processing resumes via
// resumeAfterRepoURL. This method returns immediately so ProcessTicket
// does not block the reconcile loop.
func (r *Reconciler) startRepoURLPoll(ctx context.Context, ticket ticketing.Ticket, idempotencyKey string, engineChain []string) error {
	engineName := engineChain[0]
	tr := taskrun.New(
		fmt.Sprintf("tr-%s-%d", ticket.ID, time.Now().UnixMilli()),
		idempotencyKey,
		ticket.ID,
		engineName,
	)
	tr.CurrentEngine = engineName
	tr.EngineAttempts = []string{engineName}

	if err := tr.Transition(taskrun.StateNeedsHuman); err != nil {
		return fmt.Errorf("transitioning to NeedsHuman: %w", err)
	}
	tr.HumanQuestion = "provide repository URL"
	tr.ApprovalGateType = "missing_repo_url"

	r.mu.Lock()
	r.taskRuns[idempotencyKey] = tr
	r.engineChains[idempotencyKey] = engineChain
	r.mu.Unlock()

	if err := r.taskRunStore.Save(ctx, tr); err != nil {
		r.logger.ErrorContext(ctx, "failed to save task run after repo URL gate",
			"task_run_id", tr.ID,
			"error", err,
		)
	}

	// Post a notification to get a thread reference, then start polling
	// in that thread so the question lands in the right place.
	threadRef := r.runNotifyStart(ctx, ticket)
	tr.NotificationThreadRef = threadRef

	r.logger.InfoContext(ctx, "waiting for repo URL via Slack",
		"ticket_id", ticket.ID,
		"task_run_id", tr.ID,
	)

	go r.pollRepoURL(context.Background(), tr.ID, ticket, threadRef)
	return nil
}

// pollRepoURL runs in a background goroutine, polling the RepoURLPoller
// for a human-provided repository URL. On success it updates the ticket
// cache and resumes processing; on failure it marks the ticket as failed.
func (r *Reconciler) pollRepoURL(ctx context.Context, taskRunID string, ticket ticketing.Ticket, threadRef string) {
	url, err := r.repoURLPoller.AskForRepoURL(ctx, ticket.ID, ticket.Title, threadRef)
	if err != nil || url == "" {
		reason := "no repo URL provided via Slack"
		if err != nil {
			reason = fmt.Sprintf("slack repo URL request failed: %v", err)
		}
		r.logger.WarnContext(ctx, reason, "ticket_id", ticket.ID)

		if r.ticketing != nil {
			if markErr := r.ticketing.MarkFailed(ctx, ticket.ID, reason); markErr != nil {
				r.logger.ErrorContext(ctx, "failed to mark ticket failed",
					"ticket_id", ticket.ID,
					"error", markErr,
				)
			}
		}

		// Transition the TaskRun to Failed.
		r.mu.Lock()
		for _, tr := range r.taskRuns {
			if tr.ID == taskRunID {
				_ = tr.Transition(taskrun.StateFailed)
				break
			}
		}
		r.mu.Unlock()
		return
	}

	r.logger.InfoContext(ctx, "received repo URL from Slack",
		"ticket_id", ticket.ID,
		"repo_url", url,
	)

	// Update the ticket cache with the resolved URL.
	ticket.RepoURL = url
	r.mu.Lock()
	r.ticketCache[ticket.ID] = ticket
	r.mu.Unlock()

	// Resume processing by resolving the approval gate.
	if err := r.ResolveApproval(ctx, taskRunID, true, "slack-repo-url-poller"); err != nil {
		r.logger.ErrorContext(ctx, "failed to resume after repo URL resolution",
			"task_run_id", taskRunID,
			"error", err,
		)
	}
}
