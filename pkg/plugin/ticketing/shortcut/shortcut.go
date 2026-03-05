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
	"net/url"
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
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	AppURL          string    `json:"app_url"`
	Labels          []scLabel `json:"labels"`
	ExternalLinks   []string  `json:"external_links"`
	WorkflowStateID int64     `json:"workflow_state_id"`
}

// scLabel is the subset of a Shortcut label response we parse.
type scLabel struct {
	Name string `json:"name"`
}

// scWorkflow is the subset of a Shortcut workflow response we parse.
type scWorkflow struct {
	ID     int64             `json:"id"`
	Name   string            `json:"name"`
	States []scWorkflowState `json:"states"`
}

// scWorkflowState represents a single state within a Shortcut workflow.
type scWorkflowState struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "unstarted", "started", or "done"
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

// WorkflowMapping pairs a trigger state with an in-progress state for a single
// Shortcut workflow. When PollReadyTickets finds a story in TriggerState,
// MarkInProgress will transition it to InProgressState within its own workflow.
type WorkflowMapping struct {
	// TriggerState is the human-readable name of the state that signals a story
	// is ready for RoboDev to pick up, e.g. "Ready for Development".
	TriggerState string
	// InProgressState is the human-readable name of the state that the story
	// should be transitioned to once RoboDev begins work, e.g. "In Development".
	InProgressState string
	// triggerStateID is the resolved numeric Shortcut state ID. It is
	// populated by Init and must not be set by callers.
	triggerStateID int64
}

