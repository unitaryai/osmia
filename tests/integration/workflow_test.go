//go:build integration

// Package integration_test contains Tier 3 integration tests that verify
// workflow-driven prompt construction via the PromptBuilder, ensuring that
// TDD, review-first, and empty workflow modes produce correct prompt
// content and section ordering.
package integration_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/promptbuilder"
	"github.com/unitaryai/osmia/pkg/engine"
)

// workflowTestTask is the canonical task used across workflow tests.
var workflowTestTask = engine.Task{
	ID:          "workflow-1",
	TicketID:    "TICKET-W1",
	Title:       "Workflow test task",
	Description: "Verify workflow instructions in prompt output.",
	RepoURL:     "https://github.com/org/repo",
}

// workflowProfiles returns profiles keyed by task type for workflow tests.
func workflowProfiles(workflow string) map[string]promptbuilder.TaskProfile {
	return map[string]promptbuilder.TaskProfile{
		"test-type": {
			Workflow:              workflow,
			AllowedFilePatterns:   []string{"*.go"},
			BlockedFilePatterns:   []string{".env"},
			BlockedCommands:       []string{"rm -rf"},
			MaxCostPerJob:         5.0,
			MaxJobDurationMinutes: 30,
		},
	}
}

// TestWorkflowTDDPrompt verifies that building a prompt with the "tdd"
// workflow produces TDD-specific instructions including the strict step
// ordering for test-driven development.
func TestWorkflowTDDPrompt(t *testing.T) {
	t.Parallel()

	pb, err := promptbuilder.New()
	require.NoError(t, err)

	profiles := workflowProfiles("tdd")

	result, err := pb.BuildPromptWithProfile(
		workflowTestTask,
		"Do not modify CI configuration files.",
		"claude-code",
		"test-type",
		profiles,
	)
	require.NoError(t, err)

	// Verify TDD workflow instructions are present.
	assert.Contains(t, result, "## Workflow: Test-Driven Development")
	assert.Contains(t, result, "Run the existing test suite")
	assert.Contains(t, result, "Write a failing test")
	assert.Contains(t, result, "Implement the minimum code needed")
	assert.Contains(t, result, "Run the full test suite")
	assert.Contains(t, result, "Refactor if needed")

	// Verify other sections are also present.
	assert.Contains(t, result, "## Description")
	assert.Contains(t, result, "## Guard Rails")
	assert.Contains(t, result, "## Task Profile Constraints")
}

// TestWorkflowReviewFirstPrompt verifies that building a prompt with the
// "review-first" workflow produces review-specific instructions.
func TestWorkflowReviewFirstPrompt(t *testing.T) {
	t.Parallel()

	pb, err := promptbuilder.New()
	require.NoError(t, err)

	profiles := workflowProfiles("review-first")

	result, err := pb.BuildPromptWithProfile(
		workflowTestTask,
		"Guard rails content.",
		"claude-code",
		"test-type",
		profiles,
	)
	require.NoError(t, err)

	// Verify review-first workflow instructions are present.
	assert.Contains(t, result, "## Workflow: Review First")
	assert.Contains(t, result, "Read and understand all relevant code")
	assert.Contains(t, result, "Identify the root cause")
	assert.Contains(t, result, "Write a summary of your findings")
	assert.Contains(t, result, "Implement the changes")
	assert.Contains(t, result, "Verify the changes work")

	// Verify other sections are also present.
	assert.Contains(t, result, "## Description")
	assert.Contains(t, result, "## Guard Rails")
}

// TestWorkflowEmptyPrompt verifies that building a prompt with an empty
// workflow produces no workflow section in the output.
func TestWorkflowEmptyPrompt(t *testing.T) {
	t.Parallel()

	pb, err := promptbuilder.New()
	require.NoError(t, err)

	profiles := workflowProfiles("")

	result, err := pb.BuildPromptWithProfile(
		workflowTestTask,
		"Guard rails content.",
		"claude-code",
		"test-type",
		profiles,
	)
	require.NoError(t, err)

	// No workflow section should be present.
	assert.NotContains(t, result, "## Workflow:")
	assert.NotContains(t, result, "Test-Driven Development")
	assert.NotContains(t, result, "Review First")

	// Other sections should still work.
	assert.Contains(t, result, "## Description")
	assert.Contains(t, result, "## Guard Rails")
	assert.Contains(t, result, "## Task Profile Constraints")
}

// TestWorkflowOrdering verifies that workflow instructions appear in the
// correct position within the prompt: after the task description but
// before guard rails and task profile constraints.
func TestWorkflowOrdering(t *testing.T) {
	t.Parallel()

	pb, err := promptbuilder.New()
	require.NoError(t, err)

	profiles := workflowProfiles("tdd")

	result, err := pb.BuildPromptWithProfile(
		workflowTestTask,
		"Do not modify CI configuration files.",
		"claude-code",
		"test-type",
		profiles,
	)
	require.NoError(t, err)

	descIdx := strings.Index(result, "## Description")
	workflowIdx := strings.Index(result, "## Workflow: Test-Driven Development")
	guardRailsIdx := strings.Index(result, "## Guard Rails")
	profileIdx := strings.Index(result, "## Task Profile Constraints")
	engineIdx := strings.Index(result, "## Engine")

	require.NotEqual(t, -1, descIdx, "description section must be present")
	require.NotEqual(t, -1, workflowIdx, "workflow section must be present")
	require.NotEqual(t, -1, guardRailsIdx, "guard rails section must be present")
	require.NotEqual(t, -1, profileIdx, "task profile section must be present")
	require.NotEqual(t, -1, engineIdx, "engine section must be present")

	// Verify ordering: description < workflow < guard rails < profile < engine.
	assert.Less(t, descIdx, workflowIdx, "workflow must appear after description")
	assert.Less(t, workflowIdx, guardRailsIdx, "workflow must appear before guard rails")
	assert.Less(t, guardRailsIdx, profileIdx, "guard rails must appear before task profile")
	assert.Less(t, profileIdx, engineIdx, "task profile must appear before engine")
}
