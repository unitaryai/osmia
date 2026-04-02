package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// repoURLPattern matches GitHub and GitLab repository URLs in free text.
// It captures URLs like https://github.com/org/repo or
// https://gitlab.com/group/subgroup/project — stopping at whitespace,
// quotes, angle brackets, or closing parentheses.
var repoURLPattern = regexp.MustCompile(
	`https?://(?:github\.com|gitlab\.com)/[^\s)"'<>]+`,
)

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

// resolveRepoURL attempts to fill in a missing RepoURL on the ticket.
// It first tries to extract a URL from the ticket description, then
// falls back to asking on Slack if a notification channel is available.
func (r *Reconciler) resolveRepoURL(ctx context.Context, ticket *ticketing.Ticket) error {
	// Step 1: try to extract from the description text.
	if url := extractRepoURL(ticket.Description); url != "" {
		r.logger.InfoContext(ctx, "extracted repo URL from ticket description",
			"ticket_id", ticket.ID,
			"repo_url", url,
		)
		ticket.RepoURL = url
		return nil
	}

	// Step 2: ask on Slack and wait for a reply.
	if r.slackRepoURLPoller != nil {
		r.logger.InfoContext(ctx, "no repo URL in description, asking on Slack",
			"ticket_id", ticket.ID,
		)

		// Post the question in the task's notification thread if available.
		threadRef := r.ticketNotificationRef(ticket.ID)

		url, err := r.slackRepoURLPoller.AskForRepoURL(ctx, ticket.ID, ticket.Title, threadRef)
		if err != nil {
			return fmt.Errorf("slack repo URL request failed: %w", err)
		}
		if url == "" {
			return fmt.Errorf("no repo URL provided via Slack")
		}

		r.logger.InfoContext(ctx, "received repo URL from Slack",
			"ticket_id", ticket.ID,
			"repo_url", url,
		)
		ticket.RepoURL = url
		return nil
	}

	return fmt.Errorf("no repo URL found in ticket description and no Slack channel configured to ask")
}

// ticketNotificationRef returns the notification thread reference for a
// ticket, if one exists in the controller's cache.
func (r *Reconciler) ticketNotificationRef(ticketID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if ref, ok := r.ticketNotificationRefs[ticketID]; ok {
		return ref
	}
	return ""
}

// SlackRepoURLPoller posts a question to Slack and polls for a reply
// containing a repository URL. It uses the Slack Web API directly.
type SlackRepoURLPoller struct {
	token      string
	channelID  string
	apiURL     string
	httpClient *http.Client
	timeout    time.Duration
}

// NewSlackRepoURLPoller creates a poller that uses the given Slack bot
// token and channel to ask humans for a repository URL.
func NewSlackRepoURLPoller(token, channelID string) *SlackRepoURLPoller {
	return &SlackRepoURLPoller{
		token:      token,
		channelID:  channelID,
		apiURL:     "https://slack.com/api",
		httpClient: http.DefaultClient,
		timeout:    5 * time.Minute,
	}
}

// AskForRepoURL posts a question to Slack and polls for a threaded reply
// containing a repository URL. Returns the URL or an error on timeout.
func (p *SlackRepoURLPoller) AskForRepoURL(ctx context.Context, ticketID, ticketTitle, threadTS string) (string, error) {
	question := fmt.Sprintf(
		"🔗 *No repository URL found for sc-%s (%s).*\n\nReply in this thread with the git repository URL (e.g. `https://github.com/org/repo`).",
		ticketID, ticketTitle,
	)

	questionTS, err := p.postMessage(ctx, question, threadTS)
	if err != nil {
		return "", fmt.Errorf("posting Slack message: %w", err)
	}

	// Determine which thread to poll — use the existing thread if available,
	// otherwise the question message itself becomes the thread root.
	pollThreadTS := threadTS
	if pollThreadTS == "" {
		pollThreadTS = questionTS
	}

	return p.pollForRepoURL(ctx, pollThreadTS, questionTS)
}

// postMessage sends a text message to the configured Slack channel,
// optionally as a threaded reply. Returns the message timestamp.
func (p *SlackRepoURLPoller) postMessage(ctx context.Context, text, threadTS string) (string, error) {
	payload := map[string]any{
		"channel": p.channelID,
		"text":    text,
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL+"/chat.postMessage", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK bool   `json:"ok"`
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("slack API returned ok=false")
	}
	return result.TS, nil
}

// pollForRepoURL polls a Slack thread for a non-bot reply containing a
// repository URL. Returns the extracted URL or an error on timeout.
func (p *SlackRepoURLPoller) pollForRepoURL(ctx context.Context, threadTS, afterTS string) (string, error) {
	deadline := time.After(p.timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("timed out waiting for repo URL reply (%.0fs)", p.timeout.Seconds())
		case <-ticker.C:
			url, err := p.checkReplies(ctx, threadTS, afterTS)
			if err != nil {
				continue // transient errors — keep polling
			}
			if url != "" {
				return url, nil
			}
		}
	}
}

// checkReplies fetches thread replies and looks for a non-bot message
// containing a repository URL.
func (p *SlackRepoURLPoller) checkReplies(ctx context.Context, threadTS, afterTS string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiURL+"/conversations.replies", nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	q.Set("channel", p.channelID)
	q.Set("ts", threadTS)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK       bool `json:"ok"`
		Messages []struct {
			TS    string `json:"ts"`
			Text  string `json:"text"`
			BotID string `json:"bot_id,omitempty"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("slack API returned ok=false")
	}

	for _, msg := range result.Messages {
		if msg.TS <= afterTS || msg.BotID != "" {
			continue
		}
		if url := extractRepoURL(msg.Text); url != "" {
			return url, nil
		}
	}
	return "", nil
}
