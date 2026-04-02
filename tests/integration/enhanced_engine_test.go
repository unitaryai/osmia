//go:build integration

// Package integration_test contains Tier 3 integration tests that verify
// enhanced Claude Code engine features including structured output mode,
// fallback model, tool control, and streaming flag construction.
package integration_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

// enhancedEngineTask is the canonical task used across enhanced engine tests.
var enhancedEngineTask = engine.Task{
	ID:          "enhanced-1",
	TicketID:    "TICKET-E1",
	Title:       "Enhanced engine test task",
	Description: "Verify enhanced engine flag construction.",
	RepoURL:     "https://github.com/org/repo",
}

// TestEnhancedEngineStructuredOutput verifies that building a spec with a
// JSON schema set produces the correct --output-format stream-json and
// --json-schema flags.
func TestEnhancedEngineStructuredOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []claudecode.Option
		config    engine.EngineConfig
		wantFlags []string
		denyFlags []string
	}{
		{
			name: "json schema from config enables stream-json and schema flag",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				JSONSchema:     `{"type":"object","properties":{"success":{"type":"boolean"}}}`,
			},
			wantFlags: []string{"--output-format", "stream-json", "--json-schema", "--verbose"},
		},
		{
			name: "json schema from engine option enables stream-json and schema flag",
			opts: []claudecode.Option{claudecode.WithJSONSchema(claudecode.DefaultTaskResultSchema)},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			wantFlags: []string{"--output-format", "stream-json", "--json-schema", "--verbose"},
		},
		{
			name: "config json schema overrides engine option",
			opts: []claudecode.Option{claudecode.WithJSONSchema(`{"type":"string"}`)},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				JSONSchema:     `{"type":"object"}`,
			},
			wantFlags: []string{"--json-schema", `{"type":"object"}`},
			denyFlags: []string{`{"type":"string"}`},
		},
		{
			name: "no explicit schema falls back to DefaultTaskResultSchema",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			wantFlags: []string{"--output-format", "stream-json", "--verbose", "--json-schema", claudecode.DefaultTaskResultSchema},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eng := claudecode.New(tt.opts...)
			spec, err := eng.BuildExecutionSpec(enhancedEngineTask, tt.config)
			require.NoError(t, err)
			require.NotNil(t, spec)

			for _, flag := range tt.wantFlags {
				assert.Contains(t, spec.Command, flag, "command should contain %q", flag)
			}
			for _, flag := range tt.denyFlags {
				assert.NotContains(t, spec.Command, flag, "command should not contain %q", flag)
			}
		})
	}
}

// TestEnhancedEngineFallbackModel verifies that the --fallback-model flag
// is correctly included when a fallback model is configured.
func TestEnhancedEngineFallbackModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []claudecode.Option
		config    engine.EngineConfig
		wantFlags []string
		denyFlags []string
	}{
		{
			name: "fallback model from config",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				FallbackModel:  "haiku",
			},
			wantFlags: []string{"--fallback-model", "haiku"},
		},
		{
			name: "fallback model from engine option",
			opts: []claudecode.Option{claudecode.WithFallbackModel("sonnet")},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			wantFlags: []string{"--fallback-model", "sonnet"},
		},
		{
			name: "config fallback overrides engine option",
			opts: []claudecode.Option{claudecode.WithFallbackModel("sonnet")},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				FallbackModel:  "haiku",
			},
			wantFlags: []string{"--fallback-model", "haiku"},
			denyFlags: []string{"sonnet"},
		},
		{
			name: "no fallback model omits flag",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			denyFlags: []string{"--fallback-model"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eng := claudecode.New(tt.opts...)
			spec, err := eng.BuildExecutionSpec(enhancedEngineTask, tt.config)
			require.NoError(t, err)
			require.NotNil(t, spec)

			for _, flag := range tt.wantFlags {
				assert.Contains(t, spec.Command, flag, "command should contain %q", flag)
			}
			for _, flag := range tt.denyFlags {
				assert.NotContains(t, spec.Command, flag, "command should not contain %q", flag)
			}
		})
	}
}

// TestEnhancedEngineToolControl verifies that --allowedTools and
// --disallowedTools flags are correctly constructed from whitelist and
// blacklist configurations.
func TestEnhancedEngineToolControl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []claudecode.Option
		config    engine.EngineConfig
		wantFlags []string
		denyFlags []string
	}{
		{
			name: "tool whitelist from config",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				ToolWhitelist:  []string{"Read", "Write", "Bash"},
			},
			wantFlags: []string{"--allowedTools", "Read,Write,Bash"},
		},
		{
			name: "tool whitelist from engine option",
			opts: []claudecode.Option{claudecode.WithToolWhitelist([]string{"Read", "Grep"})},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			wantFlags: []string{"--allowedTools", "Read,Grep"},
		},
		{
			name: "config whitelist overrides engine option",
			opts: []claudecode.Option{claudecode.WithToolWhitelist([]string{"Read", "Grep"})},
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				ToolWhitelist:  []string{"Bash", "Write"},
			},
			wantFlags: []string{"--allowedTools", "Bash,Write"},
			denyFlags: []string{"Read,Grep"},
		},
		{
			name: "tool blacklist from config",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
				ToolBlacklist:  []string{"Bash", "NotebookEdit"},
			},
			wantFlags: []string{"--disallowedTools", "Bash,NotebookEdit"},
		},
		{
			name: "empty lists omit flags",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			denyFlags: []string{"--allowedTools", "--disallowedTools"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eng := claudecode.New(tt.opts...)
			spec, err := eng.BuildExecutionSpec(enhancedEngineTask, tt.config)
			require.NoError(t, err)
			require.NotNil(t, spec)

			for _, flag := range tt.wantFlags {
				assert.Contains(t, spec.Command, flag, "command should contain %q", flag)
			}
			for _, flag := range tt.denyFlags {
				assert.NotContains(t, spec.Command, flag, "command should not contain %q", flag)
			}
		})
	}
}

