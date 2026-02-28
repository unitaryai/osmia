// Package slack implements an approval.Backend that delivers human-in-the-loop
// questions to a Slack channel as interactive messages with action buttons,
// and receives responses via webhook callback.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/unitaryai/robodev/pkg/plugin/approval"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

const (
	defaultAPIURL = "https://slack.com/api"
	backendName   = "slack"
)

// SlackApprovalBackend implements approval.Backend using Slack interactive
// messages. When approval is requested, it posts a message with action
// buttons. The human's button click is delivered back via HandleCallback.
type SlackApprovalBackend struct {
	channelID       string
	token           string
	apiURL          string
	httpClient      *http.Client
	pendingRequests sync.Map // taskRunID -> message timestamp (string)
	logger          *slog.Logger
}

// Compile-time check that SlackApprovalBackend implements approval.Backend.
var _ approval.Backend = (*SlackApprovalBackend)(nil)

// NewSlackApprovalBackend creates a new SlackApprovalBackend that posts
// interactive messages to the given Slack channel. The token must be a
// valid Slack Bot User OAuth token with chat:write scope.
func NewSlackApprovalBackend(channelID, token string, logger *slog.Logger) *SlackApprovalBackend {
	return &SlackApprovalBackend{
		channelID:  channelID,
		token:      token,
		apiURL:     defaultAPIURL,
		httpClient: http.DefaultClient,
		logger:     logger,
	}
}

// WithAPIURL overrides the default Slack API base URL, useful for testing.
func (s *SlackApprovalBackend) WithAPIURL(url string) *SlackApprovalBackend {
	s.apiURL = url
	return s
}

// WithHTTPClient overrides the default HTTP client, useful for testing.
func (s *SlackApprovalBackend) WithHTTPClient(client *http.Client) *SlackApprovalBackend {
	s.httpClient = client
	return s
}

// slackBlock represents a Slack Block Kit block element.
type slackBlock struct {
	Type     string        `json:"type"`
	Text     *slackText    `json:"text,omitempty"`
	BlockID  string        `json:"block_id,omitempty"`
	Elements []slackButton `json:"elements,omitempty"`
}

// slackText represents a Slack Block Kit text object.
type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// slackButton represents a Slack Block Kit button element.
type slackButton struct {
	Type     string    `json:"type"`
	Text     slackText `json:"text"`
	ActionID string    `json:"action_id"`
	Value    string    `json:"value"`
	Style    string    `json:"style,omitempty"`
}

// slackMessage is the payload sent to chat.postMessage.
type slackMessage struct {
	Channel         string       `json:"channel"`
	Text            string       `json:"text"`
	Blocks          []slackBlock `json:"blocks"`
	ReplaceOriginal bool         `json:"replace_original,omitempty"`
}

// slackUpdateMessage is the payload sent to chat.update.
type slackUpdateMessage struct {
	Channel string       `json:"channel"`
	TS      string       `json:"ts"`
	Text    string       `json:"text"`
	Blocks  []slackBlock `json:"blocks"`
}

// slackResponse is the response from the Slack API.
type slackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	TS    string `json:"ts,omitempty"`
}

// InteractionPayload represents the payload Slack sends when a user clicks
// an interactive button. This is a subset of the full Slack interaction payload.
type InteractionPayload struct {
	Type    string `json:"type"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	Message struct {
		TS string `json:"ts"`
	} `json:"message"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
}

// RequestApproval sends an interactive message to the Slack channel with
// buttons corresponding to the provided options. The message timestamp
// is stored so it can later be updated or cancelled.
func (s *SlackApprovalBackend) RequestApproval(ctx context.Context, question string, ticket ticketing.Ticket, taskRunID string, options []string) error {
	summary := fmt.Sprintf("\u2753 Approval required for: %s", ticket.Title)

	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("\u2753 *Approval required for:* %s", ticket.Title),
			},
		},
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: question,
			},
		},
	}

	if ticket.ExternalURL != "" {
		blocks = append(blocks, slackBlock{
			Type:     "context",
			Elements: []slackButton{}, // cleared below; using text fields instead
			Text:     &slackText{Type: "mrkdwn", Text: fmt.Sprintf("Ticket: <%s|%s> | Task run: `%s`", ticket.ExternalURL, ticket.ID, taskRunID)},
		})
		// Context blocks use a different element format; simplify to a section.
		blocks[len(blocks)-1] = slackBlock{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Ticket: <%s|%s> | Task run: `%s`", ticket.ExternalURL, ticket.ID, taskRunID),
			},
		}
	}

	// Build action buttons from options.
	buttons := make([]slackButton, 0, len(options))
	for i, opt := range options {
		btn := slackButton{
			Type:     "button",
			Text:     slackText{Type: "plain_text", Text: opt},
			ActionID: fmt.Sprintf("robodev_approval_%s_%d", taskRunID, i),
			Value:    opt,
		}
		// Style the first option as primary (green) and "reject" as danger.
		if i == 0 {
			btn.Style = "primary"
		}
		if opt == "reject" || opt == "deny" {
			btn.Style = "danger"
		}
		buttons = append(buttons, btn)
	}

	blocks = append(blocks, slackBlock{
		Type:     "actions",
		BlockID:  fmt.Sprintf("robodev_approval_%s", taskRunID),
		Elements: buttons,
	})

	ts, err := s.postMessage(ctx, summary, blocks)
	if err != nil {
		return err
	}

	s.pendingRequests.Store(taskRunID, ts)
	s.logger.InfoContext(ctx, "approval request sent",
		slog.String("task_run_id", taskRunID),
		slog.String("channel", s.channelID),
		slog.String("ts", ts),
	)

	return nil
}

