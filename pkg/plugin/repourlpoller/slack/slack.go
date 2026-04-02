// Package slack implements a RepoURLPoller that asks humans for a
// repository URL via Slack thread replies.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// repoURLPattern matches GitHub and GitLab repository URLs.
var repoURLPattern = regexp.MustCompile(
	`https?://(?:github\.com|gitlab\.com)/[^\s)"'<>]+`,
)

// Config holds configurable parameters for the Slack poller.
type Config struct {
	// Timeout is how long to wait for a human reply. Defaults to 5 minutes.
	Timeout time.Duration
	// PollInterval is how often to check for new replies. Defaults to 5 seconds.
	PollInterval time.Duration
}

// Poller posts a question to Slack and polls for a threaded reply
// containing a repository URL.
type Poller struct {
	token        string
	channelID    string
	apiURL       string
	httpClient   *http.Client
	timeout      time.Duration
	pollInterval time.Duration
}

// New creates a Poller with the given Slack bot token and channel ID.
func New(token, channelID string, cfg Config) *Poller {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Poller{
		token:        token,
		channelID:    channelID,
		apiURL:       "https://slack.com/api",
		httpClient:   http.DefaultClient,
		timeout:      timeout,
		pollInterval: interval,
	}
}

// AskForRepoURL posts a question to Slack and polls for a threaded reply
// containing a repository URL. Returns the URL or an error on timeout.
func (p *Poller) AskForRepoURL(ctx context.Context, ticketID, ticketTitle, threadTS string) (string, error) {
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

func (p *Poller) postMessage(ctx context.Context, text, threadTS string) (string, error) {
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

func (p *Poller) pollForRepoURL(ctx context.Context, threadTS, afterTS string) (string, error) {
	deadline := time.After(p.timeout)
	ticker := time.NewTicker(p.pollInterval)
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

func (p *Poller) checkReplies(ctx context.Context, threadTS, afterTS string) (string, error) {
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

func extractRepoURL(text string) string {
	match := repoURLPattern.FindString(text)
	if match == "" {
		return ""
	}
	return strings.TrimRight(match, ".,;:!?")
}
