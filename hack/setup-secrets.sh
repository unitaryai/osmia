#!/usr/bin/env bash
# setup-secrets.sh — Create K8s Secrets for live Osmia testing.
#
# Reads from environment variables (preferred) or prompts interactively.
# Uses dry-run + apply for idempotency.
#
# Required:
#   GITHUB_TOKEN       — GitHub personal access token (repo scope)
#   ANTHROPIC_API_KEY  — Anthropic API key for Claude Code
#
# Optional:
#   SLACK_BOT_TOKEN    — Slack bot token for notifications

set -euo pipefail

NAMESPACE="${HELM_NAMESPACE:-osmia}"

echo "=== Osmia Secret Provisioning ==="
echo "Namespace: ${NAMESPACE}"
echo ""

# Ensure the namespace exists.
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# --- GitHub Token ---
if [[ -z "${GITHUB_TOKEN:-}" ]]; then
    echo -n "Enter GitHub token (repo scope): "
    read -rs GITHUB_TOKEN
    echo ""
fi

if [[ -z "${GITHUB_TOKEN}" ]]; then
    echo "ERROR: GITHUB_TOKEN is required"
    exit 1
fi

kubectl create secret generic osmia-github-token \
    --namespace "${NAMESPACE}" \
    --from-literal=token="${GITHUB_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f -
echo "Secret osmia-github-token created/updated."

# --- Anthropic API Key ---
if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
    echo -n "Enter Anthropic API key: "
    read -rs ANTHROPIC_API_KEY
    echo ""
fi

if [[ -z "${ANTHROPIC_API_KEY}" ]]; then
    echo "ERROR: ANTHROPIC_API_KEY is required"
    exit 1
fi

kubectl create secret generic anthropic-api-key \
    --namespace "${NAMESPACE}" \
    --from-literal=ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY}" \
    --dry-run=client -o yaml | kubectl apply -f -
echo "Secret anthropic-api-key created/updated."

# --- Slack Bot Token (optional) ---
if [[ -n "${SLACK_BOT_TOKEN:-}" ]]; then
    kubectl create secret generic osmia-slack-token \
        --namespace "${NAMESPACE}" \
        --from-literal=token="${SLACK_BOT_TOKEN}" \
        --dry-run=client -o yaml | kubectl apply -f -
    echo "Secret osmia-slack-token created/updated."
else
    echo ""
    echo -n "Enter Slack bot token (leave empty to skip): "
    read -rs SLACK_BOT_TOKEN_INPUT
    echo ""
    if [[ -n "${SLACK_BOT_TOKEN_INPUT}" ]]; then
        kubectl create secret generic osmia-slack-token \
            --namespace "${NAMESPACE}" \
            --from-literal=token="${SLACK_BOT_TOKEN_INPUT}" \
            --dry-run=client -o yaml | kubectl apply -f -
        echo "Secret osmia-slack-token created/updated."
    else
        echo "Skipping Slack token (notifications will be disabled)."
    fi
fi

echo ""
echo "=== Secrets provisioned ==="
kubectl get secrets -n "${NAMESPACE}" -l '!kubernetes.io/service-account.name'
