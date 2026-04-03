// Package github provides a built-in scm.Backend implementation that
// integrates with GitHub repositories via the REST API. It uses net/http
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

const (
	defaultBaseURL = "https://api.github.com"
	backendName    = "github"
)

// Compile-time check that GitHubSCMBackend implements scm.Backend.
var _ scm.Backend = (*GitHubSCMBackend)(nil)

// GitHubSCMBackend implements scm.Backend using the GitHub REST API.
type GitHubSCMBackend struct {
	token   string
	baseURL string
	client  *http.Client
	logger  *slog.Logger
}

// Option is a functional option for configuring a GitHubSCMBackend.
type Option func(*GitHubSCMBackend)

// WithBaseURL sets a custom API base URL (e.g. for GitHub Enterprise).
func WithBaseURL(url string) Option {
	return func(b *GitHubSCMBackend) {
		b.baseURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient sets a custom http.Client for the backend.
func WithHTTPClient(c *http.Client) Option {
	return func(b *GitHubSCMBackend) {
		b.client = c
	}
}

// NewGitHubSCMBackend creates a new GitHub SCM backend.
func NewGitHubSCMBackend(token string, logger *slog.Logger, opts ...Option) *GitHubSCMBackend {
	b := &GitHubSCMBackend{
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

// ghRef is the subset of the GitHub git reference response we need.
type ghRef struct {
	Ref    string   `json:"ref"`
	Object ghObject `json:"object"`
}

// ghObject is the subset of a GitHub git object we need.
type ghObject struct {
	SHA string `json:"sha"`
}

// ghPR is the subset of the GitHub pull request response we parse.
type ghPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Head    ghHead `json:"head"`
	Base    ghHead `json:"base"`
}

// ghHead is the head/base branch info from a PR response.
type ghHead struct {
	Ref string `json:"ref"`
}

// CreateBranch creates a new branch in the repository from the specified
// base branch. If baseBranch is empty, the default branch is used.
func (b *GitHubSCMBackend) CreateBranch(ctx context.Context, repoURL string, branchName string, baseBranch string) error {
	owner, repo, err := parseOwnerRepo(repoURL)
	if err != nil {
		return fmt.Errorf("parsing repository URL: %w", err)
	}

	if baseBranch == "" {
		baseBranch = "main"
	}

	// Get the SHA of the base branch.
	refURL := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s", b.baseURL, owner, repo, baseBranch)
	body, err := b.doGet(ctx, refURL)
	if err != nil {
		return fmt.Errorf("fetching base branch ref: %w", err)
	}
	defer body.Close()

	var baseRef ghRef
	if err := json.NewDecoder(body).Decode(&baseRef); err != nil {
		return fmt.Errorf("decoding base ref: %w", err)
	}

	// Create the new branch ref.
	createURL := fmt.Sprintf("%s/repos/%s/%s/git/refs", b.baseURL, owner, repo)
	payload := map[string]string{
		"ref": "refs/heads/" + branchName,
		"sha": baseRef.Object.SHA,
	}
	if err := b.doPost(ctx, createURL, payload); err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}

	b.logger.Info("branch created", "owner", owner, "repo", repo, "branch", branchName, "base_sha", baseRef.Object.SHA)
	return nil
}

// CreatePullRequest creates a new pull request and returns the PR details.
func (b *GitHubSCMBackend) CreatePullRequest(ctx context.Context, input scm.CreatePullRequestInput) (*scm.PullRequest, error) {
	owner, repo, err := parseOwnerRepo(input.RepoURL)
	if err != nil {
		return nil, fmt.Errorf("parsing repository URL: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/pulls", b.baseURL, owner, repo)
	payload := map[string]string{
		"title": input.Title,
		"body":  input.Description,
		"head":  input.BranchName,
		"base":  input.BaseBranch,
	}

	respBody, err := b.doPostWithResponse(ctx, url, payload)
	if err != nil {
		return nil, fmt.Errorf("creating pull request: %w", err)
	}
	defer respBody.Close()

	var pr ghPR
	if err := json.NewDecoder(respBody).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decoding pull request response: %w", err)
	}

	result := prFromGH(pr)
	b.logger.Info("pull request created", "owner", owner, "repo", repo, "number", pr.Number)
	return result, nil
}

// GetPullRequestStatus retrieves the current status of a pull request by URL.
func (b *GitHubSCMBackend) GetPullRequestStatus(ctx context.Context, prURL string) (*scm.PullRequest, error) {
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		return nil, fmt.Errorf("parsing pull request URL: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", b.baseURL, owner, repo, number)
	body, err := b.doGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetching pull request: %w", err)
	}
	defer body.Close()

	var pr ghPR
	if err := json.NewDecoder(body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decoding pull request: %w", err)
	}

	return prFromGH(pr), nil
}

// Name returns the backend identifier.
func (b *GitHubSCMBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the SCM interface version implemented.
func (b *GitHubSCMBackend) InterfaceVersion() int {
	return scm.InterfaceVersion
}

// ghReviewComment is the subset of a GitHub pull request review comment we parse.
type ghReviewComment struct {
	ID          int  `json:"id"`
	InReplyToID *int `json:"in_reply_to_id,omitempty"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	CreatedAt string `json:"created_at"`
}

// ghIssueComment is the subset of a GitHub issue/PR general comment we parse.
type ghIssueComment struct {
	ID   int `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// ListReviewComments returns all review and general comments on the pull
// request, sorted by creation time.
func (b *GitHubSCMBackend) ListReviewComments(ctx context.Context, prURL string) ([]scm.ReviewComment, error) {
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		return nil, fmt.Errorf("parsing pull request URL: %w", err)
	}

	var all []scm.ReviewComment

	// Fetch line-level review comments.
	reviewURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments", b.baseURL, owner, repo, number)
	reviewBody, err := b.doGet(ctx, reviewURL)
	if err != nil {
		return nil, fmt.Errorf("fetching review comments: %w", err)
	}
	defer reviewBody.Close()

	var reviewComments []ghReviewComment
	if err := json.NewDecoder(reviewBody).Decode(&reviewComments); err != nil {
		return nil, fmt.Errorf("decoding review comments: %w", err)
	}

	for _, rc := range reviewComments {
		created, _ := time.Parse(time.RFC3339, rc.CreatedAt)
		threadID := strconv.Itoa(rc.ID)
		if rc.InReplyToID != nil {
			threadID = strconv.Itoa(*rc.InReplyToID)
		}
		all = append(all, scm.ReviewComment{
			ID:       strconv.Itoa(rc.ID),
			ThreadID: threadID,
			Author:   rc.User.Login,
			Body:     rc.Body,
			FilePath: rc.Path,
			Line:     rc.Line,
			Created:  created,
		})
	}

	// Fetch general PR comments (issue comments).
	issueURL := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", b.baseURL, owner, repo, number)
	issueBody, err := b.doGet(ctx, issueURL)
	if err != nil {
		return nil, fmt.Errorf("fetching issue comments: %w", err)
	}
	defer issueBody.Close()

	var issueComments []ghIssueComment
	if err := json.NewDecoder(issueBody).Decode(&issueComments); err != nil {
		return nil, fmt.Errorf("decoding issue comments: %w", err)
	}

	for _, ic := range issueComments {
		created, _ := time.Parse(time.RFC3339, ic.CreatedAt)
		id := strconv.Itoa(ic.ID)
		all = append(all, scm.ReviewComment{
			ID:       id,
			ThreadID: id,
			Author:   ic.User.Login,
			Body:     ic.Body,
			Created:  created,
		})
	}

	// Sort by creation time.
	sort.Slice(all, func(i, j int) bool {
		return all[i].Created.Before(all[j].Created)
	})

	return all, nil
}

// ReplyToComment posts a reply to an existing comment. For review comments
// it posts to the review comment reply endpoint; for general comments it
// posts a new issue comment. It attempts the review endpoint first and falls
// back to the issue comment endpoint.
func (b *GitHubSCMBackend) ReplyToComment(ctx context.Context, prURL string, commentID string, body string) error {
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		return fmt.Errorf("parsing pull request URL: %w", err)
	}

	// Try the review comment reply endpoint first.
	reviewReplyURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments/%s/replies",
		b.baseURL, owner, repo, number, commentID)
	replyErr := b.doPost(ctx, reviewReplyURL, map[string]string{"body": body})
	if replyErr == nil {
		return nil
	}

	// Fall back to posting a general issue comment.
	issueCommentURL := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", b.baseURL, owner, repo, number)
	return b.doPost(ctx, issueCommentURL, map[string]string{"body": body})
}

