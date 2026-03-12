package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/llm"
	"github.com/unitaryai/osmia/internal/taskrun"
)

// extractMemorySignature defines the LLM signature for knowledge extraction.
var extractMemorySignature = llm.Signature{
	Name:        "ExtractMemory",
	Description: "Extract facts and patterns from a completed task run for the knowledge graph.",
	InputFields: []llm.Field{
		{Name: "task_description", Description: "Description of the task", Type: llm.FieldTypeString, Required: true},
		{Name: "outcome", Description: "Task outcome: succeeded or failed", Type: llm.FieldTypeString, Required: true},
		{Name: "summary", Description: "Result summary", Type: llm.FieldTypeString, Required: false},
		{Name: "engine", Description: "Engine that ran the task", Type: llm.FieldTypeString, Required: true},
		{Name: "repo_url", Description: "Repository URL", Type: llm.FieldTypeString, Required: false},
	},
	OutputFields: []llm.Field{
		{Name: "facts", Description: "JSON array of fact strings extracted from the task", Type: llm.FieldTypeString, Required: true},
		{Name: "patterns", Description: "JSON array of pattern strings observed", Type: llm.FieldTypeString, Required: true},
	},
}

// LLMExtractor augments the rule-based Extractor with LLM-powered fact and
// pattern extraction. On error it falls back to V1 results only.
type LLMExtractor struct {
	module llm.Module
	v1     *Extractor
	logger *slog.Logger
}

// NewLLMExtractor creates an LLMExtractor backed by a Predict module.
func NewLLMExtractor(client llm.Client, v1 *Extractor, logger *slog.Logger) *LLMExtractor {
	module := llm.NewPredict(extractMemorySignature, client, nil)
	return &LLMExtractor{
		module: module,
		v1:     v1,
		logger: logger,
	}
}

// newLLMExtractorWithModule creates an LLMExtractor with a provided module.
// This is package-internal and intended for testing.
func newLLMExtractorWithModule(module llm.Module, v1 *Extractor, logger *slog.Logger) *LLMExtractor {
	return &LLMExtractor{
		module: module,
		v1:     v1,
		logger: logger,
	}
}

// Extract runs V1 extraction then augments with LLM-extracted facts and patterns.
// On LLM error, only V1 results are returned.
func (e *LLMExtractor) Extract(
	ctx context.Context,
	tr *taskrun.TaskRun,
	events []agentstream.StreamEvent,
) ([]Node, []Edge, error) {
	// Run V1 extraction first.
	v1Nodes, v1Edges, err := e.v1.Extract(ctx, tr, events)
	if err != nil {
		return nil, nil, err
	}

	// Build LLM inputs from the TaskRun.
	outcome := "failed"
	if tr.State == taskrun.StateSucceeded {
		outcome = "succeeded"
	}

	summary := ""
	if tr.Result != nil {
		summary = tr.Result.Summary
	}

	inputs := map[string]any{
		"task_description": tr.TicketID,
		"outcome":          outcome,
		"engine":           tr.CurrentEngine,
	}
	if summary != "" {
		inputs["summary"] = summary
	}

	outputs, err := e.module.Forward(ctx, inputs)
	if err != nil {
		e.logger.Warn("LLM extraction failed, returning V1 results only", "error", err)
		return v1Nodes, v1Edges, nil
	}

	now := time.Now()

	// Parse facts from the output.
	if factsRaw, ok := outputs["facts"]; ok {
		factsStr := fmt.Sprintf("%v", factsRaw)
		var factStrings []string
		if jsonErr := json.Unmarshal([]byte(factsStr), &factStrings); jsonErr != nil {
			e.logger.Warn("failed to parse LLM facts as JSON array", "error", jsonErr, "raw", factsStr)
		} else {
			for i, factContent := range factStrings {
				factKind := FactTypeSuccessPattern
				if outcome == "failed" {
					factKind = FactTypeFailurePattern
				}
				fact := &Fact{
					ID:         fmt.Sprintf("llm-fact-%s-%d", tr.ID, i),
					Content:    factContent,
					Source:     tr.ID,
					FactKind:   factKind,
					Confidence: 0.7,
					DecayRate:  0.02,
					ValidFrom:  now,
				}
				v1Nodes = append(v1Nodes, fact)
			}
		}
	}

	// Parse patterns from the output.
	if patternsRaw, ok := outputs["patterns"]; ok {
		patternsStr := fmt.Sprintf("%v", patternsRaw)
		var patternStrings []string
		if jsonErr := json.Unmarshal([]byte(patternsStr), &patternStrings); jsonErr != nil {
			e.logger.Warn("failed to parse LLM patterns as JSON array", "error", jsonErr, "raw", patternsStr)
		} else {
			for i, patternDesc := range patternStrings {
				pattern := &Pattern{
					ID:          fmt.Sprintf("llm-pattern-%s-%d", tr.ID, i),
					Description: patternDesc,
					Occurrences: 1,
					FirstSeen:   tr.CreatedAt,
					LastSeen:    now,
					Confidence:  0.6,
					DecayRate:   0.02,
				}
				v1Nodes = append(v1Nodes, pattern)
			}
		}
	}

	return v1Nodes, v1Edges, nil
}
