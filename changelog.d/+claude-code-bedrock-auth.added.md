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
whitespace, when the region or a model is missing or blank, when an API key
secret is also set, or when `fallback_model` is not one of the four models;
the region and models are trimmed of surrounding whitespace. Because every
other engine calls its own provider outside the region, it also refuses to
start when `engines.default`, `engines.fallback_engines`,
`competitive_execution.judge_engine` or `incident_triage.engine` names
another engine, or when another engine's block is configured, which would let
a ticket label, routing or a tournament give it a task, unless the new
`engines.claude-code.auth.allow_other_engines` is `true`. Every other auth
method is unchanged. See
[Claude Code on Amazon Bedrock](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#claude-code-on-amazon-bedrock).
