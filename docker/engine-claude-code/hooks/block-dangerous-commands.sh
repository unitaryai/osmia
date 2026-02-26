#!/usr/bin/env bash
# block-dangerous-commands.sh — PreToolUse guard rail hook for Bash tool.
#
# Reads JSON tool input from stdin and blocks dangerous commands.
# Exit codes:
#   0 — allow the command
#   2 — block the command (guard rail violation)

set -euo pipefail

INPUT="$(cat)"

# Extract the command field from the tool input JSON.
COMMAND="$(echo "${INPUT}" | jq -r '.tool_input.command // empty' 2>/dev/null || true)"

if [[ -z "${COMMAND}" ]]; then
    # No command field; not a Bash invocation — allow.
    exit 0
fi

# List of dangerous patterns to block.
BLOCKED_PATTERNS=(
    'rm -rf /'
    'rm -rf /*'
    'curl.*|.*bash'
    'curl.*|.*sh'
    'wget.*|.*bash'
    'wget.*|.*sh'
    '\beval\b'
    '\bsudo\b'
    'chmod 777'
    'git push --force.*\b(main|master)\b'
    'git push -f.*\b(main|master)\b'
    'mkfs\.'
    'dd if=.*/dev/'
    ':\(\)\{.*\|.*\}'
)

for pattern in "${BLOCKED_PATTERNS[@]}"; do
    if echo "${COMMAND}" | grep -qEi "${pattern}"; then
        echo "BLOCKED: command matches dangerous pattern '${pattern}'" >&2
        exit 2
    fi
done

# Command is safe — allow.
exit 0
