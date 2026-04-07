// Package notifications defines the NotificationChannel interface for
// sending fire-and-forget notifications to external systems (Slack,
// Microsoft Teams, Discord, etc). Notifications are one-way; for
// interactive human-in-the-loop flows, see the approval package.
package notifications

import (
	"context"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// InterfaceVersion is the current version of the NotificationChannel interface.
const InterfaceVersion = 3

// Channel is the interface that notification backends must implement.
// All methods are fire-and-forget — errors are logged but do not block
// the controller reconciliation loop.
type Channel interface {
	// Notify sends a free-form notification message associated with a ticket.
	// threadRef, when non-empty, requests that the message be posted as a reply
	// in the thread identified by that reference (e.g. a Slack message timestamp).
	// Backends that do not support threading silently ignore threadRef.
	Notify(ctx context.Context, message string, ticket ticketing.Ticket, threadRef string) error

	// NotifyStart sends a notification that an agent has begun working on a ticket.
	// It returns a thread reference that callers should pass to subsequent Notify
	// and NotifyComplete calls so that all messages for a task are grouped together.
	// Backends that do not support threading return an empty string.
	NotifyStart(ctx context.Context, ticket ticketing.Ticket) (string, error)

	// NotifyComplete sends a notification that an agent has finished working
	// on a ticket, including the task result summary.
	// threadRef, when non-empty, causes the completion message to be posted as a
	// reply (and, where supported, broadcast to the channel) in the identified thread.
	NotifyComplete(ctx context.Context, ticket ticketing.Ticket, result engine.TaskResult, threadRef string) error

	// UpdateMessage replaces the content of a previously posted message
	// identified by messageRef (e.g. a Slack message timestamp). Backends
	// that do not support updates should return nil (no-op).
	UpdateMessage(ctx context.Context, messageRef string, text string) error

	// Name returns the unique identifier for this channel (e.g. "slack", "teams").
	Name() string

	// InterfaceVersion returns the version of the NotificationChannel interface
	// that this channel implements.
	InterfaceVersion() int
}
