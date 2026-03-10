package shortcut

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// Compile-time interface check.
var _ ticketing.Backend = (*ShortcutBackend)(nil)

func TestShortcutBackend_Name(t *testing.T) {
	b := NewShortcutBackend("tok", 500, testLogger())
	assert.Equal(t, "shortcut", b.Name())
}

func TestShortcutBackend_InterfaceVersion(t *testing.T) {
	b := NewShortcutBackend("tok", 500, testLogger())
	assert.Equal(t, ticketing.InterfaceVersion, b.InterfaceVersion())
}

// --- Init: workflow state resolution ---

// workflowsHandler returns an httptest handler that serves a fixed set of
// workflows and optionally also serves /members (empty list by default).
func workflowsHandler(t *testing.T, workflows []scWorkflow) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/workflows":
			_ = json.NewEncoder(w).Encode(workflows)
		case "/members":
			_ = json.NewEncoder(w).Encode([]scMember{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

var testWorkflows = []scWorkflow{
	{
		ID:   1,
		Name: "Engineering",
		States: []scWorkflowState{
			{ID: 100, Name: "Backlog", Type: "unstarted"},
			{ID: 200, Name: "Ready for Development", Type: "unstarted"},
			{ID: 300, Name: "In Development", Type: "started"},
			{ID: 400, Name: "Done", Type: "done"},
		},
	},
}

func TestShortcutBackend_Init_ResolvesWorkflowStateName(t *testing.T) {
	srv := httptest.NewServer(workflowsHandler(t, testWorkflows))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("Ready for Development"),
	)

	require.NoError(t, b.Init(context.Background()))
	assert.Equal(t, int64(200), b.workflowStateID)
	assert.Equal(t, int64(200), b.WorkflowStateID())
}

func TestShortcutBackend_Init_WorkflowStateNameCaseInsensitive(t *testing.T) {
	wf := []scWorkflow{{States: []scWorkflowState{{ID: 42, Name: "Ready For Development", Type: "unstarted"}}}}
	srv := httptest.NewServer(workflowsHandler(t, wf))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("ready for development"),
	)

	require.NoError(t, b.Init(context.Background()))
	assert.Equal(t, int64(42), b.workflowStateID)
}

func TestShortcutBackend_Init_WorkflowStateNotFound_ListsAvailable(t *testing.T) {
	wf := []scWorkflow{{
		Name:   "Engineering",
		States: []scWorkflowState{{ID: 1, Name: "Backlog", Type: "unstarted"}, {ID: 2, Name: "Ready", Type: "unstarted"}},
	}}
	srv := httptest.NewServer(workflowsHandler(t, wf))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("Nonexistent State"),
	)

	err := b.Init(context.Background())
	require.Error(t, err)
	// Error should name the missing state AND list available options.
	assert.Contains(t, err.Error(), "Nonexistent State")
	assert.Contains(t, err.Error(), "Backlog")
	assert.Contains(t, err.Error(), "Ready")
}

func TestShortcutBackend_Init_ExplicitIDSkipsNameResolution(t *testing.T) {
	// An explicit workflowStateID prevents name-based resolution, but Init
	// still fetches and caches workflows for per-story runtime lookups.
	workflowsCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workflows" {
			workflowsCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]scWorkflow{})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 999, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("Ready for Development"), // ignored — explicit ID wins
	)

	require.NoError(t, b.Init(context.Background()))
	assert.True(t, workflowsCalled, "Init always fetches workflows for per-story runtime lookups")
	assert.Equal(t, int64(999), b.workflowStateID)
}

func TestShortcutBackend_Init_ResolvesInProgressStateName(t *testing.T) {
	srv := httptest.NewServer(workflowsHandler(t, testWorkflows))
	defer srv.Close()

	b := NewShortcutBackend("tok", 200, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithInProgressStateName("In Development"),
	)

	require.NoError(t, b.Init(context.Background()))
	// Workflows are cached; in-progress state is resolved per-story at runtime.
	assert.NotEmpty(t, b.workflows)
	assert.Equal(t, "In Development", b.inProgressStateName)
	// InProgressStateID always returns 0 — resolution is per-story at runtime.
	assert.Equal(t, int64(0), b.InProgressStateID())
}

