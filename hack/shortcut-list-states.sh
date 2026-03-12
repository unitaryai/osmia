#!/usr/bin/env bash
# shortcut-list-states.sh — Lists all workflow states in your Shortcut workspace.
#
# Use this to find the exact state names for osmia-config.yaml:
#   ticketing.config.workflow_state_name    — the state that triggers Osmia
#   ticketing.config.in_progress_state_name — the state Osmia moves stories into
#
# Usage:
#   SHORTCUT_TOKEN=your-token ./hack/shortcut-list-states.sh
#   ./hack/shortcut-list-states.sh your-token
#
# Requires: curl, jq

set -euo pipefail

TOKEN="${SHORTCUT_TOKEN:-${1:-}}"

if [[ -z "$TOKEN" ]]; then
  echo "Error: Shortcut API token required." >&2
  echo "  Set SHORTCUT_TOKEN env var or pass it as the first argument." >&2
  exit 1
fi

if ! command -v jq &>/dev/null; then
  echo "Error: jq is required (https://jqlang.github.io/jq/)." >&2
  exit 1
fi

echo "Fetching workflows from Shortcut..."
echo ""

curl -sf \
  -H "Shortcut-Token: ${TOKEN}" \
  -H "Content-Type: application/json" \
  "https://api.app.shortcut.com/api/v3/workflows" \
  | jq -r '
      .[] |
      "Workflow: \(.name)",
      (.states | sort_by(.position) | .[] |
        "  \(.id)  \(.name)"
      ),
      ""
    '

echo "Copy a state name exactly (including capitalisation) into your config:"
echo ""
echo "  ticketing:"
echo "    backend: shortcut"
echo "    config:"
echo "      workflow_state_name: \"Ready for Development\""
echo "      in_progress_state_name: \"In Development\""
