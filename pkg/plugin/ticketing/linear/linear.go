// Package linear provides a built-in ticketing.Backend implementation
// that integrates with Linear via its GraphQL API. It uses net/http
// directly to minimise external dependencies.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

const (
	defaultAPIURL = "https://api.linear.app/graphql"
	backendName   = "linear"
)

// Compile-time check that LinearBackend implements ticketing.Backend.
var _ ticketing.Backend = (*LinearBackend)(nil)

// graphqlRequest is the JSON body sent to the Linear GraphQL API.
type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// graphqlResponse is the top-level shape of a Linear GraphQL response.
type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphqlError  `json:"errors,omitempty"`
}

// graphqlError represents a single error from a GraphQL response.
type graphqlError struct {
	Message string `json:"message"`
}

// issuesResponse maps the data.issues portion of a query response.
type issuesResponse struct {
	Issues struct {
		Nodes []linearIssue `json:"nodes"`
	} `json:"issues"`
}

// linearIssue is the subset of a Linear Issue we parse.
type linearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Labels      struct {
		Nodes []linearLabel `json:"nodes"`
	} `json:"labels"`
}

// linearLabel is the subset of a Linear label we parse.
type linearLabel struct {
	Name string `json:"name"`
}

// LinearBackend implements ticketing.Backend by talking to the Linear
// GraphQL API.
type LinearBackend struct {
	token         string
	apiURL        string
	httpClient    *http.Client
	logger        *slog.Logger
	teamID        string
	labels        []string
	excludeLabels []string
	stateFilter   string
}

// Option is a functional option for configuring a LinearBackend.
type Option func(*LinearBackend)

