#!/usr/bin/env bash
# block-sensitive-files.sh — PreToolUse guard rail hook for Write and Edit tools.
#
# Reads JSON tool input from stdin and blocks writes to sensitive file paths.
# Configurable via BLOCKED_FILE_PATTERNS environment variable (colon-separated).
# Exit codes:
#   0 — allow the write
#   2 — block the write (guard rail violation)

set -euo pipefail

INPUT="$(cat)"

# Extract the file path from the tool input JSON.
FILE_PATH="$(echo "${INPUT}" | jq -r '.tool_input.file_path // .tool_input.path // empty' 2>/dev/null || true)"

if [[ -z "${FILE_PATH}" ]]; then
    # No file path found; not a file write — allow.
    exit 0
fi

# Default blocked patterns.
DEFAULT_PATTERNS='.env*:**/credentials/**:**/secrets/**:*.pem:*.key:*.p12:*.pfx:*.jks:*.keystore'

# Use configurable patterns if set, otherwise fall back to defaults.
BLOCKED_FILE_PATTERNS="${BLOCKED_FILE_PATTERNS:-${DEFAULT_PATTERNS}}"

# Split colon-separated patterns into an array.
IFS=':' read -ra PATTERNS <<< "${BLOCKED_FILE_PATTERNS}"

for pattern in "${PATTERNS[@]}"; do
    # Use bash pattern matching for glob-style patterns.
    # shellcheck disable=SC2254
    case "${FILE_PATH}" in
        ${pattern})
            echo "BLOCKED: write to '${FILE_PATH}' matches sensitive pattern '${pattern}'" >&2
            exit 2
            ;;
    esac
done

# File path is safe — allow.
exit 0
