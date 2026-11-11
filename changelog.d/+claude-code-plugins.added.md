**Claude Code agents can load plugins from a git repository.** Set
`engines.claude-code.plugins` to name one or more plugin marketplace
repositories, each pinned to a commit SHA or tag, and the plugins every task
loads. A ticket labelled `osmia-plugins:<profile>` also loads that profile's
plugins, so different tasks can get different skills, sub-agents, hooks and MCP
servers. Each agent pod fetches its sources when it starts; a token for a
private repository is read from a Kubernetes Secret and removed from the
environment before Claude Code starts. An unknown profile, plugin or ref
stops the job instead of running it without the plugins it asked for. See
[Plugins](https://github.com/unitaryai/osmia/blob/main/docs/plugins/engines.md#plugins).
