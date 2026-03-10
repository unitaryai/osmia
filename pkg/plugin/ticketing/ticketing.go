// Package ticketing defines the TicketingBackend interface for integrating
// with external issue trackers (GitHub Issues, Jira, Linear, etc).
// Built-in implementations are compiled into the controller; third-party
// backends communicate over gRPC via the hashicorp/go-plugin host.
package ticketing

import (
	"context"

	"github.com/unitaryai/osmia/pkg/engine"
)

// InterfaceVersion is the current version of the TicketingBackend interface.
// Bumped on breaking changes to the contract.
const InterfaceVersion = 1

// Ticket represents a unit of work retrieved from a ticketing backend.
type Ticket struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	TicketType  string         `json:"ticket_type"`
	Labels      []string       `json:"labels"`
	RepoURL     string         `json:"repo_url,omitempty"`
	ExternalURL string         `json:"external_url"`
	Raw         map[string]any `json:"raw"` // Original ticket data from the backend
}

// Backend is the interface that ticketing backends must implement.
// It provides operations for polling, updating, and commenting on tickets.
type Backend interface {
	// PollReadyTickets returns tickets that are ready to be processed.
	// Implementations should filter by configured labels, states, or other
	// backend-specific criteria.
	PollReadyTickets(ctx context.Context) ([]Ticket, error)

	// MarkInProgress updates a ticket's state to indicate that an agent
	// is actively working on it.
	MarkInProgress(ctx context.Context, ticketID string) error

	// MarkComplete updates a ticket's state to indicate successful completion
	// and attaches the task result (e.g. merge request URL, summary).
	MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error

	// MarkFailed updates a ticket's state to indicate failure and records
	// the reason for the failure.
	MarkFailed(ctx context.Context, ticketID string, reason string) error

	// AddComment posts a comment on the ticket, used for progress updates,
	// human interaction, and status reporting.
	AddComment(ctx context.Context, ticketID string, comment string) error

	// Name returns the unique identifier for this backend (e.g. "github", "jira").
	Name() string

	// InterfaceVersion returns the version of the TicketingBackend interface
	// that this backend implements.
	InterfaceVersion() int
}
