# RoboDev

**Kubernetes-native AI coding agent harness for enterprise-grade autonomous development at scale.**

RoboDev orchestrates autonomous developer agents (Claude Code, OpenAI Codex, Aider) inside isolated Kubernetes Jobs to perform maintenance and development tasks on your codebases. It is security-first, plugin-extensible, and built on Kubernetes primitives for isolation, observability, and scaling.

```
Ticketing Backend          RoboDev Controller           K8s Job (isolated)
(GitHub Issues, Jira)  ->  (Go operator, K8s-native) -> (AI agent + code)
        |                        |                           |
        |                   Guard Rails                      |
        |                   Cost Control                     v
        v                   Watchdog              Pull Request / Merge Request
   Slack / Teams                                        + Review
```

## Key Features

- **Multi-engine support** — Claude Code, OpenAI Codex, and Aider out of the box; extensible via plugin interface
- **Plugin architecture** — Ticketing (GitHub, Jira), notifications (Slack, Teams), secrets (K8s, Vault), SCM (GitHub, GitLab), review (CodeRabbit) — all swappable
- **Defence in depth** — Controller-level guard rails, engine-level hooks, `guardrails.md` prompt injection, quality gate, progress watchdog
- **Kubernetes-native** — Each agent runs in an isolated pod with restricted security contexts, RBAC, and network policies
- **Enterprise-ready** — Multi-tenancy, cost budgets, structured logging, Prometheus metrics, Grafana dashboards
- **Scaling** — Karpenter integration for auto-provisioning agent nodes; configurable concurrency limits

## Architecture

```
                        +---------------------------+
                        |     RoboDev Controller     |
                        |  (Go, controller-runtime)  |
                        +--+----+----+----+----+---+
                           |    |    |    |    |
              +------------+    |    |    |    +----------+
              |                 |    |    |               |
        +-----v------+   +-----v-+  | +--v------+  +----v--------+
        | Ticketing   |   |Secrets|  | |  SCM    |  |Notification |
        | (GitHub,    |   |(K8s,  |  | |(GitHub, |  |(Slack,      |
        |  Jira, ...) |   |Vault) |  | |GitLab)  |  | Teams, ...) |
        +-------------+   +-------+  | +---------+  +-------------+
                                      |
                           +----------v----------+
                           |    Engine Registry   |
                           | Claude Code | Codex  |
                           |    Aider    | ...    |
                           +----------+----------+
                                      |
                           +----------v----------+
                           |    K8s Job (pod)     |
                           | +------------------+ |
                           | | AI Agent         | |
                           | | + Guard Rail     | |
                           | |   Hooks          | |
                           | +------------------+ |
                           | | Result:          | |
                           | |  result.json     | |
                           | +------------------+ |
                           +----------------------+
```

## Quick Start

### Prerequisites

- Kubernetes 1.28+ cluster
- Helm 3.x
- GitHub personal access token (for ticketing + SCM)
- Anthropic API key (for Claude Code engine)

### Install

```bash
# Add secrets
kubectl create namespace robodev
kubectl create secret generic robodev-github-token \
  -n robodev --from-literal=token=ghp_YOUR_TOKEN
kubectl create secret generic robodev-anthropic-key \
  -n robodev --from-literal=api-key=sk-ant-YOUR_KEY

# Install with Helm
helm install robodev ./charts/robodev \
  -n robodev \
  -f examples/github-slack/values.yaml
```

### Trigger a Task

Label a GitHub issue with `robodev` and the controller will pick it up, launch a Claude Code agent, and create a pull request with the changes.

### Monitor

```bash
kubectl logs -f deployment/robodev -n robodev
kubectl get jobs -n robodev -w
```

## Configuration

RoboDev is configured via `robodev-config.yaml`, mounted as a ConfigMap:

```yaml
ticketing:
  backend: github
  config:
    owner: "your-org"
    repo: "your-repo"
    labels: ["robodev"]

engines:
  default: claude-code

guardrails:
  max_cost_per_job: 50.0
  max_concurrent_jobs: 5
  max_job_duration_minutes: 120
  blocked_file_patterns:
    - "*.env"
    - "**/secrets/**"
```

