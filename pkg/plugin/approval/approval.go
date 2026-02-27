// Package approval defines the HumanApprovalBackend interface for
// event-driven human-in-the-loop interactions. When an agent asks a
// question, the TaskRun transitions to NeedsHuman state. The approval
// backend delivers the question to a human (via Slack, Teams, etc)
// and receives the response asynchronously via webhook or callback.
package approval

import (
	"context"

	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
)

// InterfaceVersion is the current version of the HumanApprovalBackend interface.
const InterfaceVersion = 1

// Backend is the interface that approval backends must implement.
// It handles asynchronous human interactions — requesting approval
// and cancelling pending requests.
type Backend interface {
	// RequestApproval delivers a question to a human and records it as
	// pending. The taskRunID is used to correlate the response when it
	// arrives via webhook. Options provides suggested responses (e.g.
	// "approve", "reject", "skip").
	RequestApproval(ctx context.Context, question string, ticket ticketing.Ticket, taskRunID string, options []string) error

	// CancelPending cancels any outstanding approval request for the
	// given TaskRun. This is called when the progress watchdog terminates
	// a NeedsHuman job, or when the TaskRun times out.
	CancelPending(ctx context.Context, taskRunID string) error

	// Name returns the unique identifier for this backend (e.g. "slack", "teams").
	Name() string

	// InterfaceVersion returns the version of the HumanApprovalBackend
	// interface that this backend implements.
	InterfaceVersion() int
}

// Response represents a human's response to an approval request,
// delivered via webhook callback.
type Response struct {
	TaskRunID string `json:"task_run_id"`
	Approved  bool   `json:"approved"`
	Message   string `json:"message,omitempty"`
	Responder string `json:"responder,omitempty"` // Who responded (e.g. Slack user ID)
}