func TestShortcutBackend_Init_BothStateNamesFetchWorkflowsOnce(t *testing.T) {
	// When both workflow_state_name and in_progress_state_name are set,
	// Init should call /workflows only once.
	workflowCallCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workflows" {
			workflowCallCount++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testWorkflows)
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("Ready for Development"),
		WithInProgressStateName("In Development"),
	)

	require.NoError(t, b.Init(context.Background()))
	assert.Equal(t, 1, workflowCallCount, "should call /workflows exactly once")
	assert.Equal(t, int64(200), b.workflowStateID)
}

// --- Init: member resolution ---

func TestShortcutBackend_Init_ResolvesMemberByMentionName(t *testing.T) {
	members := []scMember{
		{ID: "uuid-human", Profile: scMemberProfile{MentionName: "alice"}},
		{ID: "uuid-bot", Profile: scMemberProfile{MentionName: "osmia"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/members":
			_ = json.NewEncoder(w).Encode(members)
		default:
			_ = json.NewEncoder(w).Encode([]scWorkflow{})
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithOwnerMentionName("osmia"),
	)

	require.NoError(t, b.Init(context.Background()))
	assert.Equal(t, "uuid-bot", b.ownerMemberID)
}

func TestShortcutBackend_Init_OwnerAtPrefixStripped(t *testing.T) {
	b := NewShortcutBackend("tok", 500, testLogger(),
		WithOwnerMentionName("@osmia"),
	)
	assert.Equal(t, "osmia", b.ownerMentionName)
}

func TestShortcutBackend_Init_MemberNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/members":
			_ = json.NewEncoder(w).Encode([]scMember{{ID: "other", Profile: scMemberProfile{MentionName: "alice"}}})
		default:
			_ = json.NewEncoder(w).Encode([]scWorkflow{})
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithOwnerMentionName("osmia"),
	)

	err := b.Init(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "osmia")
}

// --- ListWorkflowStates ---

func TestShortcutBackend_ListWorkflowStates(t *testing.T) {
	srv := httptest.NewServer(workflowsHandler(t, testWorkflows))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)

	states, err := b.ListWorkflowStates(context.Background())
	require.NoError(t, err)
	require.Len(t, states, 4)

	assert.Equal(t, int64(100), states[0].ID)
	assert.Equal(t, "Backlog", states[0].Name)
	assert.Equal(t, "Engineering", states[0].WorkflowName)

	assert.Equal(t, "Ready for Development", states[1].Name)
	assert.Equal(t, "In Development", states[2].Name)
	assert.Equal(t, "Done", states[3].Name)
}

// --- PollReadyTickets ---

func TestShortcutBackend_PollReadyTickets_NoOwnerFilter(t *testing.T) {
	stories := []scStory{
		{
			ID:          42,
			Name:        "Fix login bug",
			Description: "The login page crashes",
			AppURL:      "https://app.shortcut.com/org/story/42",
			Labels:      []scLabel{{Name: "bug"}},
		},
		{
			ID:          99,
			Name:        "Refactor auth",
			Description: "Clean up the auth module",
			AppURL:      "https://app.shortcut.com/org/story/99",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/search/stories", r.URL.Path)
		// Query should filter by state ID (numeric, since no state name set).
		q := r.URL.Query().Get("query")
		assert.Contains(t, q, `state:"500"`)
		assert.NotContains(t, q, "owner:")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResponse{Data: stories})
	}))
	defer srv.Close()

	b := NewShortcutBackend("test-token", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)

	tickets, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
	require.Len(t, tickets, 2)
	assert.Equal(t, "42", tickets[0].ID)
	assert.Equal(t, "Fix login bug", tickets[0].Title)
	assert.Equal(t, "99", tickets[1].ID)
}

func TestShortcutBackend_PollReadyTickets_WithOwnerFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		assert.Contains(t, q, "owner:osmia")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResponse{Data: []scStory{}})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)
	b.ownerMentionName = "osmia" // set directly, bypassing Init

	_, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
}

func TestShortcutBackend_PollReadyTickets_NoStateIDReturnsError(t *testing.T) {
	b := NewShortcutBackend("tok", 0, testLogger())
	_, err := b.PollReadyTickets(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow state is not set")
}

func TestShortcutBackend_PollReadyTickets_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResponse{Data: []scStory{}})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
	)
	tickets, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tickets)
}

func TestShortcutBackend_PollReadyTickets_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
	)
	_, err := b.PollReadyTickets(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 500")
}

