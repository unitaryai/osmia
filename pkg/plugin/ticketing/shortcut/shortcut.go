// Package shortcut provides a built-in ticketing.Backend implementation
// that integrates with Shortcut (formerly Clubhouse) via the REST API.
// It uses net/http directly to minimise external dependencies.
package shortcut

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

const (
	defaultBaseURL = "https://api.app.shortcut.com/api/v3"
	backendName    = "shortcut"
)

// Compile-time check that ShortcutBackend implements ticketing.Backend.
var _ ticketing.Backend = (*ShortcutBackend)(nil)

// scStory is the subset of the Shortcut Story response we parse.
type scStory struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	AppURL        string    `json:"app_url"`
	Labels        []scLabel `json:"labels"`
	ExternalLinks []string  `json:"external_links"`
}

// scLabel is the subset of a Shortcut label response we parse.
type scLabel struct {
	Name string `json:"name"`
}

// scWorkflow is the subset of a Shortcut workflow response we parse.
type scWorkflow struct {
	ID     int64              `json:"id"`
	Name   string             `json:"name"`
	States []scWorkflowState  `json:"states"`
}

// scWorkflowState represents a single state within a Shortcut workflow.
type scWorkflowState struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// scMember is the subset of a Shortcut member response we parse.
type scMember struct {
	ID      string          `json:"id"`
	Profile scMemberProfile `json:"profile"`
}

// scMemberProfile holds the fields we care about from a Shortcut member profile.
type scMemberProfile struct {
	MentionName string `json:"mention_name"`
	Name        string `json:"name"`
}

// ShortcutBackend implements ticketing.Backend by talking to the Shortcut
// REST API.
type ShortcutBackend struct {
	token           string
	baseURL         string
	httpClient      *http.Client
	logger          *slog.Logger
	workflowStateID   int64
	workflowStateName string // human-readable name; resolved to workflowStateID by Init
	ownerMentionName  string // mention name (e.g. "robodev"); resolved to ownerMemberID by Init
	ownerMemberID     string // resolved Shortcut member UUID for owner filtering
	excludeLabels     []string
}

