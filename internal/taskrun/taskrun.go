// Package taskrun implements the TaskRun state machine, tracking the lifecycle
// of a single execution of a task from creation through completion.
package taskrun

import (
	"fmt"
	"time"

	"github.com/unitaryai/osmia/pkg/engine"
)

// State represents the current state of a TaskRun.
type State string

const (
	// StateQueued indicates the task is waiting to be executed.
	StateQueued State = "Queued"
	// StateRunning indicates the task is actively being executed.
	StateRunning State = "Running"
	// StateNeedsHuman indicates the task requires human intervention.
	StateNeedsHuman State = "NeedsHuman"
	// StateSucceeded indicates the task completed successfully.
	StateSucceeded State = "Succeeded"
	// StateFailed indicates the task failed.
	StateFailed State = "Failed"
	// StateRetrying indicates the task is being retried after a failure.
	StateRetrying State = "Retrying"
	// StateTimedOut indicates the task exceeded its deadline.
	StateTimedOut State = "TimedOut"
)

// validTransitions defines the allowed state transitions for a TaskRun.
var validTransitions = map[State][]State{
	StateQueued:     {StateRunning, StateNeedsHuman},
	StateRunning:    {StateNeedsHuman, StateSucceeded, StateFailed, StateTimedOut},
	StateNeedsHuman: {StateRunning, StateFailed},
	StateFailed:     {StateRetrying},
	StateRetrying:   {StateRunning},
}

// DiagnosisRecord stores the result of a causal failure diagnosis for a
// single retry attempt. It is defined here (rather than in the diagnosis
// package) to avoid import cycles, since the diagnosis package imports
// taskrun.
type DiagnosisRecord struct {
	Mode            string    `json:"mode"`
	Confidence      float64   `json:"confidence"`
	Evidence        []string  `json:"evidence"`
	Prescription    string    `json:"prescription"`
	SuggestedEngine string    `json:"suggested_engine,omitempty"`
	DiagnosedAt     time.Time `json:"diagnosed_at"`
}

// TaskRun represents a single execution of a task, tracking its lifecycle
// from creation through completion.
type TaskRun struct {
	ID                        string             `json:"id"`
	IdempotencyKey            string             `json:"idempotency_key"`
	TicketID                  string             `json:"ticket_id"`
	Engine                    string             `json:"engine"`
	CurrentEngine             string             `json:"current_engine"`
	EngineAttempts            []string           `json:"engine_attempts,omitempty"`
	State                     State              `json:"state"`
	JobName                   string             `json:"job_name,omitempty"`
	CreatedAt                 time.Time          `json:"created_at"`
	UpdatedAt                 time.Time          `json:"updated_at"`
	Result                    *engine.TaskResult `json:"result,omitempty"`
	HumanQuestion             string             `json:"human_question,omitempty"`
	RetryCount                int                `json:"retry_count"`
	MaxRetries                int                `json:"max_retries"`
	HeartbeatAt               *time.Time         `json:"heartbeat_at,omitempty"`
	HeartbeatTTLSeconds       int                `json:"heartbeat_ttl_seconds"`
	TokensConsumed            int                `json:"tokens_consumed"`
	FilesChanged              int                `json:"files_changed"`
	ToolCallsTotal            int                `json:"tool_calls_total"`
	LastToolName              string             `json:"last_tool_name,omitempty"`
	ConsecutiveIdenticalTools int                `json:"consecutive_identical_tools"`
	CostUSD                   float64            `json:"cost_usd"`
	DiagnosisHistory          []DiagnosisRecord  `json:"diagnosis_history,omitempty"`
	TournamentID              string             `json:"tournament_id,omitempty"`
	CandidateIndex            int                `json:"candidate_index,omitempty"`
	TournamentState           string             `json:"tournament_state,omitempty"`

	// ApprovalGateType records which approval gate this TaskRun is held at
	// ("pre_start" or "pre_merge"), used by ResolveApproval to dispatch
	// the correct resolution logic.
	ApprovalGateType string `json:"approval_gate_type,omitempty"`

	// Review follow-up fields — populated for TaskRuns created in response
	// to review comments on a PR/MR opened by a previous Osmia task.

	// ParentTicketID is the original ticket ID. When set, handleJobComplete
	// posts a comment on this ticket rather than calling MarkComplete.
	ParentTicketID string `json:"parent_ticket_id,omitempty"`
	// ReviewCommentID is the comment ID to reply to on the original PR/MR
	// once the follow-up job completes.
	ReviewCommentID string `json:"review_comment_id,omitempty"`
	// ReviewThreadID is the discussion thread ID to resolve on completion.
	ReviewThreadID string `json:"review_thread_id,omitempty"`
	// ReviewPRURL is the PR/MR URL used for SCM reply and thread resolution.
	ReviewPRURL string `json:"review_pr_url,omitempty"`
}

// New creates a new TaskRun in the Queued state with the given parameters.
func New(id, idempotencyKey, ticketID, engineName string) *TaskRun {
	now := time.Now()
	return &TaskRun{
		ID:                  id,
		IdempotencyKey:      idempotencyKey,
		TicketID:            ticketID,
		Engine:              engineName,
		State:               StateQueued,
		CreatedAt:           now,
		UpdatedAt:           now,
		MaxRetries:          1,
		HeartbeatTTLSeconds: 300,
	}
}

// Transition attempts to move the TaskRun to the given target state.
// It returns an error if the transition is not valid.
func (tr *TaskRun) Transition(target State) error {
	allowed, ok := validTransitions[tr.State]
	if !ok {
		return fmt.Errorf("no transitions defined from state %q", tr.State)
	}

	for _, s := range allowed {
		if s == target {
			tr.State = target
			tr.UpdatedAt = time.Now()
			return nil
		}
	}

	return fmt.Errorf("invalid transition from %q to %q", tr.State, target)
}

// IsTerminal returns true if the TaskRun is in a terminal state
// (Succeeded, Failed with no retries remaining, or TimedOut).
func (tr *TaskRun) IsTerminal() bool {
	switch tr.State {
	case StateSucceeded, StateTimedOut:
		return true
	case StateFailed:
		return tr.RetryCount >= tr.MaxRetries
	default:
		return false
	}
}

// IsStale returns true if the TaskRun has not received a heartbeat
// within the configured TTL.
func (tr *TaskRun) IsStale() bool {
	if tr.HeartbeatAt == nil {
		return false
	}
	ttl := time.Duration(tr.HeartbeatTTLSeconds) * time.Second
	return time.Since(*tr.HeartbeatAt) > ttl
}
