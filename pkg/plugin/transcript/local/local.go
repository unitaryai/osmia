// Package local implements a local-filesystem TranscriptSink that writes
// NDJSON lines to files under a configured directory. Each task run produces
// one file named <taskRunID>.jsonl; each line is a JSON envelope containing a
// timestamp, event type, and the full event payload.
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/pkg/plugin/transcript"
)

// Compile-time check that LocalSink implements transcript.TranscriptSink.
var _ transcript.TranscriptSink = (*LocalSink)(nil)

// LocalSink writes task run transcripts to the local filesystem as NDJSON
// files, one file per task run. Each line is a JSON-encoded stream event
// envelope with a timestamp prefix.
type LocalSink struct {
	dir    string
	logger *slog.Logger
	mu     sync.Mutex
	files  map[string]*os.File // keyed by task run ID
}

// line is the JSON structure written for each event.
type line struct {
	Timestamp string          `json:"ts"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
}

// NewLocalSink creates a new LocalSink that writes transcripts to dir.
// The directory is created with MkdirAll if it does not already exist.
func NewLocalSink(dir string, logger *slog.Logger) *LocalSink {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		logger.Warn("transcript: failed to create transcript directory",
			"dir", dir,
			"err", err,
		)
	}
	return &LocalSink{
		dir:    dir,
		logger: logger,
		files:  make(map[string]*os.File),
	}
}

// Append adds a single stream event to the NDJSON transcript file for the
// given task run. The file is created on the first call for each task run
// and left open until Flush is called. The written line has the form:
//
//	{"ts":"2026-01-01T12:00:00Z","type":"tool_use","data":{...}}
func (s *LocalSink) Append(_ context.Context, taskRunID string, event *agentstream.StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.getOrOpenFile(taskRunID)
	if err != nil {
		s.logger.Warn("transcript: failed to open transcript file",
			"taskRunID", taskRunID,
			"err", err,
		)
		return err
	}

	// Marshal the full event as the data payload.
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling event for task run %q: %w", taskRunID, err)
	}

	entry := line{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Type:      string(event.Type),
		Data:      json.RawMessage(data),
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshalling transcript line for task run %q: %w", taskRunID, err)
	}

	// Append the JSON line followed by a newline character.
	if _, err := fmt.Fprintf(f, "%s\n", encoded); err != nil {
		return fmt.Errorf("writing transcript line for task run %q: %w", taskRunID, err)
	}

	return nil
}

// Flush closes and removes the transcript file handle for the given task run.
// It is a no-op when no events have been appended for this task run.
func (s *LocalSink) Flush(_ context.Context, taskRunID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[taskRunID]
	if !ok {
		// No events were written for this task run — nothing to do.
		return nil
	}

	delete(s.files, taskRunID)

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing transcript file for task run %q: %w", taskRunID, err)
	}

	return nil
}

// getOrOpenFile returns the open file for the given task run, creating it if
// necessary. The caller must hold s.mu.
func (s *LocalSink) getOrOpenFile(taskRunID string) (*os.File, error) {
	if f, ok := s.files[taskRunID]; ok {
		return f, nil
	}

	path := filepath.Join(s.dir, taskRunID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening transcript file %q: %w", path, err)
	}

	s.files[taskRunID] = f
	return f, nil
}
