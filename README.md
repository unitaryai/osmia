# Osmia

![Osmia](docs/images/osmia_banner.png)

Osmia runs AI coding agents (Claude Code, OpenAI Codex, Aider, OpenCode and Cline) against your tickets. Each task gets its own Kubernetes Job. The agent clones the repository, makes the change and opens a pull request.

While an agent runs, Osmia reads its output as a live stream. It uses that to spot an agent going in circles, to give it hints, to work out why a task failed before retrying it, and to learn which engine suits which kind of task.

![Osmia Architecture](docs/images/osmia_architecture.png)

**[Documentation](https://unitaryai.github.io/osmia/)** &nbsp;|&nbsp; [Quick start](#quick-start) &nbsp;|&nbsp; [Plugin system](#plugin-system)

> *Osmia* is the genus of mason bees. They are solitary: each bee works on its own nest, with no hive and no queen. Osmia agents work the same way, each on its own task.

---

## What it does beyond running jobs

A plain job runner starts a task and waits to see if it passed. Osmia also watches the agent while it works and keeps what it learns for later tasks.

| Feature | What it does |
|---|---|
| Live streaming | The agent's output arrives as an NDJSON stream, so tool calls and cost are visible as they happen, not only when the job ends. |
| Process Reward Model (PRM) | Scores each tool call. When an agent's recent steps look unproductive, Osmia writes it a hint before things get worse. |
| Episodic memory | A SQLite-backed knowledge graph of facts and patterns from earlier tasks. Relevant entries go into each new task's prompt. Old entries lose confidence over time and are pruned. |
| Failure diagnosis | Classifies why a task failed (permissions, a failing test, an unclear ticket, the environment) and changes the approach on the retry instead of repeating it. |
| Engine routing | Picks an engine for each task from past success rates, cost and task type. |
| Cost estimation | Estimates cost and duration from similar past tasks before launch. Tasks predicted to cost more than `max_predicted_cost_per_job` are rejected before a job starts. |
| Competitive execution | Optionally runs several engines on the same task and has a judge pick the best result. Worth the extra cost only when quality matters most. |
| Review follow-ups | Watches the pull requests it opens. When a reviewer leaves actionable comments, it starts a follow-up job to address them. |

A watchdog also looks for repeated tool calls, sudden cost spikes, an agent editing the same files back and forth, and long silences. Depending on what it finds, it nudges the agent, escalates to a person, or stops the job, rather than waiting for a timeout.

---

## Features

- Five built-in engines: Claude Code, OpenAI Codex, Aider, OpenCode and Cline. You can add more through the gRPC plugin interface.
- Webhooks for GitHub, GitLab, Slack, Shortcut and generic sources, with HMAC signature checks.
- Plugins for ticketing, notifications, secrets, SCM and code review. Plugins can be written in any language with gRPC support.
- Approval gates before a job starts (`pre_start`) and before merge (`pre_merge`), answered in Slack.
- Credentials come from Kubernetes Secrets or HashiCorp Vault. Tasks can also reference their own secrets, checked against a policy before the job starts.
- Several separate safety layers: controller guard rails, engine hooks, the watchdog, NetworkPolicies and the secret policy.
- Agent pods run as non-root with a read-only root filesystem and all capabilities dropped. gVisor or Kata Containers sandboxing is optional.
- Per-task cost limits, Prometheus metrics and Grafana dashboards. A multi-tenancy config schema exists but is not enforced yet; namespace isolation is planned.

---

## Quick start

```bash
kubectl create namespace osmia

kubectl create secret generic osmia-github-token \
  -n osmia --from-literal=token=ghp_YOUR_TOKEN
kubectl create secret generic osmia-anthropic-key \
  -n osmia --from-literal=api_key=sk-ant-YOUR_KEY

helm repo add osmia https://unitaryai.github.io/osmia
helm install osmia osmia/osmia -n osmia -f osmia-config.yaml
```

Minimal `osmia-config.yaml`:

```yaml
ticketing:
  backend: github
  config:
    owner: "your-org"
    repo:  "your-repo"
    token_secret: osmia-github-token
    labels: ["osmia"]

engines:
  default: claude-code
  claude-code:
    auth:
      method: api_key
      api_key_secret: osmia-anthropic-key

guardrails:
  max_cost_per_job: 5.00
  max_job_duration_minutes: 60
  allowed_repos:
    - "github.com/your-org/your-repo"
```

Label a GitHub issue `osmia` and the controller picks it up, launches a Claude Code agent, and opens a pull request with the changes.

The [documentation](https://unitaryai.github.io/osmia/) has step-by-step setup guides, the full configuration reference, and how to turn on the PRM, memory and the other features above.

---

## Plugin system

Every external integration is a plugin. Built-in plugins are compiled into the controller. Third-party plugins run as gRPC subprocesses using [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin).

| Interface | Built-in | Third-party examples |
|---|---|---|
| Ticketing | GitHub Issues, Shortcut, Linear | Jira, Monday.com |
| Notifications | Slack, Telegram, Discord | Teams, PagerDuty |
| Approval | Slack | Teams |
| Secrets | K8s Secrets, HashiCorp Vault | AWS SM, 1Password |
| SCM | GitHub, GitLab | Bitbucket |
| Review | CodeRabbit | Custom |
| Engine | Claude Code, Codex, Aider, OpenCode, Cline | Custom |

SDK helper libraries for Python, Go and TypeScript are in `sdk/`. The generated proto stubs are not checked in; run `make sdk-gen` to generate them. See the [plugin docs](https://unitaryai.github.io/osmia/plugins/writing-a-plugin/) for details.

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

Contributions are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) has the details, but in short:

- [Conventional commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `test:`
- British English in all comments, docs, and user-facing strings
- Table-driven tests with `testify`
- `gofumpt` formatting and `golangci-lint` clean

---

## Licence

Apache 2.0. See [LICENCE](LICENCE).
