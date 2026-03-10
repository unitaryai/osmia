// Package prm implements a Process Reward Model for real-time agent coaching.
// It evaluates agent behaviour by scoring tool call patterns, tracking score
// trajectories, and deciding when to intervene with nudges or escalations.
package prm

import (
	"log/slog"
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
)

// StepScore represents a single evaluation of the agent's recent behaviour.
type StepScore struct {
	// Score is the quality rating from 1 (very poor) to 10 (excellent).
	Score int
	// Reasoning explains why the score was given.
	Reasoning string
	// Timestamp is when the score was computed.
	Timestamp time.Time
}

// Scorer evaluates a rolling window of tool calls and produces step scores
// using rule-based heuristics. V1 does not use LLM calls; scoring is based
// entirely on observable patterns in the tool call stream.
type Scorer struct {
	logger     *slog.Logger
	windowSize int
}

// NewScorer creates a Scorer with the given window size. The window size
// determines how many recent tool calls are considered for each evaluation.
func NewScorer(logger *slog.Logger, windowSize int) *Scorer {
	if windowSize <= 0 {
		windowSize = 10
	}
	return &Scorer{
		logger:     logger,
		windowSize: windowSize,
	}
}

// ScoreStep evaluates the given tool call events and returns a StepScore.
// The scoring is rule-based: repeated identical tools decrease the score,
// productive patterns (read→edit, edit→test) increase it, and high volume
// without progress decreases it.
func (s *Scorer) ScoreStep(events []*agentstream.StreamEvent) *StepScore {
	if len(events) == 0 {
		return &StepScore{
			Score:     5,
			Reasoning: "no events to evaluate",
			Timestamp: time.Now(),
		}
	}

	// Extract tool names from the window.
	tools := extractToolNames(events)
	if len(tools) == 0 {
		return &StepScore{
			Score:     5,
			Reasoning: "no tool calls in evaluation window",
			Timestamp: time.Now(),
		}
	}

	score := 5 // baseline
	var reasons []string

	// Rule 1: Repeated identical tool calls decrease score.
	maxRepeat := maxConsecutiveRepeats(tools)
	if maxRepeat >= 4 {
		penalty := min(3, maxRepeat-3)
		score -= penalty
		reasons = append(reasons, "repeated identical tool calls detected")
		s.logger.Debug("scorer: repetition penalty applied",
			"max_repeat", maxRepeat,
			"penalty", penalty,
		)
	}

	// Rule 2: Read followed by edit is a productive pattern.
	if hasPattern(tools, isReadTool, isEditTool) {
		score += 2
		reasons = append(reasons, "productive read-then-edit pattern")
	}

	// Rule 3: Edit followed by test run is a productive pattern.
	if hasPattern(tools, isEditTool, isTestTool) {
		score += 2
		reasons = append(reasons, "productive edit-then-test pattern")
	}

	// Rule 4: High diversity of tools suggests exploration (slightly positive).
	uniqueRatio := float64(countUnique(tools)) / float64(len(tools))
	if uniqueRatio > 0.7 && len(tools) >= 3 {
		score++
		reasons = append(reasons, "good tool diversity")
	}

	// Rule 5: All identical tools in the window is a strong negative signal.
	if uniqueRatio <= 0.15 && len(tools) >= 5 {
		score -= 2
		reasons = append(reasons, "very low tool diversity")
	}

	// Clamp score to [1, 10].
	score = max(1, min(10, score))

	reasoning := "baseline score"
	if len(reasons) > 0 {
		reasoning = joinReasons(reasons)
	}

	return &StepScore{
		Score:     score,
		Reasoning: reasoning,
		Timestamp: time.Now(),
	}
}

// extractToolNames returns the tool names from tool call events.
func extractToolNames(events []*agentstream.StreamEvent) []string {
	var tools []string
	for _, ev := range events {
		if tc, ok := ev.Parsed.(*agentstream.ToolCallEvent); ok && tc != nil {
			tools = append(tools, tc.Tool)
		}
	}
	return tools
}

// maxConsecutiveRepeats returns the longest run of identical consecutive values.
func maxConsecutiveRepeats(tools []string) int {
	if len(tools) == 0 {
		return 0
	}
	maxRun := 1
	current := 1
	for i := 1; i < len(tools); i++ {
		if tools[i] == tools[i-1] {
			current++
			if current > maxRun {
				maxRun = current
			}
		} else {
			current = 1
		}
	}
	return maxRun
}

// hasPattern returns true if any element matching predA is immediately
// followed by an element matching predB.
func hasPattern(tools []string, predA, predB func(string) bool) bool {
	for i := 0; i < len(tools)-1; i++ {
		if predA(tools[i]) && predB(tools[i+1]) {
			return true
		}
	}
	return false
}

// countUnique returns the number of unique strings in the slice.
func countUnique(tools []string) int {
	seen := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		seen[t] = struct{}{}
	}
	return len(seen)
}

// isReadTool returns true if the tool name represents a file reading operation.
func isReadTool(tool string) bool {
	switch tool {
	case "Read", "read", "cat", "Glob", "Grep", "grep", "find", "ls":
		return true
	}
	return false
}

// isEditTool returns true if the tool name represents a file editing operation.
func isEditTool(tool string) bool {
	switch tool {
	case "Edit", "edit", "Write", "write", "sed", "awk", "edit_file", "write_file":
		return true
	}
	return false
}

// isTestTool returns true if the tool name represents a test execution.
func isTestTool(tool string) bool {
	switch tool {
	case "Bash", "bash", "test", "pytest", "go_test", "npm_test":
		return true
	}
	return false
}

// joinReasons joins multiple reason strings with semicolons.
func joinReasons(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	result := reasons[0]
	for i := 1; i < len(reasons); i++ {
		result += "; " + reasons[i]
	}
	return result
}
