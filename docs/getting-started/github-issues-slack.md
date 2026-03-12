# Guide: GitHub Issues + Slack

This guide walks you through connecting Osmia to a GitHub repository and a Slack workspace so that:

- Any issue labelled **osmia** is automatically picked up by the agent
- You receive a Slack message when work starts, completes, or fails

## Prerequisites

| Requirement | Notes |
|---|---|
| Kubernetes cluster | [Set one up first](kubernetes.md) if you don't have one |
| `kubectl` configured | Pointing at the target cluster and namespace |
| `helm` 3+ | For deploying Osmia |
| GitHub repository | The repo the agent will work on |
| Anthropic API key | For the Claude Code engine |

---

## Step 1 — Create a GitHub personal access token

1. Go to **GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)**.
2. Click **Generate new token (classic)**.
3. Give it a descriptive name: `osmia`.
4. Select these scopes:
    - `repo` (full repository access — needed to clone, push, and open PRs)
    - `issues` (read and comment on issues)
5. Click **Generate token** and copy the value — you will not see it again.

---

## Step 2 — Create a Slack bot

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App → From scratch**.
2. Name it `Osmia` and select your workspace.
3. Under **OAuth & Permissions → Bot Token Scopes**, add: `chat:write`.
4. Click **Install to Workspace** and copy the **Bot User OAuth Token** (starts with `xoxb-`).
5. In the Slack sidebar, find (or create) the channel you want notifications in, right-click it, and copy the **Channel ID** from the bottom of the channel details panel.
6. Invite the bot to the channel: `/invite @Osmia`.

---

## Step 3 — Store credentials as Kubernetes secrets

```bash
# GitHub token
kubectl create secret generic osmia-github-token \
  --namespace osmia \
  --from-literal=token=ghp_YOUR_TOKEN_HERE

# Anthropic API key (for Claude Code)
kubectl create secret generic osmia-anthropic-key \
  --namespace osmia \
  --from-literal=api_key=sk-ant-YOUR_KEY_HERE

# Slack bot token
kubectl create secret generic osmia-slack-token \
  --namespace osmia \
  --from-literal=token=xoxb-YOUR_SLACK_TOKEN_HERE
```

---

## Step 4 — Write `osmia-config.yaml`

```yaml
ticketing:
  backend: github
  config:
    owner: "your-org"           # GitHub org or username
    repo: "your-repo"           # Repository name
    token_secret: osmia-github-token
    labels:
      - "osmia"               # Issues must have this label to be picked up
    exclude_labels:
      - "osmia-in-progress"   # Prevents picking up work already in flight
      - "osmia-failed"

notifications:
  channels:
    - backend: slack
      config:
        channel_id: "C0XXXXXXXXX"   # Replace with your channel ID from Step 2
        token_secret: osmia-slack-token

engines:
  default: claude-code
  claude-code:
    auth:
      method: api_key
      api_key_secret: osmia-anthropic-key

execution:
  backend: kubernetes

guardrails:
  max_cost_per_job: 5.00
  max_job_duration_minutes: 60
  allowed_repos:
    - "github.com/your-org/your-repo"
```

!!! tip "Guardrails"
    The `max_cost_per_job` and `max_job_duration_minutes` limits are safety nets. Start conservative and raise them once you are comfortable with how the agent behaves on your codebase.

---

## Step 5 — Deploy with Helm

```bash
# Add the Osmia chart repository
helm repo add osmia https://unitaryai.github.io/Osmia
helm repo update

# Create the namespace
kubectl create namespace osmia

# Deploy — pass your config file as a values override
helm install osmia osmia/osmia \
  --namespace osmia \
  --set-file config=osmia-config.yaml
```

Verify the controller started cleanly:

```bash
kubectl logs -n osmia -l app=osmia --tail=20
```

You should see a line like:

```json
{"level":"INFO","msg":"github ticketing backend initialised"}
{"level":"INFO","msg":"slack notification channel initialised"}
{"level":"INFO","msg":"controller initialised and ready"}
```

---

## Step 6 — Label your first issue

1. Open (or create) an issue in your repository with a specific, self-contained task — the more context you give, the better the result:

    > **Title:** Add email validation to the POST /api/users endpoint
    >
    > **Body:** The handler does not validate the `email` field. Reject requests with a missing or malformed email address with a 400 status and a descriptive error message. Add unit tests.

2. Add the **osmia** label to the issue.

3. Within 30 seconds (the default poll interval) the controller will pick it up. Watch the logs:

    ```bash
    kubectl logs -n osmia -l app=osmia -f
    ```

4. Check Slack — you will receive a message confirming the agent has started work, and another when it completes.

5. Check the repository — a pull request will be opened with the agent's changes.

---

## Troubleshooting

**The issue is not being picked up.**
- Confirm the label name exactly matches `labels` in your config.
- Check `exclude_labels` — if the issue already has `osmia-in-progress` it will be skipped.
- Run `kubectl logs -n osmia -l app=osmia --tail=50` and look for polling errors.

**No Slack message received.**
- Confirm the bot is invited to the channel (`/invite @Osmia`).
- Verify the channel ID is correct — it looks like `C0XXXXXXXXX`, not the channel name.
- Check logs for `"failed to send slack notification"`.

**Authentication errors.**
- Confirm the secret names in your config match the secret names you created in Step 3.
- Check the token has the required GitHub scopes: run `curl -H "Authorization: token YOUR_TOKEN" https://api.github.com/user` and confirm it returns your user.

---

## Next Steps

- [Configuration Reference](configuration.md) — all available config options
- [Kubernetes Quick Start](kubernetes.md) — full Helm deployment walkthrough
- [Guard Rails Overview](../concepts/guardrails-overview.md) — safety limits explained
- [Shortcut + Slack guide](shortcut-slack.md) — if you use Shortcut instead of GitHub Issues
