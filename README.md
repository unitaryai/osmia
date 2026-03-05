# RoboDev

**Kubernetes-native AI coding agent harness with a real-time intelligence layer.**

RoboDev orchestrates autonomous developer agents (Claude Code, OpenAI Codex, Aider, OpenCode) inside isolated Kubernetes Jobs. It goes beyond job dispatching — a built-in intelligence layer streams live output from every running agent, scores productivity in real-time, diagnoses failures causally, routes tasks to the best engine, and accumulates cross-task knowledge that improves future runs.

![RoboDev Architecture](docs/images/RoboDev-architecture.png)

**[Documentation →](https://unitaryai.github.io/RoboDev/)** &nbsp;|&nbsp; [Quick Start](#quick-start) &nbsp;|&nbsp; [Plugin System](#plugin-system)

---

## What Makes RoboDev Different

Most agent orchestrators are job launchers — they dispatch a task and wait for success or failure. RoboDev adds an **intelligence layer** that actively monitors, coaches, and learns from every running agent:

| Capability | What it does |
|---|---|
| **Real-time streaming** | Agent output flows over a live NDJSON stream — tool calls, cost updates, and content deltas are visible line-by-line as they happen, not after the job finishes |
| **Process Reward Model (PRM)** | Every tool call is scored for productivity. If a trajectory is deteriorating, targeted guidance is injected before the situation escalates — like having a senior engineer watching over the agent's shoulder |
| **Episodic memory** | A SQLite-backed knowledge graph accumulates facts, patterns, and engine profiles across tasks. Each new task receives relevant prior knowledge injected into its prompt. Confidence decays over time; stale knowledge is pruned automatically |
| **Causal failure diagnosis** | Failed tasks are not just retried. The failure mode is classified — permission error, test failure, ambiguous spec, environment issue — and a targeted recovery strategy is generated before the next attempt |
| **Intelligent engine routing** | Historical success rates, cost, and task-type affinity guide which engine handles each task. Poor-performing engines are penalised; good ones accumulate positive signal. The selection updates continuously |
| **Predictive cost estimation** | Before a task launches, a k-NN model estimates cost and duration from similar historical outcomes. Operators can enforce pre-approval gates for tasks projected to exceed a threshold |
| **Competitive execution** | Multiple engines run the same task simultaneously. A judge engine evaluates all outputs and selects the best result — useful for critical tasks where quality matters more than cost |
| **Review-response loop** | RoboDev monitors PRs it opens, classifies incoming review comments, and automatically spawns follow-up jobs to address actionable feedback — turning a single-pass agent into a review-responsive loop |

An adaptive **watchdog** detects repetitive tool-call loops, cost-velocity spikes, thrashing between files, and idle stalls — intervening with targeted actions (nudge, escalate, or terminate) rather than waiting for a blunt timeout.

---

## Core Features

- **4 execution engines** — Claude Code, OpenAI Codex, Aider, and OpenCode; extensible via gRPC plugin interface
- **Event-driven ingestion** — Webhooks for GitHub, GitLab, Slack, Shortcut, and generic sources with HMAC signature validation
- **Plugin architecture** — Ticketing, notifications, secrets, SCM, and review backends are all swappable; write plugins in any language with gRPC support
- **Human-in-the-loop** — Approval gates at `pre_start` and `pre_merge` hold execution until a human approves via Slack
- **Task-scoped secrets** — Declarative secret references resolved via Kubernetes Secrets or HashiCorp Vault with per-tenant policy and structured audit logging
- **Defence in depth** — Seven layered safety boundaries: controller guard rails, engine hooks, prompt injection, quality gate, adaptive watchdog, NetworkPolicies, and secret resolution policy
- **Kubernetes-native** — Isolated agent pods with non-root, read-only-FS, dropped-all-capabilities security contexts; optional gVisor/Kata Containers sandboxing
- **Enterprise-ready** — Multi-tenancy, cost budgets per task, Prometheus metrics, and Grafana dashboards

---

## Quick Start

```bash
kubectl create namespace robodev

kubectl create secret generic robodev-github-token \
  -n robodev --from-literal=token=ghp_YOUR_TOKEN
kubectl create secret generic robodev-anthropic-key \
  -n robodev --from-literal=api_key=sk-ant-YOUR_KEY

helm repo add robodev https://unitaryai.github.io/RoboDev/charts
helm install robodev robodev/robodev -n robodev -f robodev-config.yaml
```

Minimal `robodev-config.yaml`:

```yaml
ticketing:
  backend: github
  config:
    owner: "your-org"
    repo:  "your-repo"
    token_secret: robodev-github-token
    labels: ["robodev"]

engines:
  default: claude-code
  claude-code:
    auth:
      method: api_key
      api_key_secret: robodev-anthropic-key

guardrails:
  max_cost_per_task_usd: 5.00
  max_duration_minutes: 60
  allowed_repos:
    - "github.com/your-org/your-repo"
```

Label a GitHub issue `robodev` and the controller picks it up, launches a Claude Code agent, and opens a pull request with the changes.

For step-by-step setup guides, the full configuration reference, and instructions for enabling the intelligence layer features, see the **[documentation](https://unitaryai.github.io/RoboDev/)**.

---

## Plugin System

All external integrations are pluggable. Built-in plugins are compiled into the controller; third-party plugins run as gRPC subprocesses via [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin).

| Interface | Built-in | Third-party examples |
|---|---|---|
| Ticketing | GitHub Issues, Shortcut, Linear | Jira, Monday.com |
| Notifications | Slack, Telegram, Discord | Teams, PagerDuty |
| Approval | Slack | Teams |
| Secrets | K8s Secrets, HashiCorp Vault | AWS SM, 1Password |
| SCM | GitHub, GitLab | Bitbucket |
| Review | CodeRabbit | Custom |
| Engine | Claude Code, Codex, Aider, OpenCode | Custom |

SDKs are provided for Python, Go, and TypeScript. See the [plugin docs](https://unitaryai.github.io/RoboDev/plugins/writing-a-plugin/) for details.

---

## Development

```bash
make test          # Run all unit tests
make build         # Build controller binary
make lint          # Run linter
make proto-lint    # Lint protobuf definitions
make docker-build  # Build all container images
```

Prerequisites: Go 1.23+, `gofumpt`, `golangci-lint`, `buf`.

---

## Contributing

We welcome contributions. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

- [Conventional commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `test:`
- British English in all comments, docs, and user-facing strings
- Table-driven tests with `testify`
- `gofumpt` formatting and `golangci-lint` clean

---

## Licence

Apache 2.0. See [LICENCE](LICENCE).
