package config

import (
	"strings"
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
		name       string
		engine     ClaudeCodeEngineConfig
		wantField  string
		wantPhrase string
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
			name:   "fallback model is one of the pinned models",
			engine: ClaudeCodeEngineConfig{Auth: bedrock, Models: models, FallbackModel: " example-haiku-model "},
		},
		{
			name:      "fallback model is not one of the pinned models",
			engine:    ClaudeCodeEngineConfig{Auth: bedrock, Models: models, FallbackModel: "example-other-model"},
			wantField: "engines.claude-code.fallback_model",
		},
		{
			name:      "fallback model is an alias",
			engine:    ClaudeCodeEngineConfig{Auth: bedrock, Models: models, FallbackModel: "haiku"},
			wantField: "engines.claude-code.fallback_model",
		},
		{
			name:   "api key method needs no models",
			engine: ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "api_key", APIKeySecret: "anthropic"}},
		},
		{
			name:   "api key method takes any fallback model",
			engine: ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "api_key", APIKeySecret: "anthropic"}, FallbackModel: "example-other-model"},
		},
		{
			name:       "allow other engines without bedrock",
			engine:     ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "api_key", APIKeySecret: "anthropic", AllowOtherEngines: true}},
			wantField:  "engines.claude-code.auth.allow_other_engines",
			wantPhrase: "is only used when",
		},
		{
			name:       "models without bedrock",
			engine:     ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "api_key", APIKeySecret: "anthropic"}, Models: models},
			wantField:  "engines.claude-code.models",
			wantPhrase: "is only used when",
		},
		{
			name:       "capitalised method",
			engine:     ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "Bedrock", BedrockRegion: "example-region-1"}, Models: models},
			wantField:  "engines.claude-code.auth.method",
			wantPhrase: "must be written exactly",
		},
		{
			name:       "method with surrounding whitespace",
			engine:     ClaudeCodeEngineConfig{Auth: AuthConfig{Method: " bedrock", BedrockRegion: "example-region-1"}, Models: models},
			wantField:  "engines.claude-code.auth.method",
			wantPhrase: "must be written exactly",
		},
		{
			name:      "whitespace-only region",
			engine:    ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "bedrock", BedrockRegion: "  "}, Models: models},
			wantField: "engines.claude-code.auth.bedrock_region",
		},
		{
			name:      "whitespace-only model",
			engine:    ClaudeCodeEngineConfig{Auth: bedrock, Models: ClaudeCodeModels{Main: " ", Opus: "o", Sonnet: "s", Haiku: "h"}},
			wantField: "engines.claude-code.models.main",
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
				assert.Contains(t, err.Error(), tt.wantPhrase)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestValidate_ClaudeCodeBedrockTrimsValues pins that the region and models
// are trimmed in place, so the values that passed validation are the ones
// the agent is given.
func TestValidate_ClaudeCodeBedrockTrimsValues(t *testing.T) {
	cfg := &Config{Engines: EnginesConfig{ClaudeCode: &ClaudeCodeEngineConfig{
		Auth: AuthConfig{Method: "bedrock", BedrockRegion: " example-region-1\t"},
		Models: ClaudeCodeModels{
			Main:   " example-main-model",
			Opus:   "example-opus-model ",
			Sonnet: "\texample-sonnet-model\n",
			Haiku:  "  example-haiku-model  ",
		},
	}}}

	err := cfg.Validate()

	require.NoError(t, err)
	assert.Equal(t, "example-region-1", cfg.Engines.ClaudeCode.Auth.BedrockRegion)
	assert.Equal(t, ClaudeCodeModels{
		Main:   "example-main-model",
		Opus:   "example-opus-model",
		Sonnet: "example-sonnet-model",
		Haiku:  "example-haiku-model",
	}, cfg.Engines.ClaudeCode.Models)
}

// TestValidate_ClaudeCodeBedrockOtherEngines pins that, with the bedrock
// method, no configuration lets an engine other than Claude Code be given a
// task, unless auth.allow_other_engines says so.
func TestValidate_ClaudeCodeBedrockOtherEngines(t *testing.T) {
	bedrockEngine := func(allowOtherEngines bool) *ClaudeCodeEngineConfig {
		return &ClaudeCodeEngineConfig{
			Auth: AuthConfig{Method: "bedrock", BedrockRegion: "example-region-1", AllowOtherEngines: allowOtherEngines},
			Models: ClaudeCodeModels{
				Main:   "example-main-model",
				Opus:   "example-opus-model",
				Sonnet: "example-sonnet-model",
				Haiku:  "example-haiku-model",
			},
		}
	}
	tournament := func(judge string) CompetitiveExecutionConfig {
		return CompetitiveExecutionConfig{Enabled: true, DefaultCandidates: 2, JudgeEngine: judge}
	}

	tests := []struct {
		name      string
		cfg       Config
		wantField string
	}{
		{
			name: "claude-code everywhere",
			cfg: Config{
				Engines: EnginesConfig{
					Default:         "claude-code",
					FallbackEngines: []string{"claude-code"},
					ClaudeCode:      bedrockEngine(false),
				},
				CompetitiveExecution: tournament("claude-code"),
				IncidentTriage:       IncidentTriageConfig{Engine: "claude-code"},
			},
		},
		{
			name: "default engine",
			cfg: Config{Engines: EnginesConfig{
				Default:    "codex",
				ClaudeCode: bedrockEngine(false),
			}},
			wantField: "engines.default",
		},
		{
			name: "fallback engine",
			cfg: Config{Engines: EnginesConfig{
				Default:         "claude-code",
				FallbackEngines: []string{"claude-code", "aider"},
				ClaudeCode:      bedrockEngine(false),
			}},
			wantField: "engines.fallback_engines[1]",
		},
		{
			name: "tournament judge",
			cfg: Config{
				Engines:              EnginesConfig{ClaudeCode: bedrockEngine(false)},
				CompetitiveExecution: tournament("codex"),
			},
			wantField: "competitive_execution.judge_engine",
		},
		{
			name: "incident triage engine",
			cfg: Config{
				Engines:        EnginesConfig{ClaudeCode: bedrockEngine(false)},
				IncidentTriage: IncidentTriageConfig{Engine: "opencode"},
			},
			wantField: "incident_triage.engine",
		},
		{
			name: "aider configured",
			cfg: Config{Engines: EnginesConfig{
				ClaudeCode: bedrockEngine(false),
				Aider:      &AiderEngineConfig{},
			}},
			wantField: "engines.aider",
		},
		{
			name: "cline configured",
			cfg: Config{Engines: EnginesConfig{
				ClaudeCode: bedrockEngine(false),
				Cline:      &ClineEngineConfig{},
			}},
			wantField: "engines.cline",
		},
		{
			name: "codex configured as a tournament candidate",
			cfg: Config{
				Engines: EnginesConfig{
					ClaudeCode: bedrockEngine(false),
					Codex:      &CodexEngineConfig{},
				},
				CompetitiveExecution: tournament(""),
			},
			wantField: "engines.codex",
		},
		{
			name: "opencode configured",
			cfg: Config{Engines: EnginesConfig{
				ClaudeCode: bedrockEngine(false),
				OpenCode:   &OpenCodeEngineConfig{},
			}},
			wantField: "engines.opencode",
		},
		{
			name: "allow other engines",
			cfg: Config{
				Engines: EnginesConfig{
					Default:         "codex",
					FallbackEngines: []string{"aider"},
					ClaudeCode:      bedrockEngine(true),
					Aider:           &AiderEngineConfig{},
					Codex:           &CodexEngineConfig{},
				},
				CompetitiveExecution: tournament("codex"),
			},
		},
		{
			name: "mixed engines without bedrock",
			cfg: Config{
				Engines: EnginesConfig{
					Default:         "codex",
					FallbackEngines: []string{"aider", "claude-code"},
					ClaudeCode:      &ClaudeCodeEngineConfig{Auth: AuthConfig{Method: "api_key", APIKeySecret: "anthropic"}},
					Aider:           &AiderEngineConfig{},
					Codex:           &CodexEngineConfig{},
				},
				CompetitiveExecution: tournament("codex"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg

			err := cfg.Validate()

			if tt.wantField != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantField)
				assert.Contains(t, err.Error(), "engines.claude-code.auth.allow_other_engines")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidate_ServiceAccountName(t *testing.T) {
	tests := []struct {
		name    string
		account string
		wantErr bool
	}{
		{name: "none configured", account: ""},
		{name: "valid name", account: "osmia-agent"},
		{name: "longest valid name", account: strings.Repeat("a", 253)},
		{name: "invalid characters", account: "Bad_Name", wantErr: true},
		{name: "too long", account: strings.Repeat("a", 254), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Execution: ExecutionConfig{ServiceAccountName: tt.account}}

			err := cfg.Validate()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "execution.service_account_name")
				return
			}
			assert.NoError(t, err)
		})
	}
}