// ResolveThread is a no-op for GitHub. The GitHub REST API does not support
// resolving conversation threads; callers should use the GraphQL API for that.
func (b *GitHubSCMBackend) ResolveThread(_ context.Context, _ string, _ string) error {
	return nil
}

// GetDiff returns the unified diff between baseBranch and branchName using
// the GitHub compare API. When baseBranch is empty, "HEAD" is used, which
// resolves to the repository's default branch.
func (b *GitHubSCMBackend) GetDiff(ctx context.Context, repoURL string, baseBranch string, branchName string) (string, error) {
	owner, repo, err := parseOwnerRepo(repoURL)
	if err != nil {
		return "", fmt.Errorf("parsing repository URL: %w", err)
	}

	if baseBranch == "" {
		baseBranch = "HEAD"
	}

	// Use the compare endpoint with diff media type.
	compareURL := fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s", b.baseURL, owner, repo, baseBranch, branchName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, compareURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating compare request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing compare request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d from compare endpoint", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading compare response: %w", err)
	}

	return string(data), nil
}

// prFromGH converts a GitHub PR response to the scm.PullRequest type.
func prFromGH(pr ghPR) *scm.PullRequest {
	state := pr.State
	if pr.Merged {
		state = "merged"
	}
	return &scm.PullRequest{
		ID:          strconv.Itoa(pr.Number),
		Number:      pr.Number,
		Title:       pr.Title,
		Description: pr.Body,
		URL:         pr.HTMLURL,
		BranchName:  pr.Head.Ref,
		BaseBranch:  pr.Base.Ref,
		State:       state,
	}
}

