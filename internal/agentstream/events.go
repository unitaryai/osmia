// Package agentstream provides types and utilities for parsing and forwarding
// the NDJSON event stream produced by AI coding agents (Claude Code, etc.)
// during task execution. Each line of the stream is a self-contained JSON
// object describing a tool call, content delta, cost update, or final result.
package agentstream

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType identifies the kind of event emitted by the agent stream.
type EventType string

const (
	// EventToolCall represents an agent invoking a tool (Bash, Read, etc.).
	EventToolCall EventType = "tool_use"
	// EventContentDelta represents a chunk of assistant or user content.
	EventContentDelta EventType = "content"
	// EventCost represents a periodic cost/token update.
	EventCost EventType = "cost"
	// EventResult represents the final result of the agent run.
	EventResult EventType = "result"
	// EventSystem represents a system-level message (e.g. heartbeat, error).
	EventSystem EventType = "system"
)

// StreamEvent is the top-level envelope for every NDJSON line in the agent
// stream. The Raw field holds the original JSON so callers can decode into
// the appropriate typed event via the helper methods.
type StreamEvent struct {
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Raw       json.RawMessage `json:"-"`

	// Parsed holds the decoded typed event (one of *ToolCallEvent,
	// *ContentDeltaEvent, *CostEvent, *ResultEvent) or nil for unknown types.
	Parsed any `json:"-"`
}

// ToolCallEvent describes a single tool invocation by the agent.
type ToolCallEvent struct {
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args,omitempty"`
	Duration time.Duration   `json:"duration,omitempty"`
}

// ContentDeltaEvent carries a chunk of text produced by the agent or user.
type ContentDeltaEvent struct {
	Content string `json:"content"`
	Role    string `json:"role"`
}

// CostEvent reports cumulative token usage and cost at a point in time.
type CostEvent struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ResultEvent describes the final outcome of an agent execution.
//
// The struct supports two serialisation formats:
//   - Our internal format: success (bool) + summary (string)
//   - Claude Code stream-json format: is_error (bool) + result (string)
//
// ParseEvent normalises both formats into the internal fields, so callers
// can always read Success and Summary regardless of which agent produced
// the event.
type ResultEvent struct {
	Success         bool   `json:"success"`
	Summary         string `json:"summary"`
	MergeRequestURL string `json:"merge_request_url,omitempty"`
	BranchName      string `json:"branch_name,omitempty"`
	TestsPassed     int    `json:"tests_passed,omitempty"`
	TestsFailed     int    `json:"tests_failed,omitempty"`
	TestsAdded      int    `json:"tests_added,omitempty"`

	// Claude Code stream-json fields — normalised into Success/Summary by ParseEvent.
	IsError   bool   `json:"is_error"`
	RawResult string `json:"result"`

	// StructuredOutput holds the schema-conforming result when --json-schema
	// is used. Claude Code puts structured data here instead of in result.
	StructuredOutput *ResultEvent `json:"structured_output,omitempty"`
}

// SystemEvent carries system-level metadata emitted at session initialisation.
// Claude Code emits a system event at startup that includes the session_id,
// which Osmia captures to enable session resumption on retry pods.
type SystemEvent struct {
	// SessionID is the Claude Code session identifier. Present in the
	// system init event emitted at the start of every claude run.
	SessionID string `json:"session_id,omitempty"`
	// Subtype provides additional context (e.g. "init", "error").
	Subtype string `json:"subtype,omitempty"`
}

// rawEnvelope is used for the initial unmarshal to extract the type field
// and timestamp before dispatching to a typed struct.
type rawEnvelope struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`
}

// ParseEvent parses a single NDJSON line into a StreamEvent with its
// typed Parsed field populated. Unknown event types are returned with a
// nil Parsed field and no error, so callers can safely ignore them.
func ParseEvent(line []byte) (*StreamEvent, error) {
	if len(line) == 0 {
		return nil, fmt.Errorf("empty line")
	}

	var env rawEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	if env.Type == "" {
		return nil, fmt.Errorf("missing event type")
	}

	ev := &StreamEvent{
		Type:      env.Type,
		Timestamp: env.Timestamp,
		Raw:       json.RawMessage(line),
	}

	switch env.Type {
	case EventToolCall:
		var tc ToolCallEvent
		if err := json.Unmarshal(line, &tc); err != nil {
			return nil, fmt.Errorf("invalid tool_use event: %w", err)
		}
		ev.Parsed = &tc

	case EventContentDelta:
		var cd ContentDeltaEvent
		if err := json.Unmarshal(line, &cd); err != nil {
			return nil, fmt.Errorf("invalid content event: %w", err)
		}
		ev.Parsed = &cd

	case EventCost:
		var ce CostEvent
		if err := json.Unmarshal(line, &ce); err != nil {
			return nil, fmt.Errorf("invalid cost event: %w", err)
		}
		ev.Parsed = &ce

	case EventResult:
		var re ResultEvent
		if err := json.Unmarshal(line, &re); err != nil {
			return nil, fmt.Errorf("invalid result event: %w", err)
		}
		// Normalise Claude Code stream-json format into our internal format.
		// Claude Code emits is_error + result; our custom format uses success + summary.
		// The presence of a non-empty RawResult field signals Claude Code origin.
		if re.RawResult != "" {
			re.Success = !re.IsError
			if re.Summary == "" {
				re.Summary = re.RawResult
			}
		}
		// When --json-schema is used, Claude Code puts the schema-conforming
		// result in structured_output instead of result. Merge those fields
		// into the top-level ResultEvent so callers see a consistent view.
		if re.StructuredOutput != nil {
			so := re.StructuredOutput
			re.Success = so.Success
			if so.Summary != "" {
				re.Summary = so.Summary
			}
			if so.MergeRequestURL != "" {
				re.MergeRequestURL = so.MergeRequestURL
			}
			if so.BranchName != "" {
				re.BranchName = so.BranchName
			}
			if so.TestsPassed > 0 {
				re.TestsPassed = so.TestsPassed
			}
			if so.TestsFailed > 0 {
				re.TestsFailed = so.TestsFailed
			}
			if so.TestsAdded > 0 {
				re.TestsAdded = so.TestsAdded
			}
			re.StructuredOutput = nil // avoid circular reference
		}
		ev.Parsed = &re

	case EventSystem:
		var se SystemEvent
		if err := json.Unmarshal(line, &se); err != nil {
			return nil, fmt.Errorf("invalid system event: %w", err)
		}
		ev.Parsed = &se

	default:
		// Unknown type — leave Parsed nil so the caller can skip it.
	}

	return ev, nil
}
