// Package taskrun implements the TaskRun state machine, tracking the lifecycle
// of a single execution of a task from creation through completion.
package taskrun

import (
	"fmt"
	"time"

	"github.com/unitaryai/robodev/pkg/engine"
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
	StateQueued:     {StateRunning},
	StateRunning:    {StateNeedsHuman, StateSucceeded, StateFailed, StateTimedOut},
	StateNeedsHuman: {StateRunning},
	StateFailed:     {StateRetrying},
	StateRetrying:   {StateRunning},
}

// TaskRun represents a single execution of a task, tracking its lifecycle
// from creation through completion.
type TaskRun struct {
	ID                        string             `json:"id"`
	IdempotencyKey            string             `json:"idempotency_key"`
	TicketID                  string             `json:"ticket_id"`
	Engine                    string             `json:"engine"`
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
