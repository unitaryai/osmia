package reviewpoller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/unitaryai/osmia/internal/llm"
	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// Classifier classifies a review comment into ignore, informational, or
// requires-action, and assigns a severity level.
type Classifier interface {
	Classify(ctx context.Context, comment scm.ReviewComment) (ClassifiedComment, error)
}

// botUsernames are username substrings that identify known automation accounts
// whose comments should be ignored.
var botUsernames = []string{
	"coderabbit-ai",
	"github-actions[bot]",
	"github-actions",
	"dependabot[bot]",
	"dependabot",
	"copilot",
	"gemini-code-assist",
	"osmia",
}

// informationalKeywords are phrases that indicate a non-actionable comment.
var informationalKeywords = []string{
	"lgtm",
	"looks good",
	"nice",
	"great",
	"approved",
	"thank you",
	"thanks",
}

// warningKeywords are phrases that indicate actionable feedback at warning level.
var warningKeywords = []string{
	"please",
	"should",
	"needs to",
	"must",
	"consider",
	"suggest",
	"recommend",
}

// errorKeywords are phrases that indicate actionable feedback at error level.
var errorKeywords = []string{
	"fix",
	"bug",
	"wrong",
	"incorrect",
	"broken",
	"fails",
	"error",
	"crash",
}

// RuleBasedClassifier classifies review comments using simple keyword and
// author pattern matching, with no external dependencies.
type RuleBasedClassifier struct{}

// NewRuleBasedClassifier creates a new RuleBasedClassifier.
func NewRuleBasedClassifier() *RuleBasedClassifier {
	return &RuleBasedClassifier{}
}

// Classify applies rule-based heuristics to determine the comment classification.
func (c *RuleBasedClassifier) Classify(_ context.Context, comment scm.ReviewComment) (ClassifiedComment, error) {
	result := ClassifiedComment{
		ReviewComment: comment,
	}

	// Ignore empty bodies.
	if strings.TrimSpace(comment.Body) == "" {
		result.Classification = ClassificationIgnore
		result.Severity = "info"
		result.Reason = "empty comment body"
		return result, nil
	}

	// Ignore known bot accounts.
	authorLower := strings.ToLower(comment.Author)
	for _, bot := range botUsernames {
		if strings.Contains(authorLower, strings.ToLower(bot)) {
			result.Classification = ClassificationIgnore
			result.Severity = "info"
			result.Reason = fmt.Sprintf("comment by known automation account %q", comment.Author)
			return result, nil
		}
	}

	bodyLower := strings.ToLower(comment.Body)

	// Check for error-level actionable keywords first.
	for _, kw := range errorKeywords {
		if strings.Contains(bodyLower, kw) {
			result.Classification = ClassificationRequiresAction
			result.Severity = "error"
			result.Reason = fmt.Sprintf("comment contains actionable keyword %q", kw)
			return result, nil
		}
	}

	// Check for warning-level actionable keywords.
	for _, kw := range warningKeywords {
		if strings.Contains(bodyLower, kw) {
			result.Classification = ClassificationRequiresAction
			result.Severity = "warning"
			result.Reason = fmt.Sprintf("comment contains actionable keyword %q", kw)
			return result, nil
		}
	}

	// Check for informational keywords.
	for _, kw := range informationalKeywords {
		if strings.Contains(bodyLower, kw) {
			result.Classification = ClassificationInformational
			result.Severity = "info"
			result.Reason = fmt.Sprintf("comment contains informational keyword %q", kw)
			return result, nil
		}
	}

	// Default: treat as informational rather than silently ignoring.
	result.Classification = ClassificationInformational
	result.Severity = "info"
	result.Reason = "no actionable keyword pattern matched"
	return result, nil
}

// classifyCommentSignature is the LLM signature for comment classification.
var classifyCommentSignature = llm.Signature{
	Name:        "ClassifyReviewComment",
	Description: "Classify a pull request review comment to determine whether it requires action.",
	InputFields: []llm.Field{
		{Name: "author", Description: "Username of the comment author", Type: llm.FieldTypeString, Required: true},
		{Name: "body", Description: "Text body of the review comment", Type: llm.FieldTypeString, Required: true},
		{Name: "file_path", Description: "File path the comment is attached to, or empty for general comments", Type: llm.FieldTypeString, Required: false},
	},
	OutputFields: []llm.Field{
		{Name: "classification", Description: "One of: ignore, informational, requires_action", Type: llm.FieldTypeString, Required: true},
		{Name: "severity", Description: "One of: info, warning, error", Type: llm.FieldTypeString, Required: true},
		{Name: "reason", Description: "Brief explanation of the classification decision", Type: llm.FieldTypeString, Required: true},
	},
}

// LLMClassifier classifies review comments using an LLM, falling back to
// RuleBasedClassifier when the LLM response is invalid or an error occurs.
type LLMClassifier struct {
	module   llm.Module
	fallback Classifier
	logger   *slog.Logger
}

// NewLLMClassifier creates an LLMClassifier backed by a ChainOfThought module.
func NewLLMClassifier(client llm.Client, logger *slog.Logger) *LLMClassifier {
	module := llm.NewChainOfThought(classifyCommentSignature, client, nil)
	return &LLMClassifier{
		module:   module,
		fallback: NewRuleBasedClassifier(),
		logger:   logger,
	}
}

// Classify uses the LLM to classify the comment, falling back on error or
// unrecognised output values.
func (c *LLMClassifier) Classify(ctx context.Context, comment scm.ReviewComment) (ClassifiedComment, error) {
	inputs := map[string]any{
		"author": comment.Author,
		"body":   comment.Body,
	}
	if comment.FilePath != "" {
		inputs["file_path"] = comment.FilePath
	}

	outputs, err := c.module.Forward(ctx, inputs)
	if err != nil {
		c.logger.Warn("LLM comment classification failed, using fallback", "error", err)
		return c.fallback.Classify(ctx, comment)
	}

	classStr, _ := outputs["classification"].(string)
	sevStr, _ := outputs["severity"].(string)
	reason, _ := outputs["reason"].(string)

	var cls Classification
	switch classStr {
	case "ignore":
		cls = ClassificationIgnore
	case "informational":
		cls = ClassificationInformational
	case "requires_action":
		cls = ClassificationRequiresAction
	default:
		c.logger.Warn("LLM returned unrecognised classification, using fallback", "classification", classStr)
		return c.fallback.Classify(ctx, comment)
	}

	if sevStr != "info" && sevStr != "warning" && sevStr != "error" {
		c.logger.Warn("LLM returned unrecognised severity, using fallback", "severity", sevStr)
		return c.fallback.Classify(ctx, comment)
	}

	return ClassifiedComment{
		ReviewComment:  comment,
		Classification: cls,
		Severity:       sevStr,
		Reason:         reason,
	}, nil
}
