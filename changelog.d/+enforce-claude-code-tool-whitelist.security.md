**`engines.claude-code.tool_whitelist` did not restrict any tools.** The
controller did not pass the list to the engine. Even when connected, the list
went to Claude Code as `--allowedTools`, which only pre-approves tools and
removes none, so it had no effect alongside `--dangerously-skip-permissions`.
The whitelist now goes to Claude Code as `--tools`, so only the listed
built-in tools are available. Claude Code silently drops entries that `--tools`
does not accept, so Osmia now refuses to start if the whitelist contains a
permission pattern such as `Bash(git *)` or an MCP tool name. MCP tools are not
affected by the whitelist; restrict them with `tool_blacklist` or the MCP
configuration. If you use skills or sub-agents, add `Skill` or `Task` to the
whitelist.
