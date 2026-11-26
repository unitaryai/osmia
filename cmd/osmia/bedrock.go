package main

import (
	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

// claudeBedrockOption returns the option that makes Claude Code call Amazon
// Bedrock, and true, when cfg's auth method is "bedrock"; otherwise false.
func claudeBedrockOption(cfg *config.ClaudeCodeEngineConfig) (claudecode.Option, bool) {
	if cfg == nil || cfg.Auth.Method != config.AuthMethodBedrock {
		return nil, false
	}
	return claudecode.WithBedrock(cfg.Auth.BedrockRegion, claudecode.BedrockModels{
		Main:   cfg.Models.Main,
		Opus:   cfg.Models.Opus,
		Sonnet: cfg.Models.Sonnet,
		Haiku:  cfg.Models.Haiku,
	}), true
}
