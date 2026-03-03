#!/bin/sh
# setup-claude.sh — initialises Claude Code user config before running the agent.
#
# The home directory (/home/robodev) is mounted as an emptyDir volume so any
# files baked into the image are not visible at runtime.  This script creates
# the necessary Claude Code config files at startup, then exec's claude with
# all arguments forwarded unchanged.

set -eu

# Create writable Claude config directory.
mkdir -p "${HOME}/.claude"

# Register the Slack/GitLab MCP server so that tools such as ask_human,
# notify_human, and wait_for_pipeline are available to the agent.
if [ -f /etc/claude-code/mcp.json ]; then
    cp /etc/claude-code/mcp.json "${HOME}/.claude/mcp.json"
fi

exec claude "$@"