// Option is a functional option for configuring a ShortcutBackend.
type Option func(*ShortcutBackend)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(b *ShortcutBackend) {
		b.baseURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient sets a custom http.Client for the backend.
func WithHTTPClient(c *http.Client) Option {
	return func(b *ShortcutBackend) {
		b.httpClient = c
	}
}

// WithWorkflowStateID sets the workflow state ID directly. Use this when you
// already know the numeric ID. See WithWorkflowStateName for name-based lookup.
func WithWorkflowStateID(id int64) Option {
	return func(b *ShortcutBackend) {
		b.workflowStateID = id
	}
}

// WithWorkflowStateName sets the human-readable workflow state name (e.g.
// "Ready for Development"). Init must be called to resolve it to a numeric ID
// before polling.
func WithWorkflowStateName(name string) Option {
	return func(b *ShortcutBackend) {
		b.workflowStateName = name
	}
}

// WithOwnerMentionName sets the Shortcut mention name of the user that stories
// must be assigned to in order to be picked up (e.g. "robodev"). Init must be
// called to resolve it to a member UUID before polling.
func WithOwnerMentionName(name string) Option {
	return func(b *ShortcutBackend) {
		// Strip a leading "@" if the caller included it.
		b.ownerMentionName = strings.TrimPrefix(name, "@")
	}
}

// WithExcludeLabels overrides the default client-side label exclusion list.
// Stories carrying any of these labels are filtered out after fetching.
func WithExcludeLabels(labels []string) Option {
	return func(b *ShortcutBackend) {
		b.excludeLabels = labels
	}
}

// NewShortcutBackend creates a new Shortcut ticketing backend.
//
// workflowStateID may be zero when WithWorkflowStateName is used; Init will
// resolve it. If both are provided, the explicit ID takes precedence.
func NewShortcutBackend(token string, workflowStateID int64, logger *slog.Logger, opts ...Option) *ShortcutBackend {
	b := &ShortcutBackend{
		token:           token,
		baseURL:         defaultBaseURL,
		httpClient:      http.DefaultClient,
		logger:          logger,
		workflowStateID: workflowStateID,
		excludeLabels:   []string{"in-progress", "robodev-failed"},
	}
	for _, opt := range opts {
		opt(b)
	}
	if len(b.excludeLabels) == 0 {
		b.logger.Warn("exclude_labels is empty; in-progress and failed stories will not be filtered out automatically")
	}
	return b
}

// Init resolves human-readable configuration (workflow state name, owner
// mention name) to the numeric / UUID values required by the Shortcut API.
// It must be called once before PollReadyTickets when WithWorkflowStateName
// or WithOwnerMentionName is used.
func (b *ShortcutBackend) Init(ctx context.Context) error {
	if b.workflowStateName != "" && b.workflowStateID == 0 {
		if err := b.resolveWorkflowStateID(ctx); err != nil {
			return fmt.Errorf("resolving workflow state %q: %w", b.workflowStateName, err)
		}
		b.logger.Info("resolved workflow state",
			slog.String("name", b.workflowStateName),
			slog.Int64("id", b.workflowStateID),
		)
	}

	if b.ownerMentionName != "" {
		if err := b.resolveMemberID(ctx); err != nil {
			return fmt.Errorf("resolving owner %q: %w", b.ownerMentionName, err)
		}
		b.logger.Info("resolved owner member",
			slog.String("mention_name", b.ownerMentionName),
			slog.String("member_id", b.ownerMemberID),
		)
	}

	return nil
}

// WorkflowStateID returns the resolved numeric workflow state ID. This is safe
// to call after Init and is used by the webhook server to filter incoming
// story updates to only those transitioning into this state.
func (b *ShortcutBackend) WorkflowStateID() int64 {
	return b.workflowStateID
}

// resolveWorkflowStateID fetches all workflows and finds the state whose name
// matches b.workflowStateName, populating b.workflowStateID.
func (b *ShortcutBackend) resolveWorkflowStateID(ctx context.Context) error {
	body, err := b.doGet(ctx, b.baseURL+"/workflows")
	if err != nil {
		return fmt.Errorf("fetching workflows: %w", err)
	}

	var workflows []scWorkflow
	if err := json.Unmarshal(body, &workflows); err != nil {
		return fmt.Errorf("decoding workflows response: %w", err)
	}

	nameLower := strings.ToLower(b.workflowStateName)
	for _, wf := range workflows {
		for _, state := range wf.States {
			if strings.ToLower(state.Name) == nameLower {
				b.workflowStateID = state.ID
				return nil
			}
		}
	}

	return fmt.Errorf("no workflow state named %q found", b.workflowStateName)
}

// resolveMemberID fetches all members and finds the one whose mention_name
// matches b.ownerMentionName, populating b.ownerMemberID.
func (b *ShortcutBackend) resolveMemberID(ctx context.Context) error {
	body, err := b.doGet(ctx, b.baseURL+"/members")
	if err != nil {
		return fmt.Errorf("fetching members: %w", err)
	}

	var members []scMember
	if err := json.Unmarshal(body, &members); err != nil {
		return fmt.Errorf("decoding members response: %w", err)
	}

	nameLower := strings.ToLower(b.ownerMentionName)
	for _, m := range members {
		if strings.ToLower(m.Profile.MentionName) == nameLower {
			b.ownerMemberID = m.ID
			return nil
		}
	}

	return fmt.Errorf("no member with mention_name %q found", b.ownerMentionName)
}

// searchRequest is the JSON body sent to the Shortcut search endpoint.
type searchRequest struct {
	WorkflowStateID int64    `json:"workflow_state_id"`
	OwnerIDs        []string `json:"owner_ids,omitempty"`
}

// PollReadyTickets searches for stories matching the configured workflow
// state and (optionally) owner.
func (b *ShortcutBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
	if b.workflowStateID == 0 {
		return nil, fmt.Errorf("workflow state ID is not set; call Init first or provide a numeric ID")
	}

	// Build exclusion set for client-side filtering.
	excludeSet := make(map[string]struct{}, len(b.excludeLabels))
	for _, l := range b.excludeLabels {
		excludeSet[l] = struct{}{}
	}

	sr := searchRequest{WorkflowStateID: b.workflowStateID}
	if b.ownerMemberID != "" {
		sr.OwnerIDs = []string{b.ownerMemberID}
	}

	body, err := b.doPost(ctx, b.baseURL+"/stories/search", sr)
	if err != nil {
		return nil, fmt.Errorf("polling ready tickets: %w", err)
	}

	var stories []scStory
	if err := json.Unmarshal(body, &stories); err != nil {
		return nil, fmt.Errorf("decoding stories response: %w", err)
	}

	var tickets []ticketing.Ticket
	for _, story := range stories {
		if hasExcludedLabel(story.Labels, excludeSet) {
			continue
		}

		labels := make([]string, 0, len(story.Labels))
		for _, l := range story.Labels {
			labels = append(labels, l.Name)
		}

		tickets = append(tickets, ticketing.Ticket{
			ID:          strconv.Itoa(story.ID),
			Title:       story.Name,
			Description: story.Description,
			TicketType:  "story",
			Labels:      labels,
			ExternalURL: story.AppURL,
		})
	}

	b.logger.Info("polled ready tickets", "count", len(tickets))
	return tickets, nil
}

// hasExcludedLabel returns true if any of the story's labels appear in the
// exclusion set.
func hasExcludedLabel(storyLabels []scLabel, excludeSet map[string]struct{}) bool {
	for _, l := range storyLabels {
		if _, ok := excludeSet[l.Name]; ok {
			return true
		}
	}
	return false
}

// MarkInProgress adds an "in-progress" label to the story.
func (b *ShortcutBackend) MarkInProgress(ctx context.Context, ticketID string) error {
	if err := b.addLabel(ctx, ticketID, "in-progress"); err != nil {
		return fmt.Errorf("adding in-progress label: %w", err)
	}
	return nil
}

// MarkComplete posts a summary comment and marks the story as completed.
func (b *ShortcutBackend) MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error {
	comment := fmt.Sprintf("Task completed successfully.\n\n**Summary:** %s", result.Summary)
	if result.MergeRequestURL != "" {
		comment += fmt.Sprintf("\n**Merge Request:** %s", result.MergeRequestURL)
	}
	if err := b.AddComment(ctx, ticketID, comment); err != nil {
		return fmt.Errorf("adding completion comment: %w", err)
	}

	// Mark the story as completed.
	url := fmt.Sprintf("%s/stories/%s", b.baseURL, ticketID)
	payload := map[string]bool{"completed": true}
	if err := b.doPut(ctx, url, payload); err != nil {
		return fmt.Errorf("marking story completed: %w", err)
	}
	return nil
}

// MarkFailed adds a "robodev-failed" label and posts the failure reason
// as a comment.
func (b *ShortcutBackend) MarkFailed(ctx context.Context, ticketID string, reason string) error {
	if err := b.addLabel(ctx, ticketID, "robodev-failed"); err != nil {
		return fmt.Errorf("adding failed label: %w", err)
	}
	comment := fmt.Sprintf("Task failed.\n\n**Reason:** %s", reason)
	if err := b.AddComment(ctx, ticketID, comment); err != nil {
		return fmt.Errorf("adding failure comment: %w", err)
	}
	return nil
}

// AddComment posts a comment on the given story.
func (b *ShortcutBackend) AddComment(ctx context.Context, ticketID string, comment string) error {
	url := fmt.Sprintf("%s/stories/%s/comments", b.baseURL, ticketID)
	payload := map[string]string{"text": comment}
	if _, err := b.doPost(ctx, url, payload); err != nil {
		return fmt.Errorf("adding comment to ticket %s: %w", ticketID, err)
	}
	return nil
}

// Name returns the backend identifier.
func (b *ShortcutBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the ticketing interface version implemented.
func (b *ShortcutBackend) InterfaceVersion() int {
	return ticketing.InterfaceVersion
}

// addLabel adds a single label to a story.
func (b *ShortcutBackend) addLabel(ctx context.Context, ticketID string, label string) error {
	url := fmt.Sprintf("%s/stories/%s/labels", b.baseURL, ticketID)
	payload := map[string]string{"name": label}
	if _, err := b.doPost(ctx, url, payload); err != nil {
		return fmt.Errorf("adding label %q to story %s: %w", label, ticketID, err)
	}
	return nil
}

// doGet performs a GET request and returns the response body.
func (b *ShortcutBackend) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	b.setAuthHeaders(req)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	return body, nil
}

// doPost performs a POST request with a JSON body and returns the response body.
func (b *ShortcutBackend) doPost(ctx context.Context, url string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	return respBody, nil
}

// doPut performs a PUT request with a JSON body.
func (b *ShortcutBackend) doPut(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	b.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// setAuthHeaders adds the Shortcut authorisation header to a request.
func (b *ShortcutBackend) setAuthHeaders(req *http.Request) {
	req.Header.Set("Shortcut-Token", b.token)
	req.Header.Set("Content-Type", "application/json")
}
