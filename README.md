# RoboDev

**Kubernetes-native AI coding agent harness with a real-time intelligence layer.**

RoboDev orchestrates autonomous developer agents (Claude Code, OpenAI Codex, Aider, OpenCode, Cline) inside isolated Kubernetes Jobs. It goes beyond job dispatching — a built-in intelligence layer streams live output from every running agent, scores productivity in real-time, diagnoses failures causally, routes tasks to the best engine, and accumulates cross-task knowledge that improves future runs.

![RoboDev Architecture](docs/images/RoboDev-architecture.png)
---

## What Makes RoboDev Different

Most agent orchestrators are job launchers — they dispatch a task and wait for success or failure. RoboDev adds an **intelligence layer** that actively monitors, coaches, and learns from every running agent:

| Capability | What it does |
|---|---|
| **Real-time streaming** | Agent output flows over a live NDJSON stream — tool calls, cost updates, and content deltas are visible line-by-line as they happen, not after the job finishes |
| **Process Reward Model (PRM)** | Every tool call is scored for productivity. If a trajectory is deteriorating, targeted guidance is delivered to the agent before the situation escalates — like having a senior engineer watching over the agent's shoulder |
| **Episodic memory** | A SQLite-backed knowledge graph accumulates facts, patterns, and engine profiles across tasks. Each new task receives relevant prior knowledge injected into its prompt. Confidence decays over time; stale knowledge is pruned automatically |
| **Causal failure diagnosis** | Failed tasks are not just retried. The failure mode is classified — permission error, test failure, ambiguous spec, environment issue — and a targeted recovery strategy is generated before the next attempt |
| **Intelligent engine routing** | Historical success rates, cost, and task-type affinity guide which engine handles each task. Poor-performing engines are penalised; good ones accumulate positive signal. The selection updates continuously |
| **Predictive cost estimation** | Before a task launches, a k-NN model estimates cost and duration from similar historical outcomes. Operators can enforce pre-approval gates for tasks projected to exceed a threshold |
| **Competitive execution** | Multiple engines can run the same task simultaneously. A judge engine evaluates all outputs and selects the best result — useful for critical tasks where quality matters more than cost |

On top of these, a sophisticated **adaptive watchdog** detects repetitive tool-call loops, cost-velocity spikes, thrashing between files, and idle stalls — and intervenes with targeted actions (nudge, escalate, or terminate) rather than waiting for a blunt timeout.

---

## Core Features

- **5 execution engines** — Claude Code, OpenAI Codex, Aider, OpenCode, and Cline; extensible via gRPC plugin interface
- **Event-driven ingestion** — Webhook receiver for GitHub, GitLab, Slack, Shortcut, and generic sources with HMAC signature validation; polling and webhooks can run together
- **Plugin architecture** — Ticketing (GitHub, Shortcut, Linear), notifications (Slack, Telegram, Discord), secrets (K8s, Vault), SCM (GitHub, GitLab), review (CodeRabbit) — all swappable
- **Task-scoped secrets** — Declarative secret references in ticket descriptions (`<!-- robodev:secrets -->`) resolved via K8s or Vault with per-tenant policy and structured audit logging
- **Human-in-the-loop** — Approval gates at `pre_start` and `pre_merge` hold execution until a human responds via Slack; the watchdog cancels unanswered requests
- **Defence in depth** — Seven layered safety boundaries: controller guard rails, engine hooks, prompt injection, quality gate, adaptive watchdog, NetworkPolicies, and secret resolution policy
- **Kubernetes-native** — Isolated agent pods with non-root, read-only-FS, dropped-all-capabilities security contexts; RBAC, PodDisruptionBudgets, and optional gVisor/Kata sandboxing
- **Enterprise-ready** — Multi-tenancy with namespace-per-tenant isolation, cost budgets per task, structured JSON logging, Prometheus metrics, and Grafana dashboards

---


## Quick Start

**Prefer a step-by-step guide?** See the setup guides for your tooling:

- [GitHub Issues + Slack](docs/getting-started/github-issues-slack.md)
- [Shortcut + Slack](docs/getting-started/shortcut-slack.md)

### Install with Helm

```bash
kubectl create namespace robodev

# Store credentials as Kubernetes Secrets
kubectl create secret generic robodev-github-token \
  -n robodev --from-literal=token=ghp_YOUR_TOKEN
kubectl create secret generic robodev-anthropic-key \
  -n robodev --from-literal=api_key=sk-ant-YOUR_KEY

# Deploy
helm repo add robodev https://unitaryai.github.io/robodev/charts
helm install robodev robodev/robodev \
  -n robodev -f robodev-config.yaml
```

### Minimal `robodev-config.yaml`

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

### Enable the intelligence layer

Add any of these blocks to your config to enable the novel features:

```yaml
# Real-time agent coaching
prm:
  enabled: true
  score_threshold_nudge: 7      # intervene when score drops below 7/10
  score_threshold_escalate: 3   # escalate when score drops below 3/10

# Cross-task episodic memory
memory:
  enabled: true
  store_path: /data/memory.db
  decay_interval_hours: 24

# Intelligent engine routing
routing:
  enabled: true
  min_samples: 5                # minimum history before routing kicks in

# Pre-execution cost estimation with approval gate
estimator:
  enabled: true
  approval_threshold_usd: 10.0  # require approval for tasks estimated above $10

# Competitive execution (run multiple engines, pick the best)
competitive_execution:
  enabled: true
  default_candidates: 3
  judge_engine: claude-code
```

