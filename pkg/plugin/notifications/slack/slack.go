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
	"strings"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/notifications"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
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
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`   // used by section blocks
	Elements []slackText `json:"elements,omitempty"` // used by context blocks
}

// slackText represents a Slack Block Kit text object.
type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// slackMessage is the payload sent to chat.postMessage.
type slackMessage struct {
	Channel        string       `json:"channel"`
	Text           string       `json:"text"`
	Blocks         []slackBlock `json:"blocks"`
	ThreadTS       string       `json:"thread_ts,omitempty"`
	ReplyBroadcast bool         `json:"reply_broadcast,omitempty"`
}

// slackResponse is the response from the Slack API.
type slackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	TS    string `json:"ts,omitempty"`
}

// Notify sends a free-form notification message to the configured Slack channel.
// When threadRef is non-empty the message is posted as a reply in that thread.
func (s *SlackChannel) Notify(ctx context.Context, message string, ticket ticketing.Ticket, threadRef string) error {
	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: message},
		},
	}

	if ticket.ExternalURL != "" {
		blocks = append(blocks, slackBlock{
			Type: "context",
			Elements: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("<%s|%s>", ticket.ExternalURL, ticket.ID)},
			},
		})
	}

	_, err := s.postMessage(ctx, message, blocks, threadRef, false)
	return err
}

// NotifyStart sends a notification that an agent has begun working on a ticket.
// It returns the Slack message timestamp, which callers should pass as threadRef
// to subsequent Notify and NotifyComplete calls to keep all messages in one thread.
func (s *SlackChannel) NotifyStart(ctx context.Context, ticket ticketing.Ticket) (string, error) {
	summary := fmt.Sprintf("\U0001F916 Osmia agent started working on: %s", ticket.Title)

	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("\U0001F916 *Osmia agent started working on:* %s", ticket.Title),
			},
		},
	}

	if ticket.ExternalURL != "" {
		blocks = append(blocks, slackBlock{
			Type: "context",
			Elements: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("Ticket: <%s|%s>", ticket.ExternalURL, ticket.ID)},
			},
		})
	}

	return s.postMessage(ctx, summary, blocks, "", false)
}

// NotifyComplete sends a notification that an agent has finished working on a ticket.
// When threadRef is non-empty the message is posted as a reply in that thread and
// broadcast to the channel so it is visible outside the thread.
func (s *SlackChannel) NotifyComplete(ctx context.Context, ticket ticketing.Ticket, result engine.TaskResult, threadRef string) error {
	statusEmoji := "\u2705"
	statusText := "succeeded"
	if !result.Success {
		statusEmoji = "\u274C"
		statusText = "failed"
	}

	summary := fmt.Sprintf("%s Osmia agent %s on: %s", statusEmoji, statusText, ticket.Title)

	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("%s *Osmia agent %s on:* %s", statusEmoji, statusText, ticket.Title),
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

	// Cost, token usage, and ticket link grouped into a single context block.
	var metaFields []string
	if result.CostEstimateUSD > 0 {
		metaFields = append(metaFields, fmt.Sprintf("Cost: $%.4f", result.CostEstimateUSD))
	}
	if result.TokenUsage != nil {
		metaFields = append(metaFields, fmt.Sprintf("Tokens: %d in / %d out", result.TokenUsage.InputTokens, result.TokenUsage.OutputTokens))
	}
	if ticket.ExternalURL != "" {
		metaFields = append(metaFields, fmt.Sprintf("Ticket: <%s|%s>", ticket.ExternalURL, ticket.ID))
	}
	if len(metaFields) > 0 {
		blocks = append(blocks, slackBlock{
			Type:     "context",
			Elements: []slackText{{Type: "mrkdwn", Text: strings.Join(metaFields, "  ·  ")}},
		})
	}

	// Post completion details in the thread and broadcast to the channel so
	// the summary is visible. The controller also updates the original message
	// in-place with the final status via UpdateMessage.
	replyBroadcast := threadRef != ""
	_, err := s.postMessage(ctx, summary, blocks, threadRef, replyBroadcast)
	return err
}

// UpdateMessage replaces the content of a previously posted message.
// messageRef must be the Slack message timestamp returned by NotifyStart.
func (s *SlackChannel) UpdateMessage(ctx context.Context, messageRef string, text string) error {
	if messageRef == "" {
		return nil
	}

	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: text},
		},
	}

	return s.updateMessage(ctx, messageRef, text, blocks)
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
// threadTS, when non-empty, posts the message as a reply in that thread.
// replyBroadcast, when true, also sends the reply to the main channel feed.
// Returns the message timestamp from the Slack API response.
func (s *SlackChannel) postMessage(ctx context.Context, fallbackText string, blocks []slackBlock, threadTS string, replyBroadcast bool) (string, error) {
	msg := slackMessage{
		Channel:        s.channelID,
		Text:           fallbackText,
		Blocks:         blocks,
		ThreadTS:       threadTS,
		ReplyBroadcast: replyBroadcast,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshalling slack message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating slack request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending slack message: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading slack response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("slack API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var slackResp slackResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return "", fmt.Errorf("unmarshalling slack response: %w", err)
	}

	if !slackResp.OK {
		return "", fmt.Errorf("slack API error: %s", slackResp.Error)
	}

	s.logger.DebugContext(ctx, "slack message sent",
		slog.String("channel", s.channelID),
		slog.String("ts", slackResp.TS),
	)

	return slackResp.TS, nil
}

// slackUpdateMessage is the payload sent to chat.update.
type slackUpdateMessage struct {
	Channel string       `json:"channel"`
	TS      string       `json:"ts"`
	Text    string       `json:"text"`
	Blocks  []slackBlock `json:"blocks"`
}

// updateMessage replaces the content of a previously posted message via
// chat.update. Requires the same chat:write scope as chat.postMessage.
func (s *SlackChannel) updateMessage(ctx context.Context, ts string, fallbackText string, blocks []slackBlock) error {
	msg := slackUpdateMessage{
		Channel: s.channelID,
		TS:      ts,
		Text:    fallbackText,
		Blocks:  blocks,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling slack update: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/chat.update", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack update request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending slack update: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading slack update response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var slackResp slackResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return fmt.Errorf("unmarshalling slack update response: %w", err)
	}

	if !slackResp.OK {
		return fmt.Errorf("slack API error: %s", slackResp.Error)
	}

	s.logger.DebugContext(ctx, "slack message updated",
		slog.String("channel", s.channelID),
		slog.String("ts", ts),
	)

	return nil
}
