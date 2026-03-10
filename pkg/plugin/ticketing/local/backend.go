// Package local provides a built-in ticketing backend backed by a local
// SQLite database. It is intended for local development and evaluation where
// RoboDev needs durable ticket lifecycle state without an external tracker.
package local

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

const backendName = "local"

// Config configures the local ticketing backend.
type Config struct {
	StorePath string
	SeedFile  string
}

// Backend implements ticketing.Backend with SQLite-backed persistence.
type Backend struct {
	db     *sql.DB
	logger *slog.Logger
}

var _ ticketing.Backend = (*Backend)(nil)

// New opens the SQLite store, initialises the schema, and imports any seed file.
func New(cfg Config, logger *slog.Logger) (*Backend, error) {
	if cfg.StorePath == "" {
		return nil, fmt.Errorf("store path must not be empty")
	}
	if logger == nil {
		logger = slog.Default()
	}

	db, err := openDatabase(cfg.StorePath)
	if err != nil {
		return nil, err
	}

	backend := &Backend{
		db:     db,
		logger: logger,
	}
	if err := backend.initialiseSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialising schema: %w", err)
	}
	if err := backend.importSeedFile(context.Background(), cfg.SeedFile); err != nil {
		db.Close()
		return nil, err
	}

	return backend, nil
}

// Close releases the SQLite connection.
func (b *Backend) Close() error {
	return b.db.Close()
}

// PollReadyTickets returns to-do tickets that are queued for another run.
func (b *Backend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
	return b.listReadyTickets(ctx)
}

// MarkInProgress moves the tracker status to in progress and marks the run active.
func (b *Backend) MarkInProgress(ctx context.Context, ticketID string) error {
	return b.transitionTicket(ctx, ticketID, eventMarkedInProgress, nil, "")
}

// MarkComplete moves the tracker status to done and records the task result.
func (b *Backend) MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error {
	return b.transitionTicket(ctx, ticketID, eventMarkedComplete, &result, "")
}

// MarkFailed stores the run failure without changing the tracker status class.
func (b *Backend) MarkFailed(ctx context.Context, ticketID string, reason string) error {
	return b.transitionTicket(ctx, ticketID, eventMarkedFailed, nil, reason)
}

// AddComment appends a durable system comment without changing ticket state.
func (b *Backend) AddComment(ctx context.Context, ticketID string, comment string) error {
	return b.addComment(ctx, commentKindSystem, ticketID, comment)
}

// AddUserComment appends a durable user comment for the local admin surface.
func (b *Backend) AddUserComment(ctx context.Context, ticketID string, comment string) error {
	return b.addComment(ctx, commentKindUser, ticketID, comment)
}

// Name returns the backend identifier.
func (b *Backend) Name() string {
	return backendName
}

// InterfaceVersion returns the implemented interface version.
func (b *Backend) InterfaceVersion() int {
	return ticketing.InterfaceVersion
}
