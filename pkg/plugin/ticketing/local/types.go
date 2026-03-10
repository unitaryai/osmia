package local

import (
	"time"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// Status represents the tracker-facing status for a local ticket.
type Status string

const (
	StatusDone       Status = "done"
	StatusInProgress Status = "in_progress"
	StatusTodo       Status = "todo"
)

const (
	statusDone       = StatusDone
	statusInProgress = StatusInProgress
	statusTodo       = StatusTodo
)

// RunState represents the outcome of the most recent automation run.
type RunState string

const (
	RunStateFailed    RunState = "failed"
	RunStateIdle      RunState = "idle"
	RunStateRunning   RunState = "running"
	RunStateSucceeded RunState = "succeeded"
)

const (
	runStateFailed    = RunStateFailed
	runStateIdle      = RunStateIdle
	runStateRunning   = RunStateRunning
	runStateSucceeded = RunStateSucceeded
)

// CommentKind identifies the source of a persisted comment.
type CommentKind string

const (
	CommentKindSystem CommentKind = "system"
	CommentKindUser   CommentKind = "user"
)

const (
	commentKindSystem = CommentKindSystem
	commentKindUser   = CommentKindUser
)

// EventType identifies a lifecycle or audit event stored for a ticket.
type EventType string

const (
	eventCommentAdded     EventType = "comment_added"
	eventCreated          EventType = "created"
	eventImported         EventType = "imported"
	eventMarkedComplete   EventType = "marked_complete"
	eventMarkedFailed     EventType = "marked_failed"
	eventMarkedInProgress EventType = "marked_in_progress"
	eventRequeued         EventType = "requeued"
)

// StoredTicket is the admin/read model exposed by the local backend.
type StoredTicket struct {
	Ticket        ticketing.Ticket   `json:"ticket"`
	Status        Status             `json:"status"`
	RunState      RunState           `json:"run_state"`
	FailureReason string             `json:"failure_reason"`
	Result        *engine.TaskResult `json:"result,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	InProgressAt  *time.Time         `json:"in_progress_at,omitempty"`
	CompletedAt   *time.Time         `json:"completed_at,omitempty"`
	FailedAt      *time.Time         `json:"failed_at,omitempty"`
}

// Ticket converts the record to the controller-facing ticket shape.
func (r StoredTicket) TicketRecord() ticketing.Ticket {
	return r.Ticket
}

// StoredComment is the persisted representation of a ticket comment.
type StoredComment struct {
	ID        int64       `json:"id"`
	TicketID  string      `json:"ticket_id"`
	Kind      CommentKind `json:"kind"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
}