// WithAPIURL sets a custom GraphQL API URL.
func WithAPIURL(url string) Option {
	return func(b *LinearBackend) {
		b.apiURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient sets a custom http.Client for the backend.
func WithHTTPClient(c *http.Client) Option {
	return func(b *LinearBackend) {
		b.httpClient = c
	}
}

// WithTeamID sets the Linear team ID used for filtering issues.
func WithTeamID(teamID string) Option {
	return func(b *LinearBackend) {
		b.teamID = teamID
	}
}

// WithLabels sets the labels used to filter issues when polling.
func WithLabels(labels []string) Option {
	return func(b *LinearBackend) {
		b.labels = labels
	}
}

// WithExcludeLabels overrides the default client-side label exclusion list.
// Issues carrying any of these labels are filtered out after fetching.
func WithExcludeLabels(labels []string) Option {
	return func(b *LinearBackend) {
		b.excludeLabels = labels
	}
}

// WithStateFilter sets the workflow state name used to filter issues
// (e.g. "Todo", "Backlog").
func WithStateFilter(state string) Option {
	return func(b *LinearBackend) {
		b.stateFilter = state
	}
}

// NewLinearBackend creates a new Linear ticketing backend.
func NewLinearBackend(token string, teamID string, logger *slog.Logger, opts ...Option) *LinearBackend {
	b := &LinearBackend{
		token:         token,
		apiURL:        defaultAPIURL,
		httpClient:    http.DefaultClient,
		logger:        logger,
		teamID:        teamID,
		excludeLabels: []string{"in-progress", "robodev-failed"},
	}
	for _, opt := range opts {
		opt(b)
	}
	if len(b.excludeLabels) == 0 {
		b.logger.Warn("exclude_labels is empty; in-progress and failed issues will not be filtered out automatically")
	}
	return b
}

const pollQuery = `query($teamId: String!, $stateFilter: String, $labels: [String!]) {
  issues(filter: {
    team: { id: { eq: $teamId } }
    state: { name: { eq: $stateFilter } }
    labels: { name: { in: $labels } }
  }) {
    nodes {
      id
      identifier
      title
      description
      url
      labels { nodes { name } }
    }
  }
}`

// PollReadyTickets queries Linear for issues matching the configured team,
// state, and labels.
func (b *LinearBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
	variables := map[string]any{
		"teamId": b.teamID,
	}
	if b.stateFilter != "" {
		variables["stateFilter"] = b.stateFilter
	}
	if len(b.labels) > 0 {
		variables["labels"] = b.labels
	}

	data, err := b.doGraphQL(ctx, pollQuery, variables)
	if err != nil {
		return nil, fmt.Errorf("polling ready tickets: %w", err)
	}

	var resp issuesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decoding issues response: %w", err)
	}

	// Build exclusion set for client-side filtering.
	excludeSet := make(map[string]struct{}, len(b.excludeLabels))
	for _, l := range b.excludeLabels {
		excludeSet[l] = struct{}{}
	}

	tickets := make([]ticketing.Ticket, 0, len(resp.Issues.Nodes))
	for _, issue := range resp.Issues.Nodes {
		if hasExcludedLabel(issue.Labels.Nodes, excludeSet) {
			continue
		}

		labels := make([]string, 0, len(issue.Labels.Nodes))
		for _, l := range issue.Labels.Nodes {
			labels = append(labels, l.Name)
		}

		tickets = append(tickets, ticketing.Ticket{
			ID:          issue.Identifier,
			Title:       issue.Title,
			Description: issue.Description,
			TicketType:  "issue",
			Labels:      labels,
			ExternalURL: issue.URL,
		})
	}

	b.logger.Info("polled ready tickets", "count", len(tickets))
	return tickets, nil
}

// hasExcludedLabel returns true if any of the issue's labels appear in the
// exclusion set.
func hasExcludedLabel(issueLabels []linearLabel, excludeSet map[string]struct{}) bool {
	for _, l := range issueLabels {
		if _, ok := excludeSet[l.Name]; ok {
			return true
		}
	}
	return false
}

const markInProgressMutation = `mutation($id: String!, $labelName: String!) {
  issueAddLabel(id: $id, labelName: $labelName) {
    success
  }
}`

// MarkInProgress adds an "in-progress" label to the issue.
func (b *LinearBackend) MarkInProgress(ctx context.Context, ticketID string) error {
	_, err := b.doGraphQL(ctx, markInProgressMutation, map[string]any{
		"id":        ticketID,
		"labelName": "in-progress",
	})
	if err != nil {
		return fmt.Errorf("marking ticket %s in progress: %w", ticketID, err)
	}
	return nil
}

const commentCreateMutation = `mutation($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
  }
}`

// MarkComplete posts a summary comment and updates the issue state to completed.
func (b *LinearBackend) MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error {
	comment := fmt.Sprintf("Task completed successfully.\n\n**Summary:** %s", result.Summary)
	if result.MergeRequestURL != "" {
		comment += fmt.Sprintf("\n**Merge Request:** %s", result.MergeRequestURL)
	}
	if err := b.AddComment(ctx, ticketID, comment); err != nil {
		return fmt.Errorf("adding completion comment: %w", err)
	}

	// Update issue state to completed.
	const mutation = `mutation($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) {
    success
  }
}`
	_, err := b.doGraphQL(ctx, mutation, map[string]any{
		"id":      ticketID,
		"stateId": "completed",
	})
	if err != nil {
		return fmt.Errorf("updating ticket %s to completed state: %w", ticketID, err)
	}
	return nil
}

// MarkFailed adds a "robodev-failed" label and posts the failure reason
// as a comment.
func (b *LinearBackend) MarkFailed(ctx context.Context, ticketID string, reason string) error {
	const addLabelMutation = `mutation($id: String!, $labelName: String!) {
  issueAddLabel(id: $id, labelName: $labelName) {
    success
  }
}`
	_, err := b.doGraphQL(ctx, addLabelMutation, map[string]any{
		"id":        ticketID,
		"labelName": "robodev-failed",
	})
	if err != nil {
		return fmt.Errorf("adding failed label to ticket %s: %w", ticketID, err)
	}

	comment := fmt.Sprintf("Task failed.\n\n**Reason:** %s", reason)
	if err := b.AddComment(ctx, ticketID, comment); err != nil {
		return fmt.Errorf("adding failure comment: %w", err)
	}
	return nil
}

// AddComment posts a comment on the given issue.
func (b *LinearBackend) AddComment(ctx context.Context, ticketID string, comment string) error {
	_, err := b.doGraphQL(ctx, commentCreateMutation, map[string]any{
		"issueId": ticketID,
		"body":    comment,
	})
	if err != nil {
		return fmt.Errorf("adding comment to ticket %s: %w", ticketID, err)
	}
	return nil
}

// Name returns the backend identifier.
func (b *LinearBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the ticketing interface version implemented.
func (b *LinearBackend) InterfaceVersion() int {
	return ticketing.InterfaceVersion
}

// doGraphQL sends a GraphQL request to the Linear API and returns the data
// field from the response.
func (b *LinearBackend) doGraphQL(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	gqlReq := graphqlRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(gqlReq)
	if err != nil {
		return nil, fmt.Errorf("marshalling graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	b.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var gqlResp graphqlResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return nil, fmt.Errorf("decoding graphql response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	return gqlResp.Data, nil
}

// setAuthHeaders adds the Linear authorisation header to a request.
func (b *LinearBackend) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", b.token)
}
