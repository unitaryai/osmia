# Quick Start: Kubernetes

This guide walks you through installing Osmia on a Kubernetes cluster, configuring it with GitHub Issues and Claude Code, and creating your first automated task. By the end you will have a working deployment that picks up labelled GitHub issues, spins up an AI coding agent, and opens a pull request with the result.

## Prerequisites

| Requirement | Minimum Version | Notes |
|---|---|---|
| Kubernetes cluster | 1.28+ | A local `kind` or `minikube` cluster works for evaluation |
| Helm | 3.x | Used to deploy the Osmia controller |
| kubectl | Matching your cluster version | For inspecting pods, logs, and secrets |
| GitHub repository | — | The repo the agent will work on |
| GitHub personal access token | — | With `repo` and `issues` scopes |
| Anthropic API key | — | Required for Claude Code engine (or an OpenAI key for Codex) |

Optional but recommended:

- **Slack workspace** — for real-time notifications when tasks start, complete, or fail
- **Prometheus + Grafana** — for monitoring agent activity and cost tracking

## 1. Create Kubernetes Secrets

Osmia reads credentials from Kubernetes Secrets. Create them in the namespace where you will install the chart:

```bash
# Create the namespace (if it does not already exist)
kubectl create namespace osmia

# GitHub personal access token (needs repo + issues scopes)
kubectl create secret generic osmia-github-token \
  --namespace osmia \
  --from-literal=token='ghp_your_github_token_here'

# Anthropic API key for Claude Code
kubectl create secret generic osmia-anthropic-key \
  --namespace osmia \
  --from-literal=api_key='sk-ant-your_anthropic_key_here'

# (Optional) Slack bot token for notifications
kubectl create secret generic osmia-slack-token \
  --namespace osmia \
  --from-literal=token='xoxb-your-slack-bot-token'
```

## 2. Write a values.yaml

Create a `values.yaml` file that configures ticketing, the engine, and (optionally) notifications. Replace the placeholder values with your own organisation and repository details.

```yaml
replicaCount: 1

image:
  repository: ghcr.io/unitaryai/osmia
  pullPolicy: IfNotPresent
  tag: "latest"

config:
  ticketing:
    backend: github
    config:
      owner: "your-org"
      repo: "your-repo"
      labels:
        - "osmia"
      token_secret: "osmia-github-token"

  engines:
    default: claude-code

  secrets:
    backend: k8s

  scm:
    backend: github
    config:
      token_secret: "osmia-github-token"

  notifications:
    channels:
      - backend: slack
        config:
          channel_id: "C0123456789"
          token_secret: "osmia-slack-token"

  guardrails:
    max_cost_per_job: 50.0
    max_concurrent_jobs: 3
    max_job_duration_minutes: 120
    blocked_file_patterns:
      - "*.env"
      - "**/secrets/**"
      - "**/credentials/**"

resources:
  limits:
    cpu: 500m
    memory: 256Mi
  requests:
    cpu: 100m
    memory: 128Mi
```

!!! tip
    A complete example lives in `examples/github-slack/values.yaml` in the Osmia repository.

## 3. Install with Helm

```bash
helm repo add osmia https://unitaryai.github.io/Osmia
helm repo update

helm install osmia osmia/osmia \
  --namespace osmia \
  --values values.yaml
```

## 4. Verify the Installation

```bash
# Check the controller pod is running
kubectl get pods -n osmia
# Expected: osmia-xxxxx   1/1   Running   0   ...

# Check the health endpoints
kubectl port-forward -n osmia deployment/osmia 8080:8080 &
curl http://localhost:8080/healthz   # should return 200
curl http://localhost:8080/readyz    # should return 200

# Check Prometheus metrics are being served
curl -s http://localhost:8080/metrics | head -20

# Check the controller logs for startup messages
kubectl logs -n osmia deployment/osmia --tail=50
```

You should see structured JSON log lines confirming that the ticketing poller has started and the engine is ready.

## 5. Label a GitHub Issue

Create an issue in your target repository describing a small code change, then add the **osmia** label. The controller polls for issues matching the configured labels and will pick it up within a few seconds.

Example issue:

> **Title:** Add input validation to the /api/users endpoint
>
> **Body:** The POST handler for `/api/users` does not validate the `email` field. Add validation that rejects requests with a missing or malformed email address. Return a 400 status with a descriptive error message. Add unit tests for the new behaviour.

