// Package notifications defines the NotificationChannel interface for
// sending fire-and-forget notifications to external systems (Slack,
// Microsoft Teams, Discord, etc). Notifications are one-way; for
// interactive human-in-the-loop flows, see the approval package.
package notifications

import (
	"context"

	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// InterfaceVersion is the current version of the NotificationChannel interface.
const InterfaceVersion = 1

// Channel is the interface that notification backends must implement.
// All methods are fire-and-forget — errors are logged but do not block
// the controller reconciliation loop.
type Channel interface {
	// Notify sends a free-form notification message associated with a ticket.
	Notify(ctx context.Context, message string, ticket ticketing.Ticket) error

	// NotifyStart sends a notification that an agent has begun working on a ticket.
	NotifyStart(ctx context.Context, ticket ticketing.Ticket) error

	// NotifyComplete sends a notification that an agent has finished working
	// on a ticket, including the task result summary.
	NotifyComplete(ctx context.Context, ticket ticketing.Ticket, result engine.TaskResult) error

	// Name returns the unique identifier for this channel (e.g. "slack", "teams").
	Name() string

	// InterfaceVersion returns the version of the NotificationChannel interface
	// that this channel implements.
	InterfaceVersion() int
}
