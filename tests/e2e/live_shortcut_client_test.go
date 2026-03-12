//go:build live

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/stretchr/testify/require"
)

const shortcutBaseURL = "https://api.app.shortcut.com/api/v3"

// shortcutTestClient is a minimal Shortcut REST client used exclusively by
// live E2E tests to create, inspect, and clean up test stories. It is
// intentionally separate from the production ShortcutBackend and only
// implements the operations needed for test orchestration.
type shortcutTestClient struct {
	token string
	http  *http.Client
}

// liveStory is the subset of the Shortcut story response used in live tests.
type liveStory struct {
	ID              int          `json:"id"`
	Name            string       `json:"name"`
	WorkflowStateID int64        `json:"workflow_state_id"`
	Labels          []liveLabel  `json:"labels"`
}

// liveLabel is a Shortcut label name.
type liveLabel struct {
	Name string `json:"name"`
}

// liveComment is a single Shortcut story comment.
type liveComment struct {
	Text string `json:"text"`
}

// liveWorkflow is the subset of a Shortcut workflow response we need.
type liveWorkflow struct {
	ID     int64               `json:"id"`
	Name   string              `json:"name"`
	States []liveWorkflowState `json:"states"`
}

// liveWorkflowState is a single state within a Shortcut workflow.
type liveWorkflowState struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "unstarted", "started", or "done"
}

// liveMember is the subset of a Shortcut member response we need.
type liveMember struct {
	ID      string            `json:"id"`
	Profile liveMemberProfile `json:"profile"`
}

// liveMemberProfile holds the fields we care about from a Shortcut member.
type liveMemberProfile struct {
	MentionName string `json:"mention_name"`
}

// -----------------------------------------------------------------------------
// Constructor and config helpers
// -----------------------------------------------------------------------------