func TestShortcutBackend_PollReadyTickets_ExcludeLabels(t *testing.T) {
	stories := []scStory{
		{
			ID:     1,
			Name:   "Normal story",
			AppURL: "https://app.shortcut.com/org/story/1",
		},
		{
			ID:     2,
			Name:   "In-progress story",
			AppURL: "https://app.shortcut.com/org/story/2",
			Labels: []scLabel{{Name: "in-progress"}},
		},
		{
			ID:     3,
			Name:   "Failed story",
			AppURL: "https://app.shortcut.com/org/story/3",
			Labels: []scLabel{{Name: "osmia-failed"}},
		},
		{
			ID:     4,
			Name:   "Clean story",
			AppURL: "https://app.shortcut.com/org/story/4",
		},
	}

	tests := []struct {
		name          string
		opts          []Option
		wantTicketIDs []string
	}{
		{
			name:          "default exclusion filters out in-progress and failed",
			wantTicketIDs: []string{"1", "4"},
		},
		{
			name:          "custom excludeLabels override",
			opts:          []Option{WithExcludeLabels([]string{"osmia-failed"})},
			wantTicketIDs: []string{"1", "2", "4"},
		},
		{
			name:          "empty excludeLabels disables exclusion",
			opts:          []Option{WithExcludeLabels([]string{})},
			wantTicketIDs: []string{"1", "2", "3", "4"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(searchResponse{Data: stories})
			}))
			defer srv.Close()

			allOpts := append([]Option{
				WithBaseURL(srv.URL),
				WithHTTPClient(srv.Client()),
			}, tc.opts...)
			b := NewShortcutBackend("tok", 500, testLogger(), allOpts...)

			tickets, err := b.PollReadyTickets(context.Background())
			require.NoError(t, err)

			gotIDs := make([]string, 0, len(tickets))
			for _, tk := range tickets {
				gotIDs = append(gotIDs, tk.ID)
			}
			assert.Equal(t, tc.wantTicketIDs, gotIDs)
		})
	}
}

func TestShortcutBackend_PollReadyTickets_RepoURLFromExternalLinks(t *testing.T) {
	stories := []scStory{
		{
			ID:            10,
			Name:          "With repo",
			AppURL:        "https://app.shortcut.com/org/story/10",
			ExternalLinks: []string{"https://gitlab.com/org/repo"},
		},
		{
			ID:     11,
			Name:   "Without repo",
			AppURL: "https://app.shortcut.com/org/story/11",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResponse{Data: stories})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithExcludeLabels([]string{}),
	)

	tickets, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
	require.Len(t, tickets, 2)
	assert.Equal(t, "https://gitlab.com/org/repo", tickets[0].RepoURL)
	assert.Empty(t, tickets[1].RepoURL)
}

// --- lifecycle methods ---

func TestShortcutBackend_MarkInProgress_LabelFallback(t *testing.T) {
	// When no in-progress state is configured, falls back to adding a label.
	var calls []string
	var commentText string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var p map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			commentText = p["text"]
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			var p map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			assert.Equal(t, "in-progress", p["name"])
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(), WithBaseURL(srv.URL))
	require.NoError(t, b.MarkInProgress(context.Background(), "42"))

	assert.Contains(t, calls, "POST /stories/42/comments")
	assert.Contains(t, calls, "POST /stories/42/labels")
	assert.Contains(t, commentText, "Osmia")
}

func TestShortcutBackend_MarkInProgress_StateTransition(t *testing.T) {
	// When inProgressStateName is set, transitions the story to the matching
	// state in the story's own workflow rather than adding a label.
	var calls []string
	var statePayload map[string]int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/stories/42":
			// Story is currently in "Ready for Development" (state 200).
			_ = json.NewEncoder(w).Encode(scStory{ID: 42, WorkflowStateID: 200})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/stories/42":
			_ = json.NewDecoder(r.Body).Decode(&statePayload)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 200, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithInProgressStateName("In Development"),
	)
	b.workflows = testWorkflows // pre-populate cache (skips Init)

	require.NoError(t, b.MarkInProgress(context.Background(), "42"))

	// Must post a comment AND transition the state, but NOT add a label.
	assert.Contains(t, calls, "POST /stories/42/comments")
	assert.Contains(t, calls, "GET /stories/42")
	assert.Contains(t, calls, "PUT /stories/42")
	assert.NotContains(t, calls, "POST /stories/42/labels")
	assert.Equal(t, int64(300), statePayload["workflow_state_id"])
}

