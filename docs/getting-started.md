# Getting Started with RoboDev

This guide walks you through installing RoboDev, configuring it with GitHub Issues and Claude Code, and creating your first automated task. By the end you will have a working deployment that picks up labelled GitHub issues, spins up an AI coding agent, and opens a pull request with the result.

## Prerequisites

Before you begin, ensure you have the following:

| Requirement | Minimum Version | Notes |
|---|---|---|
| Kubernetes cluster | 1.28+ | A local `kind` or `minikube` cluster works for evaluation |
| Helm | 3.x | Used to deploy the RoboDev controller |
| kubectl | Matching your cluster version | For inspecting pods, logs, and secrets |
| GitHub repository | — | The repo the agent will work on |
| GitHub personal access token | — | With `repo` and `issues` scopes |
| Anthropic API key | — | Required for Claude Code engine (or an OpenAI key for Codex) |

Optional but recommended:

- **Slack workspace** — for real-time notifications when tasks start, complete, or fail
- **Prometheus + Grafana** — for monitoring agent activity and cost tracking

## Quick Start — GitHub Issues + Claude Code

This section covers the simplest path to a working RoboDev deployment. It uses GitHub Issues for ticketing, Claude Code as the execution engine, and optionally Slack for notifications.

### 1. Create Kubernetes Secrets

RoboDev reads credentials from Kubernetes Secrets. Create them in the namespace where you will install the chart:

```bash
# Create the namespace (if it does not already exist)
kubectl create namespace robodev

# GitHub personal access token (needs repo + issues scopes)
kubectl create secret generic robodev-github-token \
  --namespace robodev \
  --from-literal=token='ghp_your_github_token_here'

# Anthropic API key for Claude Code
kubectl create secret generic robodev-anthropic-key \
  --namespace robodev \
  --from-literal=api_key='sk-ant-your_anthropic_key_here'

# (Optional) Slack bot token for notifications
kubectl create secret generic robodev-slack-token \
  --namespace robodev \
  --from-literal=token='xoxb-your-slack-bot-token'
```

### 2. Write a values.yaml

Create a `values.yaml` file that configures ticketing, the engine, and (optionally) notifications. Replace the placeholder values with your own organisation and repository details.

```yaml
replicaCount: 1

image:
  repository: ghcr.io/unitaryai/robodev
  pullPolicy: IfNotPresent
  tag: "latest"

config:
  ticketing:
    backend: github
    config:
      owner: "your-org"
      repo: "your-repo"
      labels:
        - "robodev"
      token_secret: "robodev-github-token"

  engines:
    default: claude-code

  secrets:
    backend: k8s

  scm:
    backend: github
    config:
      token_secret: "robodev-github-token"

  notifications:
    channels:
      - backend: slack
        config:
          channel_id: "C0123456789"
          token_secret: "robodev-slack-token"

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

> **Tip:** A complete example lives in `examples/github-slack/values.yaml` in the RoboDev repository.

### 3. Install with Helm

```bash
helm repo add robodev https://unitaryai.github.io/robodev
helm repo update

helm install robodev robodev/robodev \
  --namespace robodev \
  --values values.yaml
```

### 4. Label a GitHub Issue

Create an issue in your target repository describing a small code change, then add the **robodev** label. The controller polls for issues matching the configured labels and will pick it up within a few seconds.

### 5. Watch It Work

```bash
# Follow the controller logs
kubectl logs -n robodev deployment/robodev -f

# List agent jobs once the task is picked up
kubectl get jobs -n robodev
```

The agent will clone the repository, carry out the work described in the issue, run any tests it finds, and open a pull request. Progress updates are posted as comments on the original issue (and to Slack if configured).

## Configuration Reference

RoboDev is configured via a YAML file (`robodev-config.yaml`) which is mounted into the controller pod as a ConfigMap. When deploying with Helm, you set configuration under the `config:` key in your `values.yaml` and the chart creates the ConfigMap for you.

The top-level sections are:

| Section | Purpose |
|---|---|
| `ticketing` | Where tasks come from (GitHub Issues, GitLab Issues, Jira via plugin) |
| `engines` | Which AI coding agents are available and which is the default |
| `notifications` | Where status updates are sent (Slack, Microsoft Teams via plugin) |
| `secrets` | How the controller retrieves credentials (`k8s` for Kubernetes Secrets) |
| `scm` | Source code management backend for cloning and opening PRs |
| `guardrails` | Safety boundaries — cost limits, concurrency limits, blocked file patterns |
| `tenancy` | Multi-tenancy mode (`shared` or `namespace-per-tenant`) |
| `quality_gate` | Optional AI-powered review of agent output before merging |
| `review` | Review backend configuration |
| `progress_watchdog` | Detects stalled or looping agent jobs and intervenes |
| `plugin_health` | Health monitoring and restart behaviour for gRPC plugins |

For the full set of fields and their defaults, see `charts/robodev/values.yaml` and the struct definitions in `internal/config/config.go`.

## Verifying the Installation

After `helm install` completes, confirm everything is healthy:

```bash
# Check the controller pod is running
kubectl get pods -n robodev
# Expected: robodev-xxxxx   1/1   Running   0   ...

# Check the health endpoints
kubectl port-forward -n robodev deployment/robodev 8080:8080 &
curl http://localhost:8080/healthz   # should return 200
curl http://localhost:8080/readyz    # should return 200

