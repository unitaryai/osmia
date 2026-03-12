package diagnosis

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
)

// dependencyPatterns are substrings in event content that indicate a
// missing dependency.
var dependencyPatterns = []string{
	"module not found",
	"import error",
	"cannot find module",
	"no such module",
	"package not found",
	"could not resolve",
	"ModuleNotFoundError",
	"cannot resolve module",
	"unresolved import",
}

// permissionPatterns are substrings that indicate a permissions failure.
var permissionPatterns = []string{
	"permission denied",
	"EACCES",
	"EPERM",
	"access denied",
	"forbidden",
	"read-only file system",
}

// infraPatterns are substrings that indicate infrastructure failures.
var infraPatterns = []string{
	"OOMKilled",
	"out of memory",
	"timeout",
	"deadline exceeded",
	"connection refused",
	"network unreachable",
	"no space left on device",
	"killed",
}

// Analyser performs rule-based failure classification on a DiagnosisInput.
type Analyser struct {
	logger *slog.Logger
}

// NewAnalyser creates a new Analyser.
func NewAnalyser(logger *slog.Logger) *Analyser {
	return &Analyser{logger: logger}
}

// Analyse classifies the failure mode of a failed TaskRun using rule-based
// pattern matching against events, watchdog reason, and result data.
func (a *Analyser) Analyse(_ context.Context, input DiagnosisInput) (*Diagnosis, error) {
	eventTexts := extractEventTexts(input.Events)
	allText := strings.Join(eventTexts, "\n")
	if input.WatchdogReason != "" {
		allText += "\n" + sanitiseForPrompt(input.WatchdogReason, 2000)
	}
	if input.Result != nil && input.Result.Summary != "" {
		allText += "\n" + sanitiseForPrompt(input.Result.Summary, 2000)
	}

	// Check each failure pattern in priority order.

	// 1. Infrastructure failures (highest priority — retrying won't help if infra is broken).
	if evidence := matchPatterns(allText, infraPatterns); len(evidence) > 0 {
		return a.buildDiagnosis(InfraFailure, 0.85, evidence), nil
	}

	// 2. Permission blocked.
	if evidence := matchPatterns(allText, permissionPatterns); len(evidence) > 0 {
		return a.buildDiagnosis(PermissionBlocked, 0.80, evidence), nil
	}

	// 3. Dependency missing.
	if evidence := matchPatterns(allText, dependencyPatterns); len(evidence) > 0 {
		return a.buildDiagnosis(DependencyMissing, 0.75, evidence), nil
	}

	// 4. Model confusion: high oscillation in tool calls (undo/redo pattern).
	if evidence := a.detectModelConfusion(input); len(evidence) > 0 {
		return a.buildDiagnosis(ModelConfusion, 0.70, evidence), nil
	}

	// 5. Scope creep: too many files changed relative to what is reasonable.
	if evidence := a.detectScopeCreep(input); len(evidence) > 0 {
		return a.buildDiagnosis(ScopeCreep, 0.65, evidence), nil
	}

	// 6. Test misunderstanding: test files edited when they shouldn't be.
	if evidence := a.detectTestMisunderstanding(input); len(evidence) > 0 {
		return a.buildDiagnosis(TestMisunderstanding, 0.65, evidence), nil
	}

	// 7. Wrong approach: generic fallback for agent editing wrong files.
	if evidence := a.detectWrongApproach(input); len(evidence) > 0 {
		return a.buildDiagnosis(WrongApproach, 0.50, evidence), nil
	}

	// 8. Unknown: no pattern matched.
	return a.buildDiagnosis(Unknown, 0.30, []string{"no recognised failure pattern matched"}), nil
}

// buildDiagnosis constructs a Diagnosis with the given parameters.
func (a *Analyser) buildDiagnosis(mode FailureMode, confidence float64, evidence []string) *Diagnosis {
	a.logger.Info("failure diagnosed",
		"mode", string(mode),
		"confidence", confidence,
		"evidence_count", len(evidence),
	)
	return &Diagnosis{
		Mode:        mode,
		Confidence:  confidence,
		Evidence:    evidence,
		DiagnosedAt: time.Now(),
	}
}

// detectModelConfusion checks for oscillating tool call patterns.
func (a *Analyser) detectModelConfusion(input DiagnosisInput) []string {
	if input.TaskRun == nil {
		return nil
	}

	var evidence []string

	// High consecutive identical calls suggest confusion.
	if input.TaskRun.ConsecutiveIdenticalTools > 5 {
		evidence = append(evidence, "high consecutive identical tool calls detected")
	}

	// Check for undo/redo patterns in events.
	undoRedoCount := 0
	var prevTool string
	for _, ev := range input.Events {
		if tc, ok := ev.Parsed.(*agentstream.ToolCallEvent); ok {
			// Detect alternating pattern (e.g. edit, undo, edit, undo).
			if prevTool != "" && tc.Tool != prevTool {
				undoRedoCount++
			}
			prevTool = tc.Tool
		}
	}
	if undoRedoCount > 10 {
		evidence = append(evidence, "high oscillation in tool calls suggesting undo/redo pattern")
	}

	return evidence
}

// detectScopeCreep checks if too many files were changed.
func (a *Analyser) detectScopeCreep(input DiagnosisInput) []string {
	if input.TaskRun == nil {
		return nil
	}

	// Heuristic: more than 20 files changed is likely scope creep for
	// typical maintenance tasks.
	if input.TaskRun.FilesChanged > 20 {
		return []string{"excessive number of files changed relative to task scope"}
	}

	return nil
}

// detectTestMisunderstanding checks if test files were edited.
func (a *Analyser) detectTestMisunderstanding(input DiagnosisInput) []string {
	for _, ev := range input.Events {
		if tc, ok := ev.Parsed.(*agentstream.ToolCallEvent); ok {
			argsStr := string(tc.Args)
			if (tc.Tool == "Edit" || tc.Tool == "Write") &&
				(strings.Contains(argsStr, "_test.go") ||
					strings.Contains(argsStr, ".test.") ||
					strings.Contains(argsStr, "/tests/") ||
					strings.Contains(argsStr, "test_")) {
				return []string{"test files were edited during task execution"}
			}
		}
	}
	return nil
}

// detectWrongApproach is a generic fallback that fires when the result
// indicates failure but no more specific pattern matched.
func (a *Analyser) detectWrongApproach(input DiagnosisInput) []string {
	if input.Result != nil && !input.Result.Success && input.Result.Summary != "" {
		return []string{"task failed: " + truncate(input.Result.Summary, 200)}
	}
	return nil
}

// matchPatterns checks if any of the given patterns appear in the text.
// Returns the matched patterns as evidence.
func matchPatterns(text string, patterns []string) []string {
	lower := strings.ToLower(text)
	var matches []string
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			matches = append(matches, "matched pattern: "+p)
		}
	}
	return matches
}

// extractEventTexts extracts textual content from stream events for
// pattern matching.
func extractEventTexts(events []*agentstream.StreamEvent) []string {
	var texts []string
	for _, ev := range events {
		switch parsed := ev.Parsed.(type) {
		case *agentstream.ContentDeltaEvent:
			texts = append(texts, parsed.Content)
		case *agentstream.ResultEvent:
			texts = append(texts, parsed.Summary)
		case *agentstream.ToolCallEvent:
			texts = append(texts, string(parsed.Args))
		}
	}
	return texts
}

// truncate shortens a string to the given maximum length.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
