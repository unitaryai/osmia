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

// apply sets the Bedrock variables over whatever env already holds, removes
// the Anthropic API key from env, and drops any secret reference named like
// the API key or one of the Bedrock variables, since secret references are
// added to the Job after plain variables and would win.
func (b *bedrockSettings) apply(env map[string]string, secretKeyRefs map[string]engine.SecretKeyRef) {
	delete(env, "ANTHROPIC_API_KEY")
	delete(secretKeyRefs, "ANTHROPIC_API_KEY")
	for name, value := range b.env() {
		env[name] = value
		delete(secretKeyRefs, name)
	}
}

// env returns every variable the Bedrock settings pin, with its value.
func (b *bedrockSettings) env() map[string]string {
	return map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  b.models.Haiku,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   b.models.Opus,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": b.models.Sonnet,
		"ANTHROPIC_MODEL":                b.models.Main,
		"AWS_DEFAULT_REGION":             b.region,
		"AWS_REGION":                     b.region,
		"CLAUDE_CODE_USE_BEDROCK":        "1",
	}
}
