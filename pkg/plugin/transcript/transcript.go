// Package transcript defines the TranscriptSink interface for audit log storage.
// Implementations receive agent stream events during task execution and persist
// them as an append-only audit transcript that can be used for debugging,
// billing, and compliance purposes.
package transcript

import (
	"context"

	"github.com/unitaryai/robodev/internal/agentstream"
)

// TranscriptSink receives agent stream events and stores them as an
// append-only audit transcript. Implementations may write to the local
// filesystem, S3, GCS, or other backends.
type TranscriptSink interface {
	// Append adds a single stream event to the in-progress transcript
	// for the given task run. Implementations should be non-blocking
	// where possible; buffering is acceptable.
	Append(ctx context.Context, taskRunID string, event *agentstream.StreamEvent) error

	// Flush finalises and persists the transcript for the given task run.
	// Called once when the task run completes (successfully or not).
	// After Flush, no further Append calls will be made for this task run.
	Flush(ctx context.Context, taskRunID string) error
}
