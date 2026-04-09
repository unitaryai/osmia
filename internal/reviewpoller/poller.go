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
}

// New creates a new Poller with the given configuration and classifier.
// Call WithSCMBackend or WithSCMRouter before starting the poller.
func New(cfg config.ReviewResponseConfig, classifier Classifier, logger *slog.Logger) *Poller {
	return &Poller{
		cfg:        cfg,
		classifier: classifier,
		logger:     logger,
		tracked:    make(map[string]*TrackedPR),
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

	for _, comment := range comments {
		p.mu.Lock()
		alreadyProcessed := pr.ProcessedIDs[comment.ID]
		p.mu.Unlock()

		if alreadyProcessed {
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
