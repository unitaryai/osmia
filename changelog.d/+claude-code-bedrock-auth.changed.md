**A Claude Code configuration with `auth.method: bedrock` must now be
complete.** Before this release `engines.claude-code.auth.method: bedrock` and
`auth.bedrock_region` were accepted but ignored, and the agent kept using the
Anthropic API key. Such a configuration now makes the controller refuse to
start unless it also names all four models under `engines.claude-code.models`
(`main`, `opus`, `sonnet`, `haiku`), sets neither `auth.api_key_secret` nor
`auth.api_key_key`, and gives no task to another engine: no other engine
may be configured, or named in `engines.default`, `engines.fallback_engines`,
`competitive_execution.judge_engine` or `incident_triage.engine`, unless
`engines.claude-code.auth.allow_other_engines` is `true`. To keep calling the
Anthropic API, set another method, such as `api_key`, instead. See
[Claude Code on Amazon Bedrock](https://github.com/unitaryai/osmia/blob/main/docs/getting-started/configuration.md#claude-code-on-amazon-bedrock).
