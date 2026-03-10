// Package diagnosis implements causal failure diagnosis and self-healing
// retry logic. When a TaskRun fails, the analyser classifies the failure
// mode using rule-based pattern matching, and the prescriber generates
// safe, template-based corrective instructions for the retry attempt.
package diagnosis

import (
	"time"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

// FailureMode identifies the class of failure diagnosed from a TaskRun.
type FailureMode string

const (
	// WrongApproach indicates the agent edited wrong files or produced
	// changes unrelated to the task.
	WrongApproach FailureMode = "wrong_approach"
	// DependencyMissing indicates a missing module, package, or import.
	DependencyMissing FailureMode = "dependency_missing"
	// TestMisunderstanding indicates the agent incorrectly modified tests
	// or misunderstood test expectations.
	TestMisunderstanding FailureMode = "test_misunderstanding"
	// ScopeCreep indicates the agent changed too many files relative to
	// the task description.
	ScopeCreep FailureMode = "scope_creep"
	// PermissionBlocked indicates the agent was blocked by file system or
	// network permissions.
	PermissionBlocked FailureMode = "permission_blocked"
	// ModelConfusion indicates high oscillation in tool calls such as
	// repeated undo/redo patterns.
	ModelConfusion FailureMode = "model_confusion"
	// InfraFailure indicates infrastructure-level issues such as OOMKilled,
	// timeout, or network errors.
	InfraFailure FailureMode = "infra_failure"
	// Unknown indicates no recognised failure pattern was matched.
	Unknown FailureMode = "unknown"
)

// AllFailureModes is a convenience slice of all defined failure modes.
var AllFailureModes = []FailureMode{
	WrongApproach,
	DependencyMissing,
	TestMisunderstanding,
	ScopeCreep,
	PermissionBlocked,
	ModelConfusion,
	InfraFailure,
	Unknown,
}

// Diagnosis holds the result of analysing a failed TaskRun.
type Diagnosis struct {
	Mode            FailureMode `json:"mode"`
	Confidence      float64     `json:"confidence"`
	Evidence        []string    `json:"evidence"`
	Prescription    string      `json:"prescription"`
	SuggestedEngine string      `json:"suggested_engine,omitempty"`
	DiagnosedAt     time.Time   `json:"diagnosed_at"`
}

// DiagnosisInput contains all the data needed to diagnose a failure.
type DiagnosisInput struct {
	TaskRun        *taskrun.TaskRun
	Events         []*agentstream.StreamEvent
	WatchdogReason string
	Result         *engine.TaskResult
}
