#!/bin/sh
# setup-claude.sh — initialises Claude Code user config before running the agent.
#
# The home directory (/home/robodev) is an emptyDir volume that shadows any
# files baked into the image.  This script replicates the config files at
# container startup, matching the approach used in the PoC:
#   1. ~/.claude/settings.json  — grants permission for MCP tool use
#   2. /workspace/.mcp.json     — registers the robodev-slack MCP server
#      (project-scope file that Claude Code auto-loads from the cwd)
#   3. ~/.claude/skills/*.md    — custom skills injected via env vars (optional)
#
# The main claude invocation also passes --mcp-config /workspace/.mcp.json
# as an explicit belt-and-suspenders load path.
#
# Skill environment variables (set by the RoboDev controller):
#   CLAUDE_SKILL_INLINE_<NAME>  — base64-encoded Markdown content for an inline skill
#   CLAUDE_SKILL_PATH_<NAME>    — path to a skill file on the container image
#
# NAME is the skill name with non-alphanumeric characters replaced by
# underscores and converted to uppercase (e.g. CREATE_CHANGELOG).
# The filename written is the lowercase, hyphenated form (e.g. create-changelog.md).

set -eu

# Restore user settings (permissions + MCP tool allowlist).
mkdir -p "${HOME}/.claude"
cp /etc/claude-code/settings.json "${HOME}/.claude/settings.json"

# Copy MCP server config to the project root so it is auto-loaded and also
# available via the explicit --mcp-config flag in the claude invocation.
cp /etc/claude-code/mcp.json /workspace/.mcp.json

# Write custom skill files if any skill env vars are present.
# Inline skills: CLAUDE_SKILL_INLINE_<NAME>=<base64-encoded Markdown>
# Path skills:   CLAUDE_SKILL_PATH_<NAME>=<path on image>
if env | grep -q '^CLAUDE_SKILL_'; then
    mkdir -p "${HOME}/.claude/skills"

    # Write inline skills (base64-decoded content).
    for var in $(env | grep '^CLAUDE_SKILL_INLINE_' | sed 's/=.*//'); do
        name=$(printf '%s' "$var" | sed 's/^CLAUDE_SKILL_INLINE_//' | tr '[:upper:]' '[:lower:]' | tr '_' '-')
        printenv "$var" | base64 -d > "${HOME}/.claude/skills/${name}.md"
    done

    # Copy path-based skills from the image.
    for var in $(env | grep '^CLAUDE_SKILL_PATH_' | sed 's/=.*//'); do
        name=$(printf '%s' "$var" | sed 's/^CLAUDE_SKILL_PATH_//' | tr '[:upper:]' '[:lower:]' | tr '_' '-')
        path=$(printenv "$var")
        [ -f "$path" ] && cp "$path" "${HOME}/.claude/skills/${name}.md"
    done
fi

# Write ConfigMap-backed sub-agent files to ~/.claude/agents/.
# Sub-agent env vars: CLAUDE_SUBAGENT_PATH_<NAME>=<path on volume>
if env | grep -q '^CLAUDE_SUBAGENT_PATH_'; then
    mkdir -p "${HOME}/.claude/agents"
    for var in $(env | grep '^CLAUDE_SUBAGENT_PATH_' | sed 's/=.*//'); do
        name=$(printf '%s' "$var" | sed 's/^CLAUDE_SUBAGENT_PATH_//' | tr '[:upper:]' '[:lower:]' | tr '_' '-')
        path=$(printenv "$var")
        [ -f "$path" ] && cp "$path" "${HOME}/.claude/agents/${name}.md"
    done
fi

exec claude "$@"