# Check Prometheus metrics are being served
curl -s http://localhost:8080/metrics | head -20

# Check the controller logs for startup messages
kubectl logs -n robodev deployment/robodev --tail=50
```

You should see structured JSON log lines confirming that the ticketing poller has started and the engine is ready.

## Creating Your First Task

1. **Open an issue** in the repository you configured. Give it a clear title and description — for example:

   > **Title:** Add input validation to the /api/users endpoint
   >
   > **Body:** The POST handler for `/api/users` does not validate the `email` field. Add validation that rejects requests with a missing or malformed email address. Return a 400 status with a descriptive error message. Add unit tests for the new behaviour.

2. **Add the `robodev` label** to the issue (or whichever label you configured in `config.ticketing.config.labels`).

3. **Watch the controller logs** — within a few seconds you should see a log entry like:

   ```json
   {"level":"info","msg":"task picked up","issue":42,"repo":"your-org/your-repo"}
   ```

4. **Monitor the agent job**:

   ```bash
   kubectl get jobs -n robodev -w
   ```

5. **Review the pull request** — once the job completes, the agent opens a PR against the repository. The issue will be updated with a comment linking to the PR.

## Customising Guard Rails

Guard rails are safety boundaries that constrain what the agent is permitted to do. They operate at two levels:

### Controller-Level Guard Rails

These are set in your `values.yaml` under `config.guardrails`:

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
    require_human_approval_before_mr: true   # Require a human to approve before the PR is opened
```

### Repository-Level Guard Rails

Place a `guardrails.md` file in the root of any repository the agent works on. The agent reads this file before starting work and follows the constraints it defines. A typical `guardrails.md` looks like this:

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

See [Guard Rails documentation](guardrails.md) for the full specification.

## Choosing an Engine

RoboDev supports multiple AI coding agents. You select the default engine in your configuration and can override it per-task via issue labels.

| Engine | Best For | Auth Method |
|---|---|---|
| **Claude Code** | General-purpose coding, large refactors, multi-file changes | Anthropic API key |
| **Codex** | OpenAI-ecosystem shops, specialised coding tasks | OpenAI API key |
| **Aider** | Lightweight edits, cost-sensitive workloads | Anthropic or OpenAI API key |

To switch the default engine, update your `values.yaml`:

```yaml
config:
  engines:
    default: codex    # or "aider"
```

You can also configure engine-specific settings such as custom container images and authentication methods:

```yaml
config:
  engines:
    default: claude-code
    claude-code:
      auth:
        method: api_key
        api_key_secret: "robodev-anthropic-key"
    codex:
      auth:
        method: api_key
        api_key_secret: "robodev-openai-key"
```

See [Engine documentation](plugins/engines.md) for the full list of options including Bedrock and Vertex AI authentication.

## Troubleshooting

### Controller pod is not starting

```bash
kubectl describe pod -n robodev -l app.kubernetes.io/name=robodev
kubectl logs -n robodev deployment/robodev --previous
```

Common causes:
- **ImagePullBackOff** — check your `image.repository` and `image.tag` values, and ensure `imagePullSecrets` is set if using a private registry.
- **CrashLoopBackOff** — inspect logs for configuration errors. The most common issue is a missing or malformed `config` section in your values.

### Issues are not being picked up

- Confirm the issue has the correct label (must match `config.ticketing.config.labels` exactly).
- Check that `config.ticketing.config.owner` and `config.ticketing.config.repo` match the repository.
- Verify the GitHub token secret exists and has the required scopes:

  ```bash
  kubectl get secret robodev-github-token -n robodev
  ```

- Look for polling errors in the controller logs:

  ```bash
  kubectl logs -n robodev deployment/robodev | grep -i "ticketing"
  ```

### Agent jobs are failing

```bash
# List recent jobs and their status
kubectl get jobs -n robodev

# Get logs from a failed job's pod
kubectl logs -n robodev job/<job-name>
```

Common causes:
- **API key invalid or expired** — verify your Anthropic or OpenAI secret contains a valid key.
- **Cost limit reached** — the job was terminated because it exceeded `max_cost_per_job`. Increase the limit or simplify the task.
- **Duration limit reached** — the job exceeded `max_job_duration_minutes`. Increase the limit or break the task into smaller pieces.

### Metrics endpoint is not working

Ensure `metrics.enabled` is set to `true` in your values (this is the default). The metrics are served on the port specified by `metrics.port` (default `8080`). If you are using a `ServiceMonitor`, confirm that `metrics.serviceMonitor.enabled` is `true` and the labels match your Prometheus operator configuration.

## Next Steps

Now that you have a working RoboDev installation, explore the rest of the documentation:

- [Architecture overview](architecture.md) — how the controller, plugins, and engines fit together
- [Security model](security.md) — threat model, defence in depth, and hardening guidance
- [Scaling guide](scaling.md) — horizontal scaling, Karpenter node pools, KEDA autoscaling
- [Guard rails](guardrails.md) — full guard rail specification and advanced configuration
- [Writing a plugin](plugins/writing-a-plugin.md) — extend RoboDev with custom ticketing, notification, or secret backends
- [Engine reference](plugins/engines.md) — detailed engine configuration and tuning
