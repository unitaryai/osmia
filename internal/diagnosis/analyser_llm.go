package diagnosis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/unitaryai/osmia/internal/llm"
)

// classifyFailureSignature defines the LLM signature for failure classification.
var classifyFailureSignature = llm.Signature{
	Name:        "ClassifyFailure",
	Description: "Classify the failure mode of a failed AI coding agent task.",
	InputFields: []llm.Field{
		{Name: "watchdog_reason", Description: "Reason reported by the progress watchdog", Type: llm.FieldTypeString, Required: false},
		{Name: "result_summary", Description: "Task result summary", Type: llm.FieldTypeString, Required: false},
		{Name: "tool_call_count", Description: "Total number of tool calls made", Type: llm.FieldTypeInt, Required: false},
		{Name: "files_changed", Description: "Number of files changed", Type: llm.FieldTypeInt, Required: false},
	},
	OutputFields: []llm.Field{
		{Name: "failure_mode", Description: "One of: wrong_approach, dependency_missing, test_misunderstanding, scope_creep, permission_blocked, model_confusion, infra_failure, unknown", Type: llm.FieldTypeString, Required: true},
		{Name: "confidence", Description: "Confidence score from 0.0 to 1.0", Type: llm.FieldTypeFloat, Required: true},
		{Name: "evidence", Description: "JSON array of evidence strings", Type: llm.FieldTypeString, Required: true},
	},
}

// LLMAnalyser classifies failure modes using an LLM, falling back to the
// rule-based Analyser when the response is invalid or an error occurs.
type LLMAnalyser struct {
	module   llm.Module
	fallback *Analyser
	logger   *slog.Logger
}

// NewLLMAnalyser creates an LLMAnalyser backed by a ChainOfThought module.
func NewLLMAnalyser(client llm.Client, fallback *Analyser, logger *slog.Logger) *LLMAnalyser {
	module := llm.NewChainOfThought(classifyFailureSignature, client, nil)
	return &LLMAnalyser{
		module:   module,
		fallback: fallback,
		logger:   logger,
	}
}

// newLLMAnalyserWithModule creates an LLMAnalyser with a provided module.
// This is package-internal and intended for testing.
func newLLMAnalyserWithModule(module llm.Module, fallback *Analyser, logger *slog.Logger) *LLMAnalyser {
	return &LLMAnalyser{
		module:   module,
		fallback: fallback,
		logger:   logger,
	}
}

// Analyse classifies the failure mode using the LLM, falling back on error
// or unknown failure mode strings.
func (a *LLMAnalyser) Analyse(ctx context.Context, input DiagnosisInput) (*Diagnosis, error) {
	inputs := make(map[string]any)

	if input.WatchdogReason != "" {
		inputs["watchdog_reason"] = sanitiseForPrompt(input.WatchdogReason, 500)
	}
	if input.Result != nil && input.Result.Summary != "" {
		inputs["result_summary"] = sanitiseForPrompt(input.Result.Summary, 1000)
	}
	if input.TaskRun != nil {
		inputs["tool_call_count"] = input.TaskRun.ToolCallsTotal
		inputs["files_changed"] = input.TaskRun.FilesChanged
	}

	outputs, err := a.module.Forward(ctx, inputs)
	if err != nil {
		a.logger.Warn("LLM analysis failed, using fallback", "error", err)
		return a.fallback.Analyse(ctx, input)
	}

	// Parse failure_mode.
	modeStr, ok := outputs["failure_mode"].(string)
	if !ok {
		a.logger.Warn("LLM returned non-string failure_mode, using fallback")
		return a.fallback.Analyse(ctx, input)
	}

	// Validate the failure mode is known.
	validMode := false
	for _, m := range AllFailureModes {
		if string(m) == modeStr {
			validMode = true
			break
		}
	}
	if !validMode {
		a.logger.Warn("LLM returned unrecognised failure mode, using fallback", "mode", modeStr)
		return a.fallback.Analyse(ctx, input)
	}

	// Parse confidence.
	conf, err := extractFloatOutput(outputs, "confidence")
	if err != nil {
		a.logger.Warn("failed to parse confidence from LLM output, using fallback", "error", err)
		return a.fallback.Analyse(ctx, input)
	}

	// Parse evidence.
	var evidence []string
	if evidenceRaw, ok := outputs["evidence"]; ok {
		evidenceStr := fmt.Sprintf("%v", evidenceRaw)
		if jsonErr := json.Unmarshal([]byte(evidenceStr), &evidence); jsonErr != nil {
			// Non-critical: use the raw string as single evidence item.
			evidence = []string{evidenceStr}
		}
	}

	return &Diagnosis{
		Mode:        FailureMode(modeStr),
		Confidence:  conf,
		Evidence:    evidence,
		DiagnosedAt: time.Now(),
	}, nil
}

// extractFloatOutput extracts a float64 from the module output map.
func extractFloatOutput(outputs map[string]any, key string) (float64, error) {
	val, ok := outputs[key]
	if !ok {
		return 0, fmt.Errorf("missing output field %q", key)
	}
	switch v := val.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, fmt.Errorf("unexpected type %T for field %q", val, key)
	}
}
