#!/bin/sh
# setup-claude.sh — initialises Claude Code user config before running the agent.
#
# The home directory (/home/robodev) is an emptyDir volume that shadows any
# files baked into the image.  This script replicates the config files at
# container startup, matching the approach used in the PoC:
#   1. ~/.claude/settings.json  — grants permission for MCP tool use
#   2. /workspace/.mcp.json     — registers the robodev-slack MCP server
#      (project-scope file that Claude Code auto-loads from the cwd)
#
# The main claude invocation also passes --mcp-config /workspace/.mcp.json
# as an explicit belt-and-suspenders load path.

set -eu

# Restore user settings (permissions + MCP tool allowlist).
mkdir -p "${HOME}/.claude"
cp /etc/claude-code/settings.json "${HOME}/.claude/settings.json"

# Copy MCP server config to the project root so it is auto-loaded and also
# available via the explicit --mcp-config flag in the claude invocation.
cp /etc/claude-code/mcp.json /workspace/.mcp.json

exec claude "$@"
