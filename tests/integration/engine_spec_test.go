//go:build integration

// Package integration_test contains Tier 3 integration tests that verify
// engine spec generation and job builder behaviour without requiring a
// live Kubernetes cluster or external API calls.
package integration_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/aider"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	"github.com/unitaryai/osmia/pkg/engine/cline"
	"github.com/unitaryai/osmia/pkg/engine/codex"
	"github.com/unitaryai/osmia/pkg/engine/opencode"
)

// standardTask is the canonical task used across all engine spec tests.
var standardTask = engine.Task{
	ID:          "test-123",
	TicketID:    "TICKET-1",
	Title:       "Test task",
	Description: "Test description",
	RepoURL:     "https://github.com/org/repo",
}

// TestAllEnginesProduceValidSpecs verifies that every engine returns a
// well-formed ExecutionSpec when called with a standard task and empty
// EngineConfig.
func TestAllEnginesProduceValidSpecs(t *testing.T) {
	t.Parallel()

	engines := []engine.ExecutionEngine{
		claudecode.New(),
		codex.New(),
		aider.New(),
		opencode.New(),
		cline.New(),
	}

	for _, eng := range engines {
		eng := eng // capture range variable
		t.Run(eng.Name(), func(t *testing.T) {
			t.Parallel()

			spec, err := eng.BuildExecutionSpec(standardTask, engine.EngineConfig{})
			require.NoError(t, err)
			require.NotNil(t, spec, "spec must not be nil")

			assert.NotEmpty(t, spec.Image, "Image must not be empty")
			assert.NotEmpty(t, spec.Command, "Command must not be empty")
			assert.Greater(t, spec.ActiveDeadlineSeconds, 0, "ActiveDeadlineSeconds must be positive")
			assert.NotEmpty(t, spec.SecretEnv, "SecretEnv must not be empty")

			// Verify at least one volume is mounted at the workspace path.
			hasWorkspace := false
			for _, v := range spec.Volumes {
				if v.MountPath == "/workspace" {
					hasWorkspace = true
					break
				}
			}
			assert.True(t, hasWorkspace, "spec must have a volume mounted at /workspace")
		})
	}
}

// TestAllEnginesHaveUniqueNames verifies that all engine Name() values are
// distinct, preventing accidental collisions in the engine registry.
func TestAllEnginesHaveUniqueNames(t *testing.T) {
	t.Parallel()

	engines := []engine.ExecutionEngine{
		claudecode.New(),
		codex.New(),
		aider.New(),
		opencode.New(),
		cline.New(),
	}

	seen := make(map[string]struct{}, len(engines))
	for _, eng := range engines {
		name := eng.Name()
		assert.NotEmpty(t, name, "engine name must not be empty")
		_, duplicate := seen[name]
		assert.False(t, duplicate, "duplicate engine name: %q", name)
		seen[name] = struct{}{}
	}
}

// TestClineEngineProviderVariants verifies that each Cline provider maps to
// the correct secret environment variables, and that WithMCPEnabled(true)
// appends the --mcp flag to the command.
func TestClineEngineProviderVariants(t *testing.T) {
	t.Parallel()

	providerCases := []struct {
		name           string
		opts           []cline.Option
		wantSecretKeys []string
	}{
		{
			name:           "anthropic_default",
			opts:           nil,
			wantSecretKeys: []string{"ANTHROPIC_API_KEY"},
		},
		{
			name:           "openai",
			opts:           []cline.Option{cline.WithProvider(cline.ProviderOpenAI)},
			wantSecretKeys: []string{"OPENAI_API_KEY"},
		},
		{
			name:           "google",
			opts:           []cline.Option{cline.WithProvider(cline.ProviderGoogle)},
			wantSecretKeys: []string{"GOOGLE_API_KEY"},
		},
		{
			name:           "bedrock",
			opts:           []cline.Option{cline.WithProvider(cline.ProviderBedrock)},
			wantSecretKeys: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"},
		},
	}

	for _, tc := range providerCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			eng := cline.New(tc.opts...)
			spec, err := eng.BuildExecutionSpec(standardTask, engine.EngineConfig{})
			require.NoError(t, err)
			require.NotNil(t, spec)

			for _, key := range tc.wantSecretKeys {
				assert.Contains(t, spec.SecretEnv, key,
					"SecretEnv must contain key %q for provider %q", key, tc.name)
			}
		})
	}

	// Verify that WithMCPEnabled(true) adds --mcp to the command.
	t.Run("mcp_enabled", func(t *testing.T) {
		t.Parallel()

		eng := cline.New(cline.WithMCPEnabled(true))
		spec, err := eng.BuildExecutionSpec(standardTask, engine.EngineConfig{})
		require.NoError(t, err)
		require.NotNil(t, spec)

		assert.Contains(t, spec.Command, "--mcp", "command must contain --mcp when MCP is enabled")
	})
}
