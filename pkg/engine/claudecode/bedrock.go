package claudecode

import "github.com/unitaryai/osmia/pkg/engine"

// BedrockModels names the Amazon Bedrock model ID Claude Code uses for its
// main session and for each of the opus, sonnet and haiku aliases.
type BedrockModels struct {
	Main   string
	Opus   string
	Sonnet string
	Haiku  string
}

// bedrockSettings is where Claude Code calls Amazon Bedrock and with which
// models.
type bedrockSettings struct {
	region string
	models BedrockModels
}

// WithBedrock makes the agent call Amazon Bedrock in region with the pod's
// own AWS identity, instead of the Anthropic API with a stored key. Every
// model is pinned, so no call falls back to Claude Code's default model IDs.
func WithBedrock(region string, models BedrockModels) Option {
	return func(e *ClaudeCodeEngine) {
		e.bedrock = &bedrockSettings{region: region, models: models}
	}
}

// apply sets the Bedrock variables over whatever env already holds and
// removes the Anthropic API key from env and secretKeyRefs.
func (b *bedrockSettings) apply(env map[string]string, secretKeyRefs map[string]engine.SecretKeyRef) {
	delete(env, "ANTHROPIC_API_KEY")
	delete(secretKeyRefs, "ANTHROPIC_API_KEY")
	env["CLAUDE_CODE_USE_BEDROCK"] = "1"
	env["AWS_REGION"] = b.region
	env["AWS_DEFAULT_REGION"] = b.region
	env["ANTHROPIC_MODEL"] = b.models.Main
	env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = b.models.Opus
	env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = b.models.Sonnet
	env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = b.models.Haiku
}
