package sessionstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedPVCStore_Prepare(t *testing.T) {
	// Prepare is a no-op for the shared PVC backend.
	store := NewSharedPVCStore("osmia-agent-sessions")
	assert.NoError(t, store.Prepare(context.Background(), "tr-123"))
}

func TestSharedPVCStore_Cleanup(t *testing.T) {
	// Cleanup is a no-op for the shared PVC backend (cleaner handles removal).
	store := NewSharedPVCStore("osmia-agent-sessions")
	assert.NoError(t, store.Cleanup(context.Background(), "tr-123"))
}

func TestSharedPVCStore_VolumeMounts(t *testing.T) {
	store := NewSharedPVCStore("osmia-agent-sessions")
	mounts := store.VolumeMounts("tr-abc")

	require.Len(t, mounts, 2)

	// Claude config directory mount.
	assert.Equal(t, "session-claude", mounts[0].Name)
	assert.Equal(t, "osmia-agent-sessions", mounts[0].PVCName)
	assert.Contains(t, mounts[0].SubPath, "tr-abc")
	assert.Contains(t, mounts[0].SubPath, sessionDirName)

	// Workspace mount.
	assert.Equal(t, "session-workspace", mounts[1].Name)
	assert.Equal(t, "osmia-agent-sessions", mounts[1].PVCName)
	assert.Contains(t, mounts[1].SubPath, "tr-abc")
	assert.Contains(t, mounts[1].SubPath, workspaceDirName)
}

func TestSharedPVCStore_Env(t *testing.T) {
	store := NewSharedPVCStore("osmia-agent-sessions")
	env := store.Env("tr-abc", "my-session-id")

	assert.NotEmpty(t, env["CLAUDE_CONFIG_DIR"])
	assert.NotEmpty(t, env["OSMIA_WORKSPACE_DIR"])
	assert.Equal(t, "my-session-id", env["OSMIA_SESSION_ID"])
}

func TestSharedPVCStore_Env_EmptySessionID(t *testing.T) {
	store := NewSharedPVCStore("osmia-agent-sessions")
	env := store.Env("tr-abc", "")

	assert.NotEmpty(t, env["CLAUDE_CONFIG_DIR"])
	assert.NotContains(t, env, "OSMIA_SESSION_ID")
}

func TestSharedPVCStore_VolumeMounts_IsolationByTaskRun(t *testing.T) {
	// Two different TaskRuns must get different SubPaths so they do not share
	// session data.
	store := NewSharedPVCStore("osmia-agent-sessions")

	mounts1 := store.VolumeMounts("tr-111")
	mounts2 := store.VolumeMounts("tr-222")

	assert.NotEqual(t, mounts1[0].SubPath, mounts2[0].SubPath,
		"different TaskRuns must have different SubPaths for the claude mount")
	assert.NotEqual(t, mounts1[1].SubPath, mounts2[1].SubPath,
		"different TaskRuns must have different SubPaths for the workspace mount")
}