// newShortcutTestClient reads the Shortcut API token from the
// osmia-shortcut-token K8s Secret in the live namespace and returns a
// configured test client. The live controller must be deployed and its
// kubeconfig must be accessible from the test runner.
func newShortcutTestClient(t *testing.T) *shortcutTestClient {
	t.Helper()

	ns := liveNamespace()

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		t.Fatalf("failed to load kubeconfig: %v", err)
	}

	k8s, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create K8s client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	secret, err := k8s.CoreV1().Secrets(ns).Get(ctx, "osmia-shortcut-token", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to read osmia-shortcut-token secret from namespace %q: %v", ns, err)
	}

	token := string(secret.Data["token"])
	require.NotEmpty(t, token, "osmia-shortcut-token secret must have a non-empty 'token' key")

	return &shortcutTestClient{
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// liveNamespace returns the namespace where the live controller is deployed.
// Reads OSMIA_LIVE_NAMESPACE; defaults to "osmia".
func liveNamespace() string {
	if ns := os.Getenv("OSMIA_LIVE_NAMESPACE"); ns != "" {
		return ns
	}
	return "osmia"
}

// liveReadyStateName returns the Shortcut workflow state that triggers the
// controller to pick up a story. Reads SHORTCUT_READY_STATE; defaults to
// "Ready for Development".
func liveReadyStateName() string {
	if v := os.Getenv("SHORTCUT_READY_STATE"); v != "" {
		return v
	}
	return "Ready for Development"
}

// liveOwnerMentionName returns the Shortcut mention name that stories must be
// assigned to for the controller to pick them up. Reads
// SHORTCUT_OWNER_MENTION; defaults to "osmia".
func liveOwnerMentionName() string {
	if v := os.Getenv("SHORTCUT_OWNER_MENTION"); v != "" {
		return v
	}
	return "osmia"
}

// liveTestRepoURL returns the GitLab repo URL that will be attached to test
// stories as an external link. The agent clones this repo during execution.
// Reads SHORTCUT_TEST_REPO_URL; defaults to the shared customer1-common test
// repo. The SCM token in the cluster must have push access to this repo.
func liveTestRepoURL() string {
	if v := os.Getenv("SHORTCUT_TEST_REPO_URL"); v != "" {
		return v
	}
	return "https://gitlab.com/unitaryai/internal/osmia_tests/customer1/customer1-common"
}

// -----------------------------------------------------------------------------
// Story lifecycle
// -----------------------------------------------------------------------------

// createStory creates a test story in the configured "Ready for Development"
// state, assigns it to the osmia Shortcut member so the controller will pick
// it up, and attaches repoURL as the external link the agent will clone.
// Returns the story ID as a string (matching the format used by the ticketing
// backend).
func (c *shortcutTestClient) createStory(t *testing.T, title, description, repoURL string) string {
	t.Helper()

	stateID := c.resolveStateName(t, liveReadyStateName())
	ownerID := c.resolveMemberID(t, liveOwnerMentionName())

	payload := map[string]any{
		"name":              title,
		"description":       description,
		"external_links":    []string{repoURL},
		"workflow_state_id": stateID,
		"owner_ids":         []string{ownerID},
		"story_type":        "feature",
	}

	var story liveStory
	c.doPost(t, shortcutBaseURL+"/stories", payload, &story)

	t.Logf("created Shortcut story #%d: %q (state_id=%d)", story.ID, story.Name, story.WorkflowStateID)
	return strconv.Itoa(story.ID)
}

// deleteStory removes a test story. Intended for use in t.Cleanup; it logs
// warnings rather than failing the test so that cleanup always completes.
func (c *shortcutTestClient) deleteStory(t *testing.T, id string) {
	t.Helper()

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodDelete,
		fmt.Sprintf("%s/stories/%s", shortcutBaseURL, id),
		nil,
	)
	if err != nil {
		t.Logf("warning: could not build delete request for story #%s: %v", id, err)
		return
	}
	req.Header.Set("Shortcut-Token", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		t.Logf("warning: could not delete story #%s: %v", id, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		t.Logf("deleted Shortcut story #%s", id)
	} else {
		t.Logf("warning: unexpected status %d when deleting story #%s", resp.StatusCode, id)
	}
}

// getStory fetches the current state of a story from the Shortcut API.
func (c *shortcutTestClient) getStory(t *testing.T, id string) liveStory {
	t.Helper()
	var story liveStory
	c.doGet(t, fmt.Sprintf("%s/stories/%s", shortcutBaseURL, id), &story)
	return story
}

// storyComments fetches all comments on a story and returns their text bodies.
func (c *shortcutTestClient) storyComments(t *testing.T, id string) []string {
	t.Helper()
	var comments []liveComment
	c.doGet(t, fmt.Sprintf("%s/stories/%s/comments", shortcutBaseURL, id), &comments)
	texts := make([]string, len(comments))
	for i, c := range comments {
		texts[i] = c.Text
	}
	return texts
}

// -----------------------------------------------------------------------------
// Polling helpers
// -----------------------------------------------------------------------------

// waitForStoryDone polls until the story reaches a "done"-type workflow state.
// It fails the test immediately if the story acquires a "osmia-failed" label
// before reaching done state.
func (c *shortcutTestClient) waitForStoryDone(t *testing.T, id string, timeout time.Duration) {
	t.Helper()

	// Fetch and cache workflows once so we can resolve state types without
	// making an API call on every poll tick.
	var workflows []liveWorkflow
	c.doGet(t, shortcutBaseURL+"/workflows", &workflows)

	stateType := func(stateID int64) string {
		for _, wf := range workflows {
			for _, s := range wf.States {
				if s.ID == stateID {
					return s.Type
				}
			}
		}
		return ""
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		story := c.getStory(t, id)
		sType := stateType(story.WorkflowStateID)
		t.Logf("story #%s: state_id=%d type=%q", id, story.WorkflowStateID, sType)

		if sType == "done" {
			return
		}

		for _, label := range story.Labels {
			if label.Name == "osmia-failed" {
				t.Fatalf("story #%s was labelled osmia-failed before reaching a done state", id)
			}
		}

		time.Sleep(15 * time.Second)
	}

	t.Fatalf("timed out after %v waiting for story #%s to reach a done-type workflow state", timeout, id)
}

// waitForStoryFailed polls until the story is labelled "osmia-failed" by
// the controller, indicating the task exhausted all retries.
func (c *shortcutTestClient) waitForStoryFailed(t *testing.T, id string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		story := c.getStory(t, id)
		for _, label := range story.Labels {
			if label.Name == "osmia-failed" {
				t.Logf("story #%s confirmed osmia-failed", id)
				return
			}
		}
		t.Logf("story #%s: waiting for osmia-failed label (current labels: %v)", id, story.Labels)
		time.Sleep(15 * time.Second)
	}

	t.Fatalf("timed out after %v waiting for story #%s to be labelled osmia-failed", timeout, id)
}

