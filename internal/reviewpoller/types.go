// Package reviewpoller monitors pull/merge requests created by Osmia,
// classifies incoming review comments, and emits follow-up task requests
// for actionable feedback.
package reviewpoller

import (
	"time"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// Classification describes how a review comment should be handled.
type Classification int

const (
	// ClassificationIgnore indicates the comment should be ignored
	// (e.g. bot comment, empty body, or posted by Osmia itself).
	ClassificationIgnore Classification = iota
	// ClassificationInformational indicates the comment is informational
	// (e.g. LGTM, compliment) and requires no action.
	ClassificationInformational
	// ClassificationRequiresAction indicates the comment requests a code
	// change or fix and should trigger a follow-up task.
	ClassificationRequiresAction
)

// ClassifiedComment pairs a ReviewComment with its classification result.
type ClassifiedComment struct {
	scm.ReviewComment
	Classification Classification
	Severity       string // "info", "warning", or "error"
	Reason         string
}

// TrackedPR is a pull or merge request that Osmia is monitoring for
// incoming review comments.
type TrackedPR struct {
	// PRURL is the HTML URL of the pull/merge request.
	PRURL string
	// TicketID is the originating ticket identifier.
	TicketID string
	// OriginalTitle is the ticket title used when the PR was opened.
	OriginalTitle string
	// OriginalDescription is the ticket description used when the PR was opened.
	OriginalDescription string
	// RepoURL is the repository URL for the PR.
	RepoURL string
	// FollowUpCount tracks how many follow-up jobs have been submitted for this PR.
	FollowUpCount int
	// ProcessedIDs is the set of comment IDs that have already been handled.
	ProcessedIDs map[string]bool
	// RegisteredAt is when this PR was first registered for monitoring.
	RegisteredAt time.Time
}

// FollowUpRequest is emitted when an actionable comment is found on a
// tracked PR. The controller drains these and submits new K8s Jobs.
type FollowUpRequest struct {
	// PRURL is the HTML URL of the pull/merge request.
	PRURL string
	// TicketID is the originating ticket identifier.
	TicketID string
	// OriginalTitle is the ticket title.
	OriginalTitle string
	// OriginalDescription is the original ticket description.
	OriginalDescription string
	// RepoURL is the repository URL.
	RepoURL string
	// Comment is the classified comment that triggered this follow-up.
	Comment ClassifiedComment
	// EnrichedDescription is the original description augmented with the
	// comment context, ready to be used as the new task description.
	EnrichedDescription string
	// ReplyCommentID is the comment ID to reply to when the follow-up
	// completes. May be empty.
	ReplyCommentID string
	// ThreadID is the discussion thread ID to resolve on completion.
	// May be empty.
	ThreadID string
}
