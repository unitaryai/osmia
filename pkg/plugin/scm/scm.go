// Package scm defines the SCMBackend interface for interacting with
// source code management platforms (GitHub, GitLab, etc). The SCM backend
// handles branch creation, pull/merge request management, and repository
// operations.
package scm

import (
	"context"
	"time"
)

// InterfaceVersion is the current version of the SCMBackend interface.
const InterfaceVersion = 2

// ReviewComment is a comment posted on a pull or merge request.
type ReviewComment struct {
	// ID is the unique identifier of the comment.
	ID string
	// ThreadID is the discussion or thread ID. Used for ResolveThread.
	// For GitHub, this is the in_reply_to_id if set, otherwise the comment ID.
	// For GitLab, this is the discussion_id from the API response.
	ThreadID string
	// Author is the username of the comment author.
	Author string
	// Body is the text content of the comment.
	Body string
	// FilePath is the file the comment is attached to. Empty for general comments.
	FilePath string
	// Line is the line number the comment is attached to. 0 for general comments.
	Line int
	// Created is the timestamp when the comment was created.
	Created time.Time
}

// PullRequest represents a pull request or merge request created by an agent.
type PullRequest struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	BranchName  string `json:"branch_name"`
	BaseBranch  string `json:"base_branch"`
	State       string `json:"state"` // "open", "closed", "merged"
}

// CreatePullRequestInput contains the parameters for creating a new
// pull request or merge request.
type CreatePullRequestInput struct {
	RepoURL     string `json:"repo_url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	BranchName  string `json:"branch_name"`
	BaseBranch  string `json:"base_branch"`
}

// Backend is the interface that SCM backends must implement.
// It provides operations for branch and pull request management.
type Backend interface {
	// CreateBranch creates a new branch in the repository from the
	// specified base branch or default branch if base is empty.
	CreateBranch(ctx context.Context, repoURL string, branchName string, baseBranch string) error

	// CreatePullRequest creates a new pull/merge request and returns
	// the created PR details including its URL.
	CreatePullRequest(ctx context.Context, input CreatePullRequestInput) (*PullRequest, error)

	// GetPullRequestStatus retrieves the current status of a pull request
	// by its URL. This is used by the controller to check CI status and
	// review state.
	GetPullRequestStatus(ctx context.Context, prURL string) (*PullRequest, error)

	// ListReviewComments returns all review and general comments on the
	// pull or merge request identified by prURL, sorted by creation time.
	ListReviewComments(ctx context.Context, prURL string) ([]ReviewComment, error)

	// ReplyToComment posts a reply to an existing comment on a pull or
	// merge request. For line-level review comments the reply is attached
	// to the same thread; for general comments a new top-level comment is
	// posted.
	ReplyToComment(ctx context.Context, prURL string, commentID string, body string) error

	// ResolveThread marks a review thread as resolved. Implementations
	// that do not support thread resolution (e.g. GitHub REST) should
	// return nil silently.
	ResolveThread(ctx context.Context, prURL string, threadID string) error

	// GetDiff returns the unified diff between baseBranch and branchName.
	// When baseBranch is empty, the implementation should use the
	// repository's default branch. Implementations should return the diff
	// as a string suitable for code review. Returns an empty string and
	// nil error when no diff is available.
	GetDiff(ctx context.Context, repoURL string, baseBranch string, branchName string) (string, error)

	// Name returns the unique identifier for this backend (e.g. "github", "gitlab").
	Name() string

	// InterfaceVersion returns the version of the SCMBackend interface
	// that this backend implements.
	InterfaceVersion() int
}
