#!/usr/bin/env bash
# entrypoint.sh — Cline CLI execution entrypoint for Osmia agent jobs.
#
# Environment variables:
#   REPO_URL           — Git repository URL to clone (required)
#   REPO_BRANCH        — Branch to check out (default: main)
#   TASK_PROMPT_FILE   — Path to task prompt file (default: /config/task-prompt.md)
#   ANTHROPIC_API_KEY  — Anthropic API key (when using Anthropic provider)
#   OPENAI_API_KEY     — OpenAI API key (when using OpenAI provider)
#   GOOGLE_API_KEY     — Google API key (when using Google provider)
#   MCP_ENABLED        — Enable MCP support (default: false)
#
# Exit codes:
#   0 — success
#   1 — failure
#   2 — guard rail violation

set -euo pipefail

REPO_BRANCH="${REPO_BRANCH:-main}"
TASK_PROMPT_FILE="${TASK_PROMPT_FILE:-/config/task-prompt.md}"
MCP_ENABLED="${MCP_ENABLED:-false}"
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

# ---- Read task prompt ----
if [[ ! -f "${TASK_PROMPT_FILE}" ]]; then
    echo '{"status":"failure","error":"task prompt file not found"}' > "${RESULT_FILE}"
    exit 1
fi

TASK_PROMPT="$(cat "${TASK_PROMPT_FILE}")"

# ---- Build Cline CLI arguments ----
CLINE_ARGS=(
    --headless
    --task "${TASK_PROMPT}"
    --output-format json
)

if [[ "${MCP_ENABLED}" == "true" ]]; then
    CLINE_ARGS+=(--mcp)
fi

# ---- Environment variable stripping ----
# When ENV_STRIPPING is enabled, remove sensitive credentials from the
# environment after they have been consumed by the shell. This limits
# exposure if the agent process is compromised.
if [[ "${ENV_STRIPPING:-false}" == "true" ]]; then
    unset ANTHROPIC_API_KEY
    unset OPENAI_API_KEY
    unset GOOGLE_API_KEY
fi

# ---- Execute Cline ----
echo "Running Cline agent..."
EXIT_CODE=0
cline "${CLINE_ARGS[@]}" > /workspace/cline-output.json 2>&1 || EXIT_CODE=$?

# ---- Write result ----
if [[ ${EXIT_CODE} -eq 0 ]]; then
    echo '{"status":"success"}' | jq \
        --argjson output "$(cat /workspace/cline-output.json 2>/dev/null || echo 'null')" \
        '. + {output: $output}' > "${RESULT_FILE}"
elif [[ ${EXIT_CODE} -eq 2 ]]; then
    echo '{"status":"guardrail_violation"}' | jq \
        --argjson output "$(cat /workspace/cline-output.json 2>/dev/null || echo 'null')" \
        '. + {output: $output}' > "${RESULT_FILE}"
else
    echo '{"status":"failure"}' | jq \
        --arg code "${EXIT_CODE}" \
        --argjson output "$(cat /workspace/cline-output.json 2>/dev/null || echo 'null')" \
        '. + {exit_code: ($code | tonumber), output: $output}' > "${RESULT_FILE}"
fi

exit ${EXIT_CODE}
