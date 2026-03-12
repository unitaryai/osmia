// Package sessionstore provides backends for persisting Claude Code session
// state between agent pod restarts. When enabled, both the ~/.claude directory
// (conversation history) and the /workspace/repo directory are written to
// durable storage so that retry pods can resume via `claude --resume <id>`
// instead of starting a fresh session.
//
// Session persistence is opt-in. When disabled, the controller falls back to
// the git-based continuation strategy (clone prior branch + prompt context).
package sessionstore

import (
	"context"

	"github.com/unitaryai/osmia/pkg/engine"
)

// SessionStore manages persistent storage for a single Claude Code session.
// The controller calls Prepare before launching the first job, VolumeMounts
// and Env to populate the agent pod spec, and Cleanup when the TaskRun
// reaches a terminal state.
type SessionStore interface {
	// Prepare sets up storage for a TaskRun (e.g. creates a per-TaskRun PVC
	// or ensures the S3 prefix exists). Must be called before the first job.
	Prepare(ctx context.Context, taskRunID string) error

	// VolumeMounts returns the additional volume mounts to add to the agent
	// pod spec. The returned mounts carry the session state directory and,
	// for backends that also persist the workspace, the repo directory.
	VolumeMounts(taskRunID string) []engine.VolumeMount

	// Env returns extra environment variables for the agent container.
	// Includes CLAUDE_CONFIG_DIR pointing at the persisted session directory,
	// and OSMIA_WORKSPACE_DIR when the workspace is also persisted.
	Env(taskRunID, sessionID string) map[string]string

	// Cleanup removes storage for a completed TaskRun. Safe to call multiple
	// times; implementations must be idempotent.
	Cleanup(ctx context.Context, taskRunID string) error
}
