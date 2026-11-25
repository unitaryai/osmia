package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidate_PodAnnotations pins that a malformed agent pod annotation is
// rejected at startup, rather than surfacing as every agent Job failing.
func TestValidate_PodAnnotations(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantErr     bool
	}{
		{name: "none configured", annotations: nil},
		{name: "valid prefixed key", annotations: map[string]string{"karpenter.sh/do-not-disrupt": "true"}},
		{name: "invalid key", annotations: map[string]string{"not a valid key!": "true"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Execution: ExecutionConfig{PodAnnotations: tt.annotations}}

			err := cfg.Validate()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "execution.pod_annotations")
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestValidate_ClaudeCodeBedrock pins that Bedrock is refused at startup
// unless it names a region and every model, and has no API key.
func TestValidate_ClaudeCodeBedrock(t *testing.T) {
	models := ClaudeCodeModels{
		Main:   "example-main-model",
		Opus:   "example-opus-model",
		Sonnet: "example-sonnet-model",
		Haiku:  "example-haiku-model",
	}
	bedrock := AuthConfig{Method: "bedrock", BedrockRegion: "example-region-1"}

	tests := []struct {
		name      string
		engine    ClaudeCodeEngineConfig
		wantField string
	}{
		{name: "valid", engine: ClaudeCodeEngineConfig{Auth: bedrock, Models: models}},
		{
			name:      "no region",
			engine:    ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "bedrock"}, Models: models},
			wantField: "engines.claude-code.auth.bedrock_region",
		},
		{
			name:      "no main model",
			engine:    ClaudeCodeEngineConfig{Auth: bedrock, Models: ClaudeCodeModels{Opus: "o", Sonnet: "s", Haiku: "h"}},
			wantField: "engines.claude-code.models.main",
		},
		{
			name:      "no opus model",
			engine:    ClaudeCodeEngineConfig{Auth: bedrock, Models: ClaudeCodeModels{Main: "m", Sonnet: "s", Haiku: "h"}},
			wantField: "engines.claude-code.models.opus",
		},
		{
			name:      "no sonnet model",
			engine:    ClaudeCodeEngineConfig{Auth: bedrock, Models: ClaudeCodeModels{Main: "m", Opus: "o", Haiku: "h"}},
			wantField: "engines.claude-code.models.sonnet",
		},
		{
			name:      "no haiku model",
			engine:    ClaudeCodeEngineConfig{Auth: bedrock, Models: ClaudeCodeModels{Main: "m", Opus: "o", Sonnet: "s"}},
			wantField: "engines.claude-code.models.haiku",
		},
		{
			name: "api key secret alongside",
			engine: ClaudeCodeEngineConfig{
				Auth:   AuthConfig{Method: "bedrock", BedrockRegion: "example-region-1", APIKeySecret: "anthropic"},
				Models: models,
			},
			wantField: "engines.claude-code.auth.api_key_secret",
		},
		{
			name: "api key key alongside",
			engine: ClaudeCodeEngineConfig{
				Auth:   AuthConfig{Method: "bedrock", BedrockRegion: "example-region-1", APIKeyKey: "api_key"},
				Models: models,
			},
			wantField: "engines.claude-code.auth.api_key_key",
		},
		{
			name:   "api key method needs no models",
			engine: ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "api_key", APIKeySecret: "anthropic"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := tt.engine
			cfg := &Config{Engines: EnginesConfig{ClaudeCode: &engine}}

			err := cfg.Validate()

			if tt.wantField != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantField)
				return
			}
			assert.NoError(t, err)
		})
	}
}
