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
	// IgnoreBefore, when set, marks every comment created before it as
	// already handled. It is set only for a PR restored after a controller
	// restart, because ProcessedIDs lives in memory and is lost with it;
	// without this every comment on the PR would be classified afresh and
	// ones already addressed would spawn duplicate follow-up jobs.
	IgnoreBefore time.Time
}

// RestoredPR describes a pull/merge request to resume monitoring after a
// controller restart, reconstructed from persisted TaskRuns rather than from
// a live task completion.
type RestoredPR struct {
	// PRURL is the HTML URL of the pull/merge request.
	PRURL string
	// TicketID is the originating ticket identifier.
	TicketID string
	// OriginalTitle is the ticket title, if it was recorded.
	OriginalTitle string
	// OriginalDescription is the ticket description, if it was recorded.
	OriginalDescription string
	// RepoURL is the repository URL for the PR.
	RepoURL string
	// FollowUpCount is how many follow-up jobs have already run for this PR,
	// so a restart does not refill the MaxFollowUpJobs budget.
	FollowUpCount int
	// HandledUntil is the time up to which comments on the PR are known to
	// have been seen: the later of the originating task's completion and the
	// creation of its most recent follow-up job. Comments created before it
	// are not acted on again.
	HandledUntil time.Time
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
	// Comments is the set of actionable comments that triggered this follow-up.
	// Multiple comments from a single poll are batched into one request.
	Comments []ClassifiedComment
	// EnrichedDescription is the original description augmented with the
	// comment context, ready to be used as the new task description.
	EnrichedDescription string
	// ReplyCommentIDs are the comment IDs to reply to when the follow-up
	// completes. May be empty.
	ReplyCommentIDs []string
	// ThreadIDs are the discussion thread IDs to resolve on completion.
	// May be empty.
	ThreadIDs []string
}
