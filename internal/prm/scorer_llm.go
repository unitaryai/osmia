package prm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/llm"
)

// scoreToolCallSignature defines the LLM signature for scoring agent tool calls.
var scoreToolCallSignature = llm.Signature{
	Name:        "ScoreToolCall",
	Description: "Score the quality of recent agent tool calls on a scale of 1-10.",
	InputFields: []llm.Field{
		{Name: "tool_calls", Description: "JSON array of recent tool call names and inputs", Type: llm.FieldTypeString, Required: true},
		{Name: "task_description", Description: "Description of the task being performed", Type: llm.FieldTypeString, Required: false},
	},
	OutputFields: []llm.Field{
		{Name: "score", Description: "Quality score from 1 (very poor) to 10 (excellent)", Type: llm.FieldTypeInt, Required: true},
		{Name: "reasoning", Description: "Explanation of the score", Type: llm.FieldTypeString, Required: true},
	},
}

// LLMScorer uses an LLM module to score agent steps, falling back to the
// rule-based Scorer on error or invalid response.
type LLMScorer struct {
	module   llm.Module
	fallback *Scorer
	logger   *slog.Logger
}

// NewLLMScorer creates an LLMScorer backed by a ChainOfThought module.
func NewLLMScorer(client llm.Client, fallback *Scorer, logger *slog.Logger) *LLMScorer {
	module := llm.NewChainOfThought(scoreToolCallSignature, client, nil)
	return &LLMScorer{
		module:   module,
		fallback: fallback,
		logger:   logger,
	}
}

// newLLMScorerWithModule creates an LLMScorer with a provided module.
// This is package-internal and intended for testing.
func newLLMScorerWithModule(module llm.Module, fallback *Scorer, logger *slog.Logger) *LLMScorer {
	return &LLMScorer{
		module:   module,
		fallback: fallback,
		logger:   logger,
	}
}

// toolCallEntry is a compact representation of a tool call for serialisation.
type toolCallEntry struct {
	Tool  string `json:"tool"`
	Input string `json:"input"`
}

// ScoreStep evaluates the given events using the LLM module.
// On error or invalid score, falls back to the rule-based scorer.
func (s *LLMScorer) ScoreStep(events []*agentstream.StreamEvent) *StepScore {
	// Serialise tool calls from events.
	var entries []toolCallEntry
	for _, ev := range events {
		tc, ok := ev.Parsed.(*agentstream.ToolCallEvent)
		if !ok || tc == nil {
			continue
		}
		entries = append(entries, toolCallEntry{
			Tool:  tc.Tool,
			Input: string(tc.Args),
		})
	}

	callsJSON, err := json.Marshal(entries)
	if err != nil {
		s.logger.Warn("failed to serialise tool calls, using fallback", "error", err)
		return s.fallback.ScoreStep(events)
	}

	inputs := map[string]any{
		"tool_calls": string(callsJSON),
	}

	outputs, err := s.module.Forward(context.Background(), inputs)
	if err != nil {
		s.logger.Warn("LLM scoring failed, using fallback", "error", err)
		return s.fallback.ScoreStep(events)
	}

	// Parse score from output — values come as typed from ParseResponse.
	score, err := extractIntOutput(outputs, "score")
	if err != nil {
		s.logger.Warn("failed to parse score from LLM output, using fallback", "error", err)
		return s.fallback.ScoreStep(events)
	}

	if score < 1 || score > 10 {
		s.logger.Warn("LLM returned out-of-range score, using fallback", "score", score)
		return s.fallback.ScoreStep(events)
	}

	reasoning := ""
	if r, ok := outputs["reasoning"]; ok {
		reasoning = fmt.Sprintf("%v", r)
	}

	return &StepScore{
		Score:     score,
		Reasoning: reasoning,
		Timestamp: time.Now(),
	}
}

// extractIntOutput extracts an integer from the module output map.
// The value may be an int (from ParseResponse) or a string requiring parsing.
func extractIntOutput(outputs map[string]any, key string) (int, error) {
	val, ok := outputs[key]
	if !ok {
		return 0, fmt.Errorf("missing output field %q", key)
	}
	switch v := val.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	case string:
		return strconv.Atoi(v)
	default:
		return 0, fmt.Errorf("unexpected type %T for field %q", val, key)
	}
}
