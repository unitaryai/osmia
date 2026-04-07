// Package telegram implements a NotificationChannel that sends fire-and-forget
// messages to a Telegram chat via the Telegram Bot API (sendMessage).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/notifications"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

const (
	defaultAPIURL = "https://api.telegram.org"
	channelName   = "telegram"
)

// TelegramChannel sends fire-and-forget notifications to a Telegram chat
// using the Telegram Bot API sendMessage endpoint.
type TelegramChannel struct {
	token      string
	chatID     string
	apiURL     string
	httpClient *http.Client
	logger     *slog.Logger
	threadID   int
}

// Compile-time check that TelegramChannel implements notifications.Channel.
var _ notifications.Channel = (*TelegramChannel)(nil)

// Option is a functional option for configuring a TelegramChannel.
type Option func(*TelegramChannel)

// WithAPIURL overrides the default Telegram API base URL, useful for testing.
func WithAPIURL(url string) Option {
	return func(t *TelegramChannel) {
		t.apiURL = url
	}
}

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(t *TelegramChannel) {
		t.httpClient = c
	}
}

// WithThreadID sets the message thread ID for topic-based chats.
func WithThreadID(id int) Option {
	return func(t *TelegramChannel) {
		t.threadID = id
	}
}

// New creates a new TelegramChannel that posts messages to the given chat.
// The token must be a valid Telegram Bot API token.
func New(token, chatID string, opts ...Option) *TelegramChannel {
	ch := &TelegramChannel{
		token:      token,
		chatID:     chatID,
		apiURL:     defaultAPIURL,
		httpClient: http.DefaultClient,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(ch)
	}
	return ch
}

// telegramMessage is the payload sent to the sendMessage endpoint.
type telegramMessage struct {
	ChatID          string `json:"chat_id"`
	Text            string `json:"text"`
	ParseMode       string `json:"parse_mode"`
	MessageThreadID int    `json:"message_thread_id,omitempty"`
}

// telegramResponse is the response from the Telegram Bot API.
type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
}

// Notify sends a free-form notification message to the configured Telegram chat.
// threadRef is accepted for interface compatibility but ignored — Telegram does
// not have a threading model equivalent to Slack's.
func (t *TelegramChannel) Notify(ctx context.Context, message string, ticket ticketing.Ticket, _ string) error {
	text := message
	if ticket.ExternalURL != "" {
		text += "\n[" + ticket.ID + "](" + ticket.ExternalURL + ")"
	}
	return t.sendMessage(ctx, text)
}

// NotifyStart sends a notification that an agent has begun working on a ticket.
// Always returns an empty thread reference because Telegram does not support
// cross-message threading.
func (t *TelegramChannel) NotifyStart(ctx context.Context, ticket ticketing.Ticket) (string, error) {
	text := fmt.Sprintf("\U0001F916 *Osmia agent started working on:* %s", ticket.Title)
	if ticket.ExternalURL != "" {
		text += "\n[" + ticket.ID + "](" + ticket.ExternalURL + ")"
	}
	return "", t.sendMessage(ctx, text)
}

// NotifyComplete sends a notification that an agent has finished working on a ticket.
// threadRef is accepted for interface compatibility but ignored.
func (t *TelegramChannel) NotifyComplete(ctx context.Context, ticket ticketing.Ticket, result engine.TaskResult, _ string) error {
	var text string
	if result.Success {
		text = fmt.Sprintf("\u2705 *Task succeeded:* %s", ticket.Title)
	} else {
		text = fmt.Sprintf("\u274C *Task failed:* %s", ticket.Title)
	}

	if result.Summary != "" {
		text += "\n" + result.Summary
	}

	if result.Success && result.MergeRequestURL != "" {
		text += "\n[View merge request](" + result.MergeRequestURL + ")"
	}

	return t.sendMessage(ctx, text)
}

// UpdateMessage is a no-op — Telegram bot messages would require storing
// message IDs which is not currently supported.
func (t *TelegramChannel) UpdateMessage(_ context.Context, _ string, _ string) error {
	return nil
}

// Name returns the unique identifier for this notification channel.
func (t *TelegramChannel) Name() string {
	return channelName
}

// InterfaceVersion returns the version of the NotificationChannel interface
// that this channel implements.
func (t *TelegramChannel) InterfaceVersion() int {
	return notifications.InterfaceVersion
}

// sendMessage constructs and sends a POST request to the Telegram Bot API.
func (t *TelegramChannel) sendMessage(ctx context.Context, text string) error {
	msg := telegramMessage{
		ChatID:    t.chatID,
		Text:      text,
		ParseMode: "Markdown",
	}
	if t.threadID != 0 {
		msg.MessageThreadID = t.threadID
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling telegram message: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", t.apiURL, t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating telegram request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending telegram message: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading telegram response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var tgResp telegramResponse
	if err := json.Unmarshal(respBody, &tgResp); err != nil {
		return fmt.Errorf("unmarshalling telegram response: %w", err)
	}

	if !tgResp.OK {
		return fmt.Errorf("telegram API error: %s", tgResp.Description)
	}

	t.logger.DebugContext(ctx, "telegram message sent",
		slog.String("chat_id", t.chatID),
	)

	return nil
}
