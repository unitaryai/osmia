// Package review defines the ReviewBackend interface for integrating
// with code review tools (CodeRabbit, native review, etc). The review
// backend is used by the optional quality gate to review agent output
// before finalisation.
package review

import (
	"context"
)

// InterfaceVersion is the current version of the ReviewBackend interface.
const InterfaceVersion = 1

// GateResult represents the outcome of a quality gate review.
type GateResult struct {
	Passed           bool              `json:"passed"`
	Comments         []ReviewComment   `json:"comments,omitempty"`
	SecurityFindings []SecurityFinding `json:"security_findings,omitempty"`
	Summary          string            `json:"summary"`
}

// ReviewComment is a single comment from the review process, optionally
// associated with a specific file and line.
type ReviewComment struct {
	FilePath string `json:"file_path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Body     string `json:"body"`
	Severity string `json:"severity"` // "info", "warning", "error"
}

// SecurityFinding represents a security issue found during review.
type SecurityFinding struct {
	RuleID      string `json:"rule_id"`
	Description string `json:"description"`
	FilePath    string `json:"file_path,omitempty"`
	Line        int    `json:"line,omitempty"`
	Severity    string `json:"severity"` // "low", "medium", "high", "critical"
}

// Backend is the interface that review backends must implement.
// It provides operations for reviewing diffs and retrieving gate results.
type Backend interface {
	// ReviewDiff submits a diff for review and returns the review outcome.
	// The diff is the unified diff output from the agent's work. The
	// taskRunID is used for correlation and audit logging.
	ReviewDiff(ctx context.Context, taskRunID string, diff string) (*GateResult, error)

	// Name returns the unique identifier for this backend (e.g. "coderabbit", "native").
	Name() string

	// InterfaceVersion returns the version of the ReviewBackend interface
	// that this backend implements.
	InterfaceVersion() int
}
