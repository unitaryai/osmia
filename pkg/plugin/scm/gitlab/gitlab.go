// Package gitlab provides a built-in scm.Backend implementation that
// integrates with GitLab repositories via the REST API. It uses net/http
// directly to minimise external dependencies.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

const (
	defaultBaseURL = "https://gitlab.com/api/v4"
	backendName    = "gitlab"
)

// Compile-time check that GitLabSCMBackend implements scm.Backend.
var _ scm.Backend = (*GitLabSCMBackend)(nil)

// GitLabSCMBackend implements scm.Backend using the GitLab REST API.
type GitLabSCMBackend struct {
	token   string
	baseURL string
	client  *http.Client
	logger  *slog.Logger
}

// Option is a functional option for configuring a GitLabSCMBackend.
type Option func(*GitLabSCMBackend)

// WithBaseURL sets a custom API base URL (e.g. for self-managed GitLab).
func WithBaseURL(u string) Option {
	return func(b *GitLabSCMBackend) {
		b.baseURL = strings.TrimRight(u, "/")
	}
}

// WithHTTPClient sets a custom http.Client for the backend.
func WithHTTPClient(c *http.Client) Option {
	return func(b *GitLabSCMBackend) {
		b.client = c
	}
}

// NewGitLabSCMBackend creates a new GitLab SCM backend.
func NewGitLabSCMBackend(token string, logger *slog.Logger, opts ...Option) *GitLabSCMBackend {
	b := &GitLabSCMBackend{
		token:   token,
		baseURL: defaultBaseURL,
		client:  http.DefaultClient,
		logger:  logger,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// glBranch is the subset of the GitLab branch response we need.
type glBranch struct {
	Name   string   `json:"name"`
	Commit glCommit `json:"commit"`
}

// glCommit is the subset of a GitLab commit object we need.
type glCommit struct {
	ID string `json:"id"`
}

// glMR is the subset of the GitLab merge request response we parse.
type glMR struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"` // "opened", "closed", "merged"
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}

// CreateBranch creates a new branch in the repository from the specified
// base branch. If baseBranch is empty, the default branch is used.
func (b *GitLabSCMBackend) CreateBranch(ctx context.Context, repoURL string, branchName string, baseBranch string) error {
	projectPath, err := parseProjectPath(repoURL)
	if err != nil {
		return fmt.Errorf("parsing repository URL: %w", err)
	}

	if baseBranch == "" {
		baseBranch = "main"
	}

	encodedPath := url.PathEscape(projectPath)
	createURL := fmt.Sprintf("%s/projects/%s/repository/branches", b.baseURL, encodedPath)

	payload := map[string]string{
		"branch": branchName,
		"ref":    baseBranch,
	}

	if err := b.doPost(ctx, createURL, payload); err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}

	b.logger.Info("branch created", "project", projectPath, "branch", branchName, "base", baseBranch)
	return nil
}

// CreatePullRequest creates a new merge request and returns the MR details.
func (b *GitLabSCMBackend) CreatePullRequest(ctx context.Context, input scm.CreatePullRequestInput) (*scm.PullRequest, error) {
	projectPath, err := parseProjectPath(input.RepoURL)
	if err != nil {
		return nil, fmt.Errorf("parsing repository URL: %w", err)
	}

	encodedPath := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/projects/%s/merge_requests", b.baseURL, encodedPath)

	payload := map[string]string{
		"title":         input.Title,
		"description":   input.Description,
		"source_branch": input.BranchName,
		"target_branch": input.BaseBranch,
	}

	respBody, err := b.doPostWithResponse(ctx, apiURL, payload)
	if err != nil {
		return nil, fmt.Errorf("creating merge request: %w", err)
	}
	defer respBody.Close()

	var mr glMR
	if err := json.NewDecoder(respBody).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decoding merge request response: %w", err)
	}

	result := prFromMR(mr)
	b.logger.Info("merge request created", "project", projectPath, "iid", mr.IID)
	return result, nil
}

// GetPullRequestStatus retrieves the current status of a merge request by URL.
func (b *GitLabSCMBackend) GetPullRequestStatus(ctx context.Context, prURL string) (*scm.PullRequest, error) {
	projectPath, mrIID, err := parseMRURL(prURL)
	if err != nil {
		return nil, fmt.Errorf("parsing merge request URL: %w", err)
	}

	encodedPath := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/projects/%s/merge_requests/%d", b.baseURL, encodedPath, mrIID)

	body, err := b.doGet(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetching merge request: %w", err)
	}
	defer body.Close()

	var mr glMR
	if err := json.NewDecoder(body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decoding merge request: %w", err)
	}

	return prFromMR(mr), nil
}

