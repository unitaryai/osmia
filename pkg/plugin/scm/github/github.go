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
	"strconv"
	"strings"

	"github.com/unitaryai/robodev/pkg/plugin/scm"
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
