// Package discord implements a NotificationChannel that sends fire-and-forget
// messages to a Discord channel via Discord webhook embeds.
package discord

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
	channelName = "discord"

	// Colour codes for Discord embeds.
	colourGreen = 3066993  // 0x2ECC71 — success
	colourRed   = 15158332 // 0xE74C3C — failure
	colourBlue  = 3447003  // 0x3498DB — informational
)

// DiscordChannel sends fire-and-forget notifications to a Discord channel
// using Discord webhooks with rich embeds.
type DiscordChannel struct {
	webhookURL string
	httpClient *http.Client
	logger     *slog.Logger
}

// Compile-time check that DiscordChannel implements notifications.Channel.
var _ notifications.Channel = (*DiscordChannel)(nil)

// Option is a functional option for configuring a DiscordChannel.
type Option func(*DiscordChannel)

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(d *DiscordChannel) {
		d.httpClient = c
	}
}

// New creates a new DiscordChannel that posts messages via the given webhook URL.
// The webhook URL contains the authentication token and does not require separate credentials.
func New(webhookURL string, opts ...Option) *DiscordChannel {
	ch := &DiscordChannel{
		webhookURL: webhookURL,
		httpClient: http.DefaultClient,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(ch)
	}
	return ch
}

type discordEmbed struct {
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	URL         string         `json:"url,omitempty"`
	Colour      int            `json:"color,omitempty"`
	Fields      []discordField `json:"fields,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordWebhookPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

// Notify sends a free-form notification message to the configured Discord channel.
func (d *DiscordChannel) Notify(ctx context.Context, message string, ticket ticketing.Ticket) error {
	embed := discordEmbed{
		Description: message,
		Colour:      colourBlue,
	}
	if ticket.ExternalURL != "" {
		embed.URL = ticket.ExternalURL
	}
	return d.sendEmbed(ctx, embed)
}

// NotifyStart sends a notification that an agent has begun working on a ticket.
func (d *DiscordChannel) NotifyStart(ctx context.Context, ticket ticketing.Ticket) error {
	embed := discordEmbed{
		Title:       "RoboDev Agent Started",
		Description: ticket.Title,
		Colour:      colourBlue,
	}
	if ticket.ExternalURL != "" {
		embed.URL = ticket.ExternalURL
	}
	return d.sendEmbed(ctx, embed)
}

// NotifyComplete sends a notification that an agent has finished working on a ticket.
func (d *DiscordChannel) NotifyComplete(ctx context.Context, ticket ticketing.Ticket, result engine.TaskResult) error {
	embed := discordEmbed{
		Description: ticket.Title,
	}

	if result.Success {
		embed.Title = "Task Succeeded"
		embed.Colour = colourGreen
	} else {
		embed.Title = "Task Failed"
		embed.Colour = colourRed
	}

	if ticket.ExternalURL != "" {
		embed.URL = ticket.ExternalURL
	}

	if result.Summary != "" {
		embed.Fields = append(embed.Fields, discordField{
			Name:   "Summary",
			Value:  result.Summary,
			Inline: true,
		})
	}

	if result.MergeRequestURL != "" {
		embed.Fields = append(embed.Fields, discordField{
			Name:   "Merge Request",
			Value:  result.MergeRequestURL,
			Inline: true,
		})
	}

	return d.sendEmbed(ctx, embed)
}

// Name returns the unique identifier for this notification channel.
func (d *DiscordChannel) Name() string {
	return channelName
}

// InterfaceVersion returns the version of the NotificationChannel interface
// that this channel implements.
func (d *DiscordChannel) InterfaceVersion() int {
	return notifications.InterfaceVersion
}

// sendEmbed constructs and sends a POST request with the given embed to the Discord webhook.
func (d *DiscordChannel) sendEmbed(ctx context.Context, embed discordEmbed) error {
	payload := discordWebhookPayload{
		Embeds: []discordEmbed{embed},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating discord request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord webhook returned status %d: %s", resp.StatusCode, string(respBody))
	}

	d.logger.DebugContext(ctx, "discord webhook sent",
		slog.String("webhook_url", d.webhookURL),
	)

	return nil
}
