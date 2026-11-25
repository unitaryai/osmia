package main

import (
	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

// claudeBedrockOptions returns the option that makes Claude Code call Amazon
// Bedrock when cfg's auth method is "bedrock", and none otherwise.
func claudeBedrockOptions(cfg *config.ClaudeCodeEngineConfig) []claudecode.Option {
	if cfg == nil || cfg.Auth.Method != "bedrock" {
		return nil
	}
	return []claudecode.Option{claudecode.WithBedrock(cfg.Auth.BedrockRegion, claudecode.BedrockModels{
		Main:   cfg.Models.Main,
		Opus:   cfg.Models.Opus,
		Sonnet: cfg.Models.Sonnet,
		Haiku:  cfg.Models.Haiku,
	})}
}