// CancelPending cancels an outstanding approval request by updating the
// Slack message to show it has been cancelled and removing it from the
// pending requests map.
func (s *SlackApprovalBackend) CancelPending(ctx context.Context, taskRunID string) error {
	tsVal, ok := s.pendingRequests.LoadAndDelete(taskRunID)
	if !ok {
		s.logger.DebugContext(ctx, "no pending approval to cancel",
			slog.String("task_run_id", taskRunID),
		)
		return nil
	}

	ts, _ := tsVal.(string)

	updateMsg := slackUpdateMessage{
		Channel: s.channelID,
		TS:      ts,
		Text:    "\u274C Approval request cancelled",
		Blocks: []slackBlock{
			{
				Type: "section",
				Text: &slackText{
					Type: "mrkdwn",
					Text: fmt.Sprintf("\u274C *Approval request cancelled* for task run `%s`", taskRunID),
				},
			},
		},
	}

	body, err := json.Marshal(updateMsg)
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

	s.logger.InfoContext(ctx, "approval request cancelled",
		slog.String("task_run_id", taskRunID),
		slog.String("ts", ts),
	)

	return nil
}

// HandleCallback parses a Slack interaction payload (from a button click)
// and returns the corresponding approval.Response. The taskRunID is extracted
// from the action block ID, and the chosen option from the button value.
func (s *SlackApprovalBackend) HandleCallback(payload []byte) (*approval.Response, error) {
	var interaction InteractionPayload
	if err := json.Unmarshal(payload, &interaction); err != nil {
		return nil, fmt.Errorf("unmarshalling slack interaction payload: %w", err)
	}

	if len(interaction.Actions) == 0 {
		return nil, fmt.Errorf("no actions in slack interaction payload")
	}

	action := interaction.Actions[0]

	// Extract taskRunID from the pending requests by matching the message timestamp.
	var taskRunID string
	msgTS := interaction.Message.TS
	s.pendingRequests.Range(func(key, value any) bool {
		if value.(string) == msgTS {
			taskRunID = key.(string)
			return false
		}
		return true
	})

	if taskRunID == "" {
		return nil, fmt.Errorf("no pending approval found for message timestamp %s", msgTS)
	}

	// Remove from pending now that we have a response.
	s.pendingRequests.Delete(taskRunID)

	// Determine approval status from the chosen option.
	approved := action.Value != "reject" && action.Value != "deny"

	resp := &approval.Response{
		TaskRunID: taskRunID,
		Approved:  approved,
		Message:   action.Value,
		Responder: interaction.User.ID,
	}

	s.logger.Info("approval callback received",
		slog.String("task_run_id", taskRunID),
		slog.Bool("approved", approved),
		slog.String("responder", interaction.User.ID),
		slog.String("choice", action.Value),
	)

	return resp, nil
}

// Name returns the unique identifier for this approval backend.
func (s *SlackApprovalBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the version of the HumanApprovalBackend interface
// that this backend implements.
func (s *SlackApprovalBackend) InterfaceVersion() int {
	return approval.InterfaceVersion
}

// postMessage sends a message to the Slack channel and returns the message timestamp.
func (s *SlackApprovalBackend) postMessage(ctx context.Context, fallbackText string, blocks []slackBlock) (string, error) {
	msg := slackMessage{
		Channel: s.channelID,
		Text:    fallbackText,
		Blocks:  blocks,
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

	return slackResp.TS, nil
}