// TestEnhancedEngineCombinedFlags verifies that when ALL enhanced options
// are set simultaneously, the complete command is constructed correctly
// with no flag conflicts or missing entries.
func TestEnhancedEngineCombinedFlags(t *testing.T) {
	t.Parallel()

	eng := claudecode.New(
		claudecode.WithFallbackModel("default-fallback"),
		claudecode.WithToolWhitelist([]string{"Read"}),
		claudecode.WithJSONSchema(`{"type":"string"}`),
	)

	config := engine.EngineConfig{
		TimeoutSeconds:     3600,
		FallbackModel:      "haiku",
		JSONSchema:         claudecode.DefaultTaskResultSchema,
		AppendSystemPrompt: "Be careful with production data.",
		ToolWhitelist:      []string{"Read", "Write", "Bash"},
		ToolBlacklist:      []string{"NotebookEdit"},
		StreamingEnabled:   true,
	}

	spec, err := eng.BuildExecutionSpec(enhancedEngineTask, config)
	require.NoError(t, err)
	require.NotNil(t, spec)

	// Streaming / structured output flags.
	assert.Contains(t, spec.Command, "--output-format")
	assert.Contains(t, spec.Command, "stream-json")
	assert.Contains(t, spec.Command, "--verbose")
	assert.Contains(t, spec.Command, "--json-schema")
	assert.Contains(t, spec.Command, claudecode.DefaultTaskResultSchema)

	// Fallback model: config overrides engine option.
	assert.Contains(t, spec.Command, "--fallback-model")
	assert.Contains(t, spec.Command, "haiku")
	assert.NotContains(t, spec.Command, "default-fallback")

	// --no-session-persistence must never be emitted (flag was removed from Claude Code CLI).
	assert.NotContains(t, spec.Command, "--no-session-persistence")

	// System prompt.
	assert.Contains(t, spec.Command, "--append-system-prompt")
	assert.Contains(t, spec.Command, "Be careful with production data.")

	// Tool control: config overrides engine option.
	assert.Contains(t, spec.Command, "--allowedTools")
	assert.Contains(t, spec.Command, "Read,Write,Bash")
	assert.Contains(t, spec.Command, "--disallowedTools")
	assert.Contains(t, spec.Command, "NotebookEdit")

	// Core flags always present.
	assert.Contains(t, spec.Command, "--max-turns")
	assert.Contains(t, spec.Command, "--dangerously-skip-permissions")
}

// TestEnhancedEngineStreamingEnabled verifies that StreamingEnabled
// without a JSON schema still uses stream-json and --verbose.
func TestEnhancedEngineStreamingEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    engine.EngineConfig
		wantFlags []string
		denyFlags []string
	}{
		{
			name: "streaming enabled without explicit schema uses default schema",
			config: engine.EngineConfig{
				TimeoutSeconds:   3600,
				StreamingEnabled: true,
			},
			wantFlags: []string{"--output-format", "stream-json", "--verbose", "--json-schema", claudecode.DefaultTaskResultSchema},
		},
		{
			name: "streaming enabled with schema includes all flags",
			config: engine.EngineConfig{
				TimeoutSeconds:   3600,
				StreamingEnabled: true,
				JSONSchema:       `{"type":"object"}`,
			},
			wantFlags: []string{"--output-format", "stream-json", "--verbose", "--json-schema"},
		},
		{
			name: "without explicit streaming flag uses default schema",
			config: engine.EngineConfig{
				TimeoutSeconds: 3600,
			},
			wantFlags: []string{"--output-format", "stream-json", "--verbose", "--json-schema", claudecode.DefaultTaskResultSchema},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eng := claudecode.New()
			spec, err := eng.BuildExecutionSpec(enhancedEngineTask, tt.config)
			require.NoError(t, err)
			require.NotNil(t, spec)

			for _, flag := range tt.wantFlags {
				assert.Contains(t, spec.Command, flag, "command should contain %q", flag)
			}
			for _, flag := range tt.denyFlags {
				assert.NotContains(t, spec.Command, flag, "command should not contain %q", flag)
			}
		})
	}
}