// -----------------------------------------------------------------------------
// Shortcut API resolution helpers
// -----------------------------------------------------------------------------

// resolveStateName searches all workflows for a state whose name matches
// (case-insensitive) and returns its numeric ID. Fails the test if not found.
func (c *shortcutTestClient) resolveStateName(t *testing.T, name string) int64 {
	t.Helper()

	var workflows []liveWorkflow
	c.doGet(t, shortcutBaseURL+"/workflows", &workflows)

	nameLower := strings.ToLower(name)
	var available []string
	for _, wf := range workflows {
		for _, s := range wf.States {
			if strings.ToLower(s.Name) == nameLower {
				t.Logf("resolved Shortcut state %q → id=%d (workflow %q)", name, s.ID, wf.Name)
				return s.ID
			}
			available = append(available, fmt.Sprintf("%q (workflow: %s)", s.Name, wf.Name))
		}
	}

	t.Fatalf("no Shortcut workflow state named %q found; available: %v", name, available)
	return 0
}

// resolveMemberID looks up the member UUID for the given mention name
// (case-insensitive). Fails the test if not found.
func (c *shortcutTestClient) resolveMemberID(t *testing.T, mentionName string) string {
	t.Helper()

	var members []liveMember
	c.doGet(t, shortcutBaseURL+"/members", &members)

	nameLower := strings.ToLower(mentionName)
	for _, m := range members {
		if strings.ToLower(m.Profile.MentionName) == nameLower {
			t.Logf("resolved Shortcut member @%s → %s", mentionName, m.ID)
			return m.ID
		}
	}

	t.Fatalf("no Shortcut member with mention_name %q found", mentionName)
	return ""
}

// -----------------------------------------------------------------------------
// Low-level HTTP helpers
// -----------------------------------------------------------------------------

// doGet executes a GET request to the Shortcut API and JSON-decodes the
// response body into result.
func (c *shortcutTestClient) doGet(t *testing.T, url string, result any) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err, "building GET request for %s", url)
	req.Header.Set("Shortcut-Token", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	require.NoError(t, err, "executing GET %s", url)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "reading GET %s response body", url)
	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
		"GET %s returned %d: %s", url, resp.StatusCode, string(body))

	require.NoError(t, json.Unmarshal(body, result), "decoding GET %s response", url)
}

// doPost executes a POST request with a JSON body and decodes the response
// into result (may be nil to discard the response body).
func (c *shortcutTestClient) doPost(t *testing.T, url string, payload, result any) {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err, "marshalling POST payload for %s", url)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err, "building POST request for %s", url)
	req.Header.Set("Shortcut-Token", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	require.NoError(t, err, "executing POST %s", url)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "reading POST %s response body", url)
	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
		"POST %s returned %d: %s", url, resp.StatusCode, string(respBody))

	if result != nil {
		require.NoError(t, json.Unmarshal(respBody, result), "decoding POST %s response", url)
	}
}

// -----------------------------------------------------------------------------
// Assertion helpers
// -----------------------------------------------------------------------------

// hasCommentContaining returns true if any comment text contains the given
// substring (case-insensitive).
func hasCommentContaining(comments []string, sub string) bool {
	subLower := strings.ToLower(sub)
	for _, c := range comments {
		if strings.Contains(strings.ToLower(c), subLower) {
			return true
		}
	}
	return false
}
