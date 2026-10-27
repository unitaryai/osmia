package reviewpoller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/scmrouter"
	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// Poller monitors open pull/merge requests created by Osmia and emits
// FollowUpRequests when actionable review comments are found.
type Poller struct {
	scmBackend scm.Backend       // non-nil when using a single backend
	scmRouter  *scmrouter.Router // non-nil when using multi-backend routing
	classifier Classifier
	cfg        config.ReviewResponseConfig
	logger     *slog.Logger

	mu        sync.Mutex
	tracked   map[string]*TrackedPR // keyed by PR URL
	followUps []FollowUpRequest

	// selfUsers caches, per backend, the username Osmia's own credentials
	// post comments as, so the poller can ignore its own comments. An empty
	// string records a backend that cannot report its identity, so it is not
	// asked again.
	selfUsers map[scm.Backend]string
}

// New creates a new Poller with the given configuration and classifier.
// Call WithSCMBackend or WithSCMRouter before starting the poller.
func New(cfg config.ReviewResponseConfig, classifier Classifier, logger *slog.Logger) *Poller {
	return &Poller{
		cfg:        cfg,
		classifier: classifier,
		logger:     logger,
		tracked:    make(map[string]*TrackedPR),
		selfUsers:  make(map[scm.Backend]string),
	}
}

// WithSCMBackend configures the poller to use a single SCM backend.
func (p *Poller) WithSCMBackend(b scm.Backend) *Poller {
	p.scmBackend = b
	return p
}

// WithSCMRouter configures the poller to route SCM calls through a
// multi-backend router.
func (p *Poller) WithSCMRouter(r *scmrouter.Router) *Poller {
	p.scmRouter = r
	return p
}

// Register begins monitoring a pull/merge request for review comments. If
// the PR URL is already tracked this call is a no-op.
func (p *Poller) Register(prURL, ticketID, originalTitle, originalDescription, repoURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.tracked[prURL]; ok {
		return
	}

	p.tracked[prURL] = &TrackedPR{
		PRURL:               prURL,
		TicketID:            ticketID,
		OriginalTitle:       originalTitle,
		OriginalDescription: originalDescription,
		RepoURL:             repoURL,
		ProcessedIDs:        make(map[string]bool),
		RegisteredAt:        time.Now(),
	}

	p.logger.Info("registered PR for review monitoring",
		"pr_url", prURL,
		"ticket_id", ticketID,
	)
}

// Restore resumes monitoring a PR after a controller restart. Unlike
// Register it carries the PR's follow-up count and the point up to which its
// comments were already handled, both of which would otherwise be lost with
// the poller's in-memory state. It is a no-op if the PR is already tracked,
// so a PR registered by a task completing during startup is not overwritten.
func (p *Poller) Restore(r RestoredPR) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.tracked[r.PRURL]; ok {
		return
	}

	p.tracked[r.PRURL] = &TrackedPR{
		PRURL:               r.PRURL,
		TicketID:            r.TicketID,
		OriginalTitle:       r.OriginalTitle,
		OriginalDescription: r.OriginalDescription,
		RepoURL:             r.RepoURL,
		FollowUpCount:       r.FollowUpCount,
		ProcessedIDs:        make(map[string]bool),
		// Settling exists to let review bots finish posting after a PR is
		// opened. That happened before the restart, so measure it from then
		// rather than delaying a restored PR by a fresh settling period.
		RegisteredAt: r.HandledUntil,
		IgnoreBefore: r.HandledUntil,
	}

	p.logger.Info("restored PR for review monitoring after restart",
		"pr_url", r.PRURL,
		"ticket_id", r.TicketID,
		"follow_up_count", r.FollowUpCount,
		"ignore_comments_before", r.HandledUntil,
	)
}

// IsTracked reports whether prURL is currently being monitored.
func (p *Poller) IsTracked(prURL string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.tracked[prURL]
	return ok
}

// DrainFollowUps returns and clears the accumulated list of follow-up
// requests. The caller is responsible for submitting jobs for each request.
func (p *Poller) DrainFollowUps() []FollowUpRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.followUps) == 0 {
		return nil
	}
	out := p.followUps
	p.followUps = nil
	return out
}

