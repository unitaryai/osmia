package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver.
)

const createFingerprintsTable = `CREATE TABLE IF NOT EXISTS fingerprints (
	engine_name TEXT PRIMARY KEY,
	data_json TEXT NOT NULL,
	updated_at DATETIME NOT NULL
)`

// fingerprintJSON is an unexported shadow of EngineFingerprint used for
// JSON serialisation, omitting the sync.RWMutex field which must not be
// marshalled.
type fingerprintJSON struct {
	EngineName  string                     `json:"engine_name"`
	Dimensions  map[string]*DimensionStats `json:"dimensions"`
	LastUpdated time.Time                  `json:"last_updated"`
	TotalTasks  int                        `json:"total_tasks"`
}

// SQLiteFingerprintStore is a FingerprintStore backed by a SQLite database
// using the pure-Go modernc.org/sqlite driver (no CGO required).
type SQLiteFingerprintStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSQLiteFingerprintStore opens (or creates) a SQLite database at the
// given path and runs auto-migration to ensure the fingerprints table
// exists.
func NewSQLiteFingerprintStore(path string, logger *slog.Logger) (*SQLiteFingerprintStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting wal mode: %w", err)
	}

	store := &SQLiteFingerprintStore{db: db, logger: logger}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return store, nil
}

// migrate creates the fingerprints table if it does not already exist.
func (s *SQLiteFingerprintStore) migrate() error {
	if _, err := s.db.Exec(createFingerprintsTable); err != nil {
		return fmt.Errorf("executing migration: %w", err)
	}
	return nil
}

// Save persists the given engine fingerprint, overwriting any existing
// record for the same engine.
func (s *SQLiteFingerprintStore) Save(ctx context.Context, fp *EngineFingerprint) error {
	if fp == nil {
		return fmt.Errorf("cannot save nil fingerprint")
	}

	shadow := fingerprintJSON{
		EngineName:  fp.EngineName,
		Dimensions:  fp.Dimensions,
		LastUpdated: fp.LastUpdated,
		TotalTasks:  fp.TotalTasks,
	}

	data, err := json.Marshal(shadow)
	if err != nil {
		return fmt.Errorf("marshalling fingerprint %q: %w", fp.EngineName, err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO fingerprints (engine_name, data_json, updated_at)
		 VALUES (?, ?, ?)`,
		fp.EngineName,
		string(data),
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upserting fingerprint %q: %w", fp.EngineName, err)
	}
	return nil
}

// Get retrieves the fingerprint for the named engine. Returns an error
// if no fingerprint exists.
func (s *SQLiteFingerprintStore) Get(ctx context.Context, engineName string) (*EngineFingerprint, error) {
	var dataJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT data_json FROM fingerprints WHERE engine_name = ?`,
		engineName,
	).Scan(&dataJSON)
	if err != nil {
		return nil, fmt.Errorf("fingerprint for engine %q not found: %w", engineName, err)
	}

	var shadow fingerprintJSON
	if err := json.Unmarshal([]byte(dataJSON), &shadow); err != nil {
		return nil, fmt.Errorf("unmarshalling fingerprint %q: %w", engineName, err)
	}

	return &EngineFingerprint{
		EngineName:  shadow.EngineName,
		Dimensions:  shadow.Dimensions,
		LastUpdated: shadow.LastUpdated,
		TotalTasks:  shadow.TotalTasks,
	}, nil
}

// List returns all stored fingerprints.
func (s *SQLiteFingerprintStore) List(ctx context.Context) ([]*EngineFingerprint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data_json FROM fingerprints`)
	if err != nil {
		return nil, fmt.Errorf("listing fingerprints: %w", err)
	}
	defer rows.Close()

	var result []*EngineFingerprint
	for rows.Next() {
		var dataJSON string
		if err := rows.Scan(&dataJSON); err != nil {
			return nil, fmt.Errorf("scanning fingerprint row: %w", err)
		}

		var shadow fingerprintJSON
		if err := json.Unmarshal([]byte(dataJSON), &shadow); err != nil {
			s.logger.Warn("skipping undeserialisable fingerprint", "error", err)
			continue
		}

		result = append(result, &EngineFingerprint{
			EngineName:  shadow.EngineName,
			Dimensions:  shadow.Dimensions,
			LastUpdated: shadow.LastUpdated,
			TotalTasks:  shadow.TotalTasks,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating fingerprint rows: %w", err)
	}
	return result, nil
}

// Close closes the underlying database connection.
func (s *SQLiteFingerprintStore) Close() error {
	return s.db.Close()
}