// ShortcutBackend implements ticketing.Backend by talking to the Shortcut
// REST API.
type ShortcutBackend struct {
	token               string
	baseURL             string
	httpClient          *http.Client
	logger              *slog.Logger
	workflowStateID     int64
	workflowStateName   string            // human-readable name; resolved to workflowStateID by Init
	inProgressStateName string            // e.g. "In Development"; resolved per-story at runtime
	workflowMappings    []WorkflowMapping // multi-workflow support; synthesised from legacy fields by Init
	ownerMentionName    string            // mention name (e.g. "robodev"); resolved to ownerMemberID by Init
	ownerMemberID       string            // resolved Shortcut member UUID for owner filtering
	excludeLabels       []string
	workflows           []scWorkflow // cached at Init; used for per-story state lookups
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

// WithInProgressStateName sets the human-readable workflow state name that
// stories are moved into when RoboDev picks them up (e.g. "In Development").
// Init must be called to resolve it to a numeric ID. When set, MarkInProgress
// transitions the story's state rather than adding a label, which provides
// cleaner visibility in the Shortcut board.
func WithInProgressStateName(name string) Option {
	return func(b *ShortcutBackend) {
		b.inProgressStateName = name
	}
}

// WithWorkflowMappings configures multiple workflow mappings on the backend.
// Each mapping pairs a trigger state name with an in-progress state name for a
// single Shortcut workflow. When this option is used it supersedes
// WithWorkflowStateName and WithInProgressStateName; those legacy options are
// ignored for the purposes of polling and state transitions.
//
// Init must be called after applying this option so that each mapping's
// TriggerState name is resolved to its numeric Shortcut state ID.
func WithWorkflowMappings(mappings []WorkflowMapping) Option {
	return func(b *ShortcutBackend) {
		b.workflowMappings = mappings
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

// Init resolves human-readable configuration (workflow state names, owner
// mention name) to the numeric / UUID values required by the Shortcut API.
// Workflows are cached so that MarkInProgress and MarkComplete can look up
// the correct target state for each story's actual workflow at runtime.
// It must be called once before PollReadyTickets.
//
// When WorkflowMappings are configured, each mapping's TriggerState name is
// resolved to a numeric ID. When no mappings are configured but the legacy
// workflowStateName / inProgressStateName fields are set, Init synthesises a
// single mapping so that the rest of the code only deals with workflowMappings.
func (b *ShortcutBackend) Init(ctx context.Context) error {
	// Always fetch and cache workflows — needed both for state name resolution
	// and for per-story runtime lookups in MarkInProgress/MarkComplete.
	var err error
	b.workflows, err = b.fetchWorkflows(ctx)
	if err != nil {
		return fmt.Errorf("fetching workflows: %w", err)
	}

	if len(b.workflowMappings) > 0 {
		// Resolve the numeric trigger state ID for every mapping.
		for i, m := range b.workflowMappings {
			id, err := findStateID(b.workflows, m.TriggerState)
			if err != nil {
				return fmt.Errorf("resolving trigger state for mapping %d (%q): %w", i, m.TriggerState, err)
			}
			b.workflowMappings[i].triggerStateID = id
			b.logger.Info("resolved trigger workflow state for mapping",
				slog.Int("mapping_index", i),
				slog.String("trigger_state", m.TriggerState),
				slog.Int64("id", id),
				slog.String("in_progress_state", m.InProgressState),
			)
		}
	} else {
		// Legacy single-state path: resolve the workflow state name if needed,
		// then synthesise a single WorkflowMapping so the rest of the code only
		// needs to deal with workflowMappings.
		if b.workflowStateName != "" && b.workflowStateID == 0 {
			id, err := findStateID(b.workflows, b.workflowStateName)
			if err != nil {
				return fmt.Errorf("resolving trigger state: %w", err)
			}
			b.workflowStateID = id
			b.logger.Info("resolved trigger workflow state",
				slog.String("name", b.workflowStateName),
				slog.Int64("id", b.workflowStateID),
			)
		}

		if b.inProgressStateName != "" {
			// Log the configured name; actual resolution happens per-story at
			// runtime so that stories in any workflow are handled correctly.
			b.logger.Info("in-progress state will be resolved per story",
				slog.String("name", b.inProgressStateName),
			)
		}

		// Synthesise a single mapping from the legacy fields so that
		// PollReadyTickets and MarkInProgress have a uniform code path.
		if b.workflowStateID != 0 || b.workflowStateName != "" {
			b.workflowMappings = []WorkflowMapping{
				{
					TriggerState:    b.workflowStateName,
					InProgressState: b.inProgressStateName,
					triggerStateID:  b.workflowStateID,
				},
			}
		}
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

// InProgressStateID returns zero; state resolution is now done per-story at
// runtime to support stories across multiple workflows.
func (b *ShortcutBackend) InProgressStateID() int64 {
	return 0
}

// WorkflowStateID returns the resolved numeric workflow state ID for the first
// configured mapping. This is used by the webhook server to filter incoming
// story updates to only those transitioning into a trigger state. When multiple
// mappings are configured, callers that need all trigger state IDs should
// iterate WorkflowMappings directly.
func (b *ShortcutBackend) WorkflowStateID() int64 {
	if len(b.workflowMappings) > 0 {
		return b.workflowMappings[0].triggerStateID
	}
	return b.workflowStateID
}

// WorkflowMappings returns the resolved workflow mappings configured on this
// backend. The slice is nil until Init has been called.
func (b *ShortcutBackend) WorkflowMappings() []WorkflowMapping {
	return b.workflowMappings
}

// WorkflowState is a resolved Shortcut workflow state with its workflow name
// for display purposes. It is returned by ListWorkflowStates.
type WorkflowState struct {
	ID           int64
	Name         string
	WorkflowName string
}

// ListWorkflowStates fetches all workflows and returns a flat list of states
// across all workflows. Use this to discover state names for
// workflow_state_name and in_progress_state_name configuration.
func (b *ShortcutBackend) ListWorkflowStates(ctx context.Context) ([]WorkflowState, error) {
	workflows, err := b.fetchWorkflows(ctx)
	if err != nil {
		return nil, err
	}

	var states []WorkflowState
	for _, wf := range workflows {
		for _, s := range wf.States {
			states = append(states, WorkflowState{
				ID:           s.ID,
				Name:         s.Name,
				WorkflowName: wf.Name,
			})
		}
	}
	return states, nil
}

// fetchWorkflows retrieves all Shortcut workflows from the API.
func (b *ShortcutBackend) fetchWorkflows(ctx context.Context) ([]scWorkflow, error) {
	body, err := b.doGet(ctx, b.baseURL+"/workflows")
	if err != nil {
		return nil, fmt.Errorf("fetching workflows: %w", err)
	}

	var workflows []scWorkflow
	if err := json.Unmarshal(body, &workflows); err != nil {
		return nil, fmt.Errorf("decoding workflows response: %w", err)
	}
	return workflows, nil
}

// findStateID searches workflows for a state matching name (case-insensitive)
// and returns its numeric ID. When not found, the error message lists all
// available state names to help with configuration.
func findStateID(workflows []scWorkflow, name string) (int64, error) {
	nameLower := strings.ToLower(name)
	var available []string
	for _, wf := range workflows {
		for _, state := range wf.States {
			if strings.ToLower(state.Name) == nameLower {
				return state.ID, nil
			}
			available = append(available, fmt.Sprintf("%q (workflow: %s)", state.Name, wf.Name))
		}
	}
	return 0, fmt.Errorf("no workflow state named %q found; available states: %s",
		name, strings.Join(available, ", "))
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

// searchResponse is the wrapper returned by the Shortcut search API.
type searchResponse struct {
	Data []scStory `json:"data"`
}

// pollQuery executes a single Shortcut search query and returns the raw stories
// from the response. It is a helper used by PollReadyTickets.
func (b *ShortcutBackend) pollQuery(ctx context.Context, stateName string) ([]scStory, error) {
	query := fmt.Sprintf(`state:"%s"`, stateName)
	if b.ownerMentionName != "" {
		query += fmt.Sprintf(` owner:%s`, b.ownerMentionName)
	}

	body, err := b.doGet(ctx, b.baseURL+"/search/stories?query="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}

	var result searchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decoding stories response: %w", err)
	}
	return result.Data, nil
}

// PollReadyTickets searches for stories matching every configured workflow
// mapping's trigger state, merges the results, deduplicates by story ID, and
// applies exclusion label filtering. If some—but not all—queries fail, the
// failures are logged as warnings and the partial results are returned. An
// error is returned only when every query fails.
func (b *ShortcutBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
	if len(b.workflowMappings) == 0 && b.workflowStateName == "" && b.workflowStateID == 0 {
		return nil, fmt.Errorf("workflow state is not set; call Init first or provide a numeric ID")
	}

	// Build exclusion set for client-side filtering.
	excludeSet := make(map[string]struct{}, len(b.excludeLabels))
	for _, l := range b.excludeLabels {
		excludeSet[l] = struct{}{}
	}

	// Collect the state names to query. workflowMappings is always populated
	// after Init (either directly or synthesised from the legacy fields), but we
	// also handle the case where Init has not been called and a numeric ID was
	// provided directly via the constructor.
	type querySpec struct {
		stateName string
	}
	var queries []querySpec
	if len(b.workflowMappings) > 0 {
		for _, m := range b.workflowMappings {
			// Prefer the human-readable name; fall back to the numeric ID.
			name := m.TriggerState
			if name == "" {
				name = strconv.FormatInt(m.triggerStateID, 10)
			}
			queries = append(queries, querySpec{stateName: name})
		}
	} else {
		// Pre-Init fallback: use the raw legacy fields.
		name := b.workflowStateName
		if name == "" {
			name = strconv.FormatInt(b.workflowStateID, 10)
		}
		queries = append(queries, querySpec{stateName: name})
	}

	// Execute one query per trigger state. Tolerate individual failures but
	// return an error if every query fails.
	seen := make(map[int]struct{})
	var allStories []scStory
	var lastErr error
	failCount := 0

	for _, q := range queries {
		stories, err := b.pollQuery(ctx, q.stateName)
		if err != nil {
			b.logger.Warn("failed to poll trigger state",
				slog.String("state", q.stateName),
				slog.String("error", err.Error()),
			)
			lastErr = err
			failCount++
			continue
		}
		for _, story := range stories {
			if _, dup := seen[story.ID]; dup {
				continue
			}
			seen[story.ID] = struct{}{}
			allStories = append(allStories, story)
		}
	}

	if failCount == len(queries) {
		return nil, fmt.Errorf("polling ready tickets: %w", lastErr)
	}

	var tickets []ticketing.Ticket
	for _, story := range allStories {
		if hasExcludedLabel(story.Labels, excludeSet) {
			continue
		}

		labels := make([]string, 0, len(story.Labels))
		for _, l := range story.Labels {
			labels = append(labels, l.Name)
		}

		var repoURL string
		if len(story.ExternalLinks) > 0 {
			repoURL = story.ExternalLinks[0]
		}

		tickets = append(tickets, ticketing.Ticket{
			ID:          strconv.Itoa(story.ID),
			Title:       story.Name,
			Description: story.Description,
			TicketType:  "story",
			Labels:      labels,
			ExternalURL: story.AppURL,
			RepoURL:     repoURL,
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

// storyWorkflow fetches the story and returns the workflow it currently belongs
// to by matching its workflow_state_id against the cached workflows.
func (b *ShortcutBackend) storyWorkflow(ctx context.Context, ticketID string) (*scWorkflow, error) {
	body, err := b.doGet(ctx, fmt.Sprintf("%s/stories/%s", b.baseURL, ticketID))
	if err != nil {
		return nil, fmt.Errorf("fetching story %s: %w", ticketID, err)
	}
	var story scStory
	if err := json.Unmarshal(body, &story); err != nil {
		return nil, fmt.Errorf("decoding story %s: %w", ticketID, err)
	}
	for i := range b.workflows {
		for _, s := range b.workflows[i].States {
			if s.ID == story.WorkflowStateID {
				return &b.workflows[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no cached workflow contains state ID %d (story %s)", story.WorkflowStateID, ticketID)
}

// findStateInWorkflow returns the ID of the first state in wf whose name
// matches (case-insensitive). Returns an error listing available states if
// not found.
func findStateInWorkflow(wf *scWorkflow, name string) (int64, error) {
	nameLower := strings.ToLower(name)
	var available []string
	for _, s := range wf.States {
		if strings.ToLower(s.Name) == nameLower {
			return s.ID, nil
		}
		available = append(available, fmt.Sprintf("%q", s.Name))
	}
	return 0, fmt.Errorf("no state named %q in workflow %q; available: %s",
		name, wf.Name, strings.Join(available, ", "))
}

// findDoneStateInWorkflow returns the ID of the first state of type "done" in wf.
func findDoneStateInWorkflow(wf *scWorkflow) (int64, error) {
	for _, s := range wf.States {
		if s.Type == "done" {
			return s.ID, nil
		}
	}
	return 0, fmt.Errorf("no done-type state found in workflow %q", wf.Name)
}

// resolveInProgressStateName returns the in-progress state name to use for the
// given story. It fetches the story to find its current workflow_state_id, then
// matches that against the configured WorkflowMappings to pick the right
// InProgressState. If no mapping matches (the story may have already moved), it
// falls back to the first mapping's InProgressState with a warning. Returns an
// empty string when no mappings have an InProgressState configured.
func (b *ShortcutBackend) resolveInProgressStateName(ctx context.Context, ticketID string) (string, error) {
	if len(b.workflowMappings) == 0 {
		return b.inProgressStateName, nil
	}

	// Fetch the story to determine its current trigger state.
	body, err := b.doGet(ctx, fmt.Sprintf("%s/stories/%s", b.baseURL, ticketID))
	if err != nil {
		return "", fmt.Errorf("fetching story %s: %w", ticketID, err)
	}
	var story scStory
	if err := json.Unmarshal(body, &story); err != nil {
		return "", fmt.Errorf("decoding story %s: %w", ticketID, err)
	}

	// Find the mapping whose triggerStateID matches the story's current state.
	for _, m := range b.workflowMappings {
		if m.triggerStateID != 0 && m.triggerStateID == story.WorkflowStateID {
			return m.InProgressState, nil
		}
	}

	// No exact match — the story may have already advanced. Fall back to the
	// first mapping's InProgressState.
	fallback := b.workflowMappings[0].InProgressState
	b.logger.Warn("story trigger state does not match any mapping; using first mapping as fallback",
		slog.String("ticket_id", ticketID),
		slog.Int64("current_state_id", story.WorkflowStateID),
		slog.String("fallback_in_progress_state", fallback),
	)
	return fallback, nil
}

// MarkInProgress signals that RoboDev has started working on the story. It
// posts a start comment for visibility, then transitions the story to the
// in-progress state determined by the matching WorkflowMapping. When multiple
// mappings are configured the correct mapping is selected by comparing the
// story's current workflow_state_id to each mapping's triggerStateID.
func (b *ShortcutBackend) MarkInProgress(ctx context.Context, ticketID string) error {
	// Post a start comment so humans can see progress on the Shortcut board.
	startComment := "🤖 RoboDev has picked up this story and is working on it. A pull request will be opened when the task is complete."
	if err := b.AddComment(ctx, ticketID, startComment); err != nil {
		// Non-fatal: log and continue — the agent should not be blocked by a
		// comment failure.
		b.logger.Warn("failed to post start comment on story",
			slog.String("ticket_id", ticketID),
			slog.String("error", err.Error()),
		)
	}

	// Determine which in-progress state name to use for this story.
	inProgressStateName, err := b.resolveInProgressStateName(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("resolving in-progress state for story %s: %w", ticketID, err)
	}

	if inProgressStateName == "" {
		// Fallback: add label when no in-progress state name is configured.
		if err := b.addLabel(ctx, ticketID, "in-progress"); err != nil {
			return fmt.Errorf("adding in-progress label: %w", err)
		}
		return nil
	}

	// Resolve the in-progress state within the story's actual workflow so that
	// stories from any workflow are handled correctly.
	wf, err := b.storyWorkflow(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("resolving workflow for story %s: %w", ticketID, err)
	}
	stateID, err := findStateInWorkflow(wf, inProgressStateName)
	if err != nil {
		return fmt.Errorf("finding in-progress state in workflow %q: %w", wf.Name, err)
	}

	storyURL := fmt.Sprintf("%s/stories/%s", b.baseURL, ticketID)
	if err := b.doPut(ctx, storyURL, map[string]int64{"workflow_state_id": stateID}); err != nil {
		return fmt.Errorf("transitioning story %s to in-progress state: %w", ticketID, err)
	}
	return nil
}

// MarkComplete posts a summary comment and transitions the story to the first
// done-type state in its workflow.
func (b *ShortcutBackend) MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error {
	comment := fmt.Sprintf("✅ Task completed successfully.\n\n**Summary:** %s", result.Summary)
	if result.MergeRequestURL != "" {
		comment += fmt.Sprintf("\n**Merge Request:** %s", result.MergeRequestURL)
	}
	if err := b.AddComment(ctx, ticketID, comment); err != nil {
		return fmt.Errorf("adding completion comment: %w", err)
	}

	wf, err := b.storyWorkflow(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("resolving workflow for story %s: %w", ticketID, err)
	}
	doneStateID, err := findDoneStateInWorkflow(wf)
	if err != nil {
		return fmt.Errorf("finding done state in workflow %q: %w", wf.Name, err)
	}

	storyURL := fmt.Sprintf("%s/stories/%s", b.baseURL, ticketID)
	if err := b.doPut(ctx, storyURL, map[string]int64{"workflow_state_id": doneStateID}); err != nil {
		return fmt.Errorf("marking story %s as done: %w", ticketID, err)
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
