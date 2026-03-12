//go:build live

package e2e

// Live E2E workflow tests exercise the full Osmia pipeline against real
// external services: Shortcut ticketing, GitLab SCM, and the Claude Code
// engine. They require the live controller to be running in the kind cluster
// with valid secrets.
//
// # Prerequisites
//
//   - kind cluster running with `make live-up` or equivalent
//   - osmia-shortcut-token, osmia-scm-token, and osmia-anthropic-key
//     K8s Secrets present in the live namespace
//   - kubeconfig pointing at the kind cluster (kubectl context kind-osmia)
//
// # Running
//
//	make e2e-live-test
//
// or directly:
//
//	go test -tags=live -v -timeout=1200s ./tests/e2e/ -run TestLive
//
// # Environment variables
//
//	OSMIA_LIVE_NAMESPACE      K8s namespace where the controller runs (default: "osmia")
//	SHORTCUT_TEST_REPO_URL      GitLab repo URL for agent tasks (default: customer1-common test repo)
//	SHORTCUT_READY_STATE        Trigger workflow state name (default: "Ready for Development")
//	SHORTCUT_OWNER_MENTION      Shortcut mention name to assign stories to (default: "osmia")

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLiveHappyPath validates the complete end-to-end pipeline against real
// external services:
//
//  1. Creates a Shortcut story in "Ready for Development" state, assigned to
//     @osmia, with the test GitLab repo as an external link.
//  2. Waits for the live controller to poll and pick up the story (up to 30s).
//  3. Waits for the Claude Code agent K8s Job to complete successfully.
//  4. Asserts that Shortcut transitions the story to a done-type state and
//     posts a "Task completed successfully" comment.
//
// The task description is deliberately minimal so any non-empty repo will
// satisfy it: append a single comment line to README.md.
func TestLiveHappyPath(t *testing.T) {
	repoURL := liveTestRepoURL()
	sc := newShortcutTestClient(t)

	title := fmt.Sprintf("Osmia E2E: append comment to README — %d", time.Now().Unix())
	description := `Append exactly one line to the bottom of README.md:

` + "```" + `
<!-- osmia-e2e-test -->
` + "```" + `

Do not modify any other files. Open a merge request with the change.`

	storyID := sc.createStory(t, title, description, repoURL)
	// Always delete the test story on exit regardless of outcome.
	// If a K8s Job is still running when cleanup fires, the controller will
	// call MarkComplete on the now-deleted story and log a 404 — this is
	// harmless.
	t.Cleanup(func() { sc.deleteStory(t, storyID) })

	// The controller polls every 30 s; allow 15 minutes for the full cycle:
	// poll pickup → K8s Job scheduling → git clone → Claude Code run → MR →
	// MarkComplete → Shortcut state transition.
	sc.waitForStoryDone(t, storyID, 15*time.Minute)

	comments := sc.storyComments(t, storyID)
	assert.True(t,
		hasCommentContaining(comments, "Task completed successfully"),
		"expected a completion comment on story #%s; got: %v", storyID, comments,
	)
}

// TestLiveGracefulCloneFailure validates the graceful error-handling path: a
// story whose repo URL is inaccessible causes the agent's git clone to fail.
// The Claude Code agent handles this at the application level — it detects the
// error, records a description, and exits 0. The controller therefore calls
// MarkComplete (not MarkFailed), transitioning the story to a done-type state
// with a comment that includes the clone-failure details.
//
// Asserts:
//   - Story reaches a done-type workflow state within the timeout.
//   - At least one comment describes the clone failure (contains "failed",
//     "error", or "could not").
//
// No SHORTCUT_TEST_REPO_URL is required — the invalid URL is intentional.
func TestLiveGracefulCloneFailure(t *testing.T) {
	sc := newShortcutTestClient(t)

	title := fmt.Sprintf("Osmia E2E: clone-failure graceful handling — %d", time.Now().Unix())
	description := "Fix the authentication bug in the login module."

	// This repo does not exist; the git clone inside the agent container will
	// fail. Claude Code detects this, writes a result describing the error, and
	// exits 0 — so the controller calls MarkComplete with the error summary.
	invalidRepoURL := "https://gitlab.com/osmia-e2e-nonexistent/repo-does-not-exist"

	storyID := sc.createStory(t, title, description, invalidRepoURL)
	t.Cleanup(func() { sc.deleteStory(t, storyID) })

	// Clone failure is detected quickly; allow 5 minutes for the full cycle.
	sc.waitForStoryDone(t, storyID, 5*time.Minute)

	comments := sc.storyComments(t, storyID)
	assert.True(t,
		hasCommentContaining(comments, "failed") ||
			hasCommentContaining(comments, "error") ||
			hasCommentContaining(comments, "could not"),
		"expected a comment describing the clone failure on story #%s; got: %v", storyID, comments,
	)
}
