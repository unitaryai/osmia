package reviewpoller

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
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
//
// Non-inline comments (no file position) from authors matching the built-in
// bot list or user-provided patterns are classified as Ignore. Inline diff
// comments (with a file position) are always evaluated regardless of author,
// so actionable review feedback from bots like CodeRabbit is not lost.
type RuleBasedClassifier struct {
	summaryAuthorPatterns []*regexp.Regexp
}

// NewRuleBasedClassifier creates a new RuleBasedClassifier. The extraPatterns
// parameter accepts additional regex patterns for author usernames whose
// non-inline comments should be ignored (e.g. "^group_\\d+_bot_" for GitLab
// group bots). These are merged with the built-in botUsernames defaults.
func NewRuleBasedClassifier(extraPatterns []string) *RuleBasedClassifier {
	var patterns []*regexp.Regexp

	// Compile built-in bot usernames as literal substring patterns.
	for _, bot := range botUsernames {
		pat := regexp.MustCompile(regexp.QuoteMeta(strings.ToLower(bot)))
		patterns = append(patterns, pat)
	}

	// Compile user-provided regex patterns.
	for _, p := range extraPatterns {
		compiled, err := regexp.Compile("(?i)" + p)
		if err != nil {
			// Skip invalid patterns rather than failing hard.
			continue
		}
		patterns = append(patterns, compiled)
	}

	return &RuleBasedClassifier{summaryAuthorPatterns: patterns}
}

// prefilterBotSummary checks whether a comment should be ignored before any
// classification logic runs. Non-inline comments (no file position) from
// authors matching the given patterns are ignored — they are typically bot
// summaries, coverage reports, or initial review messages. Inline diff
// comments are never skipped so actionable review feedback is preserved.
// Returns a classified result and true if the comment should be skipped, or
// an empty result and false if classification should proceed.
func prefilterBotSummary(comment scm.ReviewComment, patterns []*regexp.Regexp) (ClassifiedComment, bool) {
	if comment.FilePath != "" {
		return ClassifiedComment{}, false
	}
	authorLower := strings.ToLower(comment.Author)
	for _, pat := range patterns {
		if pat.MatchString(authorLower) {
			return ClassifiedComment{
				ReviewComment:  comment,
				Classification: ClassificationIgnore,
				Severity:       "info",
				Reason:         fmt.Sprintf("non-inline comment by known automation account %q", comment.Author),
			}, true
		}
	}
	return ClassifiedComment{}, false
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

	// Skip non-inline comments from known bots.
	if filtered, ok := prefilterBotSummary(comment, c.summaryAuthorPatterns); ok {
		return filtered, nil
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
	module                llm.Module
	fallback              Classifier
	summaryAuthorPatterns []*regexp.Regexp
	logger                *slog.Logger
}

// NewLLMClassifier creates an LLMClassifier backed by a ChainOfThought module.
func NewLLMClassifier(client llm.Client, logger *slog.Logger, extraPatterns []string) *LLMClassifier {
	module := llm.NewChainOfThought(classifyCommentSignature, client, nil)
	fb := NewRuleBasedClassifier(extraPatterns)
	return &LLMClassifier{
		module:                module,
		fallback:              fb,
		summaryAuthorPatterns: fb.summaryAuthorPatterns,
		logger:                logger,
	}
}

// Classify uses the LLM to classify the comment, falling back on error or
// unrecognised output values. Non-inline bot comments are prefiltered before
// the LLM call to avoid wasting tokens on automated summaries.
func (c *LLMClassifier) Classify(ctx context.Context, comment scm.ReviewComment) (ClassifiedComment, error) {
	// Apply the same bot/FilePath prefilter before calling the LLM.
	if filtered, ok := prefilterBotSummary(comment, c.summaryAuthorPatterns); ok {
		return filtered, nil
	}

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
