// Package webhook provides an HTTP server that receives webhook events from
// ticketing backends and approval channels, validates their signatures, and
// feeds parsed tickets into the controller's reconciliation loop.
package webhook

import (
	"context"
	"encoding/json"
	"time"

	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// EventHandler is the interface that consumers must implement to receive
// parsed webhook events. Typically the controller's reconciliation loop
// implements this to enqueue tickets for processing.
type EventHandler interface {
	// HandleWebhookEvent processes a batch of tickets extracted from an
	// incoming webhook request. The source parameter identifies which
	// webhook backend produced the event (e.g. "github", "gitlab").
	HandleWebhookEvent(ctx context.Context, source string, tickets []ticketing.Ticket) error
}

// WebhookEvent represents a parsed webhook delivery. It contains the
// extracted tickets along with metadata about the original request.
type WebhookEvent struct {
	// Source identifies the webhook backend (e.g. "github", "gitlab", "slack").
	Source string `json:"source"`

	// Tickets contains the ticketing items extracted from the payload.
	Tickets []ticketing.Ticket `json:"tickets"`

	// RawPayload holds the original request body for auditing or debugging.
	RawPayload json.RawMessage `json:"raw_payload"`

	// ReceivedAt records when the webhook was received by the server.
	ReceivedAt time.Time `json:"received_at"`
}
