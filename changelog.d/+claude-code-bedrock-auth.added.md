**Claude Code can call Amazon Bedrock in one region.** With
`engines.claude-code.auth.method: bedrock`, the agent calls Bedrock in
`auth.bedrock_region` with its pod's own AWS identity instead of the Anthropic
API with a stored key, and every model it uses is pinned by the new
`engines.claude-code.models` block (`main`, `opus`, `sonnet`, `haiku`), since
Claude Code's own Bedrock defaults are cross-region. No API key is mounted,
the settings cannot be replaced by any other source of environment, and a
task-scoped secret named `ANTHROPIC_*`, `AWS_*` or `CLAUDE_CODE_*` is refused
for such an agent. The controller refuses to start when the region or a model
is missing, or when an API key secret is also set. Every other auth method is
unchanged. See
[Claude Code on Amazon Bedrock](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#claude-code-on-amazon-bedrock).
