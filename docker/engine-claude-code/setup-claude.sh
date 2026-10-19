#!/bin/sh
# setup-claude.sh — initialises Claude Code user config before running the agent.
#
# The home directory (/home/osmia) is an emptyDir volume that shadows any
# files baked into the image.  This script replicates the config files at
# container startup, matching the approach used in the PoC:
#   1. ~/.claude/settings.json  — grants permission for MCP tool use
#   2. /workspace/.mcp.json     — registers the osmia-slack MCP server
#      (project-scope file that Claude Code auto-loads from the cwd)
#   3. ~/.claude/skills/*.md    — custom skills injected via env vars (optional)
#
# The main claude invocation also passes --mcp-config /workspace/.mcp.json
# as an explicit belt-and-suspenders load path.
#
# Session persistence support:
#   CLAUDE_CONFIG_DIR    — overrides ~/.claude; set by the session store to
#                          point at a PVC-backed directory so that session
#                          JSONL files survive pod restarts.
#   OSMIA_WORKSPACE_DIR  — when set and already populated (a .git directory
#                          is present), the git-clone step is skipped so the
#                          agent continues in the same workspace.
#
# Skill environment variables (set by the Osmia controller):
#   CLAUDE_SKILL_INLINE_<NAME>  — base64-encoded Markdown content for an inline skill
#   CLAUDE_SKILL_PATH_<NAME>    — path to a skill file on the container image
#
# NAME is the skill name with non-alphanumeric characters replaced by
# underscores and converted to uppercase (e.g. CREATE_CHANGELOG).
# The filename written is the lowercase, hyphenated form (e.g. create-changelog.md).

set -eu

# Use CLAUDE_CONFIG_DIR when set (session persistence), otherwise ~/.claude.
CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-${HOME}/.claude}"

# Always create the directory — it will either be the PVC-backed path or
# the ephemeral emptyDir home.
mkdir -p "${CLAUDE_DIR}"

# Canonicalise before validating. The skill and sub-agent directories below
# are cleared with rm -rf before being regenerated, so the path they are
# derived from has to be checked after "..", symlinks and repeated separators
# are resolved, not as written. A literal check would pass "/tmp/.." and "//",
# both of which put the skills path at "/skills" — where the ConfigMap
# volumes are mounted. `cd && pwd -P` is the portable POSIX way to resolve a
# path; realpath is not guaranteed present.
CLAUDE_DIR=$(cd "${CLAUDE_DIR}" && pwd -P) || {
    echo "setup-claude.sh: refusing to run: cannot resolve CLAUDE_DIR" >&2
    exit 1
}

# Squeeze repeated separators. POSIX leaves a leading "//" implementation-
# defined and some shells return it from `pwd -P` verbatim, which would then
# satisfy the nested-path check below while still meaning the root.
CLAUDE_DIR=$(printf '%s' "${CLAUDE_DIR}" | tr -s '/')

