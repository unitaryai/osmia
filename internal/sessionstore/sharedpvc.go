package sessionstore

import (
	"context"
	"path/filepath"

	"github.com/unitaryai/osmia/pkg/engine"
)

const (
	// sharedPVCSessionMountPath is the base path on the shared PVC where all
	// session directories are stored. Each TaskRun gets its own subdirectory.
	sharedPVCSessionMountPath = "/session-store"

	// sessionDirName is the subdirectory name for Claude's config directory.
	sessionDirName = "claude"

	// workspaceDirName is the subdirectory name for the git workspace.
	workspaceDirName = "workspace"
)

// SharedPVCStore implements SessionStore using a single ReadWriteMany PVC.
// Each TaskRun's session data lives under <pvcName>/<taskRunID>/claude and
// <pvcName>/<taskRunID>/workspace, isolated via SubPath volume mounts.
type SharedPVCStore struct {
	pvcName string
}

// NewSharedPVCStore returns a SharedPVCStore backed by the named PVC.
// The PVC must already exist with ReadWriteMany access mode.
func NewSharedPVCStore(pvcName string) *SharedPVCStore {
	return &SharedPVCStore{pvcName: pvcName}
}

// Prepare is a no-op for the shared PVC backend — the PVC is pre-provisioned
// and subdirectory creation is handled by the agent pod itself.
func (s *SharedPVCStore) Prepare(_ context.Context, _ string) error {
	return nil
}

// VolumeMounts returns two mounts: one for ~/.claude (session state) and one
// for /workspace/repo (workspace). Each uses a SubPath scoped to the TaskRun
// so multiple concurrent TaskRuns do not interfere with each other.
func (s *SharedPVCStore) VolumeMounts(taskRunID string) []engine.VolumeMount {
	return []engine.VolumeMount{
		{
			Name:      "session-claude",
			MountPath: filepath.Join(sharedPVCSessionMountPath, "claude"),
			SubPath:   filepath.Join(taskRunID, sessionDirName),
			PVCName:   s.pvcName,
		},
		{
			Name:      "session-workspace",
			MountPath: filepath.Join(sharedPVCSessionMountPath, "workspace"),
			SubPath:   filepath.Join(taskRunID, workspaceDirName),
			PVCName:   s.pvcName,
		},
	}
}

// Env returns the environment variables that direct Claude Code and the setup
// script to use the persisted directories.
func (s *SharedPVCStore) Env(taskRunID, sessionID string) map[string]string {
	env := map[string]string{
		// CLAUDE_CONFIG_DIR overrides ~/.claude so session JSONL files land on
		// the PVC rather than the ephemeral emptyDir home volume.
		"CLAUDE_CONFIG_DIR": filepath.Join(sharedPVCSessionMountPath, "claude"),
		// OSMIA_WORKSPACE_DIR tells setup-claude.sh and the agent to use the
		// persisted workspace directory instead of cloning fresh each run.
		"OSMIA_WORKSPACE_DIR": filepath.Join(sharedPVCSessionMountPath, "workspace"),
	}
	if sessionID != "" {
		env["OSMIA_SESSION_ID"] = sessionID
	}
	return env
}

// Cleanup is a no-op for the shared PVC backend. The background cleaner
// (sessionstore.Cleaner) handles TTL-based removal of stale subdirectories.
func (s *SharedPVCStore) Cleanup(_ context.Context, _ string) error {
	return nil
}
