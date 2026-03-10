package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// Extractor performs post-task heuristic knowledge extraction from completed
// TaskRuns and their event streams. It produces Nodes and Edges that capture
// patterns, failure modes, and engine capabilities.
type Extractor struct {
	logger *slog.Logger
}

// NewExtractor creates a new Extractor.
func NewExtractor(logger *slog.Logger) *Extractor {
	return &Extractor{logger: logger}
}

// Extract analyses a completed TaskRun and its event stream to produce
// knowledge nodes and edges. The heuristic rules are:
//   - Task succeeded → extract success pattern (engine + task type + repo)
//   - Task failed → extract failure pattern with reason
//   - Watchdog triggered (stale heartbeat) → extract anomaly fact
//   - Engine fallback occurred → extract engine capability fact
//   - Repeated tool patterns → extract behavioural pattern
func (e *Extractor) Extract(
	_ context.Context,
	tr *taskrun.TaskRun,
	events []agentstream.StreamEvent,
) ([]Node, []Edge, error) {
	if tr == nil {
		return nil, nil, fmt.Errorf("cannot extract from nil task run")
	}

	var nodes []Node
	var edges []Edge
	now := time.Now()

	// Rule 1: Success pattern.
	if tr.State == taskrun.StateSucceeded {
		fact := &Fact{
			ID:         fmt.Sprintf("success-%s", tr.ID),
			Content:    fmt.Sprintf("engine %q succeeded on task %q", tr.CurrentEngine, tr.TicketID),
			Source:     tr.ID,
			FactKind:   FactTypeSuccessPattern,
			ValidFrom:  now,
			Confidence: 0.8,
			DecayRate:  0.01,
			TenantID:   "",
		}
		if tr.Result != nil && tr.Result.Summary != "" {
			fact.Content = fmt.Sprintf("engine %q succeeded on task %q: %s",
				tr.CurrentEngine, tr.TicketID, tr.Result.Summary)
		}
		nodes = append(nodes, fact)
	}

	// Rule 2: Failure pattern.
	if tr.State == taskrun.StateFailed || tr.State == taskrun.StateTimedOut {
		reason := "unknown"
		if tr.Result != nil && tr.Result.Summary != "" {
			reason = tr.Result.Summary
		}
		if tr.State == taskrun.StateTimedOut {
			reason = "task timed out"
		}

		fact := &Fact{
			ID:         fmt.Sprintf("failure-%s", tr.ID),
			Content:    fmt.Sprintf("engine %q failed on task %q: %s", tr.CurrentEngine, tr.TicketID, reason),
			Source:     tr.ID,
			FactKind:   FactTypeFailurePattern,
			ValidFrom:  now,
			Confidence: 0.9,
			DecayRate:  0.02,
			TenantID:   "",
		}
		nodes = append(nodes, fact)
	}

	// Rule 3: Watchdog anomaly (stale heartbeat indicates the agent stopped
	// making progress).
	if tr.IsStale() {
		fact := &Fact{
			ID:         fmt.Sprintf("stale-%s", tr.ID),
			Content:    fmt.Sprintf("agent became unresponsive during task %q with engine %q", tr.TicketID, tr.CurrentEngine),
			Source:     tr.ID,
			FactKind:   FactTypeRepoBehaviour,
			ValidFrom:  now,
			Confidence: 0.7,
			DecayRate:  0.03,
			TenantID:   "",
		}
		nodes = append(nodes, fact)
	}

	// Rule 4: Engine fallback occurred.
	if len(tr.EngineAttempts) > 1 {
		for i := 0; i < len(tr.EngineAttempts)-1; i++ {
			failedEngine := tr.EngineAttempts[i]
			fact := &Fact{
				ID:         fmt.Sprintf("fallback-%s-%d", tr.ID, i),
				Content:    fmt.Sprintf("engine %q required fallback during task %q", failedEngine, tr.TicketID),
				Source:     tr.ID,
				FactKind:   FactTypeEngineCapability,
				ValidFrom:  now,
				Confidence: 0.85,
				DecayRate:  0.015,
				TenantID:   "",
			}
			nodes = append(nodes, fact)
		}
	}

	// Rule 5: Repeated tool patterns from event stream.
	toolCounts := countToolCalls(events)
	for tool, count := range toolCounts {
		if count >= 5 {
			pattern := &Pattern{
				ID:          fmt.Sprintf("toolpattern-%s-%s", tr.ID, sanitiseID(tool)),
				Description: fmt.Sprintf("heavy use of %q tool (%d calls) during task %q", tool, count, tr.TicketID),
				Occurrences: count,
				FirstSeen:   tr.CreatedAt,
				LastSeen:    now,
				Confidence:  0.6,
				DecayRate:   0.02,
				TenantID:    "",
			}
			nodes = append(nodes, pattern)
		}
	}

	// Create edges between related nodes from this extraction.
	if len(nodes) > 1 {
		for i := 1; i < len(nodes); i++ {
			edge := Edge{
				FromID:    nodes[0].NodeID(),
				ToID:      nodes[i].NodeID(),
				Relation:  RelationRelatesTo,
				Weight:    0.5,
				CreatedAt: now,
			}
			edges = append(edges, edge)
		}
	}

	e.logger.Debug("extracted knowledge from task run",
		"task_run_id", tr.ID,
		"nodes", len(nodes),
		"edges", len(edges),
	)

	return nodes, edges, nil
}

// countToolCalls tallies how many times each tool was invoked in the event stream.
func countToolCalls(events []agentstream.StreamEvent) map[string]int {
	counts := make(map[string]int)
	for i := range events {
		if events[i].Type != agentstream.EventToolCall {
			continue
		}
		tc, ok := events[i].Parsed.(*agentstream.ToolCallEvent)
		if !ok || tc == nil {
			continue
		}
		counts[tc.Tool]++
	}
	return counts
}

// sanitiseID replaces characters that are problematic in IDs with hyphens.
func sanitiseID(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s)
}
