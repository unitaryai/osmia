// Package slack implements a NotificationChannel that sends fire-and-forget
// messages to a Slack channel via the Slack Web API (chat.postMessage).
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/notifications"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

const (
	defaultAPIURL = "https://slack.com/api"
	channelName   = "slack"
)

// SlackChannel sends fire-and-forget notifications to a Slack channel
// using the Slack Web API chat.postMessage endpoint.
type SlackChannel struct {
	channelID  string
	token      string
	apiURL     string
	httpClient *http.Client
	logger     *slog.Logger
}

// Compile-time check that SlackChannel implements notifications.Channel.
var _ notifications.Channel = (*SlackChannel)(nil)

// NewSlackChannel creates a new SlackChannel that posts messages to the
// given Slack channel. The token must be a valid Slack Bot User OAuth token
// with the chat:write scope.
func NewSlackChannel(channelID, token string, logger *slog.Logger) *SlackChannel {
	return &SlackChannel{
		channelID:  channelID,
		token:      token,
		apiURL:     defaultAPIURL,
		httpClient: http.DefaultClient,
		logger:     logger,
	}
}

// WithAPIURL overrides the default Slack API base URL, useful for testing.
func (s *SlackChannel) WithAPIURL(url string) *SlackChannel {
	s.apiURL = url
	return s
}

// WithHTTPClient overrides the default HTTP client, useful for testing.
func (s *SlackChannel) WithHTTPClient(client *http.Client) *SlackChannel {
	s.httpClient = client
	return s
}

// slackBlock represents a Slack Block Kit block element.
type slackBlock struct {
	Type   string      `json:"type"`
	Text   *slackText  `json:"text,omitempty"`
	Fields []slackText `json:"fields,omitempty"`
}

// slackText represents a Slack Block Kit text object.
type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// slackMessage is the payload sent to chat.postMessage.
type slackMessage struct {
	Channel string       `json:"channel"`
	Text    string       `json:"text"`
	Blocks  []slackBlock `json:"blocks"`
}

// slackResponse is the response from the Slack API.
type slackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	TS    string `json:"ts,omitempty"`
}

// Notify sends a free-form notification message to the configured Slack channel.
func (s *SlackChannel) Notify(ctx context.Context, message string, ticket ticketing.Ticket) error {
	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: message},
		},
	}

	if ticket.ExternalURL != "" {
		blocks = append(blocks, slackBlock{
			Type: "context",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("<%s|%s>", ticket.ExternalURL, ticket.ID)},
			},
		})
	}

	return s.postMessage(ctx, message, blocks)
}

// NotifyStart sends a notification that an agent has begun working on a ticket.
func (s *SlackChannel) NotifyStart(ctx context.Context, ticket ticketing.Ticket) error {
	summary := fmt.Sprintf("\U0001F916 RoboDev agent started working on: %s", ticket.Title)

	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("\U0001F916 *RoboDev agent started working on:* %s", ticket.Title),
			},
		},
	}

	if ticket.ExternalURL != "" {
		blocks = append(blocks, slackBlock{
			Type: "context",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("Ticket: <%s|%s>", ticket.ExternalURL, ticket.ID)},
			},
		})
	}

	return s.postMessage(ctx, summary, blocks)
}

// NotifyComplete sends a notification that an agent has finished working on a ticket.
func (s *SlackChannel) NotifyComplete(ctx context.Context, ticket ticketing.Ticket, result engine.TaskResult) error {
	statusEmoji := "\u2705"
	statusText := "succeeded"
	if !result.Success {
		statusEmoji = "\u274C"
		statusText = "failed"
	}

	summary := fmt.Sprintf("%s RoboDev agent %s on: %s", statusEmoji, statusText, ticket.Title)

	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("%s *RoboDev agent %s on:* %s", statusEmoji, statusText, ticket.Title),
			},
		},
	}

	if result.Summary != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: fmt.Sprintf("*Summary:* %s", result.Summary)},
		})
	}

	if result.MergeRequestURL != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Merge request:* <%s|View MR>", result.MergeRequestURL),
			},
		})
	}

	if ticket.ExternalURL != "" {
		blocks = append(blocks, slackBlock{
			Type: "context",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("Ticket: <%s|%s>", ticket.ExternalURL, ticket.ID)},
			},
		})
	}

	return s.postMessage(ctx, summary, blocks)
}

// Name returns the unique identifier for this notification channel.
func (s *SlackChannel) Name() string {
	return channelName
}

// InterfaceVersion returns the version of the NotificationChannel interface
// that this channel implements.
func (s *SlackChannel) InterfaceVersion() int {
	return notifications.InterfaceVersion
}

// postMessage sends a message to the Slack channel via chat.postMessage.
func (s *SlackChannel) postMessage(ctx context.Context, fallbackText string, blocks []slackBlock) error {
	msg := slackMessage{
		Channel: s.channelID,
		Text:    fallbackText,
		Blocks:  blocks,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling slack message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending slack message: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading slack response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var slackResp slackResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return fmt.Errorf("unmarshalling slack response: %w", err)
	}

	if !slackResp.OK {
		return fmt.Errorf("slack API error: %s", slackResp.Error)
	}

	s.logger.DebugContext(ctx, "slack message sent",
		slog.String("channel", s.channelID),
		slog.String("ts", slackResp.TS),
	)

	return nil
}