func TestShortcutBackend_MarkInProgress_CommentFailureNonFatal(t *testing.T) {
	// A failed start comment should not block the state transition.
	var putCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/stories/42":
			_ = json.NewEncoder(w).Encode(scStory{ID: 42, WorkflowStateID: 200})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusInternalServerError) // comment fails
		case r.Method == http.MethodPut:
			putCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 200, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithInProgressStateName("In Development"),
	)
	b.workflows = testWorkflows

	err := b.MarkInProgress(context.Background(), "42")
	require.NoError(t, err, "comment failure should not propagate as an error")
	assert.True(t, putCalled, "state transition should still happen after comment failure")
}

func TestShortcutBackend_MarkComplete(t *testing.T) {
	var commentText string
	var statePayload map[string]int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/stories/42":
			// Story is in "In Development" (state 300).
			_ = json.NewEncoder(w).Encode(scStory{ID: 42, WorkflowStateID: 300})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			commentText = payload["text"]
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/stories/42":
			_ = json.NewDecoder(r.Body).Decode(&statePayload)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)
	b.workflows = testWorkflows

	result := engine.TaskResult{
		Success:         true,
		Summary:         "Fixed the bug",
		MergeRequestURL: "https://github.com/owner/repo/pull/10",
	}
	err := b.MarkComplete(context.Background(), "42", result)
	require.NoError(t, err)

	assert.Contains(t, commentText, "Fixed the bug")
	assert.Contains(t, commentText, "https://github.com/owner/repo/pull/10")
	// Story should be transitioned to the "Done" state (ID 400, type "done").
	assert.Equal(t, int64(400), statePayload["workflow_state_id"])
}

func TestShortcutBackend_MarkFailed(t *testing.T) {
	var labelPayload map[string]string
	var commentPayload map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			_ = json.NewDecoder(r.Body).Decode(&labelPayload)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			_ = json.NewDecoder(r.Body).Decode(&commentPayload)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(), WithBaseURL(srv.URL))
	err := b.MarkFailed(context.Background(), "7", "timeout exceeded")
	require.NoError(t, err)

	assert.Equal(t, "osmia-failed", labelPayload["name"])
	assert.Contains(t, commentPayload["text"], "timeout exceeded")
}

func TestShortcutBackend_AddComment(t *testing.T) {
	var receivedBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/stories/5/comments", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(), WithBaseURL(srv.URL))
	err := b.AddComment(context.Background(), "5", "progress update")
	require.NoError(t, err)
	assert.Equal(t, "progress update", receivedBody["text"])
}

func TestShortcutBackend_WithBaseURL(t *testing.T) {
	b := NewShortcutBackend("tok", 500, testLogger(), WithBaseURL("https://custom.shortcut.com/api/v3/"))
	assert.Equal(t, "https://custom.shortcut.com/api/v3", b.baseURL)
}

// --- multi-workflow mapping tests ---

// twoWorkflowFixture returns a pair of Shortcut workflows suitable for use in
// multi-mapping tests. Workflow A contains "Ready for Dev A" (ID 201) and
// "In Dev A" (ID 301). Workflow B contains "Ready for Dev B" (ID 202) and
// "In Dev B" (ID 302).
var twoWorkflowFixture = []scWorkflow{
	{
		ID:   10,
		Name: "Workflow A",
		States: []scWorkflowState{
			{ID: 101, Name: "Backlog A", Type: "unstarted"},
			{ID: 201, Name: "Ready for Dev A", Type: "unstarted"},
			{ID: 301, Name: "In Dev A", Type: "started"},
			{ID: 401, Name: "Done A", Type: "done"},
		},
	},
	{
		ID:   20,
		Name: "Workflow B",
		States: []scWorkflowState{
			{ID: 102, Name: "Backlog B", Type: "unstarted"},
			{ID: 202, Name: "Ready for Dev B", Type: "unstarted"},
			{ID: 302, Name: "In Dev B", Type: "started"},
			{ID: 402, Name: "Done B", Type: "done"},
		},
	},
}