// Name returns the backend identifier.
func (b *GitLabSCMBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the SCM interface version implemented.
func (b *GitLabSCMBackend) InterfaceVersion() int {
	return scm.InterfaceVersion
}

// glNote is the subset of a GitLab MR note (comment) we parse.
type glNote struct {
	ID     int  `json:"id"`
	System bool `json:"system"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	Body         string      `json:"body"`
	Position     *glPosition `json:"position,omitempty"`
	DiscussionID string      `json:"discussion_id,omitempty"`
	CreatedAt    string      `json:"created_at"`
}

// glPosition holds the file position for a line-level note.
type glPosition struct {
	NewPath string `json:"new_path"`
	NewLine int    `json:"new_line"`
}

// ListReviewComments returns all non-system notes on the merge request,
// sorted ascending by creation time.
func (b *GitLabSCMBackend) ListReviewComments(ctx context.Context, prURL string) ([]scm.ReviewComment, error) {
	projectPath, mrIID, err := parseMRURL(prURL)
	if err != nil {
		return nil, fmt.Errorf("parsing merge request URL: %w", err)
	}

	encodedPath := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes?sort=asc&order_by=created_at",
		b.baseURL, encodedPath, mrIID)

	body, err := b.doGet(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetching merge request notes: %w", err)
	}
	defer body.Close()

	var notes []glNote
	if err := json.NewDecoder(body).Decode(&notes); err != nil {
		return nil, fmt.Errorf("decoding merge request notes: %w", err)
	}

	var comments []scm.ReviewComment
	for _, n := range notes {
		if n.System {
			continue
		}
		created, _ := time.Parse(time.RFC3339, n.CreatedAt)
		rc := scm.ReviewComment{
			ID:       strconv.Itoa(n.ID),
			ThreadID: n.DiscussionID,
			Author:   n.Author.Username,
			Body:     n.Body,
			Created:  created,
		}
		if n.Position != nil {
			rc.FilePath = n.Position.NewPath
			rc.Line = n.Position.NewLine
		}
		comments = append(comments, rc)
	}

	return comments, nil
}

// ReplyToComment posts a reply to an existing comment on the merge request.
// When threadID is non-empty the reply is added to the discussion; otherwise
// a standalone note is posted.
func (b *GitLabSCMBackend) ReplyToComment(ctx context.Context, prURL string, commentID string, body string) error {
	projectPath, mrIID, err := parseMRURL(prURL)
	if err != nil {
		return fmt.Errorf("parsing merge request URL: %w", err)
	}

	encodedPath := url.PathEscape(projectPath)

	// Look up the discussion ID for this comment by fetching the notes list.
	// We need the discussion_id to post a reply to the correct thread.
	notesURL := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes?sort=asc&order_by=created_at",
		b.baseURL, encodedPath, mrIID)
	notesBody, err := b.doGet(ctx, notesURL)
	if err != nil {
		return fmt.Errorf("fetching notes to locate discussion: %w", err)
	}
	defer notesBody.Close()

	var notes []glNote
	if err := json.NewDecoder(notesBody).Decode(&notes); err != nil {
		return fmt.Errorf("decoding notes: %w", err)
	}

	threadID := ""
	for _, n := range notes {
		if strconv.Itoa(n.ID) == commentID {
			threadID = n.DiscussionID
			break
		}
	}

	if threadID != "" {
		apiURL := fmt.Sprintf("%s/projects/%s/merge_requests/%d/discussions/%s/notes",
			b.baseURL, encodedPath, mrIID, threadID)
		return b.doPost(ctx, apiURL, map[string]string{"body": body})
	}

	// Fall back to posting a standalone note.
	apiURL := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes", b.baseURL, encodedPath, mrIID)
	return b.doPost(ctx, apiURL, map[string]string{"body": body})
}

// ResolveThread marks a GitLab merge request discussion thread as resolved.
func (b *GitLabSCMBackend) ResolveThread(ctx context.Context, prURL string, threadID string) error {
	projectPath, mrIID, err := parseMRURL(prURL)
	if err != nil {
		return fmt.Errorf("parsing merge request URL: %w", err)
	}

	encodedPath := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/projects/%s/merge_requests/%d/discussions/%s",
		b.baseURL, encodedPath, mrIID, threadID)

	return b.doPut(ctx, apiURL, map[string]bool{"resolved": true})
}

// GetDiff returns the unified diff between baseBranch and branchName using
// the GitLab compare API. When baseBranch is empty, the compare endpoint
// uses the project's default branch.
func (b *GitLabSCMBackend) GetDiff(ctx context.Context, repoURL string, baseBranch string, branchName string) (string, error) {
	projectPath, err := parseProjectPath(repoURL)
	if err != nil {
		return "", fmt.Errorf("parsing repository URL: %w", err)
	}

	encodedPath := url.PathEscape(projectPath)

	// When baseBranch is empty, fetch the project's default branch first.
	if baseBranch == "" {
		defaultBranch, dbErr := b.getDefaultBranch(ctx, encodedPath)
		if dbErr != nil {
			return "", fmt.Errorf("fetching default branch: %w", dbErr)
		}
		baseBranch = defaultBranch
	}

	compareURL := fmt.Sprintf("%s/projects/%s/repository/compare?from=%s&to=%s",
		b.baseURL, encodedPath, url.QueryEscape(baseBranch), url.QueryEscape(branchName))

	body, err := b.doGet(ctx, compareURL)
	if err != nil {
		return "", fmt.Errorf("fetching compare: %w", err)
	}
	defer body.Close()

	var result struct {
		Diffs []struct {
			OldPath string `json:"old_path"`
			NewPath string `json:"new_path"`
			Diff    string `json:"diff"`
		} `json:"diffs"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding compare response: %w", err)
	}

	var buf strings.Builder
	for _, d := range result.Diffs {
		fmt.Fprintf(&buf, "--- a/%s\n+++ b/%s\n%s\n", d.OldPath, d.NewPath, d.Diff)
	}

	return buf.String(), nil
}

