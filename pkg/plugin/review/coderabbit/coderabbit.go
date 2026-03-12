// Package coderabbit provides a built-in review.Backend implementation that
// integrates with the CodeRabbit API for automated code review and quality
// gate checks. If no API key is provided, it acts as a pass-through that
// always passes.
package coderabbit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/unitaryai/osmia/pkg/plugin/review"
)

const (
	defaultAPIURL = "https://api.coderabbit.ai/v1"
	backendName   = "coderabbit"
)

// Compile-time check that CodeRabbitBackend implements review.Backend.
var _ review.Backend = (*CodeRabbitBackend)(nil)

// CodeRabbitBackend implements review.Backend using the CodeRabbit API.
// When no API key is configured, it acts as a pass-through that always
// returns a passing gate result.
type CodeRabbitBackend struct {
	apiKey string
	apiURL string
	client *http.Client
	logger *slog.Logger
}

// Option is a functional option for configuring a CodeRabbitBackend.
type Option func(*CodeRabbitBackend)

// WithAPIURL sets a custom API URL for the CodeRabbit backend.
func WithAPIURL(u string) Option {
	return func(b *CodeRabbitBackend) {
		b.apiURL = strings.TrimRight(u, "/")
	}
}

// WithHTTPClient sets a custom http.Client for the backend.
func WithHTTPClient(c *http.Client) Option {
	return func(b *CodeRabbitBackend) {
		b.client = c
	}
}

// NewCodeRabbitBackend creates a new CodeRabbit review backend.
// If apiKey is empty, the backend acts as a pass-through.
func NewCodeRabbitBackend(apiKey string, logger *slog.Logger, opts ...Option) *CodeRabbitBackend {
	b := &CodeRabbitBackend{
		apiKey: apiKey,
		apiURL: defaultAPIURL,
		client: http.DefaultClient,
		logger: logger,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// crReviewRequest is the request payload sent to the CodeRabbit API.
type crReviewRequest struct {
	Diff      string `json:"diff"`
	TaskRunID string `json:"task_run_id"`
}

// crReviewResponse is the response payload from the CodeRabbit API.
type crReviewResponse struct {
	Passed   bool              `json:"passed"`
	Summary  string            `json:"summary"`
	Comments []crComment       `json:"comments,omitempty"`
	Security []crSecurityIssue `json:"security,omitempty"`
}

// crComment is a single review comment from CodeRabbit.
type crComment struct {
	FilePath string `json:"file_path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Body     string `json:"body"`
	Severity string `json:"severity"` // "info", "warning", "error", "critical"
}

// crSecurityIssue is a security finding from CodeRabbit.
type crSecurityIssue struct {
	RuleID      string `json:"rule_id"`
	Description string `json:"description"`
	FilePath    string `json:"file_path,omitempty"`
	Line        int    `json:"line,omitempty"`
	Severity    string `json:"severity"` // "info", "low", "medium", "high", "critical"
}

// ReviewDiff submits a diff for review and returns the review outcome.
// If no API key is configured, a pass-through result is returned.
func (b *CodeRabbitBackend) ReviewDiff(ctx context.Context, taskRunID string, diff string) (*review.GateResult, error) {
	if b.apiKey == "" {
		b.logger.Info("no API key configured, returning pass-through result", "task_run_id", taskRunID)
		return &review.GateResult{
			Passed:  true,
			Summary: "pass-through: no CodeRabbit API key configured",
		}, nil
	}

	reqPayload := crReviewRequest{
		Diff:      diff,
		TaskRunID: taskRunID,
	}

	data, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshalling review request: %w", err)
	}

	apiURL := fmt.Sprintf("%s/reviews", b.apiURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing review request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var crResp crReviewResponse
	if err := json.NewDecoder(resp.Body).Decode(&crResp); err != nil {
		return nil, fmt.Errorf("decoding review response: %w", err)
	}

	result := &review.GateResult{
		Passed:  crResp.Passed,
		Summary: crResp.Summary,
	}

	for _, c := range crResp.Comments {
		result.Comments = append(result.Comments, review.ReviewComment{
			FilePath: c.FilePath,
			Line:     c.Line,
			Body:     c.Body,
			Severity: mapCommentSeverity(c.Severity),
		})
	}

	for _, s := range crResp.Security {
		result.SecurityFindings = append(result.SecurityFindings, review.SecurityFinding{
			RuleID:      s.RuleID,
			Description: s.Description,
			FilePath:    s.FilePath,
			Line:        s.Line,
			Severity:    mapSecuritySeverity(s.Severity),
		})
	}

	b.logger.Info("review completed",
		"task_run_id", taskRunID,
		"passed", result.Passed,
		"comments", len(result.Comments),
		"security_findings", len(result.SecurityFindings),
	)

	return result, nil
}

// Name returns the backend identifier.
func (b *CodeRabbitBackend) Name() string {
	return backendName
}

// InterfaceVersion returns the review interface version implemented.
func (b *CodeRabbitBackend) InterfaceVersion() int {
	return review.InterfaceVersion
}

// mapCommentSeverity maps CodeRabbit comment severity levels to Osmia levels.
// CodeRabbit may return "critical" which we map to "error".
func mapCommentSeverity(severity string) string {
	switch strings.ToLower(severity) {
	case "info":
		return "info"
	case "warning":
		return "warning"
	case "error", "critical":
		return "error"
	default:
		return "info"
	}
}

// mapSecuritySeverity maps CodeRabbit security severity levels to Osmia levels.
// CodeRabbit may return "info" which we map to "low".
func mapSecuritySeverity(severity string) string {
	switch strings.ToLower(severity) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "critical":
		return "critical"
	case "info":
		return "low"
	default:
		return "low"
	}
}