func TestShortcutBackend_Init_MultipleWorkflowMappings(t *testing.T) {
	// Init should resolve the triggerStateID for each mapping independently.
	srv := httptest.NewServer(workflowsHandler(t, twoWorkflowFixture))
	defer srv.Close()

	mappings := []WorkflowMapping{
		{TriggerState: "Ready for Dev A", InProgressState: "In Dev A"},
		{TriggerState: "Ready for Dev B", InProgressState: "In Dev B"},
	}

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowMappings(mappings),
	)

	require.NoError(t, b.Init(context.Background()))

	got := b.WorkflowMappings()
	require.Len(t, got, 2)
	assert.Equal(t, int64(201), got[0].triggerStateID, "first mapping trigger state ID")
	assert.Equal(t, int64(202), got[1].triggerStateID, "second mapping trigger state ID")

	// WorkflowStateID should return the first mapping's resolved ID.
	assert.Equal(t, int64(201), b.WorkflowStateID())
}

func TestShortcutBackend_PollReadyTickets_MultipleWorkflows(t *testing.T) {
	// Two mappings with different trigger states. The mock server returns
	// distinct stories for each query. Verify that all stories are returned in
	// the merged result.
	storiesA := []scStory{
		{ID: 1, Name: "Story in WF A", AppURL: "https://app.shortcut.com/org/story/1"},
	}
	storiesB := []scStory{
		{ID: 2, Name: "Story in WF B", AppURL: "https://app.shortcut.com/org/story/2"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, "Ready for Dev A"):
			_ = json.NewEncoder(w).Encode(searchResponse{Data: storiesA})
		case strings.Contains(q, "Ready for Dev B"):
			_ = json.NewEncoder(w).Encode(searchResponse{Data: storiesB})
		default:
			_ = json.NewEncoder(w).Encode(searchResponse{Data: []scStory{}})
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowMappings([]WorkflowMapping{
			{TriggerState: "Ready for Dev A", InProgressState: "In Dev A"},
			{TriggerState: "Ready for Dev B", InProgressState: "In Dev B"},
		}),
		WithExcludeLabels([]string{}),
	)

	tickets, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
	require.Len(t, tickets, 2)

	ids := make(map[string]bool, 2)
	for _, tk := range tickets {
		ids[tk.ID] = true
	}
	assert.True(t, ids["1"], "story 1 from workflow A should be present")
	assert.True(t, ids["2"], "story 2 from workflow B should be present")
}

func TestShortcutBackend_PollReadyTickets_DeduplicatesOverlap(t *testing.T) {
	// When the same story appears in the results for two different trigger state
	// queries (e.g. because the search DSL is fuzzy), it should only appear
	// once in the final result.
	sharedStory := scStory{ID: 99, Name: "Shared story", AppURL: "https://app.shortcut.com/org/story/99"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Both queries return the same story.
		_ = json.NewEncoder(w).Encode(searchResponse{Data: []scStory{sharedStory}})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowMappings([]WorkflowMapping{
			{TriggerState: "State A", InProgressState: "In Dev A"},
			{TriggerState: "State B", InProgressState: "In Dev B"},
		}),
		WithExcludeLabels([]string{}),
	)

	tickets, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
	require.Len(t, tickets, 1, "duplicate story should appear only once")
	assert.Equal(t, "99", tickets[0].ID)
}

func TestShortcutBackend_MarkInProgress_PicksCorrectMapping(t *testing.T) {
	// When two mappings are configured, MarkInProgress should select the
	// mapping whose triggerStateID matches the story's current state and
	// transition to that mapping's InProgressState — not the other one.
	//
	// The story is currently in state 202 ("Ready for Dev B"), so mapping 2
	// should be selected and the story transitioned to state 302 ("In Dev B").
	var statePayload map[string]int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/stories/55":
			// Story is currently in "Ready for Dev B" (state 202).
			_ = json.NewEncoder(w).Encode(scStory{ID: 55, WorkflowStateID: 202})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/stories/55":
			_ = json.NewDecoder(r.Body).Decode(&statePayload)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowMappings([]WorkflowMapping{
			{TriggerState: "Ready for Dev A", InProgressState: "In Dev A", triggerStateID: 201},
			{TriggerState: "Ready for Dev B", InProgressState: "In Dev B", triggerStateID: 202},
		}),
	)
	b.workflows = twoWorkflowFixture // pre-populate cache (skips Init)

	require.NoError(t, b.MarkInProgress(context.Background(), "55"))

	// The state transition must use "In Dev B" (ID 302), not "In Dev A" (ID 301).
	assert.Equal(t, int64(302), statePayload["workflow_state_id"],
		"expected transition to 'In Dev B' (state 302) based on mapping 2")
}