# Refuse exactly the two values that would aim the clear below at something it
# must not touch: "/", which puts the skills path at /skills where the
# ConfigMap volumes are mounted, and /skills itself. Anything else absolute is
# fine — including a single-segment path. The per-taskrun-pvc session store
# sets CLAUDE_CONFIG_DIR=/session, and an earlier version of this check
# demanded two segments and so refused every job on that backend.
case "${CLAUDE_DIR}" in
    / | /skills | /skills/*)
        echo "setup-claude.sh: refusing to run: CLAUDE_DIR resolves to '${CLAUDE_DIR}', which would point skill cleanup at the root or the ConfigMap mounts" >&2
        exit 1
        ;;
    /*) ;;
    *)
        echo "setup-claude.sh: refusing to run: CLAUDE_DIR resolves to '${CLAUDE_DIR}', which is not an absolute path" >&2
        exit 1
        ;;
esac

# Clear the managed directories unconditionally, before the per-kind guards
# below. Doing it inside them would skip the clear in exactly the case that
# needs it most: an operator removing the last configured skill leaves no
# CLAUDE_SKILL_* variable, so nothing would run, and the stale skill would
# stay readable on a persisted config directory indefinitely. Everything
# under these two paths is generated from the environment on every start.
rm -rf "${CLAUDE_DIR}/skills" "${CLAUDE_DIR}/agents"

# Overwrite settings.json and the MCP config unconditionally — these are
# controlled by the operator and must reflect the current policy even on
# resumed sessions.  Session JSONL files are left untouched.
cp /etc/claude-code/settings.json "${CLAUDE_DIR}/settings.json"

# Copy MCP server config to the project root so it is auto-loaded and also
# available via the explicit --mcp-config flag in the claude invocation.
cp /etc/claude-code/mcp.json /workspace/.mcp.json

# Conditionally register the incident.io remote MCP server when an API key
# is present on the pod (set via SecretKeyRef from IncidentTriageConfig).
# We assemble the workspace mcp.json ourselves rather than relying on Claude
# Code's ${VAR} substitution in HTTP headers, which has known bugs
# (anthropics/claude-code#51581, #6204).
if [ -n "${INCIDENT_IO_API_KEY:-}" ]; then
    jq --arg key "$INCIDENT_IO_API_KEY" \
        '.mcpServers."incident-io" = {
            type: "http",
            url: "https://mcp.incident.io/mcp",
            headers: {"Authorization": ("Bearer " + $key)}
        }' /workspace/.mcp.json > /workspace/.mcp.json.tmp \
        && mv /workspace/.mcp.json.tmp /workspace/.mcp.json
fi

# Export CLAUDE_CONFIG_DIR so claude itself picks it up when the non-default
# path is in use.  A no-op when CLAUDE_CONFIG_DIR was already set in the env.
export CLAUDE_CONFIG_DIR="${CLAUDE_DIR}"

# Write custom skill files if any skill env vars are present. Skills go to
# ${CLAUDE_DIR}/skills/<name>/SKILL.md — directory per skill, file named
# exactly SKILL.md. A flat <name>.md is not discovered by the slash-command
# loader; invocations fail with "Unknown skill: <name>" even though the file
# exists.
#
# On the directory: when CLAUDE_CONFIG_DIR is set, Claude Code reads
# user-scoped skills from ${CLAUDE_CONFIG_DIR}/skills/ and does NOT read
# ${HOME}/.claude/skills/. Writing them under ${HOME} unconditionally, as
# this script did between #47 and this change, made every skill invisible on
# any deployment with session persistence enabled — which is the only case
# where CLAUDE_CONFIG_DIR is set at all. Verified by direct repro against
# claude-code 2.0.28, 2.1.145 and 2.1.160: with CLAUDE_CONFIG_DIR set, only
# the skill under it is listed; with it unset, only the one under ${HOME}.
# Sub-agents behave identically.
#
# Skill env vars:
#   CLAUDE_SKILL_INLINE_<NAME>=<base64-encoded Markdown>
#   CLAUDE_SKILL_PATH_<NAME>=<path on image or single-key ConfigMap mount>
#   CLAUDE_SKILL_DIR_<NAME>=<directory mount of a multi-file ConfigMap>
if env | grep -q '^CLAUDE_SKILL_'; then
    SKILLS_DIR="${CLAUDE_DIR}/skills"
    mkdir -p "${SKILLS_DIR}"

    # Write inline skills (base64-decoded content).
    for var in $(env | grep '^CLAUDE_SKILL_INLINE_' | sed 's/=.*//'); do
        name=$(printf '%s' "$var" | sed 's/^CLAUDE_SKILL_INLINE_//' | tr '[:upper:]' '[:lower:]' | tr '_' '-')
        mkdir -p "${SKILLS_DIR}/${name}"
        printenv "$var" | base64 -d > "${SKILLS_DIR}/${name}/SKILL.md"
    done

    # Copy single-file skills, either from the image or from a one-key
    # ConfigMap mounted at /skills/<name>.md.
    for var in $(env | grep '^CLAUDE_SKILL_PATH_' | sed 's/=.*//'); do
        name=$(printf '%s' "$var" | sed 's/^CLAUDE_SKILL_PATH_//' | tr '[:upper:]' '[:lower:]' | tr '_' '-')
        path=$(printenv "$var")
        if [ -f "$path" ]; then
            mkdir -p "${SKILLS_DIR}/${name}"
            cp "$path" "${SKILLS_DIR}/${name}/SKILL.md"
        fi
    done

    # Copy directory-style (multi-file) skills from a mounted ConfigMap. The
    # whole ConfigMap is mounted as a directory; copy every Markdown file
    # (SKILL.md plus its sibling reference files) into the skill directory.
    # `cp -L` dereferences the symlinks a ConfigMap projection creates, and
    # the *.md glob skips the projection's own ..data entries.
    for var in $(env | grep '^CLAUDE_SKILL_DIR_' | sed 's/=.*//'); do
        name=$(printf '%s' "$var" | sed 's/^CLAUDE_SKILL_DIR_//' | tr '[:upper:]' '[:lower:]' | tr '_' '-')
        dir=$(printenv "$var")
        if [ -d "$dir" ]; then
            mkdir -p "${SKILLS_DIR}/${name}"
            # An unmatched glob stays literal under sh, so guard each entry.
            # A real copy failure of an existing file still aborts (set -eu)
            # rather than starting the agent with a half-written skill.
            for f in "$dir"/*.md; do
                [ -e "$f" ] || continue
                cp -L "$f" "${SKILLS_DIR}/${name}/"
            done
            if [ ! -f "${SKILLS_DIR}/${name}/SKILL.md" ]; then
                echo "setup-claude.sh: WARNING: multi-file skill '${name}' has no SKILL.md in ${dir}" >&2
            fi
        fi
    done
fi

# Write ConfigMap-backed sub-agent files to ${CLAUDE_DIR}/agents/<name>.md —
# a flat file per sub-agent, unlike skills. Same CLAUDE_CONFIG_DIR reasoning
# as skills above; the directory was already cleared near the top.
#
# Sub-agent env vars: CLAUDE_SUBAGENT_PATH_<NAME>=<path on volume>
if env | grep -q '^CLAUDE_SUBAGENT_PATH_'; then
    AGENTS_DIR="${CLAUDE_DIR}/agents"
    mkdir -p "${AGENTS_DIR}"
    for var in $(env | grep '^CLAUDE_SUBAGENT_PATH_' | sed 's/=.*//'); do
        name=$(printf '%s' "$var" | sed 's/^CLAUDE_SUBAGENT_PATH_//' | tr '[:upper:]' '[:lower:]' | tr '_' '-')
        path=$(printenv "$var")
        [ -f "$path" ] && cp "$path" "${AGENTS_DIR}/${name}.md"
    done
fi

exec claude "$@"
