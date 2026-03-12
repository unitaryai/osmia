#!/usr/bin/env bash
# entrypoint.sh — Claude Code execution entrypoint for Osmia agent jobs.
#
# Environment variables:
#   REPO_URL           — Git repository URL to clone (required)
#   REPO_BRANCH        — Branch to check out (default: main)
#   TASK_PROMPT_FILE   — Path to task prompt file (default: /config/task-prompt.md)
#   CLAUDE_MD_FILE     — Path to CLAUDE.md to inject (optional)
#   GUARDRAILS_FILE    — Path to guardrails config to inject (optional)
#   MAX_TURNS          — Maximum number of agentic turns (optional)
#
# Exit codes:
#   0 — success
#   1 — failure
#   2 — guard rail violation

set -euo pipefail

REPO_BRANCH="${REPO_BRANCH:-main}"
TASK_PROMPT_FILE="${TASK_PROMPT_FILE:-/config/task-prompt.md}"
RESULT_FILE="/workspace/result.json"

# ---- Git configuration ----
git config --global user.name "Osmia"
git config --global user.email "osmia@localhost"
git config --global init.defaultBranch main

# ---- Clone the target repository ----
if [[ -z "${REPO_URL:-}" ]]; then
    echo '{"status":"failure","error":"REPO_URL environment variable is required"}' > "${RESULT_FILE}"
    exit 1
fi

echo "Cloning ${REPO_URL} (branch: ${REPO_BRANCH})..."
git clone --depth=1 --branch="${REPO_BRANCH}" "${REPO_URL}" /workspace/repo
cd /workspace/repo

# ---- Inject CLAUDE.md if provided ----
if [[ -n "${CLAUDE_MD_FILE:-}" && -f "${CLAUDE_MD_FILE}" ]]; then
    echo "Injecting CLAUDE.md from ${CLAUDE_MD_FILE}..."
    cp "${CLAUDE_MD_FILE}" /workspace/repo/CLAUDE.md
fi

# ---- Inject guardrails if provided ----
if [[ -n "${GUARDRAILS_FILE:-}" && -f "${GUARDRAILS_FILE}" ]]; then
    echo "Injecting guardrails from ${GUARDRAILS_FILE}..."
    mkdir -p /workspace/repo/.claude
    cp "${GUARDRAILS_FILE}" /workspace/repo/.claude/settings.json
fi

# ---- Read task prompt ----
if [[ ! -f "${TASK_PROMPT_FILE}" ]]; then
    echo '{"status":"failure","error":"task prompt file not found"}' > "${RESULT_FILE}"
    exit 1
fi

TASK_PROMPT="$(cat "${TASK_PROMPT_FILE}")"

# ---- Build Claude CLI arguments ----
CLAUDE_ARGS=(
    --print
    --output-format json
    --max-turns "${MAX_TURNS:-50}"
)

# ---- Environment variable stripping ----
# When ENV_STRIPPING is enabled, remove sensitive credentials from the
# environment after they have been consumed by the shell. This limits
# exposure if the agent process is compromised.
if [[ "${ENV_STRIPPING:-false}" == "true" ]]; then
    unset ANTHROPIC_API_KEY
fi

# ---- Execute Claude Code ----
echo "Running Claude Code agent..."
EXIT_CODE=0
claude "${CLAUDE_ARGS[@]}" "${TASK_PROMPT}" > /workspace/claude-output.json 2>&1 || EXIT_CODE=$?

# ---- Write result ----
if [[ ${EXIT_CODE} -eq 0 ]]; then
    echo '{"status":"success"}' | jq \
        --argjson output "$(cat /workspace/claude-output.json 2>/dev/null || echo 'null')" \
        '. + {output: $output}' > "${RESULT_FILE}"
elif [[ ${EXIT_CODE} -eq 2 ]]; then
    echo '{"status":"guardrail_violation"}' | jq \
        --argjson output "$(cat /workspace/claude-output.json 2>/dev/null || echo 'null')" \
        '. + {output: $output}' > "${RESULT_FILE}"
else
    echo '{"status":"failure"}' | jq \
        --arg code "${EXIT_CODE}" \
        --argjson output "$(cat /workspace/claude-output.json 2>/dev/null || echo 'null')" \
        '. + {exit_code: ($code | tonumber), output: $output}' > "${RESULT_FILE}"
fi

exit ${EXIT_CODE}
