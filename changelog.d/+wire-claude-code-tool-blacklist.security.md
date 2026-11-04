**`engines.claude-code.tool_blacklist` had no effect.** The config loader read
the list, but the controller never passed it to the Claude Code engine. Agents
therefore ran with every tool available, even when an operator had blocked
tools such as `WebFetch` or `Bash`, and nothing warned that the restriction was
not applied. The engine now passes the list to Claude Code as
`--disallowedTools`. A blacklist set for a single task is added to the
engine-level blacklist instead of replacing it, so a narrower setting cannot
lift an operator-level restriction.
