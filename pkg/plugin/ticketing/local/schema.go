package local

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // Pure Go SQLite driver.
)

const createTicketsTable = `CREATE TABLE IF NOT EXISTS tickets (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	ticket_type TEXT NOT NULL DEFAULT '',
	labels_json TEXT NOT NULL DEFAULT '[]',
	repo_url TEXT NOT NULL DEFAULT '',
	external_url TEXT NOT NULL DEFAULT '',
	raw_json TEXT NOT NULL DEFAULT '{}',
	state TEXT NOT NULL,
	run_state TEXT NOT NULL DEFAULT 'idle',
	result_json TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	branch_name TEXT NOT NULL DEFAULT '',
	merge_request_url TEXT NOT NULL DEFAULT '',
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cost_estimate_usd REAL NOT NULL DEFAULT 0,
	failure_reason TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	in_progress_at TEXT NOT NULL DEFAULT '',
	completed_at TEXT NOT NULL DEFAULT '',
	failed_at TEXT NOT NULL DEFAULT ''
)`

const createCommentsTable = `CREATE TABLE IF NOT EXISTS ticket_comments (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ticket_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	body TEXT NOT NULL,
	created_at TEXT NOT NULL
)`

const createEventsTable = `CREATE TABLE IF NOT EXISTS ticket_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ticket_id TEXT NOT NULL,
	event_type TEXT NOT NULL,
	payload_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL
)`

const (
	createTicketsStateIdx   = `CREATE INDEX IF NOT EXISTS idx_tickets_state_created_at ON tickets(state, created_at, id)`
	createCommentsTicketIdx = `CREATE INDEX IF NOT EXISTS idx_ticket_comments_ticket_created_at ON ticket_comments(ticket_id, created_at, id)`
	createEventsTicketIdx   = `CREATE INDEX IF NOT EXISTS idx_ticket_events_ticket_created_at ON ticket_events(ticket_id, created_at, id)`
)

func openDatabase(path string) (*sql.DB, error) {
	if err := ensureStoreDirectory(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting wal mode: %w", err)
	}

	return db, nil
}

func ensureStoreDirectory(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating store directory %q: %w", dir, err)
	}

	return nil
}

func (b *Backend) initialiseSchema() error {
	statements := []string{
		createTicketsTable,
		createCommentsTable,
		createEventsTable,
		createCommentsTicketIdx,
		createEventsTicketIdx,
		createTicketsStateIdx,
	}

	for _, statement := range statements {
		if _, err := b.db.Exec(statement); err != nil {
			return fmt.Errorf("executing migration: %w", err)
		}
	}

	return nil
}
