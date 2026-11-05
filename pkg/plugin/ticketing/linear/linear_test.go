package linear

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

// labelServer fakes Linear's label calls for an issue on team-1.
type labelServer struct {
	teamLabels      []string
	workspaceLabels []string
	createConflict  bool

	created   map[string]any
	addedID   string
	commented string
}

// labelNodesOf builds a label connection; an ID starting with "group-" is a
// label group.
func labelNodesOf(ids []string) map[string]any {
	nodes := []map[string]any{}
	for _, id := range ids {
		nodes = append(nodes, map[string]any{"id": id, "isGroup": strings.HasPrefix(id, "group-")})
	}
	return map[string]any{"nodes": nodes}
}

func (ls *labelServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req graphqlRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		resp := graphqlResponse{}
		var data any
		switch {
		case strings.Contains(req.Query, "issueLabelCreate"):
			ls.created = req.Variables
			if ls.createConflict {
				// The other run's label now exists; Linear refuses a duplicate.
				ls.teamLabels = []string{"lbl-other-run"}
				resp.Errors = []graphqlError{{Message: "duplicate label name"}}
				break
			}
			data = map[string]any{"issueLabelCreate": map[string]any{"success": true, "issueLabel": map[string]any{"id": "lbl-new"}}}
		case strings.Contains(req.Query, "issueAddLabel"):
			ls.addedID, _ = req.Variables["labelId"].(string)
			data = map[string]any{"issueAddLabel": map[string]any{"success": true}}
		case strings.Contains(req.Query, "commentCreate"):
			ls.commented, _ = req.Variables["body"].(string)
			data = map[string]any{"commentCreate": map[string]any{"success": true}}
		default:
			data = map[string]any{
				"issue":       map[string]any{"team": map[string]any{"id": "team-1", "labels": labelNodesOf(ls.teamLabels)}},
				"issueLabels": labelNodesOf(ls.workspaceLabels),
			}
		}
		if data != nil {
			resp.Data, _ = json.Marshal(data)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Linear's issueAddLabel takes a label ID, so the label is looked up by name
// on the issue's team and the workspace, and created on the team if missing.
func TestLinearBackend_MarkInProgress(t *testing.T) {
	tests := []struct {
		name        string
		server      labelServer
		wantAdded   string
		wantCreated bool
	}{
		{
			name:      "uses an existing workspace label",
			server:    labelServer{workspaceLabels: []string{"lbl-workspace"}},
			wantAdded: "lbl-workspace",
		},
		{
			name:        "skips a label group of the same name",
			server:      labelServer{teamLabels: []string{"group-in-progress"}},
			wantAdded:   "lbl-new",
			wantCreated: true,
		},
		{
			name:        "creates the label on the issue's team when there is none",
			wantAdded:   "lbl-new",
			wantCreated: true,
		},
		{
			name:        "uses the label another run created first",
			server:      labelServer{createConflict: true},
			wantAdded:   "lbl-other-run",
			wantCreated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := tt.server
			b := NewLinearBackend("tok", "team-1", testLogger(), WithAPIURL(ls.start(t).URL))

			require.NoError(t, b.MarkInProgress(context.Background(), "ENG-123"))

			assert.Equal(t, tt.wantAdded, ls.addedID)
			if tt.wantCreated {
				assert.Equal(t, map[string]any{"teamId": "team-1", "name": "in-progress"}, ls.created)
			} else {
				assert.Nil(t, ls.created, "an existing label must not be duplicated")
			}
		})
	}
}

// statesPage builds a team-states response; a non-empty cursor means more pages follow.
func statesPage(cursor string, nodes ...map[string]any) map[string]any {
	return map[string]any{"issue": map[string]any{"team": map[string]any{"states": map[string]any{
		"nodes":    nodes,
		"pageInfo": map[string]any{"hasNextPage": cursor != "", "endCursor": cursor},
	}}}}
}

func TestLinearBackend_MarkComplete(t *testing.T) {
	done := map[string]any{"id": "state-done", "name": "Done", "position": 3.0}
	released := map[string]any{"id": "state-released", "name": "Released", "position": 5.0}
	review := map[string]any{"id": "state-review", "name": "In Review", "position": 2.0}
	completedType := map[string]any{"type": map[string]any{"eq": "completed"}}
	inReviewName := map[string]any{"name": map[string]any{"eq": "In Review"}}

	tests := []struct {
		name         string
		stateName    string
		pages        []map[string]any
		rejectUpdate bool
		wantFilter   map[string]any
		wantMovedTo  string
		wantErr      string
	}{
		{
			name:        "default picks the lowest-position completed state across pages",
			pages:       []map[string]any{statesPage("c1", released), statesPage("", done)},
			wantFilter:  completedType,
			wantMovedTo: "state-done",
		},
		{
			name:        "configured state name is used",
			stateName:   "In Review",
			pages:       []map[string]any{statesPage("", review)},
			wantFilter:  inReviewName,
			wantMovedTo: "state-review",
		},
		{
			name:       "configured state name that does not exist is an error",
			stateName:  "In Review",
			pages:      []map[string]any{statesPage("")},
			wantFilter: inReviewName,
			wantErr:    `no workflow state named "In Review"`,
		},
		{
			name:         "update rejected by Linear is an error",
			pages:        []map[string]any{statesPage("", done)},
			rejectUpdate: true,
			wantFilter:   completedType,
			wantMovedTo:  "state-done",
			wantErr:      "did not update ticket ENG-123",
		},
		{
			name:       "repeated cursor is an error, not an endless loop",
			pages:      []map[string]any{statesPage("c1", done), statesPage("c1", done)},
			wantFilter: completedType,
			wantErr:    "did not advance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				filter    any
				movedTo   string
				commented bool
				page      int
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req graphqlRequest
				_ = json.NewDecoder(r.Body).Decode(&req)

				var data any
				switch {
				case strings.Contains(req.Query, "issueUpdate"):
					movedTo, _ = req.Variables["stateId"].(string)
					data = map[string]any{"issueUpdate": map[string]any{"success": !tt.rejectUpdate}}
				case strings.Contains(req.Query, "commentCreate"):
					commented = true
					data = map[string]any{"commentCreate": map[string]any{"success": true}}
				default:
					if page >= len(tt.pages) {
						t.Errorf("unexpected states query %d", page+1)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					// Only pages after the first carry a cursor.
					assert.Equal(t, page > 0, req.Variables["after"] != nil, "states query %d: after = %v", page+1, req.Variables["after"])
					filter = req.Variables["filter"]
					data = tt.pages[page]
					page++
				}
				raw, _ := json.Marshal(data)
				_ = json.NewEncoder(w).Encode(graphqlResponse{Data: raw})
			}))
			defer srv.Close()

			opts := []Option{WithAPIURL(srv.URL)}
			if tt.stateName != "" {
				opts = append(opts, WithCompletedStateName(tt.stateName))
			}
			b := NewLinearBackend("tok", "team-1", testLogger(), opts...)
			err := b.MarkComplete(context.Background(), "ENG-123", engine.TaskResult{Summary: "Fixed the bug"})

			assert.Equal(t, tt.wantFilter, filter)
			assert.Equal(t, tt.wantMovedTo, movedTo)
			assert.Equal(t, tt.wantErr == "", commented, "comment is posted only after a successful update")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestLinearBackend_MarkFailed(t *testing.T) {
	ls := &labelServer{teamLabels: []string{"lbl-failed"}}
	b := NewLinearBackend("tok", "team-1", testLogger(), WithAPIURL(ls.start(t).URL))

	require.NoError(t, b.MarkFailed(context.Background(), "ENG-7", "timeout exceeded"))

	assert.Equal(t, "lbl-failed", ls.addedID)
	assert.Contains(t, ls.commented, "timeout exceeded")
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
