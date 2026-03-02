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

	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
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

func TestShortcutBackend_Init_ResolvesWorkflowStateName(t *testing.T) {
	workflows := []scWorkflow{
		{
			ID:   1,
			Name: "Engineering",
			States: []scWorkflowState{
				{ID: 100, Name: "Backlog"},
				{ID: 200, Name: "Ready for Development"},
				{ID: 300, Name: "In Progress"},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/workflows", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(workflows)
	}))
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
	workflows := []scWorkflow{
		{States: []scWorkflowState{{ID: 42, Name: "Ready For Development"}}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(workflows)
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("ready for development"),
	)

	require.NoError(t, b.Init(context.Background()))
	assert.Equal(t, int64(42), b.workflowStateID)
}

func TestShortcutBackend_Init_WorkflowStateNotFound(t *testing.T) {
	workflows := []scWorkflow{
		{States: []scWorkflowState{{ID: 1, Name: "Backlog"}}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(workflows)
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 0, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("Nonexistent State"),
	)

	err := b.Init(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Nonexistent State")
}

func TestShortcutBackend_Init_ExplicitIDSkipsLookup(t *testing.T) {
	// If workflowStateID is set explicitly, Init should not call /workflows.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workflows" {
			called = true
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]scMember{})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 999, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithWorkflowStateName("Ready for Development"), // ignored — explicit ID wins
	)

	require.NoError(t, b.Init(context.Background()))
	assert.False(t, called, "should not call /workflows when explicit ID is set")
	assert.Equal(t, int64(999), b.workflowStateID)
}

// --- Init: member resolution ---

func TestShortcutBackend_Init_ResolvesMemberByMentionName(t *testing.T) {
	members := []scMember{
		{ID: "uuid-human", Profile: scMemberProfile{MentionName: "alice"}},
		{ID: "uuid-bot", Profile: scMemberProfile{MentionName: "robodev"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/members", r.URL.Path)
		json.NewEncoder(w).Encode(members)
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithOwnerMentionName("robodev"),
	)

	require.NoError(t, b.Init(context.Background()))
	assert.Equal(t, "uuid-bot", b.ownerMemberID)
}

func TestShortcutBackend_Init_OwnerAtPrefixStripped(t *testing.T) {
	b := NewShortcutBackend("tok", 500, testLogger(),
		WithOwnerMentionName("@robodev"),
	)
	assert.Equal(t, "robodev", b.ownerMentionName)
}

func TestShortcutBackend_Init_MemberNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]scMember{{ID: "other", Profile: scMemberProfile{MentionName: "alice"}}})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithOwnerMentionName("robodev"),
	)

	err := b.Init(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "robodev")
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
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/stories/search", r.URL.Path)

		var sr searchRequest
		json.NewDecoder(r.Body).Decode(&sr)
		assert.Equal(t, int64(500), sr.WorkflowStateID)
		assert.Empty(t, sr.OwnerIDs)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stories)
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
	var capturedRequest searchRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]scStory{})
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(),
		WithBaseURL(srv.URL),
		WithHTTPClient(srv.Client()),
	)
	b.ownerMemberID = "uuid-bot" // set directly, bypassing Init

	_, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"uuid-bot"}, capturedRequest.OwnerIDs)
}

func TestShortcutBackend_PollReadyTickets_NoStateIDReturnsError(t *testing.T) {
	b := NewShortcutBackend("tok", 0, testLogger())
	_, err := b.PollReadyTickets(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow state ID is not set")
}

func TestShortcutBackend_PollReadyTickets_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]scStory{})
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
			Labels: []scLabel{{Name: "robodev-failed"}},
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
			opts:          []Option{WithExcludeLabels([]string{"robodev-failed"})},
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
				json.NewEncoder(w).Encode(stories)
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

// --- lifecycle methods ---

func TestShortcutBackend_MarkInProgress(t *testing.T) {
	var calls []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels") {
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			assert.Equal(t, "in-progress", payload["name"])
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(), WithBaseURL(srv.URL))
	err := b.MarkInProgress(context.Background(), "42")
	require.NoError(t, err)

	assert.Contains(t, calls, "POST /stories/42/labels")
}

func TestShortcutBackend_MarkComplete(t *testing.T) {
	var commentText string
	var putCompleted bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			commentText = payload["text"]
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/stories/42"):
			var payload map[string]bool
			json.NewDecoder(r.Body).Decode(&payload)
			putCompleted = payload["completed"]
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(), WithBaseURL(srv.URL))

	result := engine.TaskResult{
		Success:         true,
		Summary:         "Fixed the bug",
		MergeRequestURL: "https://github.com/owner/repo/pull/10",
	}
	err := b.MarkComplete(context.Background(), "42", result)
	require.NoError(t, err)

	assert.Contains(t, commentText, "Fixed the bug")
	assert.Contains(t, commentText, "https://github.com/owner/repo/pull/10")
	assert.True(t, putCompleted)
}

func TestShortcutBackend_MarkFailed(t *testing.T) {
	var labelPayload map[string]string
	var commentPayload map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			json.NewDecoder(r.Body).Decode(&labelPayload)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			json.NewDecoder(r.Body).Decode(&commentPayload)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	b := NewShortcutBackend("tok", 500, testLogger(), WithBaseURL(srv.URL))
	err := b.MarkFailed(context.Background(), "7", "timeout exceeded")
	require.NoError(t, err)

	assert.Equal(t, "robodev-failed", labelPayload["name"])
	assert.Contains(t, commentPayload["text"], "timeout exceeded")
}

func TestShortcutBackend_AddComment(t *testing.T) {
	var receivedBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/stories/5/comments", r.URL.Path)
		json.NewDecoder(r.Body).Decode(&receivedBody)
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
