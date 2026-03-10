# Guide: Linear + Slack

This guide walks you through connecting Osmia to a Linear workspace and a Slack workspace so that:

- Issues labelled **osmia** and in the configured state are automatically picked up by the agent
- The issue receives an `in-progress` label when work begins
- You receive a Slack message when work starts, completes, or fails

## Prerequisites

| Requirement | Notes |
|---|---|
| Kubernetes cluster | [Set one up first](kubernetes.md) if you don't have one |
| `kubectl` configured | Pointing at the target cluster and namespace |
| `helm` 3+ | For deploying Osmia |
| Linear workspace | With admin access to create API keys and labels |
| Anthropic API key | For the Claude Code engine |

---

## Step 1 — Create a Linear API key

1. In Linear, go to **Settings → API → Personal API keys**.
2. Click **Create key**, name it `osmia`, and copy the value.

!!! tip "Service accounts"
    For production use, create a dedicated Linear member for Osmia and generate the API key under that account, so activity is clearly attributed and the key can be revoked independently.

---

## Step 2 — Find your team ID

Osmia needs the Linear team UUID (not the team name) to scope its queries.

```bash
curl -s -H "Authorization: YOUR_LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"query": "{ teams { nodes { id name } } }"}' \
  https://api.linear.app/graphql | jq '.data.teams.nodes'
```

Output looks like:

```json
[
  { "id": "a1b2c3d4-...", "name": "Engineering" }
]
```

Copy the `id` value — you will need it for the config.

---

## Step 3 — Create the required labels

Osmia uses two labels to track issue state:

1. In Linear, go to **Settings → Labels**.
2. Create a label named `osmia` — this is the trigger label you add to issues you want the agent to pick up.
3. Create a label named `osmia-failed` — Osmia adds this when a task fails so the issue is not retried automatically.

The `in-progress` label is typically already present in Linear. If not, create it too.

---

## Step 4 — Create a Slack bot

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App → From scratch**.
2. Name it `Osmia` and select your workspace.
3. Under **OAuth & Permissions → Bot Token Scopes**, add: `chat:write`.
4. Click **Install to Workspace** and copy the **Bot User OAuth Token** (starts with `xoxb-`).
5. In the Slack sidebar, right-click the target channel and copy the **Channel ID** from the channel details panel.
6. Invite the bot to the channel: `/invite @Osmia`.

---

## Step 5 — Store credentials as Kubernetes secrets

```bash
# Linear API key
kubectl create secret generic osmia-linear-token \
  --namespace osmia \
  --from-literal=token=YOUR_LINEAR_API_KEY

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

## Step 6 — Write `osmia-config.yaml`

```yaml
ticketing:
  backend: linear
  config:
    token_secret: osmia-linear-token
    team_id: "a1b2c3d4-..."          # team UUID from Step 2
    state_filter: "Todo"             # only pick up issues in this state
    labels:
      - "osmia"                    # issues must have this label
    exclude_labels:
      - "in-progress"                # skip issues already being worked on
      - "osmia-failed"             # skip issues that previously failed

notifications:
  channels:
    - backend: slack
      config:
        channel_id: "C0XXXXXXXXX"   # channel ID from Step 4
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
```

!!! tip "State filter"
    Set `state_filter` to the exact name of the Linear workflow state you want Osmia to poll (e.g. `"Todo"`, `"Backlog"`, `"Ready"`). Issues in any other state are ignored even if they have the `osmia` label.

---

## Step 7 — Deploy with Helm

```bash
helm repo add osmia https://unitaryai.github.io/Osmia
helm repo update

kubectl create namespace osmia

helm install osmia osmia/osmia \
  --namespace osmia \
  --set-file config=osmia-config.yaml
```

Verify the controller started cleanly:

```bash
kubectl logs -n osmia -l app=osmia --tail=20
```

You should see:

```json
{"level":"INFO","msg":"linear ticketing backend initialised"}
{"level":"INFO","msg":"slack notification channel initialised"}
{"level":"INFO","msg":"controller initialised and ready"}
```

---

## Step 8 — Create your first issue

1. In Linear, create an issue in your team with a specific, well-scoped task:

    > **Title:** Add input validation to the POST /api/users endpoint
    >
    > **Description:** The handler does not validate the `email` field. Reject requests with a missing or malformed email with a 400 and a descriptive error message. Include unit tests.

2. Add the **osmia** label to the issue.
3. Ensure the issue is in the `state_filter` state you configured (e.g. `"Todo"`).

Within 30 seconds (the default poll interval) the controller will pick it up:

- The `in-progress` label is added to the issue
- A Slack message confirms the agent has started work
- When complete, a comment is posted on the issue with a link to the pull request

---

## Troubleshooting

**Issues are not being picked up.**

- Confirm the label name exactly matches what you configured in `labels`.
- Confirm the issue is in the workflow state named in `state_filter`.
- Check `exclude_labels` — if the issue has `in-progress` or `osmia-failed` it will be skipped.
- Run `kubectl logs -n osmia -l app=osmia --tail=50` and look for polling errors.

**Authentication errors.**

- Verify the secret name matches what you created in Step 5.
- Test the API key directly:
  ```bash
  curl -s -H "Authorization: YOUR_KEY" \
    -H "Content-Type: application/json" \
    -d '{"query":"{ viewer { id name } }"}' \
    https://api.linear.app/graphql
  ```
  You should see your user details returned.

**No Slack message received.**

- Confirm the bot is invited to the channel (`/invite @Osmia`).
- Verify the channel ID is correct — it looks like `C0XXXXXXXXX`, not the channel name.
- Check logs for `"failed to send slack notification"`.

---

## Next Steps

- [Configuration Reference](configuration.md) — all available config options
- [Kubernetes Quick Start](kubernetes.md) — full Helm deployment walkthrough
- [Guard Rails Overview](../concepts/guardrails-overview.md) — safety limits explained
- [Shortcut + Slack guide](shortcut-slack.md) — if you use Shortcut instead of Linear
- [GitHub Issues + Slack guide](github-issues-slack.md) — if you use GitHub Issues instead of Linear
