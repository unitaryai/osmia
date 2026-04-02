# Guide: Shortcut + Slack

This guide walks you through connecting Osmia to a Shortcut workspace and a Slack workspace so that:

- Stories assigned to **@osmia** and moved to **Ready for Development** are automatically picked up
- The story moves to **In Development** and receives a comment when work begins
- You receive a Slack message when work completes or fails

## Prerequisites

| Requirement | Notes |
|---|---|
| Kubernetes cluster | [Set one up first](kubernetes.md) if you don't have one |
| `kubectl` configured | Pointing at the target cluster and namespace |
| `helm` 3+ | For deploying Osmia |
| Shortcut workspace | With admin access to create API tokens and webhooks |
| Anthropic API key | For the Claude Code engine |

---

## Step 1 — Create a Shortcut API token

1. In Shortcut, go to **Settings → API Tokens**.
2. Click **Generate Token**, name it `osmia`, and copy the value.

---

## Step 2 — Create a Shortcut user for Osmia

Osmia filters stories by assignee so it only picks up work explicitly assigned to it.

1. In Shortcut, go to **Settings → Members → Invite a member**.
2. Create a member with the email address you control (e.g. `osmia@your-company.com`) and the mention name `osmia`.
3. Accept the invitation and note the exact mention name — you will need it for the config.

!!! note
    If you already have an `@osmia` user, skip this step. Use whatever mention name they have.

---

## Step 3 — Find your workflow state names

Osmia needs to know the exact names of your "trigger" state and your "in progress" state. Use the helper script:

```bash
SHORTCUT_TOKEN=your_api_token ./hack/shortcut-list-states.sh
```

Output looks like:

```
Workflow: Engineering
  500100001  Unstarted
  500100002  Ready for Development
  500100003  In Development
  500100004  In Review
  500100005  Done
```

Note the exact names (including capitalisation) of:

- The state that means "ready for the agent to start" (e.g. `Ready for Development`)
- The state the agent should move stories into while working (e.g. `In Development`)

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
# Shortcut API token
kubectl create secret generic osmia-shortcut-token \
  --namespace osmia \
  --from-literal=token=YOUR_SHORTCUT_API_TOKEN

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
  backend: shortcut
  config:
    token_secret: osmia-shortcut-token
    workflow_state_name: "Ready for Development"   # exact name from Step 3
    in_progress_state_name: "In Development"       # exact name from Step 3
    completed_state_name: "Ready for Review"       # state set after agent succeeds (optional)
    owner_mention_name: "osmia"                  # mention name from Step 2
    exclude_labels:
      - "osmia-failed"                           # skip stories that previously failed

notifications:
  channels:
    - backend: slack
      config:
        channel_id: "C0XXXXXXXXX"    # Replace with your channel ID from Step 4
        token_secret: osmia-slack-token

engines:
  default: claude-code
  claude-code:
    auth:
      method: api_key
      api_key_secret: osmia-anthropic-key

execution:
  backend: kubernetes

webhook:
  enabled: true
  port: 8081
  shortcut:
    secret: "choose-a-random-secret-string"   # used to verify Shortcut payloads

guardrails:
  max_cost_per_job: 5.00
  max_job_duration_minutes: 60
  allowed_repos:
    - "github.com/your-org/your-repo"
```

!!! tip "Polling vs. webhooks"
    With only polling configured, Osmia checks Shortcut every 30 seconds. Adding the webhook (above) means work starts within a second or two of you moving a story. Both can run together — the webhook speeds things up and polling is the safety net.

!!! tip "Multiple workflows"
    If your workspace has several Shortcut workflows with different state names, replace `workflow_state_name` and `in_progress_state_name` with a `workflows` array:

    ```yaml
    ticketing:
      backend: shortcut
      config:
        token_secret: osmia-shortcut-token
        owner_mention_name: "osmia"
        completed_state_name: "Ready for Review"
        workflows:
          - trigger_state: "Ready for Development"
            in_progress_state: "In Development"
          - trigger_state: "Agent Queue"
            in_progress_state: "In Progress"
    ```

---

## Step 7 — Deploy with Helm

```bash
helm repo add osmia https://unitaryai.github.io/osmia
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
{"level":"INFO","msg":"resolved trigger workflow state","name":"Ready for Development","id":500100002}
{"level":"INFO","msg":"resolved in-progress workflow state","name":"In Development","id":500100003}
{"level":"INFO","msg":"shortcut ticketing backend initialised"}
{"level":"INFO","msg":"slack notification channel initialised"}
{"level":"INFO","msg":"controller initialised and ready"}
```

---

## Step 8 — Register the Shortcut webhook (optional but recommended)

1. In Shortcut, go to **Settings → Integrations → Webhooks**.
2. Click **Add Webhook**.
3. Set the **URL** to `https://YOUR_OSMIA_HOST:8081/webhook/shortcut`.
4. Set the **Secret** to the same value you used for `webhook.shortcut.secret` in Step 6.
5. Click **Save**.

!!! info "Exposing the webhook port"
    You will need an ingress or `LoadBalancer` service exposing port 8081. See the [Kubernetes Quick Start](kubernetes.md) for details on how to configure this with the Helm chart.

---

## Step 9 — Create your first story

1. In Shortcut, create a story in your chosen project with a specific, well-scoped task:

    > **Title:** Add input validation to the POST /api/users endpoint
    >
    > **Description:** The handler does not validate the `email` field. Reject requests with a missing or malformed email with a 400 and a descriptive error. Include unit tests.

2. Assign the story to **@osmia**.
3. Move the story to **Ready for Development**.

Within seconds (webhook) or up to 30 seconds (polling):

- The story moves to **In Development**
- A comment appears on the story: *"Osmia has started work on this story."*
- A Slack message appears in your configured channel

When the agent finishes, a pull request is opened and another Slack message confirms completion.

---

## Troubleshooting

**The state name could not be resolved at startup.**
The controller logs will list every available state in your workspace if the name doesn't match. Check for extra spaces or capitalisation differences. Run the helper script again to copy the name exactly:

```bash
SHORTCUT_TOKEN=your_token ./hack/shortcut-list-states.sh
```

**Stories are not being picked up.**
- Confirm the story is assigned to the correct user (`owner_mention_name` in config).
- Confirm the story is in exactly the state named in `workflow_state_name`.
- Check controller logs for polling errors.

**The webhook is not triggering.**
- Confirm port 8081 is reachable from Shortcut's servers.
- Confirm the webhook secret in Shortcut matches `webhook.shortcut.secret` in your config.
- Check controller logs for `"shortcut webhook signature mismatch"`.

**No Slack message received.**
- Confirm the bot is invited to the channel (`/invite @Osmia`).
- Verify the channel ID (`C0XXXXXXXXX`) is correct — not the channel name.
- Check logs for `"failed to send slack notification"`.

---

## Next Steps

- [Configuration Reference](configuration.md) — all available config options
- [Kubernetes Quick Start](kubernetes.md) — full Helm deployment walkthrough
- [Guard Rails Overview](../concepts/guardrails-overview.md) — safety limits explained
- [GitHub Issues + Slack guide](github-issues-slack.md) — if you use GitHub Issues instead of Shortcut
