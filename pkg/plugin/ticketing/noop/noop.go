// Package noop provides a no-op ticketing backend that silently accepts
// all state transitions and returns empty results. It is used as a fallback
// when no real ticketing backend is configured, preventing nil-pointer
// panics in webhook-only or test deployments.
package noop

import (
	"context"
	"log/slog"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// Backend is a no-op implementation of the ticketing.Backend interface.
type Backend struct {
	logger *slog.Logger
}

// New returns a new no-op ticketing backend.
func New() *Backend {
	return &Backend{logger: slog.Default()}
}

// PollReadyTickets returns no tickets.
func (b *Backend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
	b.logger.DebugContext(ctx, "noop ticketing: poll ready tickets (returning empty)")
	return nil, nil
}

// MarkInProgress silently accepts the transition.
func (b *Backend) MarkInProgress(ctx context.Context, ticketID string) error {
	b.logger.DebugContext(ctx, "noop ticketing: mark in progress", "ticket_id", ticketID)
	return nil
}

// MarkComplete silently accepts the transition.
func (b *Backend) MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error {
	b.logger.DebugContext(ctx, "noop ticketing: mark complete", "ticket_id", ticketID)
	return nil
}

// MarkFailed silently accepts the transition.
func (b *Backend) MarkFailed(ctx context.Context, ticketID string, reason string) error {
	b.logger.DebugContext(ctx, "noop ticketing: mark failed", "ticket_id", ticketID, "reason", reason)
	return nil
}

// AddComment silently discards the comment.
func (b *Backend) AddComment(ctx context.Context, ticketID string, comment string) error {
	b.logger.DebugContext(ctx, "noop ticketing: add comment", "ticket_id", ticketID)
	return nil
}

// Name returns the backend identifier.
func (b *Backend) Name() string {
	return "noop"
}

// InterfaceVersion returns the ticketing interface version this backend implements.
func (b *Backend) InterfaceVersion() int {
	return ticketing.InterfaceVersion
}
