package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

func TestClaudeBedrockOption(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.ClaudeCodeEngineConfig
		wantEnv    map[string]string
		wantAPIKey bool
	}{
		{
			name: "bedrock builds an engine that calls Bedrock",
			cfg: &config.ClaudeCodeEngineConfig{
				Auth: config.AuthConfig{Method: "bedrock", BedrockRegion: "example-region-1"},
				Models: config.ClaudeCodeModels{
					Main:   "example-main-model",
					Opus:   "example-opus-model",
					Sonnet: "example-sonnet-model",
					Haiku:  "example-haiku-model",
				},
			},
			wantEnv: map[string]string{
				"CLAUDE_CODE_USE_BEDROCK":        "1",
				"AWS_REGION":                     "example-region-1",
				"AWS_DEFAULT_REGION":             "example-region-1",
				"ANTHROPIC_MODEL":                "example-main-model",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "example-opus-model",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "example-sonnet-model",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "example-haiku-model",
			},
		},
		{
			name:       "api key keeps the API key",
			cfg:        &config.ClaudeCodeEngineConfig{Auth: config.AuthConfig{Method: "api_key"}},
			wantAPIKey: true,
		},
		{
			name:       "no claude-code block keeps the API key",
			cfg:        nil,
			wantAPIKey: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []claudecode.Option
			opt, ok := claudeBedrockOption(tt.cfg)
			assert.Equal(t, !tt.wantAPIKey, ok)
			if ok {
				opts = append(opts, opt)
			}
			e := claudecode.New(opts...)

			spec, err := e.BuildExecutionSpec(engine.Task{ID: "task-1", Title: "Fix it"}, engine.EngineConfig{})

			require.NoError(t, err)
			for name, value := range tt.wantEnv {
				assert.Equal(t, value, spec.Env[name], name)
			}
			_, hasKey := spec.SecretKeyRefs["ANTHROPIC_API_KEY"]
			assert.Equal(t, tt.wantAPIKey, hasKey)
			if tt.wantAPIKey {
				assert.NotContains(t, spec.Env, "CLAUDE_CODE_USE_BEDROCK")
			}
		})
	}
}

// TestClaudeBedrockOptionAfterValidate pins that a region with surrounding
// whitespace reaches the agent trimmed once the config has been validated.
func TestClaudeBedrockOptionAfterValidate(t *testing.T) {
	cfg := &config.Config{Engines: config.EnginesConfig{ClaudeCode: &config.ClaudeCodeEngineConfig{
		Auth: config.AuthConfig{Method: "bedrock", BedrockRegion: " example-region-1 "},
		Models: config.ClaudeCodeModels{
			Main:   " example-main-model ",
			Opus:   "example-opus-model",
			Sonnet: "example-sonnet-model",
			Haiku:  "example-haiku-model",
		},
	}}}
	require.NoError(t, cfg.Validate())
	opt, ok := claudeBedrockOption(cfg.Engines.ClaudeCode)
	require.True(t, ok)

	spec, err := claudecode.New(opt).BuildExecutionSpec(engine.Task{ID: "task-1", Title: "Fix it"}, engine.EngineConfig{})

	require.NoError(t, err)
	assert.Equal(t, "example-region-1", spec.Env["AWS_REGION"])
	assert.Equal(t, "example-main-model", spec.Env["ANTHROPIC_MODEL"])
}
