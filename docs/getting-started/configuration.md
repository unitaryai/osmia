# Configuration Reference

Osmia is configured via a YAML file (`osmia-config.yaml`) which is mounted into the controller pod as a ConfigMap. When deploying with Helm, you set configuration under the `config:` key in your `values.yaml` and the chart creates the ConfigMap for you.

## Top-Level Sections

| Section | Purpose |
|---|---|
| `ticketing` | Where tasks come from (GitHub Issues, GitLab Issues, Jira via plugin) |
| `engines` | Which AI coding agents are available and which is the default |
| `notifications` | Where status updates are sent (Slack, Microsoft Teams via plugin) |
| `secrets` | How the controller retrieves credentials (`k8s` for Kubernetes Secrets) |
| `scm` | Source code management backend for cloning and opening PRs |
| `guardrails` | Safety boundaries — cost limits, concurrency limits, blocked file patterns |
| `tenancy` | Multi-tenancy config schema (namespace-per-tenant runtime isolation is planned) |
| `quality_gate` | Optional AI-powered review of agent output before merging |
| `review` | Review backend configuration |
| `review_response` | Automated follow-up on PR/MR review comments |
| `progress_watchdog` | Detects stalled or looping agent jobs and intervenes |
| `plugin_health` | Health monitoring and restart behaviour for gRPC plugins |
| `execution` | Execution backend (`job`, `sandbox`, or `local`) |
| `webhook` | Optional webhook receiver for instant ticket ingestion |
| `secret_resolver` | Secret resolution policy configuration (infrastructure in place; per-task ticket references not yet wired into the execution path) |
| `streaming` | Real-time agent output streaming configuration |
| `taskrun_store` | Persistent TaskRun store backend (`memory`, `sqlite`, `postgres`) |
| `prm` | Process Reward Model for real-time agent coaching (disabled by default) |
| `memory` | Episodic memory knowledge graph (disabled by default) |
| `diagnosis` | Causal failure diagnosis for informed retries (disabled by default) |
| `routing` | Intelligent engine selection based on historical data (disabled by default) |
| `estimator` | Pre-execution cost and duration prediction (disabled by default) |
| `competitive_execution` | Tournament-style parallel execution (disabled by default) |

!!! note "Intelligence features"
    All intelligence subsystems (`prm`, `memory`, `diagnosis`, `routing`, `estimator`, `competitive_execution`) are fully integrated into the controller and functional when enabled. They are all disabled by default and have no effect unless you add their configuration blocks.