// getDefaultBranch fetches the project's default branch name from the GitLab API.
func (b *GitLabSCMBackend) getDefaultBranch(ctx context.Context, encodedPath string) (string, error) {
	apiURL := fmt.Sprintf("%s/projects/%s", b.baseURL, encodedPath)
	body, err := b.doGet(ctx, apiURL)
	if err != nil {
		return "", fmt.Errorf("fetching project: %w", err)
	}
	defer body.Close()

	var project struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(body).Decode(&project); err != nil {
		return "", fmt.Errorf("decoding project response: %w", err)
	}
	if project.DefaultBranch == "" {
		return "main", nil
	}
	return project.DefaultBranch, nil
}

// doPut performs a PUT request with a JSON body, discarding the response.
func (b *GitLabSCMBackend) doPut(ctx context.Context, u string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	b.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// prFromMR converts a GitLab MR response to the scm.PullRequest type.
func prFromMR(mr glMR) *scm.PullRequest {
	state := mr.State
	// GitLab uses "opened" whilst scm.PullRequest uses "open".
	if state == "opened" {
		state = "open"
	}
	return &scm.PullRequest{
		ID:          strconv.Itoa(mr.IID),
		Number:      mr.IID,
		Title:       mr.Title,
		Description: mr.Description,
		URL:         mr.WebURL,
		BranchName:  mr.SourceBranch,
		BaseBranch:  mr.TargetBranch,
		State:       state,
	}
}

// httpsRepoPattern matches https://gitlab.com/group/project or
// https://gitlab.com/group/subgroup/project style URLs.
var httpsRepoPattern = regexp.MustCompile(`^https?://[^/]+/(.+?)(?:\.git)?$`)

// sshRepoPattern matches git@gitlab.com:group/project.git style URLs.
var sshRepoPattern = regexp.MustCompile(`^git@[^:]+:(.+?)(?:\.git)?$`)

// parseProjectPath extracts the project path (e.g. "group/project" or
// "group/subgroup/project") from a GitLab repository URL.
func parseProjectPath(repoURL string) (string, error) {
	if m := httpsRepoPattern.FindStringSubmatch(repoURL); m != nil {
		path := strings.TrimRight(m[1], "/")
		if path == "" {
			return "", fmt.Errorf("empty project path in URL: %s", repoURL)
		}
		return path, nil
	}
	if m := sshRepoPattern.FindStringSubmatch(repoURL); m != nil {
		path := strings.TrimRight(m[1], "/")
		if path == "" {
			return "", fmt.Errorf("empty project path in URL: %s", repoURL)
		}
		return path, nil
	}
	return "", fmt.Errorf("unsupported repository URL format: %s", repoURL)
}

// mrURLPattern matches GitLab merge request URLs.
// It captures the project path and the MR IID.
var mrURLPattern = regexp.MustCompile(`^https?://[^/]+/(.+?)/-/merge_requests/(\d+)`)

// parseMRURL extracts the project path and MR IID from a GitLab merge request URL.
func parseMRURL(mrURL string) (string, int, error) {
	m := mrURLPattern.FindStringSubmatch(mrURL)
	if m == nil {
		return "", 0, fmt.Errorf("unsupported merge request URL format: %s", mrURL)
	}
	iid, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, fmt.Errorf("invalid merge request IID: %w", err)
	}
	return m[1], iid, nil
}

// doGet performs a GET request and returns the response body.
// It validates that the response is JSON before returning — HTML responses
// (e.g. from auth redirects to a sign-in page) are rejected with a clear error.
func (b *GitLabSCMBackend) doGet(ctx context.Context, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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

	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected content-type %q (expected application/json) — the token may lack access to this resource", ct)
	}

	return resp.Body, nil
}

// doPost performs a POST request with a JSON body, discarding the response.
func (b *GitLabSCMBackend) doPost(ctx context.Context, u string, payload any) error {
	body, err := b.doPostWithResponse(ctx, u, payload)
	if err != nil {
		return err
	}
	body.Close()
	return nil
}

// doPostWithResponse performs a POST request and returns the response body.
func (b *GitLabSCMBackend) doPostWithResponse(ctx context.Context, u string, payload any) (io.ReadCloser, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	b.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// setAuthHeaders adds the authorisation headers to a request.
func (b *GitLabSCMBackend) setAuthHeaders(req *http.Request) {
	req.Header.Set("PRIVATE-TOKEN", b.token)
	req.Header.Set("Content-Type", "application/json")
}
