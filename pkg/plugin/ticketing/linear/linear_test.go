package linear

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
var _ ticketing.Backend = (*LinearBackend)(nil)

func TestLinearBackend_Name(t *testing.T) {
	b := NewLinearBackend("tok", "team-1", testLogger())
	assert.Equal(t, "linear", b.Name())
}

func TestLinearBackend_InterfaceVersion(t *testing.T) {
	b := NewLinearBackend("tok", "team-1", testLogger())
	assert.Equal(t, ticketing.InterfaceVersion, b.InterfaceVersion())
}

// graphqlHandler creates a test handler that validates the GraphQL request
// and returns the given data payload.
func graphqlHandler(t *testing.T, wantAuthHeader string, validateFn func(t *testing.T, gqlReq graphqlRequest), respData any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		if wantAuthHeader != "" {
			assert.Equal(t, wantAuthHeader, r.Header.Get("Authorization"))
		}

		var gqlReq graphqlRequest
		err := json.NewDecoder(r.Body).Decode(&gqlReq)
		require.NoError(t, err)

		if validateFn != nil {
			validateFn(t, gqlReq)
		}

		resp := graphqlResponse{}
		if respData != nil {
			data, err := json.Marshal(respData)
			require.NoError(t, err)
			resp.Data = data
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func TestLinearBackend_PollReadyTickets(t *testing.T) {
	respData := issuesResponse{}
	respData.Issues.Nodes = []linearIssue{
		{
			ID:          "id-1",
			Identifier:  "ENG-123",
			Title:       "Fix login bug",
			Description: "The login page crashes",
			URL:         "https://linear.app/org/issue/ENG-123",
			Labels: struct {
				Nodes []linearLabel `json:"nodes"`
			}{
				Nodes: []linearLabel{{Name: "osmia"}, {Name: "bug"}},
			},
		},
		{
			ID:          "id-2",
			Identifier:  "ENG-456",
			Title:       "Refactor auth",
			Description: "Clean up the auth module",
			URL:         "https://linear.app/org/issue/ENG-456",
			Labels: struct {
				Nodes []linearLabel `json:"nodes"`
			}{
				Nodes: []linearLabel{{Name: "osmia"}},
			},
		},
	}

	srv := httptest.NewServer(graphqlHandler(t, "test-token",
		func(t *testing.T, gqlReq graphqlRequest) {
			t.Helper()
			assert.Contains(t, gqlReq.Query, "issues(filter:")
			assert.Equal(t, "team-1", gqlReq.Variables["teamId"])
			assert.Equal(t, "Todo", gqlReq.Variables["stateFilter"])
			assert.Equal(t, []any{"osmia"}, gqlReq.Variables["labels"])
		},
		respData,
	))
	defer srv.Close()

	b := NewLinearBackend("test-token", "team-1", testLogger(),
		WithAPIURL(srv.URL),
		WithHTTPClient(srv.Client()),
		WithLabels([]string{"osmia"}),
		WithStateFilter("Todo"),
	)

	tickets, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
	require.Len(t, tickets, 2)

	assert.Equal(t, "ENG-123", tickets[0].ID)
	assert.Equal(t, "Fix login bug", tickets[0].Title)
	assert.Equal(t, "The login page crashes", tickets[0].Description)
	assert.Equal(t, "issue", tickets[0].TicketType)
	assert.Equal(t, []string{"osmia", "bug"}, tickets[0].Labels)
	assert.Equal(t, "https://linear.app/org/issue/ENG-123", tickets[0].ExternalURL)

	assert.Equal(t, "ENG-456", tickets[1].ID)
}

func TestLinearBackend_PollReadyTickets_EmptyResponse(t *testing.T) {
	respData := issuesResponse{}

	srv := httptest.NewServer(graphqlHandler(t, "", nil, respData))
	defer srv.Close()

	b := NewLinearBackend("tok", "team-1", testLogger(),
		WithAPIURL(srv.URL),
		WithLabels([]string{"osmia"}),
	)
	tickets, err := b.PollReadyTickets(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tickets)
}

func TestLinearBackend_PollReadyTickets_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := NewLinearBackend("tok", "team-1", testLogger(),
		WithAPIURL(srv.URL),
		WithLabels([]string{"osmia"}),
	)
	_, err := b.PollReadyTickets(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 500")
}

func TestLinearBackend_PollReadyTickets_GraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := graphqlResponse{
			Errors: []graphqlError{{Message: "invalid team ID"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	b := NewLinearBackend("tok", "bad-team", testLogger(),
		WithAPIURL(srv.URL),
	)
	_, err := b.PollReadyTickets(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid team ID")
}

func TestLinearBackend_PollReadyTickets_ExcludeLabels(t *testing.T) {
	makeIssues := func() issuesResponse {
		resp := issuesResponse{}
		resp.Issues.Nodes = []linearIssue{
			{
				ID: "id-1", Identifier: "ENG-1", Title: "Normal",
				URL: "https://linear.app/org/issue/ENG-1",
				Labels: struct {
					Nodes []linearLabel `json:"nodes"`
				}{Nodes: []linearLabel{{Name: "osmia"}}},
			},
			{
				ID: "id-2", Identifier: "ENG-2", Title: "In-progress",
				URL: "https://linear.app/org/issue/ENG-2",
				Labels: struct {
					Nodes []linearLabel `json:"nodes"`
				}{Nodes: []linearLabel{{Name: "osmia"}, {Name: "in-progress"}}},
			},
			{
				ID: "id-3", Identifier: "ENG-3", Title: "Failed",
				URL: "https://linear.app/org/issue/ENG-3",
				Labels: struct {
					Nodes []linearLabel `json:"nodes"`
				}{Nodes: []linearLabel{{Name: "osmia"}, {Name: "osmia-failed"}}},
			},
			{
				ID: "id-4", Identifier: "ENG-4", Title: "Clean",
				URL: "https://linear.app/org/issue/ENG-4",
				Labels: struct {
					Nodes []linearLabel `json:"nodes"`
				}{Nodes: []linearLabel{{Name: "osmia"}}},
			},
		}
		return resp
	}

	tests := []struct {
		name          string
		opts          []Option
		wantTicketIDs []string
	}{
		{
			name:          "default exclusion filters out in-progress and failed",
			wantTicketIDs: []string{"ENG-1", "ENG-4"},
		},
		{
			name:          "custom excludeLabels override",
			opts:          []Option{WithExcludeLabels([]string{"osmia-failed"})},
			wantTicketIDs: []string{"ENG-1", "ENG-2", "ENG-4"},
		},
		{
			name:          "empty excludeLabels disables exclusion",
			opts:          []Option{WithExcludeLabels([]string{})},
			wantTicketIDs: []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(graphqlHandler(t, "", nil, makeIssues()))
			defer srv.Close()

			allOpts := append([]Option{
				WithAPIURL(srv.URL),
				WithHTTPClient(srv.Client()),
				WithLabels([]string{"osmia"}),
			}, tc.opts...)
			b := NewLinearBackend("tok", "team-1", testLogger(), allOpts...)

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

func TestLinearBackend_MarkInProgress(t *testing.T) {
	srv := httptest.NewServer(graphqlHandler(t, "",
		func(t *testing.T, gqlReq graphqlRequest) {
			t.Helper()
			assert.Contains(t, gqlReq.Query, "issueAddLabel")
			assert.Equal(t, "ENG-123", gqlReq.Variables["id"])
			assert.Equal(t, "in-progress", gqlReq.Variables["labelName"])
		},
		map[string]any{"issueAddLabel": map[string]any{"success": true}},
	))
	defer srv.Close()

	b := NewLinearBackend("tok", "team-1", testLogger(), WithAPIURL(srv.URL))
	err := b.MarkInProgress(context.Background(), "ENG-123")
	require.NoError(t, err)
}

func TestLinearBackend_MarkComplete(t *testing.T) {
	var calls []graphqlRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var gqlReq graphqlRequest
		_ = json.NewDecoder(r.Body).Decode(&gqlReq)
		calls = append(calls, gqlReq)

		resp := graphqlResponse{}
		data, _ := json.Marshal(map[string]any{
			"commentCreate": map[string]any{"success": true},
			"issueUpdate":   map[string]any{"success": true},
		})
		resp.Data = data

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	b := NewLinearBackend("tok", "team-1", testLogger(), WithAPIURL(srv.URL))

	result := engine.TaskResult{
		Success:         true,
		Summary:         "Fixed the bug",
		MergeRequestURL: "https://github.com/owner/repo/pull/10",
	}
	err := b.MarkComplete(context.Background(), "ENG-123", result)
	require.NoError(t, err)
	require.Len(t, calls, 2)

	// First call: comment creation.
	assert.Contains(t, calls[0].Query, "commentCreate")
	assert.Equal(t, "ENG-123", calls[0].Variables["issueId"])
	body, ok := calls[0].Variables["body"].(string)
	require.True(t, ok)
	assert.Contains(t, body, "Fixed the bug")
	assert.Contains(t, body, "https://github.com/owner/repo/pull/10")

	// Second call: state update.
	assert.Contains(t, calls[1].Query, "issueUpdate")
	assert.Equal(t, "ENG-123", calls[1].Variables["id"])
	assert.Equal(t, "completed", calls[1].Variables["stateId"])
}

func TestLinearBackend_MarkFailed(t *testing.T) {
	var calls []graphqlRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var gqlReq graphqlRequest
		_ = json.NewDecoder(r.Body).Decode(&gqlReq)
		calls = append(calls, gqlReq)

		resp := graphqlResponse{}
		data, _ := json.Marshal(map[string]any{
			"issueAddLabel": map[string]any{"success": true},
			"commentCreate": map[string]any{"success": true},
		})
		resp.Data = data

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	b := NewLinearBackend("tok", "team-1", testLogger(), WithAPIURL(srv.URL))
	err := b.MarkFailed(context.Background(), "ENG-7", "timeout exceeded")
	require.NoError(t, err)
	require.Len(t, calls, 2)

	// First call: add failed label.
	assert.Contains(t, calls[0].Query, "issueAddLabel")
	assert.Equal(t, "osmia-failed", calls[0].Variables["labelName"])

	// Second call: failure comment.
	assert.Contains(t, calls[1].Query, "commentCreate")
	body, ok := calls[1].Variables["body"].(string)
	require.True(t, ok)
	assert.Contains(t, body, "timeout exceeded")
}

func TestLinearBackend_AddComment(t *testing.T) {
	srv := httptest.NewServer(graphqlHandler(t, "",
		func(t *testing.T, gqlReq graphqlRequest) {
			t.Helper()
			assert.Contains(t, gqlReq.Query, "commentCreate")
			assert.Equal(t, "ENG-5", gqlReq.Variables["issueId"])
			assert.Equal(t, "progress update", gqlReq.Variables["body"])
		},
		map[string]any{"commentCreate": map[string]any{"success": true}},
	))
	defer srv.Close()

	b := NewLinearBackend("tok", "team-1", testLogger(), WithAPIURL(srv.URL))
	err := b.AddComment(context.Background(), "ENG-5", "progress update")
	require.NoError(t, err)
}

func TestLinearBackend_WithAPIURL(t *testing.T) {
	b := NewLinearBackend("tok", "team-1", testLogger(), WithAPIURL("https://custom.linear.app/graphql/"))
	assert.Equal(t, "https://custom.linear.app/graphql", b.apiURL)
}
