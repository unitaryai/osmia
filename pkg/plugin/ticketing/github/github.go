// Package github provides a built-in ticketing.Backend implementation
// that integrates with GitHub Issues via the REST API. It uses net/http
// directly to minimise external dependencies.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/robodev-inc/robodev/pkg/engine"
	"github.com/robodev-inc/robodev/pkg/plugin/ticketing"
)

const (
	defaultBaseURL = "https://api.github.com"
	backendName    = "github"
)

// Compile-time check that GitHubBackend implements ticketing.Backend.
var _ ticketing.Backend = (*GitHubBackend)(nil)

// ghIssue is the subset of the GitHub Issue response we parse.
type ghIssue struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	HTMLURL string    `json:"html_url"`
	Labels  []ghLabel `json:"labels"`
}

// ghLabel is the subset of a GitHub label response we parse.
type ghLabel struct {
	Name string `json:"name"`
}

// GitHubBackend implements ticketing.Backend by talking to the GitHub
// Issues REST API. The API base URL is configurable for GitHub Enterprise
// support.
type GitHubBackend struct {
	owner         string
	repo          string
	labels        []string
	token         string
	baseURL       string
	client        *http.Client
	logger        *slog.Logger
	assignee      string   // GitHub username filter (optional)
	milestone     string   // milestone number filter (optional)
	state         string   // "open" (default), "closed", "all"
	excludeLabels []string // client-side exclusion (default: ["in-progress", "robodev-failed"])
}

// Option is a functional option for configuring a GitHubBackend.
type Option func(*GitHubBackend)

