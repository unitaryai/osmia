**Claude Code can call Amazon Bedrock in one region.** With
`engines.claude-code.auth.method: bedrock`, the agent calls Bedrock in
`auth.bedrock_region` instead of the Anthropic API with a stored key, and
every model it uses is pinned by the new `engines.claude-code.models` block
(`main`, `opus`, `sonnet`, `haiku`), since Claude Code's own Bedrock defaults
are cross-region. Agent pods run under the `default` service account of their
namespace, so the AWS identity bound to that account signs the calls, and
every pod in the namespace that uses that account shares it. No API key is
mounted, the settings are applied after every other source of environment the
engine merges, a secret reference of the same name is dropped, and a
task-scoped secret named `ANTHROPIC_*`, `AWS_*` or `CLAUDE_CODE_*` is refused
for such an agent. `auth.method` must be written exactly `bedrock`: the
controller refuses to start when it differs only in case or surrounding
whitespace, when the region or a model is missing or blank, or when an API key
secret is also set. Every other auth method is unchanged. See
[Claude Code on Amazon Bedrock](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#claude-code-on-amazon-bedrock).