See [`examples/`](examples/) for full configuration examples:
- [`github-slack/`](examples/github-slack/) — Minimal setup with GitHub + Slack
- [`gitlab-teams/`](examples/gitlab-teams/) — GitLab + Microsoft Teams
- [`enterprise/`](examples/enterprise/) — Full enterprise with multi-tenancy, quality gate, agent teams

## Guard Rails

RoboDev provides layered safety boundaries:

1. **Controller-level** — Cost limits, concurrent job caps, allowed repos, task type restrictions
2. **Engine-level** — Claude Code hooks that block dangerous commands and sensitive file access
3. **Prompt-level** — `guardrails.md` appended to every agent prompt with natural-language policies
4. **Quality gate** — Optional post-completion review before merge request creation
5. **Progress watchdog** — Detects looping, thrashing, or stalled agents during execution

## Plugin System

All external integrations are pluggable. Built-in plugins are compiled into the controller; third-party plugins run as gRPC subprocesses via [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin).

| Interface | Built-in | Third-party examples |
|-----------|----------|---------------------|
| Ticketing | GitHub Issues | Jira, Linear, Monday.com |
| Notifications | Slack | Teams, Discord, Telegram |
| Approval | Slack | Teams |
| Secrets | K8s Secrets | Vault, AWS SM, 1Password |
| SCM | GitHub, GitLab | Bitbucket |
| Review | CodeRabbit | Custom |
| Engine | Claude Code, Codex, Aider | Custom |

Write plugins in **any language** with gRPC support. SDKs are provided for Python, Go, and TypeScript. See [docs/plugins/writing-a-plugin.md](docs/plugins/writing-a-plugin.md).

## Execution Engines

| Engine | Status | Guard Rails |
|--------|--------|-------------|
| Claude Code | Production | Hooks + prompt |
| OpenAI Codex | Production | Prompt-only |
| Aider | Production | Prompt-only |

## Scaling

- **Karpenter** — Auto-provisions dedicated nodes for agent workloads with spot instance support
- **Concurrency limits** — Configurable `max_concurrent_jobs`
- **Leader election** — HA via controller-runtime Lease objects
- **Multi-tenancy** — Namespace-per-tenant isolation with separate RBAC and secrets

See [docs/scaling.md](docs/scaling.md) and [`examples/karpenter/`](examples/karpenter/).

## Observability

- **Prometheus metrics** — `robodev_taskruns_total`, `robodev_active_jobs`, `robodev_taskrun_duration_seconds`, `robodev_plugin_errors_total`
- **Grafana dashboard** — Included in Helm chart ([`charts/robodev/dashboards/`](charts/robodev/dashboards/))
- **Structured logging** — JSON output via Go's `slog`

## Security

RoboDev is designed as a security-first platform. See [docs/security.md](docs/security.md).

- Container isolation with restrictive security contexts (non-root, read-only FS, dropped capabilities)
- Network policies limiting egress to HTTPS/SSH
- RBAC scoped to minimum required permissions
- Image signing with cosign and SBOM generation with syft
- No secrets in logs; input validation on all external data

## Project Structure

```
cmd/robodev/           — Controller entrypoint
internal/controller/   — Reconciliation loop
internal/jobbuilder/   — ExecutionSpec → K8s Job
internal/taskrun/      — TaskRun state machine
internal/watchdog/     — Progress watchdog
internal/config/       — Configuration loading
internal/metrics/      — Prometheus metrics
pkg/engine/            — ExecutionEngine interface + engines
pkg/plugin/            — Plugin interfaces + built-in backends
proto/                 — Protobuf definitions
charts/robodev/        — Helm chart
docker/                — Dockerfiles for controller + engines
examples/              — Configuration examples
docs/                  — Documentation
```

## Development

```bash
# Prerequisites: Go 1.23+, gofumpt, golangci-lint

make test          # Run all tests
make build         # Build controller binary
make lint          # Run linter
make fmt           # Format code
make proto-lint    # Lint protobuf files
make docker-build  # Build all container images
make helm-lint     # Lint Helm chart
```

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

- Use [conventional commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `test:`
- British English in all comments, docs, and user-facing strings
- Table-driven tests with `testify`
- `gofumpt` formatting and `golangci-lint` clean

## Licence

Apache 2.0. See [LICENCE](LICENCE).