// WithBaseURL sets a custom API base URL (e.g. for GitHub Enterprise).
func WithBaseURL(url string) Option {
	return func(b *GitHubBackend) {
		b.baseURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient sets a custom http.Client for the backend.
func WithHTTPClient(c *http.Client) Option {
	return func(b *GitHubBackend) {
		b.client = c
	}
}

// WithAssignee filters issues by the given GitHub username.
func WithAssignee(assignee string) Option {
	return func(b *GitHubBackend) {
		b.assignee = assignee
	}
}

// WithMilestone filters issues by milestone number (not title).
func WithMilestone(milestone string) Option {
	return func(b *GitHubBackend) {
		b.milestone = milestone
	}
}

// WithState sets the issue state filter: "open" (default), "closed", or "all".
func WithState(state string) Option {
	return func(b *GitHubBackend) {
		b.state = state
	}
}

// WithExcludeLabels overrides the default client-side label exclusion list.
// Issues carrying any of these labels are filtered out after fetching.
func WithExcludeLabels(labels []string) Option {
	return func(b *GitHubBackend) {
		b.excludeLabels = labels
	}
}

// NewGitHubBackend creates a new GitHub ticketing backend.
func NewGitHubBackend(owner, repo string, labels []string, token string, logger *slog.Logger, opts ...Option) *GitHubBackend {
	b := &GitHubBackend{
		owner:         owner,
		repo:          repo,
		labels:        labels,
		token:         token,
		baseURL:       defaultBaseURL,
		client:        http.DefaultClient,
		logger:        logger,
		state:         "open",
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

// PollReadyTickets lists issues filtered by the configured criteria.
func (b *GitHubBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
	params := url.Values{}
	params.Set("state", b.state)
	if len(b.labels) > 0 {
		params.Set("labels", strings.Join(b.labels, ","))
	}
	if b.assignee != "" {
		params.Set("assignee", b.assignee)
	}
	if b.milestone != "" {
		params.Set("milestone", b.milestone)
	}

	reqURL := fmt.Sprintf("%s/repos/%s/%s/issues?%s", b.baseURL, b.owner, b.repo, params.Encode())

	body, err := b.doGet(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("polling ready tickets: %w", err)
	}
	defer body.Close()

	var issues []ghIssue
	if err := json.NewDecoder(body).Decode(&issues); err != nil {
		return nil, fmt.Errorf("decoding issues response: %w", err)
	}

	// Build exclusion set for client-side filtering.
	excludeSet := make(map[string]struct{}, len(b.excludeLabels))
	for _, l := range b.excludeLabels {
		excludeSet[l] = struct{}{}
	}

	tickets := make([]ticketing.Ticket, 0, len(issues))
	for _, issue := range issues {
		if hasExcludedLabel(issue.Labels, excludeSet) {
			continue
		}

		labels := make([]string, 0, len(issue.Labels))
		for _, l := range issue.Labels {
			labels = append(labels, l.Name)
		}
		tickets = append(tickets, ticketing.Ticket{
			ID:          strconv.Itoa(issue.Number),
			Title:       issue.Title,
			Description: issue.Body,
			TicketType:  "issue",
			Labels:      labels,
			RepoURL:     fmt.Sprintf("https://github.com/%s/%s", b.owner, b.repo),
			ExternalURL: issue.HTMLURL,
		})
	}

	b.logger.Info("polled ready tickets", "count", len(tickets))
	return tickets, nil
}

// hasExcludedLabel returns true if any of the issue's labels appear in the
// exclusion set.
func hasExcludedLabel(issueLabels []ghLabel, excludeSet map[string]struct{}) bool {
	for _, l := range issueLabels {
		if _, ok := excludeSet[l.Name]; ok {
			return true
		}
	}
	return false
}

// MarkInProgress adds the "in-progress" label and removes the configured
// trigger labels from the issue.
func (b *GitHubBackend) MarkInProgress(ctx context.Context, ticketID string) error {
	// Add "in-progress" label.
	if err := b.addLabels(ctx, ticketID, []string{"in-progress"}); err != nil {
		return fmt.Errorf("adding in-progress label: %w", err)
	}
	// Remove each configured trigger label.
	for _, label := range b.labels {
		if err := b.removeLabel(ctx, ticketID, label); err != nil {
			b.logger.Warn("failed to remove label", "label", label, "ticket", ticketID, "error", err)
		}
	}
	return nil
}

// MarkComplete closes the issue and adds a summary comment with the merge
// request URL.
func (b *GitHubBackend) MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error {
	comment := fmt.Sprintf("Task completed successfully.\n\n**Summary:** %s", result.Summary)
	if result.MergeRequestURL != "" {
		comment += fmt.Sprintf("\n**Pull Request:** %s", result.MergeRequestURL)
	}
	if err := b.AddComment(ctx, ticketID, comment); err != nil {
		return fmt.Errorf("adding completion comment: %w", err)
	}

	if err := b.closeIssue(ctx, ticketID); err != nil {
		return fmt.Errorf("closing issue: %w", err)
	}
	return nil
}

// MarkFailed adds a "robodev-failed" label and posts the failure reason
// as a comment.
func (b *GitHubBackend) MarkFailed(ctx context.Context, ticketID string, reason string) error {
	if err := b.addLabels(ctx, ticketID, []string{"robodev-failed"}); err != nil {
		return fmt.Errorf("adding failed label: %w", err)
	}
	comment := fmt.Sprintf("Task failed.\n\n**Reason:** %s", reason)
	if err := b.AddComment(ctx, ticketID, comment); err != nil {
		return fmt.Errorf("adding failure comment: %w", err)
	}
	return nil
}

// AddComment posts a comment on the given issue.
func (b *GitHubBackend) AddComment(ctx context.Context, ticketID string, comment string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%s/comments", b.baseURL, b.owner, b.repo, ticketID)
	payload := map[string]string{"body": comment}
	if err := b.doPost(ctx, url, payload); err != nil {
		return fmt.Errorf("adding comment to ticket %s: %w", ticketID, err)
	}
	return nil
}

// Name returns the backend identifier.
func (b *GitHubBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the ticketing interface version implemented.
func (b *GitHubBackend) InterfaceVersion() int {
	return ticketing.InterfaceVersion
}

// addLabels adds one or more labels to an issue.
func (b *GitHubBackend) addLabels(ctx context.Context, ticketID string, labels []string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%s/labels", b.baseURL, b.owner, b.repo, ticketID)
	payload := map[string][]string{"labels": labels}
	return b.doPost(ctx, url, payload)
}

// removeLabel removes a single label from an issue.
func (b *GitHubBackend) removeLabel(ctx context.Context, ticketID string, label string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%s/labels/%s", b.baseURL, b.owner, b.repo, ticketID, label)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("creating delete request: %w", err)
	}
	b.setAuthHeaders(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing delete request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("unexpected status %d removing label %q", resp.StatusCode, label)
	}
	return nil
}

// closeIssue sets the issue state to "closed".
func (b *GitHubBackend) closeIssue(ctx context.Context, ticketID string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%s", b.baseURL, b.owner, b.repo, ticketID)
	payload := map[string]string{"state": "closed"}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling close payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating patch request: %w", err)
	}
	b.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing patch request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d closing issue", resp.StatusCode)
	}
	return nil
}

// doGet performs a GET request and returns the response body.
func (b *GitHubBackend) doGet(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	b.setAuthHeaders(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// doPost performs a POST request with a JSON body.
func (b *GitHubBackend) doPost(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	b.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// setAuthHeaders adds the authorisation and accept headers to a request.
func (b *GitHubBackend) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/vnd.github+json")
}