For the full set of fields and their defaults, see `charts/osmia/values.yaml` and the struct definitions in [`internal/config/config.go`](https://github.com/unitaryai/osmia/blob/main/internal/config/config.go).

## Ticketing

The ticketing backend is the primary input source. The controller polls it every reconciliation cycle (default: 30 seconds).

### GitHub Issues

```yaml
ticketing:
  backend: github
  config:
    owner: "your-org"               # GitHub org or username
    repo: "your-repo"               # Repository name
    token_secret: "osmia-github-token"
    labels:
      - "osmia"                   # Issues must have this label to be picked up
    exclude_labels:
      - "osmia-in-progress"       # Skip issues already in flight
      - "osmia-failed"            # Skip issues that previously failed
```

| Field | Required | Description |
|---|---|---|
| `token_secret` | Yes | Kubernetes Secret name containing a GitHub token with `repo` + `issues` scopes |
| `owner` | Yes | GitHub organisation or username |
| `repo` | Yes | Repository name |
| `labels` | No | Issues must carry at least one of these labels. Defaults to `["osmia"]` |
| `exclude_labels` | No | Issues carrying any of these labels are skipped |

### Shortcut

```yaml
ticketing:
  backend: shortcut
  config:
    token_secret: "osmia-shortcut-token"
    workflow_state_name: "Ready for Development"   # trigger state — exact name
    in_progress_state_name: "In Development"       # state set when agent starts
    completed_state_name: "Ready for Review"       # state set on success (optional)
    owner_mention_name: "osmia"                  # mention name of the Osmia user
    exclude_labels:
      - "osmia-failed"
```

| Field | Required | Description |
|---|---|---|
| `token_secret` | Yes | Kubernetes Secret name containing a Shortcut API token |
| `workflow_state_name` | Yes | Exact name of the state that triggers pickup (e.g. `"Ready for Development"`) |
| `in_progress_state_name` | No | State the story is moved to when the agent starts work |
| `completed_state_name` | No | State the story is moved to on success. Defaults to the first done-type state in the workflow |
| `owner_mention_name` | No | Only pick up stories assigned to this Shortcut user (e.g. `"osmia"`) |
| `exclude_labels` | No | Stories with any of these labels are skipped |

**Multi-workflow support** — if your workspace has several workflows with different state names, use the `workflows` array instead of the flat keys above:

```yaml
ticketing:
  backend: shortcut
  config:
    token_secret: "osmia-shortcut-token"
    owner_mention_name: "osmia"
    completed_state_name: "Ready for Review"
    workflows:
      - trigger_state: "Ready for Development"
        in_progress_state: "In Development"
      - trigger_state: "Agent Queue"
        in_progress_state: "In Progress"
```

When `workflows` is set it supersedes `workflow_state_name` and `in_progress_state_name`.

### Linear

```yaml
ticketing:
  backend: linear
  config:
    token_secret: "osmia-linear-token"
    team_id: "YOUR_TEAM_ID"          # Linear team UUID
    state_filter: "Todo"             # only pick up issues in this state
    labels:
      - "osmia"
    exclude_labels:
      - "in-progress"
      - "osmia-failed"
```

| Field | Required | Description |
|---|---|---|
| `token_secret` | Yes | Kubernetes Secret name containing a Linear API key |
| `team_id` | Yes | Linear team UUID (find it in Settings → API) |
| `state_filter` | No | Only pick up issues in this workflow state name |
| `labels` | No | Issues must carry at least one of these labels |
| `exclude_labels` | No | Issues carrying any of these labels are skipped. Defaults to `["in-progress", "osmia-failed"]` |

### Local

```yaml
ticketing:
  backend: local
  config:
    store_path: "/data/local-ticketing.db"  # required
    seed_file: "/data/tasks.yaml"           # optional one-time import
```

| Field | Required | Description |
|---|---|---|
| `store_path` | Yes | SQLite database path for the local ticket store |
| `seed_file` | No | YAML file imported once at startup; existing ticket IDs are left unchanged |

When `ticketing.backend` is `local`, the controller exposes an embedded frontend on a dedicated local UI listener. By default it binds to `http://127.0.0.1:8082/`; override this with the `-local-ui-addr` flag if needed. That UI shows a small local board with `To do`, `In progress`, and `Done` columns, lets you inspect comment history, create tickets, add operator comments, and move tickets back to `To do` for another local run.

The legacy `ticketing.config.task_file` key is no longer supported. Replace it with `ticketing.backend: local`, set `ticketing.config.store_path` to the SQLite database path, and optionally use `ticketing.config.seed_file` to import tickets from YAML once at startup.

## Engines

```yaml
engines:
  default: claude-code     # Default engine for all tasks
  fallback_engines:        # Tried in order if the default fails
    - codex
    - aider
  claude_code:
    auth:
      method: api_key
      api_key_secret: "osmia-anthropic-key"
    fallback_model: haiku
    append_system_prompt: "Always run the test suite before committing."
    tool_whitelist: [Bash, Read, Write, Edit, Grep, Glob]
    # Optional: enable session persistence so retry pods resume via --resume
    # instead of starting fresh. Disabled by default; requires a shared PVC.
    session_persistence:
      enabled: false
      backend: shared-pvc
      pvc_name: osmia-agent-sessions
    skills:                              # custom skills for the agent
      - name: create-changelog
        inline: |
          # Create Changelog
          Generate a CHANGELOG.md entry for the changes made.
      - name: security-review
        path: /opt/osmia/skills/security-review.md
      - name: deploy-guide
        configmap: deploy-skills         # load from a K8s ConfigMap
    sub_agents:                          # delegate subtasks to specialised agents
      - name: reviewer
        description: "Reviews code for correctness"
        prompt: "You are a code reviewer."
        model: haiku
      - name: architect
        description: "Architecture review"
        configmap: architect-agent       # load prompt from ConfigMap
  codex:
    auth:
      method: api_key
      api_key_secret: "osmia-openai-key"
  opencode:
    provider: anthropic    # "anthropic", "openai", "google"
    auth:
      method: api_key
      api_key_secret: "osmia-anthropic-key"
  # cline: no pre-built image is published yet — see Engine Reference.
```

See [Engine Reference](../plugins/engines.md) for the full list of Claude Code fields (skills, agent teams, tool whitelist/blacklist, JSON schema, etc.) and detailed per-engine configuration.

### Authentication Methods

| Method | Description |
|---|---|
| `api_key` | API key stored in a Kubernetes Secret |
| `bedrock` | AWS Bedrock via IRSA (IAM Roles for Service Accounts) |
| `vertex` | Google Vertex AI via Workload Identity Federation |
| `credentials_file` | Credentials file mounted from a Kubernetes Secret |
| `setup_token` | Setup token for initial authentication |

## Guard Rails

```yaml
guardrails:
  max_cost_per_job: 50.0              # Maximum USD spend per task
  max_concurrent_jobs: 5              # Concurrent job limit
  max_job_duration_minutes: 120       # Hard timeout for jobs
  allowed_repos:                      # Glob patterns for permitted repos
    - "org/frontend-*"
    - "org/backend-*"
  blocked_file_patterns:              # Files the agent must never modify
    - "*.env"
    - "**/secrets/**"
    - "**/credentials/**"
  allowed_task_types:                 # Restrict to specific task categories
    - "bug_fix"
    - "documentation"
    - "dependency_upgrade"
  task_profiles:                      # Per-task-type permissions
    documentation:
      allowed_file_patterns: ["*.md", "docs/**"]
      max_cost_per_job: 10.0
  approval_gates:                     # Cost thresholds requiring approval
    - "high_cost"
  approval_cost_threshold_usd: 25.0
```

See [Guard Rails](../guardrails.md) for the full specification.

## Notifications

```yaml
notifications:
  channels:
    - backend: slack
      config:
        channel_id: "C0123456789"
        token_secret: "osmia-slack-token"
    - backend: teams
      config:
        webhook_url_secret: "osmia-teams-webhook"
```

Multiple channels can be configured simultaneously. All channels receive all events. Notification failures are logged but do not block the controller.

## Secrets

```yaml
secrets:
  backend: k8s             # "k8s" (built-in) or a plugin name
  config:
    namespace: "osmia"   # Optional — defaults to the controller's namespace
```

For external secret stores, see the [Secrets plugin documentation](../plugins/secrets.md).

## Secret Resolver

The secret resolver provides task-scoped secret resolution with policy enforcement:

```yaml
secret_resolver:
  backends:
    - scheme: k8s
      backend: k8s
    - scheme: vault
      backend: vault
      config:
        address: "https://vault.example.com"
  aliases:
    anthropic-key:
      uri: "k8s://osmia/osmia-anthropic-key/api_key"
  policy:
    allowed_env_patterns: ["ANTHROPIC_*", "OPENAI_*", "GITHUB_*"]
    blocked_env_patterns: ["AWS_SECRET_*"]
    allow_raw_refs: false
    allowed_schemes: ["k8s", "vault"]
```

## Quality Gate

```yaml
quality_gate:
  enabled: true
  mode: "post-completion"          # or "security-only"
  engine: claude-code              # Engine used for reviews
  max_cost_per_review: 5.0
  security_checks:
    scan_for_secrets: true
    check_owasp_patterns: true
    verify_guardrail_compliance: true
    check_dependency_cves: true
  on_failure: "retry_with_feedback"  # or "block_mr", "notify_human"
```

## Progress Watchdog

```yaml
progress_watchdog:
  enabled: true
  check_interval_seconds: 60
  min_consecutive_ticks: 2
  research_grace_period_minutes: 5
  loop_detection_threshold: 10
  thrashing_token_threshold: 80000
  stall_idle_seconds: 300
  cost_velocity_max_per_10_min: 15.0
  unanswered_human_timeout_minutes: 30
```

See [Guard Rails Overview](../concepts/guardrails-overview.md) for an explanation of each detection rule.

## Execution

```yaml
execution:
  backend: job               # "job" (default), "sandbox", or "local"
  sandbox:
    runtime_class: gvisor     # or "kata"
    warm_pool:
      enabled: true
      size: 2
    env_stripping: true
```

## Webhook

```yaml
webhook:
  enabled: true
  port: 8081
  github:
    secret: "your-github-webhook-secret"
  gitlab:
    secret: "your-gitlab-webhook-secret"
  slack:
    secret: "your-slack-signing-secret"
  shortcut:
    secret: "your-shortcut-webhook-secret"
  generic:
    auth_token: "your-bearer-token"
    field_map:
      title: "summary"
      description: "body"
```

## Streaming

```yaml
streaming:
  enabled: true
  live_notifications: true
```

## TaskRun Store

```yaml
taskrun_store:
  backend: memory            # "memory" (default), "sqlite", or "postgres"
  sqlite:
    path: "/data/taskruns.db"
```

!!! note "Current limitation"
    Only the `memory` backend is currently implemented. The `sqlite` and `postgres` backends are planned but not yet available.

## Tenancy

```yaml
tenancy:
  mode: "namespace-per-tenant"   # or "shared"
  tenants:
    - name: "team-alpha"
      namespace: "osmia-alpha"
      ticketing:
        backend: github
        config:
          repo: "alpha-org/repos"
      secrets:
        backend: k8s
```

## Plugin Health

```yaml
plugin_health:
  max_plugin_restarts: 3
  restart_backoff: [1, 5, 30]    # Seconds between restart attempts
  critical_plugins:
    - "ticketing"
```

## SCM

```yaml
scm:
  backend: github
  config:
    token_secret: "osmia-github-token"
```

| Field | Type | Default | Description |
|---|---|---|---|
| `backend` | string | — | SCM backend: `github` or `gitlab` |
| `config` | map | — | Backend-specific settings (e.g. `token_secret`, `base_url`, `group`) |
| `branch_prefix` | string | `"osmia/"` | Prepended to the ticket ID to form branch names and MR title references. Set to `"sc-"` for Shortcut VCS integration so branches like `sc-28671` auto-link to stories |
| `backends` | list | `[]` | Multi-backend routing entries (advanced) |

### Shortcut VCS integration

Shortcut auto-links branches and MRs to stories when the branch name contains `sc-<storyID>`. Set `branch_prefix: "sc-"` to enable this:

```yaml
scm:
  backend: gitlab
  branch_prefix: "sc-"
  config:
    token_secret: "osmia-gitlab"
```

This produces branches like `sc-28671` and MR titles like `fix: resolve null check [sc-28671]`, both of which Shortcut recognises automatically.

## Review

```yaml
review:
  backend: coderabbit
  config:
    api_key_secret: "coderabbit-api-key"
```

## Review Response

When enabled, Osmia monitors merge requests it has opened for new review comments. Actionable comments (e.g. inline suggestions from CodeRabbit or human reviewers) trigger a follow-up job that addresses the feedback and pushes to the existing MR branch.

```yaml
review_response:
  enabled: true
  poll_interval_minutes: 5        # How often to check for new comments
  settling_minutes: 10            # Wait before first poll (for bots to finish)
  min_severity: "warning"         # Minimum severity to act on
  max_follow_up_jobs: 3           # Max batched follow-ups per PR
  reply_to_comments: true         # Post acknowledgement replies
  resolve_threads: true           # Resolve threads on completion (GitLab only)
  ignore_summary_authors:         # Regex patterns for bot summary filtering
    - "^group_\\d+_bot_"          # GitLab group bot tokens
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Enables the review response subsystem |
| `poll_interval_minutes` | int | `5` | Minutes between comment polling cycles |
| `settling_minutes` | int | `0` | Minimum minutes to wait after a PR is registered before polling. Set this to give review bots time to finish posting — e.g. `10` for CodeRabbit which typically takes 8-10 minutes |
| `min_severity` | string | `"warning"` | Minimum comment severity that triggers a follow-up. One of `info`, `warning`, `error` |
| `max_follow_up_jobs` | int | `3` | Maximum number of batched follow-up jobs per PR over its lifetime |
| `reply_to_comments` | bool | `true` | Post an acknowledgement reply to each actionable comment |
| `resolve_threads` | bool | `false` | Resolve discussion threads on follow-up completion. Only supported on GitLab |
| `llm_classifier` | bool | `false` | Use LLM-backed classification with rule-based fallback |
| `ignore_summary_authors` | list | `[]` | Regex patterns for author usernames whose non-inline comments (summaries, coverage reports) are ignored. Inline diff comments from these authors are still processed. Merged with built-in defaults (`osmia`, `dependabot`, `github-actions[bot]`, `copilot`, `gemini-code-assist`, `coderabbit-ai`) |

### Settling period

Review bots like CodeRabbit take several minutes to analyse a diff and post all their comments. Without a settling period, Osmia may poll the MR before the bot has finished, act on the first few comments, and miss later ones. Setting `settling_minutes: 10` ensures Osmia waits at least 10 minutes after the MR is opened before checking for comments.

### Comment batching

All actionable comments discovered in a single poll cycle are batched into one follow-up job. The job receives an enriched description containing all comments. This prevents multiple separate jobs from being spawned for what is logically one round of review feedback.

### Bot summary filtering

Bots like CodeRabbit post both summary comments (general MR notes) and inline diff comments (positioned on specific file and line). The `ignore_summary_authors` patterns only filter non-inline comments — inline review suggestions from matched authors are still treated as actionable. This ensures Osmia addresses actual code review feedback without reacting to automated summaries.

For GitLab, group bot tokens use usernames like `group_123456_bot_abcdef...`. Add `"^group_\\d+_bot_"` to catch all of them.

## Process Reward Model (PRM)

Real-time agent coaching that scores tool calls and intervenes when agents become unproductive. See [Real-Time Agent Coaching](../concepts/prm.md) for a full explanation.

```yaml
prm:
  enabled: true                     # Enable PRM scoring
  evaluation_interval: 5            # Evaluate every N tool calls
  window_size: 10                   # Rolling window of recent events
  score_threshold_nudge: 7          # Scores below this trigger a nudge
  score_threshold_escalate: 3       # Scores below this trigger escalation
  hint_file_path: "/workspace/.osmia-hint.md"
  max_trajectory_length: 50         # Maximum trajectory points stored
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Enables PRM scoring of agent tool calls |
| `evaluation_interval` | int | `5` | Tool calls between evaluations |
| `window_size` | int | `10` | Events in the scoring window |
| `score_threshold_nudge` | float | `7.0` | Score below this produces a nudge |
| `score_threshold_escalate` | float | `3.0` | Score below this produces an escalation |
| `hint_file_path` | string | `/workspace/.osmia-hint.md` | Path for hint files in the agent pod |
| `max_trajectory_length` | int | `50` | Maximum trajectory points retained |

## Episodic Memory

Cross-task knowledge graph that accumulates lessons from every completed task and injects relevant prior knowledge into future prompts. See [Episodic Memory](../concepts/memory.md) for a full explanation.

```yaml
memory:
  enabled: true                     # Enable episodic memory
  store_path: "/data/memory.db"     # SQLite database path
  decay_interval_hours: 24          # Hours between decay cycles
  prune_threshold: 0.05             # Remove facts below this confidence
  max_facts_per_query: 10           # Max facts injected per prompt
  tenant_isolation: true            # Enforce cross-tenant boundaries
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Enables episodic memory |
| `store_path` | string | `/var/lib/osmia/memory.db` | Path to the SQLite database |
| `decay_interval_hours` | int | `24` | Hours between confidence decay cycles |
| `prune_threshold` | float | `0.05` | Facts below this confidence are pruned |
| `max_facts_per_query` | int | `10` | Maximum facts returned per query |
| `tenant_isolation` | bool | `true` | Whether to enforce tenant boundaries |

!!! tip "Persistent storage"
    In Kubernetes, mount a PVC at the `store_path` directory so memory survives pod restarts.

## Environment Variable Overrides

Configuration values can be overridden via environment variables following the pattern `OSMIA_<SECTION>_<FIELD>`:

| Variable | Overrides |
|---|---|
| `OSMIA_TICKETING_BACKEND` | `ticketing.backend` |
| `OSMIA_ENGINE_DEFAULT` | `engines.default` |
| `OSMIA_GUARDRAILS_MAX_COST_PER_JOB` | `guardrails.max_cost_per_job` |
| `OSMIA_GUARDRAILS_MAX_CONCURRENT_JOBS` | `guardrails.max_concurrent_jobs` |