// Start runs the polling loop in the current goroutine until ctx is cancelled.
// It should be called as a background goroutine: go poller.Start(ctx).
func (p *Poller) Start(ctx context.Context) {
	interval := p.cfg.PollIntervalMinutes
	if interval <= 0 {
		interval = 5
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Minute)
	defer ticker.Stop()

	p.logger.Info("review poller started", "poll_interval_minutes", interval)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("review poller stopped")
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// poll inspects all tracked PRs once, classifying new comments and emitting
// follow-up requests as needed.
func (p *Poller) poll(ctx context.Context) {
	p.mu.Lock()
	// Snapshot the tracked map so we can release the lock during I/O.
	snapshot := make(map[string]*TrackedPR, len(p.tracked))
	for k, v := range p.tracked {
		snapshot[k] = v
	}
	p.mu.Unlock()

	for prURL, pr := range snapshot {
		p.pollPR(ctx, prURL, pr)
	}
}

// pollPR inspects a single tracked PR. Untracked PRs (merged/closed) are
// removed; new actionable comments are batched into a single follow-up request.
func (p *Poller) pollPR(ctx context.Context, prURL string, pr *TrackedPR) {
	// Skip PRs still in the settling period — give review bots time to
	// finish posting all their comments before acting.
	if p.cfg.SettlingMinutes > 0 {
		settlingDuration := time.Duration(p.cfg.SettlingMinutes) * time.Minute
		if time.Since(pr.RegisteredAt) < settlingDuration {
			p.logger.Debug("PR still in settling period, skipping",
				"pr_url", prURL,
				"registered_at", pr.RegisteredAt,
				"settling_minutes", p.cfg.SettlingMinutes,
			)
			return
		}
	}

	backend, err := p.scmFor(pr.RepoURL)
	if err != nil {
		p.logger.Warn("no SCM backend for PR, skipping",
			"pr_url", prURL,
			"error", err,
		)
		return
	}

	// Check PR status — untrack merged or closed PRs.
	status, err := backend.GetPullRequestStatus(ctx, prURL)
	if err != nil {
		p.logger.Warn("failed to get PR status", "pr_url", prURL, "error", err)
		return
	}
	if status.State == "merged" || status.State == "closed" {
		p.mu.Lock()
		delete(p.tracked, prURL)
		p.mu.Unlock()
		p.logger.Info("PR merged or closed, untracking", "pr_url", prURL, "state", status.State)
		return
	}

	// Fetch all comments.
	comments, err := backend.ListReviewComments(ctx, prURL)
	if err != nil {
		p.logger.Warn("failed to list review comments", "pr_url", prURL, "error", err)
		return
	}

	maxFollowUps := p.cfg.MaxFollowUpJobs
	if maxFollowUps <= 0 {
		maxFollowUps = 3
	}

	minSeverity := p.cfg.MinSeverity
	if minSeverity == "" {
		minSeverity = "warning"
	}

	// Collect all actionable comments from this poll, then emit a single
	// batched follow-up request rather than one per comment.
	var actionable []ClassifiedComment

	self := p.selfUser(ctx, backend)

	for _, comment := range comments {
		p.mu.Lock()
		alreadyProcessed := pr.ProcessedIDs[comment.ID]
		p.mu.Unlock()

		if alreadyProcessed {
			continue
		}

		// For a PR restored after a restart, comments from before the point
		// it was last handled were already dealt with. A comment without a
		// timestamp cannot be placed, so it is treated the same way: missing
		// one comment is cheaper than replaying a whole thread into duplicate
		// follow-up jobs.
		if !pr.IgnoreBefore.IsZero() && comment.Created.Before(pr.IgnoreBefore) {
			if comment.Created.IsZero() {
				p.logger.Warn("skipping untimed comment on a PR restored after restart",
					"pr_url", prURL, "comment_id", comment.ID)
			}
			p.mu.Lock()
			pr.ProcessedIDs[comment.ID] = true
			p.mu.Unlock()
			continue
		}

		// Never act on a comment Osmia posted itself. A follow-up job ends by
		// summarising what it did ("Addressed. Fixed the 2 ... errors"), and
		// that summary is full of the words the classifier treats as
		// actionable, so without this each follow-up triggers the next until
		// MaxFollowUpJobs runs out. Applies to inline replies as well as
		// top-level notes.
		if self != "" && strings.EqualFold(comment.Author, self) {
			p.mu.Lock()
			pr.ProcessedIDs[comment.ID] = true
			p.mu.Unlock()
			continue
		}

		classified, err := p.classifier.Classify(ctx, comment)
		if err != nil {
			p.logger.Warn("comment classification failed",
				"pr_url", prURL,
				"comment_id", comment.ID,
				"error", err,
			)
			// Mark as processed to avoid infinite retry.
			p.mu.Lock()
			pr.ProcessedIDs[comment.ID] = true
			p.mu.Unlock()
			continue
		}

		// Always mark the comment as processed regardless of classification.
		p.mu.Lock()
		pr.ProcessedIDs[comment.ID] = true
		p.mu.Unlock()

		if classified.Classification != ClassificationRequiresAction {
			continue
		}

		if !meetsMinSeverity(classified.Severity, minSeverity) {
			continue
		}

		actionable = append(actionable, classified)
	}

	if len(actionable) == 0 {
		return
	}

	// Check follow-up limit before emitting.
	p.mu.Lock()
	if pr.FollowUpCount >= maxFollowUps {
		p.mu.Unlock()
		p.logger.Info("max follow-up limit reached for PR, skipping batch",
			"pr_url", prURL,
			"follow_up_count", pr.FollowUpCount,
			"max", maxFollowUps,
			"actionable_comments", len(actionable),
		)
		return
	}
	pr.FollowUpCount++
	p.mu.Unlock()

	// Reply to each actionable comment individually for visibility.
	var replyIDs []string
	var threadIDs []string
	for _, c := range actionable {
		if p.cfg.ReplyToComments {
			if replyErr := backend.ReplyToComment(ctx, prURL, c.ID, c.ThreadID,
				"👋 Osmia is addressing this feedback."); replyErr != nil {
				p.logger.Warn("failed to reply to comment",
					"pr_url", prURL,
					"comment_id", c.ID,
					"error", replyErr,
				)
			}
			replyIDs = append(replyIDs, c.ID)
		}
		// Always append to maintain 1:1 correspondence with replyIDs.
		threadIDs = append(threadIDs, c.ThreadID)
	}

	req := FollowUpRequest{
		PRURL:               pr.PRURL,
		TicketID:            pr.TicketID,
		OriginalTitle:       pr.OriginalTitle,
		OriginalDescription: pr.OriginalDescription,
		RepoURL:             pr.RepoURL,
		Comments:            actionable,
		EnrichedDescription: buildEnrichedDescription(pr.OriginalDescription, actionable),
		ReplyCommentIDs:     replyIDs,
		ThreadIDs:           threadIDs,
	}

	p.mu.Lock()
	p.followUps = append(p.followUps, req)
	p.mu.Unlock()

	p.logger.Info("batched follow-up request emitted",
		"pr_url", prURL,
		"ticket_id", pr.TicketID,
		"comments", len(actionable),
	)
}

// selfUser returns the username backend's credentials authenticate as, or
// an empty string when it cannot be determined.
//
// The answer is cached per backend. A backend that does not implement
// scm.IdentityProvider is cached as unknown and not asked again. A failed
// lookup is not cached, so a transient API error is retried on the next
// poll; until it succeeds the poller falls back to the classifier's
// name-based bot filtering, which is the behaviour before this existed.
func (p *Poller) selfUser(ctx context.Context, backend scm.Backend) string {
	p.mu.Lock()
	user, cached := p.selfUsers[backend]
	p.mu.Unlock()
	if cached {
		return user
	}

	provider, ok := backend.(scm.IdentityProvider)
	if !ok {
		p.mu.Lock()
		p.selfUsers[backend] = ""
		p.mu.Unlock()
		return ""
	}

	user, err := provider.AuthenticatedUser(ctx)
	if err != nil {
		p.logger.Warn("could not determine Osmia's own SCM identity; its own comments will only be ignored if its account name matches a known bot pattern",
			"backend", backend.Name(),
			"error", err,
		)
		return ""
	}

	p.mu.Lock()
	p.selfUsers[backend] = user
	p.mu.Unlock()

	p.logger.Info("ignoring review comments from Osmia's own SCM account",
		"backend", backend.Name(),
		"username", user,
	)
	return user
}

// scmFor returns the appropriate SCM backend for the given repository URL.
func (p *Poller) scmFor(repoURL string) (scm.Backend, error) {
	if p.scmRouter != nil {
		return p.scmRouter.For(repoURL)
	}
	if p.scmBackend != nil {
		return p.scmBackend, nil
	}
	return nil, fmt.Errorf("no SCM backend configured")
}

// buildEnrichedDescription constructs the follow-up task description by
// appending all review comment contexts to the original ticket description.
func buildEnrichedDescription(originalDescription string, comments []ClassifiedComment) string {
	var sb strings.Builder
	sb.WriteString(originalDescription)

	sb.WriteString("\n\n---\n\n# Review Comments\n")
	for i, comment := range comments {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		sb.WriteString("\n## Comment from @")
		sb.WriteString(comment.Author)
		sb.WriteString("\n\n> ")
		// Quote the comment body (prefix each line with "> ").
		lines := strings.Split(comment.Body, "\n")
		sb.WriteString(strings.Join(lines, "\n> "))

		if comment.FilePath != "" {
			sb.WriteString("\n\n")
			sb.WriteString(comment.FilePath)
			if comment.Line > 0 {
				sb.WriteString(fmt.Sprintf(":%d", comment.Line))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nPlease address all of the above review comments.")
	return sb.String()
}

// meetsMinSeverity returns true if the given severity is at least as severe
// as the minimum required severity.
//
// Severity order (ascending): info < warning < error.
func meetsMinSeverity(severity, minSeverity string) bool {
	order := map[string]int{"info": 0, "warning": 1, "error": 2}
	return order[severity] >= order[minSeverity]
}