// sshRepoPattern matches git@github.com:owner/repo.git style URLs.
var sshRepoPattern = regexp.MustCompile(`^git@github\.com:([^/]+)/([^/.]+?)(?:\.git)?$`)

// httpsRepoPattern matches https://github.com/owner/repo style URLs.
var httpsRepoPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/.]+?)(?:\.git)?(?:/.*)?$`)

// parseOwnerRepo extracts owner and repo from a GitHub repository URL.
// Supports both HTTPS and SSH formats.
func parseOwnerRepo(repoURL string) (string, string, error) {
	if m := httpsRepoPattern.FindStringSubmatch(repoURL); m != nil {
		return m[1], m[2], nil
	}
	if m := sshRepoPattern.FindStringSubmatch(repoURL); m != nil {
		return m[1], m[2], nil
	}
	return "", "", fmt.Errorf("unsupported repository URL format: %s", repoURL)
}

// prURLPattern matches GitHub pull request URLs.
var prURLPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// parsePRURL extracts the owner, repo, and PR number from a GitHub PR URL.
func parsePRURL(prURL string) (string, string, int, error) {
	m := prURLPattern.FindStringSubmatch(prURL)
	if m == nil {
		return "", "", 0, fmt.Errorf("unsupported pull request URL format: %s", prURL)
	}
	number, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid pull request number: %w", err)
	}
	return m[1], m[2], number, nil
}

// doGet performs a GET request and returns the response body.
func (b *GitHubSCMBackend) doGet(ctx context.Context, url string) (io.ReadCloser, error) {
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

	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected content-type %q (expected application/json) — the token may lack access to this resource", ct)
	}

	return resp.Body, nil
}

// doPost performs a POST request with a JSON body, discarding the response.
func (b *GitHubSCMBackend) doPost(ctx context.Context, url string, payload any) error {
	body, err := b.doPostWithResponse(ctx, url, payload)
	if err != nil {
		return err
	}
	body.Close()
	return nil
}

// doPostWithResponse performs a POST request and returns the response body.
func (b *GitHubSCMBackend) doPostWithResponse(ctx context.Context, url string, payload any) (io.ReadCloser, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
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

// setAuthHeaders adds the authorisation and accept headers to a request.
func (b *GitHubSCMBackend) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/vnd.github+json")
}