---

## Guard Rails

RoboDev provides seven layered safety boundaries:

1. **Controller-level** — Cost limits, concurrent job caps, allowed repos, task type restrictions
2. **Engine-level** — Claude Code hooks that block dangerous commands and sensitive file access
3. **Prompt-level** — `guardrails.md` appended to every agent prompt with natural-language policies
4. **Quality gate** — Optional post-completion review (via CodeRabbit or custom reviewer) before merge request creation
5. **Adaptive watchdog** — Detects and intervenes on tool-call loops, cost-velocity spikes, thrashing, and idle stalls
6. **Network policies** — Agent pods deny all ingress and restrict egress to HTTPS + SSH; controller pods restrict ingress to webhook and metrics ports
7. **Secret resolution policy** — Allowed/blocked environment variable patterns, URI scheme allowlists, and per-tenant scoping prevent secret exfiltration

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
| Engine | Claude Code, Codex, Aider, OpenCode, Cline | Custom |

Write plugins in **any language** with gRPC support. SDKs are provided for Python, Go, and TypeScript. See [docs/plugins/writing-a-plugin.md](docs/plugins/writing-a-plugin.md).

---

## Execution Engines

| Engine | Guard Rails | Providers |
|---|---|---|
| Claude Code | Hooks + prompt | Anthropic |
| OpenAI Codex | Prompt-only | OpenAI |
| Aider | Prompt-only | Anthropic, OpenAI |
| OpenCode | Prompt-only | Anthropic, OpenAI, Google |
| Cline | Prompt-only + MCP | Anthropic, OpenAI, Google, Bedrock |

Engine fallback chains are configurable — if the primary engine fails, RoboDev automatically retries with the next engine in the chain. With intelligent routing enabled, engine selection adapts based on historical performance. See [docs/plugins/engines.md](docs/plugins/engines.md) for the full comparison.

---

## Observability

- **Prometheus metrics** — `robodev_taskruns_total`, `robodev_active_jobs`, `robodev_taskrun_duration_seconds`, `robodev_prm_interventions_total`, `robodev_memory_nodes_total`, and more
- **Grafana dashboard** — Included in the Helm chart ([`charts/robodev/dashboards/`](charts/robodev/dashboards/))
- **Structured logging** — JSON output via Go's `slog`; all log lines include task run ID and engine name for correlation

---

## Security

See [docs/security.md](docs/security.md) for the full threat model.

- Non-root containers with read-only root filesystems and dropped capabilities
- NetworkPolicies for agent pods (deny all ingress, restrict egress to HTTPS/SSH)
- Webhook HMAC-SHA256 signature validation with replay attack prevention
- Task-scoped secret resolution — secrets are never logged; audit log records every access
- RBAC scoped to minimum required permissions
- Image signing with cosign and SBOM generation with syft
- Optional gVisor/Kata Containers sandboxing for higher-risk workloads

---

## Project Structure

```
cmd/robodev/              — Controller entrypoint (all backends wired here)
internal/controller/      — Reconciliation loop
internal/agentstream/     — Real-time NDJSON event stream from running agents
internal/prm/             — Process Reward Model (real-time agent coaching)
internal/memory/          — Episodic memory (cross-task knowledge graph + SQLite store)
internal/diagnosis/       — Causal failure diagnosis and self-healing retry logic
internal/routing/         — Intelligent engine selection from historical performance
internal/estimator/       — Predictive cost + duration estimation (k-NN)
internal/tournament/      — Competitive execution (parallel engines, AI judge)
internal/watchdog/        — Adaptive watchdog (loop/thrash/stall detection)
internal/llm/             — DSPy-inspired LLM abstraction (signatures, modules, budget)
internal/jobbuilder/      — ExecutionSpec → Kubernetes Job
internal/taskrun/         — TaskRun state machine + idempotency store
internal/config/          — Configuration loading
internal/metrics/         — Prometheus metrics
internal/webhook/         — Webhook receiver (GitHub, GitLab, Slack, Shortcut, generic)
internal/secretresolver/  — Task-scoped secret resolution + policy + audit
internal/promptbuilder/   — Prompt construction (profiles, guard rails, memory injection)
internal/sandboxbuilder/  — gVisor/Kata sandbox CR builder
pkg/engine/               — ExecutionEngine interface + built-in engines
pkg/plugin/               — Plugin interfaces + built-in backends
proto/                    — Protobuf definitions (source of truth for all interfaces)
charts/robodev/           — Helm chart (NetworkPolicy, PDB, dashboards)
docker/                   — Multi-stage Dockerfiles for controller + engines
docs/                     — Documentation site (MkDocs Material)
```

---

## Development

```bash
# Prerequisites: Go 1.23+, gofumpt, golangci-lint, buf

make test          # Run all unit tests
make build         # Build controller binary
make lint          # Run linter
make fmt           # Format code (gofumpt)
make proto-lint    # Lint protobuf definitions
make docker-build  # Build all container images
make helm-lint     # Lint Helm chart
```

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