## 6. Watch It Work

```bash
# Follow the controller logs
kubectl logs -n osmia deployment/osmia -f

# List agent jobs once the task is picked up
kubectl get jobs -n osmia
```

The agent will clone the repository, carry out the work described in the issue, run any tests it finds, and open a pull request. Progress updates are posted as comments on the original issue (and to Slack if configured).

## Customising Guard Rails

Guard rails are safety boundaries that constrain what the agent is permitted to do. They operate at two levels:

### Controller-Level Guard Rails

Set in your `values.yaml` under `config.guardrails`:

```yaml
config:
  guardrails:
    max_cost_per_job: 25.0            # Maximum USD spend per task
    max_concurrent_jobs: 3            # Limit parallel agent jobs
    max_job_duration_minutes: 60      # Kill jobs that run too long
    allowed_repos:                    # Restrict which repos the agent may work on
      - "your-org/frontend"
      - "your-org/backend"
    blocked_file_patterns:            # Files the agent must never modify
      - "*.env"
      - "**/migrations/**"
      - ".github/**"
    require_human_approval_before_mr: true
```

### Repository-Level Guard Rails

Place a `guardrails.md` (or `CLAUDE.md`) file in the root of any repository the agent works on. Claude Code reads `CLAUDE.md` automatically at startup — other engines do not. These files are advisory: the controller does not enforce compliance, but they are the simplest way to give Claude Code per-repo constraints.

!!! note
    Prompt-builder injection of `guardrails.md` for all engines is on the roadmap.

```markdown
## Never Do
- Never modify CI/CD pipeline configuration files
- Never change database migration files
- Never alter authentication or authorisation logic
- Never commit secrets, API keys, or credentials

## Always Do
- Always run the full test suite before creating a pull request
- Always add tests for new functionality
- Always follow the existing code style in the repository
- Always create a new branch for changes (never push to main)
```

See [Guard Rails documentation](../guardrails.md) for the full specification.

## Choosing an Engine

Osmia supports multiple AI coding agents. See [Engines Explained](../concepts/engines.md) for a comparison, or the full [Engine Reference](../plugins/engines.md) for detailed configuration.

| Engine | Best For | Guard Rails |
|---|---|---|
| **Claude Code** | General-purpose coding, large refactors | Hook-based (deterministic) |
| **Codex** | OpenAI-ecosystem shops | Prompt-based (advisory) |
| **Aider** | Lightweight edits, cost-sensitive workloads | Prompt-based (advisory) |
| **OpenCode** | BYOM (Anthropic/OpenAI/Google) | Prompt-based (advisory) |
| **Cline** | AWS Bedrock, MCP support *(community template — no pre-built image)* | Prompt-based (advisory) |

To switch the default engine:

```yaml
config:
  engines:
    default: codex    # or "aider", "opencode"
```

## Webhook Setup (Optional)

Instead of polling, Osmia can receive webhook events for near-instant ticket ingestion.

### Enable webhooks

```yaml
webhook:
  enabled: true
  port: 8081

config:
  webhook:
    github:
      secret: "your-github-webhook-secret"
```

### Configure your provider

- **GitHub:** Add a webhook pointing to `https://<your-osmia-host>:8081/webhooks/github`. Set content type to `application/json`, provide the same secret, and select "Issues" events.
- **GitLab:** Add a webhook to `https://<your-osmia-host>:8081/webhooks/gitlab` with the secret token. Select "Issues events" and "Merge request events".
- **Slack:** Configure a Slack app with an interactive endpoint at `https://<your-osmia-host>:8081/webhooks/slack`.

!!! info "Network policies"
    If `networkPolicy.enabled` is set, the controller network policy automatically allows ingress on the webhook port. You can restrict the source CIDR via `networkPolicy.controller.webhookSourceCIDR`.

## Next Steps

- [Configuration Reference](configuration.md) — full config options and defaults
- [Troubleshooting](troubleshooting.md) — common issues and solutions
- [Architecture overview](../architecture.md) — how the controller, plugins, and engines fit together
- [Security model](../security.md) — threat model, defence in depth, and hardening guidance
- [Scaling guide](../scaling.md) — horizontal scaling, Karpenter, KEDA
- [Writing a plugin](../plugins/writing-a-plugin.md) — extend Osmia with custom backends
