# Osmia OSS Plan

## Executive Summary

Osmia is a Kubernetes-native AI coding agent harness that orchestrates autonomous developer agents (Claude Code, OpenAI Codex) to perform maintenance and development tasks on codebases at scale. It differentiates from tools like OpenClaw by being enterprise-grade, security-first, and built on Kubernetes primitives for isolation, observability, and scaling.

This plan covers the transformation of Unitary's internal Osmia into an open-source project (Apache 2.0), including technical architecture, plugin system design, implementation phases, and community bootstrapping.

### Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| OSS strategy | Fork & generalise | New OSS repo; Unitary deploys as downstream consumer with Shortcut/Slack plugins |
| LLM support | Agnostic from day one | Abstract execution engine interface; ship with Claude Code + Codex |
| Licence | Apache 2.0 | Enterprise-friendly, patent grant, CNCF-aligned |
| Agent teams | Experiment with in-process mode | Try Claude Code's experimental agent teams inside K8s Jobs |
| Project name | Osmia | No major conflicts; direct, descriptive |
| MVP scope | Core + GitHub Issues + Slack | Lowest barrier to entry for OSS contributors |
| Plan scope | Technical + community | Cover architecture, implementation, and OSS bootstrapping |
| Language | Go controller + polyglot plugins | K8s operators are Go's home ground; plugins via gRPC for language-agnostic extensibility |

---

## 1. Architecture Overview

### 1.1 High-Level Design

```
                          +---------------------------+
                          |    Ticketing Backends      |
                          | (GitHub Issues, Jira, etc) |
                          +-------------+-------------+
                                        |
                                        v
+-------------------+    +------------------------------+    +---------------------+
|  Notification     |<-->|    Osmia Controller        |<-->|  Secrets Backend     |
|  Channels         |    |  (controller-runtime operator)|    | (K8s, Vault, AWS SM) |
| (Slack, Discord,  |    |                              |    +---------------------+
|  Teams, etc)      |    |  - Polls ticketing backend   |
+-------------------+    |  - Manages TaskRun CRDs      |    +---------------------+
                          |  - Creates K8s Jobs          |<-->|  Code Review Backend |
+-------------------+    |  - Monitors completion       |    | (CodeRabbit, native) |
|  Human Approval   |<-->|  - Tracks costs              |    +---------------------+
|  Backend          |    +-------------+----------------+
| (Slack, PagerDuty)|                  |
+-------------------+                  | creates per ticket
                                        v
                          +------------------------------+
                          |    K8s Job (per ticket)       |
                          |                              |
                          |  +------------------------+  |
                          |  | Execution Engine       |  |
                          |  | (Claude Code / Codex)  |  |
                          |  +------------------------+  |
                          |  | Guard Rails (hooks)    |  |
                          |  +------------------------+  |
                          |  | MCP Server (stdio)     |  |
                          |  | - notify, ask_human    |  |
                          |  | - wait_for_pipeline    |  |
                          |  +------------------------+  |
                          |  | Git + SCM CLI          |  |
                          |  | (git, glab, gh)        |  |
                          |  +------------------------+  |
                          |  | Result: /workspace/    |  |
                          |  |   result.json          |  |
                          |  +------------------------+  |
                          +------------------------------+
```

### 1.2 TaskRun State Machine

Every ticket-to-job lifecycle is tracked via a `TaskRun` CRD (or in-memory state object for simpler deployments). This provides idempotency, auditability, and crash recovery.

```
                    +----------+
                    |  Queued   |
                    +----+-----+
                         |  controller creates K8s Job
                         v
                    +----------+
             +----->| Running  |
             |      +----+-----+
             |           |
             |     +-----+------+-------+
             |     |            |       |
             |     v            v       v
         +---+------+  +-------+--+  +-+----------+
         |NeedsHuman|  |Succeeded |  |  Failed    |
         +---+------+  +----------+  +-----+------+
             |                              |
             | human responds               | if retryable
             |                              v
             +-----------+            +-----+------+
                         |            |  Retrying  |
                         |            +------------+
                         v
                    (resumes Running)
```

**Idempotency:** Each TaskRun has a unique key derived from `{ticket_id}-{ticket_revision}`. The controller checks for existing TaskRuns before creating jobs, preventing duplicate work on restart or network partition.

**Fields:**
```python
class TaskRun(BaseModel):
    id: str                          # Unique identifier
    idempotency_key: str             # {ticket_id}-{revision}
    ticket_id: str
    engine: str                      # "claude-code" | "codex"
    state: TaskRunState              # Queued | Running | NeedsHuman | Succeeded | Failed | Retrying | TimedOut
    job_name: str | None             # K8s Job name once created
    created_at: datetime
    updated_at: datetime
    result: TaskResult | None        # Populated on completion
    human_question: str | None       # Set when state is NeedsHuman
    retry_count: int = 0
    max_retries: int = 1
    heartbeat_at: datetime | None    # Last heartbeat from job
    heartbeat_ttl_seconds: int = 300 # Mark stale if no heartbeat
    # Progress signals (enriched heartbeat, used by progress watchdog - see 3.7)
    tokens_consumed: int = 0
    files_changed: int = 0
    tool_calls_total: int = 0
    last_tool_name: str | None = None
    consecutive_identical_tools: int = 0
```

### 1.3 Language Decision: Go Controller, Polyglot Plugins

The initial assumption was Python for everything — it's what the Unitary team knows best, and the existing internal Osmia is Python. However, the controller is a Kubernetes operator, and that changes the calculus significantly.

**What the controller actually does:**
- Runs a K8s operator reconciliation loop (poll ticketing, reconcile TaskRuns, create/monitor Jobs)
- Loads and communicates with plugins
- Exposes Prometheus metrics
- Makes HTTP calls to ticketing/notification APIs
- Reads heartbeat data from ConfigMaps/CRD status
- Manages CRDs and RBAC

**What it does not do:**
- CPU-intensive computation
- Handle high-throughput request serving (it's a single-replica operator)
- Anything latency-sensitive (the agents themselves take minutes to hours)

**Why Go for the controller:**
- **client-go is the reference K8s client.** Every other client (Python, Rust, Java) is a wrapper or port. CRD controllers, informers, leader election, watch semantics — all first-class in Go.
- **controller-runtime and kubebuilder** are the standard for building K8s operators. They are mature, battle-tested, and what every K8s contributor already knows. By contrast, kopf (Python) is a solid framework but a distant second in community adoption; it does support multi-replica deployments via its Peering mechanism, but leader election is not as seamless as controller-runtime's Lease-based approach and the Python K8s ecosystem is generally less mature.
- **Static binary, tiny image, low memory.** A Go controller compiles to a single binary with no runtime dependencies. The container image can be built FROM scratch or distroless, typically under 30 MB. This matters for an OSS project where people run it alongside production workloads.
- **The OSS K8s community writes Go.** Contributors who want to work on a K8s operator expect Go. Choosing Python would be a friction point for the exact audience we're targeting.
- **Leader election for HA.** controller-runtime has built-in leader election via Lease objects. kopf supports HA via Peering, but controller-runtime's approach is more mature, more widely deployed, and integrates naturally with the K8s ecosystem.

**Why not Python for the controller:**
- kopf works, but it's swimming against the current in the K8s ecosystem.
- The Python kubernetes client is auto-generated from OpenAPI specs and is often behind the Go client in feature support.
- kopf supports HA via Peering, but it's an add-on rather than a built-in primitive, and the community experience around it is thin compared to controller-runtime's Lease-based leader election.

**Why not Rust:**
- Overkill for this workload. The controller is I/O-bound, not CPU-bound. Rust's strengths (memory safety without GC, zero-cost abstractions) don't buy much here.
- kube-rs exists but the ecosystem is small compared to client-go.
- It would significantly shrink the contributor pool.

**Why polyglot plugins (not Go-only):**
- Forcing every plugin author to write Go would be a barrier to adoption. Someone building a Jira ticketing plugin shouldn't need to learn Go.
- Go's built-in `plugin` package is essentially abandoned. The alternative is hashicorp/go-plugin, which communicates over gRPC — heavier than Python entry points but far more robust and language-agnostic.
- A gRPC plugin interface means plugins can be written in Python, Go, TypeScript, or any language with gRPC support. A Python plugin SDK (a thin wrapper around the gRPC stubs) keeps the plugin development experience easy for Python developers.
- First-party plugins that ship with the controller (GitHub Issues, Slack, K8s Secrets) are written in Go and compiled into the binary for zero-config deployments. Third-party plugins run as separate processes communicating over gRPC.

**The architecture:**

```
Controller (Go binary, single static image)
├── Built-in plugins (compiled in)
│   ├── GitHub Issues ticketing
│   ├── Slack notifications
│   ├── K8s Secrets backend
│   └── GitHub/GitLab SCM
├── gRPC plugin host (hashicorp/go-plugin)
│   ├── External plugin: Jira (Python, Go, or any gRPC language)
│   ├── External plugin: Teams (TypeScript)
│   └── External plugin: Vault secrets (Go)
└── Plugin SDKs (generated from protobuf)
    ├── osmia-plugin-sdk-python   # pip install osmia-plugin-sdk
    ├── osmia-plugin-sdk-go       # go get github.com/osmia/plugin-sdk-go
    └── osmia-plugin-sdk-ts       # npm install @osmia/plugin-sdk
```

**Impact on Unitary:**
- Unitary's `osmia-plugin-shortcut` (Phase 6) can be written in Python using the Python SDK. No need for the Unitary team to learn Go for plugin development.
- The controller itself will be built by Claude, which is equally capable in Go and Python.

### 1.4 Plugin Architecture

The core innovation for OSS is a plugin system that makes every external integration swappable. First-party plugins are compiled into the controller binary. Third-party plugins run as separate processes communicating over gRPC (via hashicorp/go-plugin), allowing plugin authors to use any language with gRPC support.

```
osmia/
  cmd/osmia/        # Main entrypoint
  internal/
    controller/       # Reconciliation loop (controller-runtime)
    jobbuilder/       # ExecutionSpec -> K8s Job translation
    taskrun/          # TaskRun state machine + idempotency
    watchdog/         # Progress watchdog loop
    config/           # Configuration loading
    metrics/          # Prometheus metrics
  pkg/
    engine/           # ExecutionEngine interface + built-in engines
      claudecode/     # Claude Code engine (compiled in)
      codex/          # OpenAI Codex engine (compiled in)
    plugin/           # gRPC plugin host (hashicorp/go-plugin)
      ticketing/      # TicketingBackend interface + built-in
        github/       # GitHub Issues (compiled in)
      notifications/  # NotificationChannel interface + built-in
        slack/        # Slack (compiled in)
      approval/       # HumanApprovalBackend interface + built-in
        slack/        # Slack approval (compiled in)
      secrets/        # SecretsBackend interface + built-in
        k8s/          # Kubernetes Secrets (compiled in)
      review/         # ReviewBackend interface
        coderabbit/   # CodeRabbit (compiled in)
      scm/            # SCMBackend interface
        github/       # GitHub (gh CLI, compiled in)
        gitlab/       # GitLab (glab CLI, compiled in)
  proto/              # Protobuf definitions for plugin interfaces
    ticketing.proto
    notifications.proto
    approval.proto
    secrets.proto
    review.proto
    scm.proto
```

### 1.5 Plugin Discovery & Registration

**Built-in plugins** are compiled into the controller binary and require no external process. They are registered in Go code at build time and always available.

**External plugins** run as separate processes communicating over gRPC (hashicorp/go-plugin). They are configured in `osmia-config.yaml`:

```yaml
# osmia-config.yaml
plugins:
  ticketing:
    jira:
      command: "/opt/osmia/plugins/osmia-plugin-jira"  # Go binary
      interface_version: 1
    shortcut:
      command: "python -m osmia_plugin_shortcut"  # Python via SDK
      interface_version: 1
  notifications:
    teams:
      command: "node /opt/osmia/plugins/osmia-plugin-teams/index.js"  # TypeScript
      interface_version: 1
```

The controller spawns each external plugin as a subprocess, establishes a gRPC connection, and performs a health check + interface version handshake at startup. If a plugin fails to respond or declares an incompatible version, the controller logs a clear error and refuses to start.

**Plugin deployment — getting binaries onto the controller pod:**

External plugin binaries must be available on the controller pod's filesystem. There are three supported approaches, in order of preference:

1. **Custom controller image** (recommended for production): Build a Dockerfile that starts `FROM ghcr.io/osmia/controller:latest` and copies plugin binaries into `/opt/osmia/plugins/`. This produces a single self-contained image with all plugins baked in.
   ```dockerfile
   FROM ghcr.io/osmia/controller:latest
   COPY --from=ghcr.io/myorg/osmia-plugin-jira:latest /plugin /opt/osmia/plugins/osmia-plugin-jira
   COPY --from=ghcr.io/myorg/osmia-plugin-pagerduty:latest /plugin /opt/osmia/plugins/osmia-plugin-pagerduty
   ```

2. **Init containers** (recommended for Helm-based deployments): Each external plugin ships as a container image containing its binary. Init containers copy the binary into a shared `emptyDir` volume before the controller starts.
   ```yaml
   initContainers:
     - name: plugin-jira
       image: ghcr.io/myorg/osmia-plugin-jira:v1.2.0
       command: ["cp", "/plugin", "/plugins/osmia-plugin-jira"]
       volumeMounts:
         - name: plugins
           mountPath: /plugins
   containers:
     - name: controller
       image: ghcr.io/osmia/controller:latest
       volumeMounts:
         - name: plugins
           mountPath: /opt/osmia/plugins
   volumes:
     - name: plugins
       emptyDir: {}
   ```

3. **Python plugins via pip** (for Python SDK plugins): If a plugin is a Python package, the controller can invoke it as `python -m plugin_name` provided the Python runtime and package are available. A sidecar or init container can install the package. This is the simplest approach for Python plugin authors but adds a Python runtime dependency to the controller pod.

The Helm chart includes a `plugins` values section for configuring init containers declaratively:
```yaml
# values.yaml
plugins:
  - name: jira
    image: ghcr.io/myorg/osmia-plugin-jira:v1.2.0
    binaryPath: /plugin
  - name: pagerduty
    image: ghcr.io/myorg/osmia-plugin-pagerduty:v0.3.1
    binaryPath: /plugin
```

**Plugin crash handling and restart:**

External plugins run as gRPC subprocesses managed by hashicorp/go-plugin. If a plugin process crashes:

1. **Detection**: The controller detects the crash via the gRPC connection dropping. hashicorp/go-plugin surfaces this as an error on the next RPC call.
2. **Restart**: The controller attempts to restart the plugin subprocess up to `max_plugin_restarts` times (default: 3) with exponential backoff (1s, 5s, 30s). On restart, the version handshake is re-performed.
3. **Degraded mode**: If restarts are exhausted, the controller enters degraded mode for that plugin category. For non-critical plugins (notifications, review), the controller continues operating and logs warnings. For critical plugins (ticketing, secrets), the controller stops processing new tickets and raises an alert.
4. **Metrics**: Plugin health is exposed via Prometheus metrics (`osmia_plugin_restarts_total`, `osmia_plugin_healthy` gauge).

```yaml
# osmia-config.yaml
plugin_health:
  max_plugin_restarts: 3
  restart_backoff: [1, 5, 30]  # seconds
  critical_plugins: ["ticketing", "secrets", "scm"]
```

**Plugin SDKs** are generated from the protobuf definitions in `proto/` and published as language-specific packages. Each SDK includes:
- Generated gRPC client/server stubs
- A base class (Python) or interface (Go) with boilerplate handled
- A CLI entrypoint for running the plugin as a gRPC subprocess
- A testing harness for local development (mock controller that exercises the plugin interface)

```bash
# Python plugin author
pip install osmia-plugin-sdk
osmia-plugin scaffold --interface ticketing --name my-jira-plugin
# Creates a working skeleton with tests, Dockerfile, and CI config
# Implement the TicketingBackend gRPC service, then run:
osmia-plugin serve --port 0  # Port allocated by hashicorp/go-plugin

# Go plugin author
go get github.com/osmia/plugin-sdk-go
# Implement the TicketingBackend interface, compile, done.

# Test locally without a controller
osmia-plugin test --interface ticketing --binary ./my-plugin
```

### 1.6 Plugin Interface Versioning

Every plugin interface has an `interface_version` field in its protobuf service definition. The controller and plugin perform a version handshake during the gRPC health check at startup.

```protobuf
// proto/ticketing.proto
service TicketingBackend {
  rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
  rpc PollReadyTickets(PollRequest) returns (PollResponse);
  rpc MarkInProgress(MarkInProgressRequest) returns (Empty);
  // ...
}

message HandshakeResponse {
  int32 interface_version = 1;  // Bumped on breaking changes
  string plugin_name = 2;
  string plugin_version = 3;
}
```

**Compatibility contract:**
- Patch version changes (same `interface_version`): backwards-compatible additions only (new optional fields in protobuf messages)
- Major version bumps: new `interface_version` value; old plugins stop loading with a descriptive error message pointing to a migration guide
- The migration guide for each interface version bump is published in the documentation

The controller loads plugins at startup from configuration:

```yaml
# osmia-config.yaml (mounted as ConfigMap)
ticketing:
  backend: github  # or "jira", "shortcut", "linear"
  config:
    repo: "org/repo"
    labels: ["osmia"]

notifications:
  channels:
    - backend: slack
      config:
        channel_id: "C12345"

secrets:
  backend: k8s  # or "vault", "aws-sm", "1password"

engines:
  default: claude-code  # or "codex"
  claude-code:
    image: ghcr.io/osmia/engine-claude-code:latest
  codex:
    image: ghcr.io/osmia/engine-codex:latest

review:
  backend: coderabbit  # or "native", "none"

scm:
  backend: github  # or "gitlab"
```

---

## 2. Core Abstractions

All interfaces are defined in protobuf (source of truth) and implemented as Go interfaces in the controller. Plugin SDKs for Python, Go, and TypeScript are generated from these protobufs. The examples below show the Go interfaces for the controller; plugin authors interact with the generated gRPC stubs in their language of choice.

### 2.1 Execution Engine Interface

The execution engine is the most critical abstraction — it wraps the AI coding tool that runs inside each K8s Job. The interface is deliberately decoupled from Kubernetes: engines return an `ExecutionSpec` (a pure data struct), and the core `JobBuilder` translates that into a `V1Job`. This enables testing without a cluster and opens the door to non-K8s runtimes (Docker, Podman) in future.

```go
// pkg/engine/engine.go

// TaskResult is the structured result written by the engine to /workspace/result.json.
type TaskResult struct {
    Success         bool       `json:"success"`
    MergeRequestURL string     `json:"merge_request_url,omitempty"`
    BranchName      string     `json:"branch_name,omitempty"`
    Summary         string     `json:"summary"`
    TokenUsage      *TokenUsage `json:"token_usage,omitempty"`
    CostEstimateUSD float64    `json:"cost_estimate_usd,omitempty"`
    ExitCode        int        `json:"exit_code"` // 0=success, 1=agent failure, 2=guard rail blocked
}

// ExecutionSpec is an engine-agnostic description of what to run.
// The core JobBuilder translates this into a K8s Job (or Docker run, etc).
type ExecutionSpec struct {
    Image                  string            `json:"image"`
    Command                []string          `json:"command"`
    Env                    map[string]string `json:"env"`
    SecretEnv              map[string]string `json:"secret_env"`
    ResourceRequests       Resources         `json:"resource_requests"`
    ResourceLimits         Resources         `json:"resource_limits"`
    Volumes                []VolumeMount     `json:"volumes"`
    ActiveDeadlineSeconds  int               `json:"active_deadline_seconds"`
}

// ExecutionEngine wraps an AI coding tool (Claude Code, Codex, etc).
type ExecutionEngine interface {
    // BuildExecutionSpec returns a runtime-agnostic spec; the core JobBuilder
    // handles translation to K8s Jobs, Docker containers, etc.
    BuildExecutionSpec(task Task, config EngineConfig) (*ExecutionSpec, error)

    // BuildPrompt constructs the task prompt for this engine.
    BuildPrompt(task Task) (string, error)

    // Name returns a unique engine identifier (e.g. "claude-code", "codex").
    Name() string

    // InterfaceVersion returns the version this engine implements.
    InterfaceVersion() int
}
```

The `TaskResult` is not parsed from pod logs. Instead, engines must write a structured JSON file to `/workspace/result.json` on exit. The controller reads this from the completed pod's filesystem. Exit codes carry semantic meaning:
- `0` — success (result.json contains the outcome)
- `1` — agent failure (result.json may contain partial information)
- `2` — guard rail blocked the operation

### 2.2 Ticketing Backend Interface

Defined in `proto/ticketing.proto` and implemented as a Go interface for built-in plugins:

```go
// pkg/plugin/ticketing/ticketing.go

type Ticket struct {
    ID          string            `json:"id"`
    Title       string            `json:"title"`
    Description string            `json:"description,omitempty"`
    TicketType  string            `json:"ticket_type"`
    Labels      []string          `json:"labels"`
    RepoURL     string            `json:"repo_url,omitempty"`
    ExternalURL string            `json:"external_url"`
    Raw         map[string]any    `json:"raw"` // Original ticket data
}

type TicketingBackend interface {
    PollReadyTickets(ctx context.Context) ([]Ticket, error)
    MarkInProgress(ctx context.Context, ticketID string) error
    MarkComplete(ctx context.Context, ticketID string, result TaskResult) error
    MarkFailed(ctx context.Context, ticketID string, reason string) error
    AddComment(ctx context.Context, ticketID string, comment string) error
}
```

### 2.3 Notification Channel Interface

Notifications and human approvals are deliberately separated. `NotificationChannel` is fire-and-forget; `HumanApprovalBackend` handles event-driven interactions. This allows a Discord bot to send notifications while a Slack bot handles approvals, for example.

```go
// pkg/plugin/notifications/notifications.go

type NotificationChannel interface {
    Notify(ctx context.Context, message string, ticket Ticket) error
    NotifyStart(ctx context.Context, ticket Ticket) error
    NotifyComplete(ctx context.Context, ticket Ticket, result TaskResult) error
}

// pkg/plugin/approval/approval.go

// HumanApprovalBackend is event-driven, not blocking. When a question is asked,
// the TaskRun transitions to NeedsHuman state. When the human responds,
// a webhook/callback resumes the flow.
type HumanApprovalBackend interface {
    RequestApproval(ctx context.Context, question string, ticket Ticket, taskRunID string, options []string) error
    CancelPending(ctx context.Context, taskRunID string) error
}
```

### 2.4 Secrets Backend Interface

```go
// pkg/plugin/secrets/secrets.go

type SecretsBackend interface {
    GetSecret(ctx context.Context, key string) (string, error)
    GetSecrets(ctx context.Context, keys []string) (map[string]string, error)
    BuildEnvVars(secretRefs map[string]string) ([]corev1.EnvVar, error)
}
```

---

## 3. Guard Rails System

Enterprises need configurable safety boundaries. Osmia provides guard rails at three levels, plus an optional quality gate and progress watchdog for defence in depth:

### 3.1 Controller-Level Guards

Applied before a job is created. Configured in `osmia-config.yaml`:

```yaml
guardrails:
  # Maximum cost per job (USD)
  max_cost_per_job: 50.00
  # Maximum concurrent jobs
  max_concurrent_jobs: 5
  # Maximum job duration
  max_job_duration_minutes: 120
  # Allowed repositories (glob patterns)
  allowed_repos:
    - "org/frontend-*"
    - "org/backend-*"
  # Blocked file patterns (never modify these)
  blocked_file_patterns:
    - "*.env"
    - "**/secrets/**"
    - "**/credentials/**"
  # Require human approval before creating MR
  require_human_approval_before_mr: false
  # Allowed task types
  allowed_task_types:
    - "dependency_upgrade"
    - "test_fix"
    - "bug_fix"
    - "documentation"
```

### 3.2 Engine-Level Guards (Claude Code Hooks)

Applied inside the execution container via Claude Code hooks. These are injected as a `settings.json` file mounted into the container:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/osmia/hooks/block-dangerous-commands.sh"
          }
        ]
      },
      {
        "matcher": "Write|Edit",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/osmia/hooks/block-sensitive-files.sh"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/opt/osmia/hooks/on-complete.sh"
          }
        ]
      }
    ]
  }
}
```

### 3.3 Custom Guard Rails via Markdown

Users can provide a `guardrails.md` file that is appended to every prompt sent to the execution engine. This allows natural-language policy enforcement:

```markdown
# Guard Rails

## Never Do
- Never modify CI/CD pipeline configuration files
- Never change database migration files
- Never alter authentication or authorisation logic
- Never commit secrets, API keys, or credentials

## Always Do
- Always run the full test suite before creating an MR
- Always add tests for new functionality
- Always follow the existing code style in the repository
```

### 3.4 Per-Task-Type Permission Profiles

Different task types carry different risk profiles. The guard rails system supports permission profiles that tailor file access, allowed commands, and cost limits per task type:

```yaml
guardrails:
  task_profiles:
    dependency_upgrade:
      allowed_file_patterns:
        - "pyproject.toml"
        - "uv.lock"
        - "requirements*.txt"
        - "Dockerfile"
        - ".github/workflows/*"
      max_cost_per_job: 30.00
      max_job_duration_minutes: 60

    bug_fix:
      blocked_file_patterns:
        - "*.env"
        - "**/migrations/**"
        - "**/auth/**"
      max_cost_per_job: 50.00
      max_job_duration_minutes: 120

    documentation:
      allowed_file_patterns:
        - "*.md"
        - "docs/**"
        - "README*"
      blocked_commands:
        - "git push"
        - "pip install"
      max_cost_per_job: 10.00
      max_job_duration_minutes: 30
```

The controller selects the profile based on ticket labels or type field from the ticketing backend. If no profile matches, the global guard rails apply.

### 3.5 Quality Gate

Inspired by CI/CD quality gates and Gas Town's "Mayor" and "Witness" roles, Osmia supports an optional quality gate that reviews the work of execution agents before it is finalised. The quality gate runs as a separate, lightweight K8s Job after the main agent completes — a pass/fail check between agent output and MR creation.

**Architecture:**

```
K8s Job (main agent)          K8s Job (quality gate, optional)
+----------------------+      +---------------------------+
| Execution Engine     | ---> | Quality Gate Agent        |
| (writes code, MR)    |      | (reviews output)          |
| result.json          |      |                           |
+----------------------+      | Checks:                   |
                               | - Code quality            |
                               | - Security scan           |
                               | - Guard rail compliance   |
                               | - Test coverage           |
                               | gate-result.json          |
                               +---------------------------+
```

**Two quality gate modes:**

1. **Post-completion review** (default): The gate runs after the main agent finishes, receives the diff and `result.json`, and produces a `gate-result.json` with a pass/fail verdict and comments. If it fails, the controller can retry the main agent with the gate's feedback appended to the prompt.

2. **Security gate**: A specialised variant focused exclusively on security concerns:
   - Scans the diff for committed secrets (using patterns like `trufflehog` or `gitleaks`)
   - Checks for common vulnerability patterns (SQL injection, XSS, command injection)
   - Validates that guard rails were respected (no blocked files modified)
   - Verifies that no new dependencies with known CVEs were introduced

```yaml
quality_gate:
  enabled: false  # Opt-in
  mode: "post-completion"  # or "security-only"
  engine: claude-code      # Can use a cheaper/faster model
  max_cost_per_review: 5.00
  security_checks:
    scan_for_secrets: true
    check_owasp_patterns: true
    verify_guardrail_compliance: true
    check_dependency_cves: true
  on_failure: "retry_with_feedback"  # or "block_mr", "notify_human"
```

The quality gate is deliberately lightweight — it does not have write access to the repository. It only reads the diff, runs checks, and produces a verdict. This separation of concerns ensures the gate cannot introduce its own issues.

**Future consideration:** When agent teams matures, the quality gate could run as a teammate rather than a separate job, enabling real-time oversight during execution rather than only post-completion review.

### 3.6 Session Transcript Persistence

Every agent execution produces a full transcript (prompt, tool calls, results, final output) that is persisted as an append-only log alongside `result.json`. This is invaluable for debugging, auditing, cost analysis, and improving prompt engineering.

```
/workspace/
  result.json           # Structured outcome
  transcript.jsonl      # Append-only event log (one JSON object per line)
  cost-breakdown.json   # Token counts and cost estimates per turn
```

The transcript format follows a simple event schema:

```python
class TranscriptEvent(BaseModel):
    timestamp: datetime
    event_type: str           # "prompt" | "tool_call" | "tool_result" | "response" | "error"
    content: dict             # Event-specific payload
    token_count: int | None   # Tokens consumed by this event
```

The controller copies these artefacts from completed pods and attaches them to the TaskRun record. For long-term storage, they can be forwarded to an object store (S3, GCS) or log aggregation system.

### 3.7 Progress Watchdog

The quality gate (section 3.5) reviews agent output after completion. The progress watchdog addresses a different problem: detecting agents that are stalled, looping, or otherwise unproductive *during* execution, before they exhaust their timeout or cost budget.

**Failure modes the progress watchdog detects:**

1. **Looping**: Agent repeatedly calls the same tool with the same arguments on the same files, never converging on a solution (e.g., editing a file, running tests, reverting, editing the same file again).
2. **Thrashing**: High token consumption with no meaningful progress — cost climbing but no new file changes, no commits, no MR created.
3. **Stalled process**: Agent process is alive (heartbeat continues) but has stopped making tool calls — stuck waiting on something internal.
4. **Unanswered human question**: Agent entered `NeedsHuman` state but no human responded within a configurable window. The job is idle, holding resources.
5. **Telemetry failure**: The heartbeat hook itself has crashed or was never initialised — the progress watchdog must distinguish this from a genuine stall.

**Architecture:**

The progress watchdog is a controller-side loop, not a separate job. It runs on a configurable interval (default: 60 seconds) and inspects progress signals for each active TaskRun.

```
Controller
+------------------------------------------+
|  Progress Watchdog Timer (controller loop)|
|                                          |
|  For each active TaskRun:                |
|    1. Read heartbeat from K8s resource   |
|    2. Compare progress signals to        |
|       previous checkpoint                |
|    3. Evaluate rules for current state   |
|       (Running vs NeedsHuman)            |
|    4. Require 2+ consecutive ticks       |
|       before any terminate action        |
|    5. CAS update TaskRun status          |
+------------------------------------------+
```

**Per-state rule activation:**

Not all anomaly rules apply to all TaskRun states. The progress watchdog uses a state-based activation table:

| Rule | `Running` | `NeedsHuman` |
|------|-----------|--------------|
| Loop detection | Active | Inactive |
| Thrashing detection | Active | Inactive |
| Stall detection | Active | Inactive |
| Cost velocity | Active | Inactive |
| Unanswered human timeout | Inactive | Active |
| Telemetry failure | Active | Inactive |

When the progress watchdog terminates a `NeedsHuman` job, it must also call `HumanApprovalBackend.cancel_pending()` to clean up the pending question in Slack/Teams, so the human does not see a reply prompt for a job that no longer exists.

**Enriched heartbeat — push-based telemetry:**

The existing heartbeat mechanism (section 1.2) is extended with progress signals. Inside the agent container, the `PostToolUse` hook emits heartbeat data after every tool call. The data is pushed to a Kubernetes resource (ConfigMap or TaskRun status subresource keyed by TaskRun UID), which the controller watches directly. This avoids the need for `kubectl exec` into agent pods, which would require high-privilege `pods/exec` RBAC and create a significant blast radius if the controller is compromised.

The hook writes to a local file (`/workspace/heartbeat.json`) using atomic write-then-rename to prevent partial reads, then a lightweight sidecar or init-container-configured CronJob pushes updates to the K8s API.

```python
class Heartbeat(BaseModel):
    seq: int                          # Monotonically increasing sequence number
    run_id: str                       # TaskRun UID (detect stale/misdirected data)
    timestamp: datetime
    tokens_consumed: int
    files_changed: int                # Number of unique files modified
    tool_calls_total: int
    last_tool_name: str | None
    last_tool_args_hash: str | None   # Hash of arguments (for loop detection)
    consecutive_identical_calls: int  # Same (tool, args_hash) in succession
    last_meaningful_change_at: datetime | None  # Last file write/edit
    cost_estimate_usd: float
```

The controller rejects any heartbeat where `seq` is not greater than the last seen value and where `run_id` does not match the expected TaskRun.

For Codex (no hooks), a wrapper script around the Codex process tails its stdout and extracts progress indicators, writing the same heartbeat format.

**Anomaly rules:**

```yaml
progress_watchdog:
  enabled: true
  check_interval_seconds: 60
  # Require anomaly on 2+ consecutive ticks before any terminate action
  min_consecutive_ticks: 2
  # Grace period at the start of a job for research/exploration
  research_grace_period_minutes: 5
  rules:
    # Agent has called the same (tool, args_hash) N times consecutively
    # without files_changed increasing — distinguishes genuine loops from
    # legitimate TDD cycles (edit-test-edit-test is normal)
    loop_detection:
      consecutive_identical_call_threshold: 10
      require_no_file_progress: true
      action: "terminate_with_feedback"

    # Tokens consumed without any file changes (relaxed during grace period)
    thrashing_detection:
      tokens_without_progress_threshold: 80000
      action: "warn"  # Warn first, then terminate on next tick
      escalation_action: "terminate_with_feedback"

    # No tool calls for N seconds despite heartbeat seq advancing
    stall_detection:
      idle_seconds_threshold: 300
      action: "terminate"

    # NeedsHuman with no response for N minutes
    unanswered_human_timeout_minutes: 30
    unanswered_human_action: "terminate_and_notify"

    # Cost velocity: spending more than $X in the last Y minutes
    cost_velocity:
      max_usd_per_10_minutes: 15.00
      action: "warn"

    # Heartbeat seq stopped advancing but hook was previously active
    telemetry_failure:
      stale_ticks_threshold: 3  # 3 ticks (~3 minutes) without seq change
      action: "warn"  # Don't kill a healthy agent due to hook failure
```

**Actions:**

| Action | Behaviour |
|--------|-----------|
| `terminate` | Kill the K8s Job immediately. TaskRun transitions to `Failed` with a diagnostic reason. |
| `terminate_with_feedback` | Kill the job. Append a structured diagnosis to the prompt and retry (if retries remain). |
| `terminate_and_notify` | Kill the job and notify the human via `NotificationChannel`. For `NeedsHuman`, also calls `cancel_pending()`. |
| `warn` | Log a warning, send a notification, and set a `progress_watchdog_warning` condition on the TaskRun. If the anomaly persists on the next tick, escalate to `escalation_action`. |

**Concurrency and state transitions:**

The progress watchdog timer and the main controller reconciliation loop can race on TaskRun state updates. To prevent conflicts, all progress watchdog state writes use optimistic concurrency (compare-and-swap on `resourceVersion` for CRD-backed TaskRuns, or a `progress_watchdog_terminating` condition flag for in-memory state). If the CAS fails, the progress watchdog skips the update on that tick — the reconciliation loop's write takes precedence.

**Retry with feedback:**

When the progress watchdog terminates an agent, the retry prompt is rendered from a structured `WatchdogReason` object using a fixed template — raw tool arguments and file paths from the agent's session are never interpolated directly into the retry prompt, preventing prompt injection from adversarial content in file names or tool output.

```python
class WatchdogReason(BaseModel):
    reason_code: str   # "loop" | "thrashing" | "stall" | "cost_velocity"
    tool_name: str | None
    call_count: int | None
    tokens_consumed: int
    cost_estimate_usd: float
    message: str       # Human-readable, rendered from template
```

**Relationship to other mechanisms:**

- **Hard timeout** (`activeDeadlineSeconds`): A blunt safety net. The progress watchdog intervenes earlier and more intelligently — it can detect a loop at minute 5 rather than waiting for the 120-minute timeout.
- **Cost ceiling** (`max_cost_per_job`): Prevents runaway spend but doesn't diagnose *why*. The progress watchdog provides diagnostic context.
- **Quality gate** (section 3.5): Reviews quality *after* completion. The progress watchdog prevents wasted execution *during* the run.
- **Heartbeat TTL**: Detects a dead process. The progress watchdog detects a *live but unproductive* process.

**Implementation notes:**

- **Push-based telemetry is preferred.** The `PostToolUse` hook writes to `/workspace/heartbeat.json` (atomic rename: `.tmp` → `.json`), and a lightweight sidecar pushes to a ConfigMap or TaskRun status subresource. This eliminates the need for `pods/exec` RBAC on the controller's ServiceAccount. If push-based telemetry is not feasible, `kubectl exec` can be used as a fallback — but it must be constrained to the agent namespace with label selectors, and all exec invocations must be audit-logged.
- **Cooldown/hysteresis prevents false positives.** All `terminate*` actions require the anomaly to be present on at least `min_consecutive_ticks` (default: 2) consecutive progress watchdog ticks. A single slow tool call cannot trigger termination.
- **Research grace period.** During the first `research_grace_period_minutes` of a job, the thrashing detector uses a relaxed threshold. Agents legitimately spend tokens reading documentation and exploring the codebase before making changes.
- **Progress watchdog rules are intentionally conservative by default** — a false positive that kills a productive agent is worse than a false negative that wastes some tokens. Operators should tune thresholds based on their workload patterns.

---

## 4. MCP Servers, Skills & Plugins

### 4.1 MCP Server Configuration

Claude Code supports MCP (Model Context Protocol) servers via stdio and HTTP (streamable) transports. Project-scoped servers are configured in `.mcp.json` at the repository root (committed to source control), while user-scoped servers go in `~/.claude.json`. Enterprise-managed servers can be deployed via `/etc/claude-code/managed-mcp.json` (Linux) or `/Library/Application Support/ClaudeCode/managed-mcp.json` (macOS). Environment variable expansion (`${VAR}` and `${VAR:-default}`) is supported in all fields.

For Osmia, MCP servers serve two purposes:

1. **Built-in MCP server**: Osmia ships a notification/interaction MCP server that runs inside each job pod, providing `notify_human`, `ask_human`, and `wait_for_pipeline` tools to the agent.

2. **User-configured MCP servers**: Operators can bundle additional MCP servers into the container image or mount them, giving agents access to domain-specific tools (databases, internal APIs, monitoring systems).

```yaml
# osmia-config.yaml
engines:
  claude-code:
    mcp_servers:
      # Built-in (always present)
      osmia:
        command: "/opt/osmia/mcp/server"
        args: ["--mode", "stdio"]
      # User-configured (optional)
      database:
        command: "npx"
        args: ["-y", "@modelcontextprotocol/server-postgres"]
        env:
          DATABASE_URL: "${DATABASE_URL}"
      sentry:
        command: "npx"
        args: ["-y", "@sentry/mcp-server"]
        env:
          SENTRY_AUTH_TOKEN: "${SENTRY_AUTH_TOKEN}"
```

The engine translates this into a `.mcp.json` file at the workspace root:

```json
{
  "mcpServers": {
    "osmia": {
      "type": "stdio",
      "command": "/opt/osmia/mcp/server",
      "args": ["--mode", "stdio"]
    },
    "database": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-postgres"],
      "env": { "DATABASE_URL": "${DATABASE_URL}" }
    },
    "remote-tool": {
      "type": "http",
      "url": "${REMOTE_MCP_URL:-https://mcp.internal.company.com/mcp}",
      "headers": { "Authorization": "Bearer ${MCP_AUTH_TOKEN}" }
    }
  }
}
```

Project-scoped servers in `.mcp.json` normally require approval before use. In headless mode with `--dangerously-skip-permissions`, this is bypassed — the expected pattern for K8s jobs.

**Security consideration:** MCP servers run inside the agent container and can make network calls, access files, and execute commands. They are subject to the same NetworkPolicy and securityContext as the agent. Operators should audit MCP server packages before adding them - the `allowedMcpServers` guard rail can restrict which servers are permitted:

```yaml
guardrails:
  allowed_mcp_servers:
    - "osmia"       # Always allowed
    - "database"
    - "sentry"
  # Any MCP server not in this list will be stripped from settings.json
```

### 4.2 Claude Code Plugins & Marketplace

Claude Code has a structured plugin system. A plugin is a directory containing a `.claude-plugin/plugin.json` manifest alongside component directories for skills, agents, hooks, MCP servers, and settings. Plugins are namespaced — a skill called `review` in a plugin named `my-plugin` becomes `/my-plugin:review`.

There are three marketplace sources:
1. **Official Anthropic marketplace** (`claude-plugins-official`): Pre-loaded, includes integrations for GitHub, GitLab, Slack, Sentry, etc.
2. **Custom team marketplaces**: Any Git repository or URL containing a `.claude-plugin/marketplace.json`. Configured via `extraKnownMarketplaces` in settings.
3. **Community marketplace** (`claudecodemarketplace.com`): Unofficial, aggregates third-party plugins.

For Osmia containers, plugins can be loaded in three ways:

1. **`--plugin-dir` flag** (recommended for K8s): Point at plugin directories baked into the image. No formal installation needed.
   ```bash
   claude --plugin-dir /opt/osmia/plugins/security-tools -p "your prompt"
   ```

2. **Mounted from a ConfigMap/volume**: Plugin directories mounted at runtime, allowing per-deployment customisation without image rebuilds.

3. **Pre-enabled via settings.json**: The `enabledPlugins` field in `.claude/settings.json` specifies which marketplace plugins are active.

```yaml
# osmia-config.yaml
engines:
  claude-code:
    plugins:
      # Loaded via --plugin-dir (pre-baked in image)
      - path: "/opt/osmia/plugins/security-tools"
      # Enabled from team marketplace
      - name: "coderabbit-integration@unitary-tools"
    # Team marketplace configuration
    marketplaces:
      unitary-tools:
        source: "gitlab"
        repo: "unitaryai/internal-claude-plugins"
```

**Security consideration:** Plugins can define hooks, MCP servers, and custom tools. They run with the same permissions as Claude Code itself. Enterprise managed settings support `strictKnownMarketplaces` to restrict which marketplaces are allowed, `allowManagedHooksOnly` to block plugin-defined hooks, and `allowedMcpServers`/`deniedMcpServers` to control which MCP servers (including those bundled with plugins) can actually load.

### 4.3 Skills & Custom Commands

Claude Code supports two equivalent mechanisms for custom slash commands:
- **Legacy**: `.claude/commands/<name>.md` files (still supported)
- **Recommended**: `.claude/skills/<name>/SKILL.md` files (supports frontmatter for invocation control, `context: fork` for subagent execution, and supporting files)

Both create `/command-name` slash commands. Skills also support dynamic context injection via the `!`command`` syntax, which executes shell commands before the skill content is sent to Claude.

For task-type-specific behaviour, Osmia bundles skills into the container and copies them into the workspace:

```
/opt/osmia/skills/
  dependency-upgrade/
    SKILL.md               # "Upgrade the specified dependency..."
  test-fix/
    SKILL.md               # "Analyse the failing test and fix..."
  bug-fix/
    SKILL.md               # "Investigate the bug described in..."
```

Example SKILL.md with frontmatter:

```yaml
---
name: osmia-dependency-upgrade
description: Upgrade a specified dependency following project conventions
allowed-tools: Bash(uv *), Bash(git *), Read, Edit, Write
---

Upgrade the dependency specified in $ARGUMENTS.

Current branch: !`git branch --show-current`
Current Python version: !`python --version`

1. Check current pinned version in pyproject.toml
2. Run `uv add <package>@latest` to upgrade
3. Run `uv sync` to update the lock file
4. Run the test suite to verify compatibility
5. If tests fail, investigate and fix breaking changes
```

These are copied into `.claude/skills/` in the workspace before the agent starts, and the prompt instructs the agent to use the appropriate skill for its task type.

Claude Code also supports custom **subagents** defined in `.claude/agents/<name>.md`. These run in forked context and can be useful for the quality gate pattern (see section 3.5).

### 4.4 Codex & Aider Equivalents

- **Codex**: Uses `AGENTS.md` for repository context. MCP servers and plugins are not supported. Task-specific instructions must be injected directly into the prompt.
- **Aider**: Uses `.aider.conf.yml` for configuration. Supports custom commands but not MCP. Repository conventions go in a `.aider/conventions.md` file.

The `ExecutionEngine` interface abstracts these differences - each engine's `build_execution_spec` method handles the engine-specific configuration.

---

## 5. Authentication & Secrets

### 5.1 Execution Engine Authentication

| Engine | Method | K8s Implementation |
|--------|--------|--------------------|
| Claude Code | `ANTHROPIC_API_KEY` env var | K8s Secret mounted as env var |
| Claude Code | `apiKeyHelper` script | Script in container + Vault/AWS SM integration |
| Claude Code | `setup-token` (Teams/Max) | Pre-generated token stored as K8s Secret, set via `CLAUDE_CODE_OAUTH_TOKEN` |
| Claude Code | Amazon Bedrock | IRSA (IAM Roles for Service Accounts) + `CLAUDE_CODE_USE_BEDROCK=1` |
| Claude Code | Google Vertex AI | Workload Identity Federation + `CLAUDE_CODE_USE_VERTEX=1` |
| OpenAI Codex | `OPENAI_API_KEY` env var | K8s Secret mounted as env var |

### 5.2 The OAuth Challenge (Teams/Enterprise/Max Plans)

Users on Anthropic Teams, Enterprise, or Max plans have included usage allowances and want to use those rather than paying per-token via API keys. However, Claude Code's OAuth flow requires a browser-based login (`claude /login`), which is fundamentally incompatible with headless K8s environments.

**Current state (February 2026):**
- Browser-based OAuth is the only supported flow for Teams/Enterprise/Max
- A device-code flow (RFC 8628) has been formally requested (GitHub issue #22992) but is not yet implemented
- Copying `~/.claude/.credentials.json` from a logged-in machine has a known bug where the refresh token is not used, causing 401 errors after token expiry (GitHub issue #21765)
- `claude setup-token` can generate a long-lived OAuth token (1-year lifetime, format `sk-ant-oat01-...`) but requires an initial browser-based authentication on a capable machine
- `ANTHROPIC_API_KEY` via env var works reliably in headless mode but uses API billing, not plan allowances
- The `apiKeyHelper` TTL cache has a known bug (GitHub issue #11639) where the helper is called more frequently than configured

**Osmia's approach:**
1. **Default: API key** - Works reliably today. Recommend `apiKeyHelper` for rotation.
2. **setup-token (workaround)**: For Teams/Max plans, an admin can run `claude setup-token` on a desktop machine to generate a long-lived token. This token is stored as a K8s Secret and set via `CLAUDE_CODE_OAUTH_TOKEN` env var. Note: the `sk-ant-oat01-` format is non-standard and not accepted by all tools (GitHub issue #18340).
3. **Track upstream**: Monitor the device-code flow RFC 8628 request. When implemented, add native support.
4. **Cloud provider pass-through**: If the organisation uses Bedrock (`CLAUDE_CODE_USE_BEDROCK=1`) or Vertex AI (`CLAUDE_CODE_USE_VERTEX=1`), those authentication methods (IRSA, Workload Identity Federation) work natively in K8s and avoid the OAuth problem entirely. This is the most robust enterprise-headless pattern.
5. **Credential injection (experimental)**: For organisations willing to accept the refresh token bug risk, the controller could accept a pre-authenticated credentials file via a K8s Secret. This should be clearly documented as unsupported and fragile.

```yaml
engines:
  claude-code:
    auth:
      method: api_key  # "api_key" | "setup_token" | "bedrock" | "vertex" | "credentials_file"
      # For api_key:
      api_key_secret: "osmia-anthropic-key"
      # For bedrock:
      bedrock_region: "us-east-1"
      # For credentials_file (experimental, fragile):
      credentials_secret: "osmia-claude-credentials"
```

### 5.3 apiKeyHelper Integration

For enterprise deployments with secret rotation requirements:

```bash
#!/bin/bash
# /opt/osmia/get-api-key.sh
# Called by Claude Code every 5 minutes (configurable via CLAUDE_CODE_API_KEY_HELPER_TTL_MS)

# Option 1: AWS Secrets Manager
aws secretsmanager get-secret-value \
  --secret-id osmia/anthropic-api-key \
  --query SecretString --output text

# Option 2: HashiCorp Vault
vault kv get -field=api_key secret/osmia/anthropic

# Option 3: 1Password CLI
op read "op://Infrastructure/Anthropic/api-key"
```

### 5.4 Secrets Backend Plugins

The `SecretsBackend` interface allows different secrets providers:

- **`k8s`** (default): Standard Kubernetes Secrets. Simplest setup.
- **`vault`**: HashiCorp Vault via the Vault Agent sidecar or CSI driver.
- **`aws-sm`**: AWS Secrets Manager via IRSA.
- **`1password`**: 1Password Connect server or CLI.
- **`external-secrets`**: Kubernetes External Secrets Operator (supports multiple backends).

---

## 6. Execution Engines

### 6.1 Claude Code Engine

The primary engine. Runs Claude Code CLI in headless mode inside a K8s Job pod.

**Container contents:**
- Node.js runtime (for Claude Code CLI)
- Claude Code CLI (`@anthropic-ai/claude-code`)
- Git, SCM CLI (gh/glab)
- Python + uv (for target repos)
- Local stdio MCP server (for notifications/human interaction)
- Guard rail hooks
- `CLAUDE.md` or `guardrails.md` (mounted from ConfigMap)

**Invocation:**
```bash
claude -p "$(cat /config/task-prompt.md)" \
  --output-format json \
  --max-turns 50 \
  --dangerously-skip-permissions \
  --allowedTools "Bash,Read,Write,Edit,Glob,Grep,Task"
```

**Agent Teams Experimentation:**

Claude Code's agent teams feature (experimental) can be tested in-process mode inside K8s Jobs. The approach:

1. Set `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` in the container environment
2. Use `--teammate-mode in-process` (no tmux required)
3. Define a team configuration that splits work (e.g., one agent writes code, another writes tests)
4. Monitor via `TeammateIdle` and `TaskCompleted` hooks

This is experimental and should be behind a feature flag:

```yaml
engines:
  claude-code:
    agent_teams:
      enabled: false  # Opt-in experimental feature
      mode: "in-process"
      max_teammates: 3
```

### 6.2 OpenAI Codex Engine

Runs OpenAI's Codex CLI in a K8s Job pod.

**Container contents:**
- Node.js runtime
- Codex CLI
- Git, SCM CLI
- Python + uv
- `AGENTS.md` (Codex equivalent of CLAUDE.md)

**Invocation:**
```bash
codex --quiet \
  --approval-mode full-auto \
  --full-stdout \
  "$(cat /config/task-prompt.md)"
```

**Key differences from Claude Code:**
- Uses `AGENTS.md` instead of `CLAUDE.md` for repository context
- Different tool/permission model
- No hooks system (guard rails must be implemented differently - likely via wrapping commands)
- No MCP support (notification/interaction must use a different mechanism)

### 6.3 Engine Adapter Pattern for Missing Features

Where an engine lacks a feature (e.g., Codex has no hooks), the adapter provides it:

```go
type CodexEngine struct { /* ... */ }

func (e *CodexEngine) BuildExecutionSpec(task Task, config EngineConfig) (*ExecutionSpec, error) {
    // Wrap commands in a script that enforces guard rails
    // since Codex has no PreToolUse hooks
    // ...
}

func (e *CodexEngine) BuildPrompt(task Task) (string, error) {
    // Include guard rails directly in the prompt text
    // since Codex has no hooks to enforce them
    prompt, _ := e.basePrompt(task)
    prompt += "\n\n" + e.loadGuardrailsAsText()
    return prompt, nil
}
```

---

## 7. Scaling & Infrastructure

### 7.1 Karpenter Integration

Osmia jobs are ephemeral, CPU-bound workloads. Karpenter provisions nodes on-demand when jobs are created.

**Recommended NodePool:**

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: osmia
spec:
  disruption:
    consolidateAfter: 2m
    consolidationPolicy: WhenEmpty
  template:
    spec:
      requirements:
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64"]
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["spot", "on-demand"]
        - key: node.kubernetes.io/instance-type
          operator: In
          values:
            - c6i.xlarge    # 4 vCPU, 8 GiB
            - c6i.2xlarge   # 8 vCPU, 16 GiB
            - c7i.xlarge
            - c7i.2xlarge
            - m6i.xlarge    # 4 vCPU, 16 GiB (if memory needed)
      taints:
        - effect: NoSchedule
          key: osmia.io/agent
          value: "true"
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: osmia
  limits:
    cpu: "64"              # Max 64 vCPU across all osmia nodes
    memory: "128Gi"
```

**Key design decisions:**
- **Spot instances** for cost savings (jobs are stateless and retryable)
- **Taints** to isolate agent workloads from production services
- **`WhenEmpty` consolidation** to avoid disrupting running jobs
- **Instance type diversity** across c6i/c7i/m6i for spot availability
- **CPU limits** to cap maximum spend

### 7.2 Horizontal Scaling

The controller supports high-availability deployments via controller-runtime's built-in leader election (Lease objects). Multiple replicas can run simultaneously; only the leader processes tickets and creates jobs. Failover is automatic. (Note: kopf also supports HA via its Peering mechanism, but controller-runtime's Lease-based approach is more widely deployed and better understood by K8s operators.)

```yaml
guardrails:
  max_concurrent_jobs: 10   # Adjustable per deployment
```

For higher scale deployments, consider:
- **KEDA** to scale based on ticket queue depth
- **Sharded controllers** with ticket partitioning by namespace or label (future enhancement)

### 7.3 Observability

**Prometheus metrics** (exposed by the controller):

| Metric | Type | Description |
|--------|------|-------------|
| `osmia_jobs_total` | Counter | Total jobs created, by status |
| `osmia_jobs_active` | Gauge | Currently running jobs |
| `osmia_job_duration_seconds` | Histogram | Job execution duration |
| `osmia_tokens_total` | Counter | Total tokens consumed, by engine |
| `osmia_cost_usd_total` | Counter | Estimated cost in USD |
| `osmia_tickets_processed_total` | Counter | Tickets processed, by status |
| `osmia_human_interactions_total` | Counter | Human questions asked |

**Structured logging** via Go's `slog` (standard library, structured, JSON output):

```go
slog.Info("job created",
    "ticket_id", ticket.ID,
    "engine", engineName,
    "repo", ticket.RepoURL,
)
```

**Grafana dashboard** shipped as a JSON model in the Helm chart.

---

## 8. Repository Structure

```
osmia/
  .github/
    workflows/
      ci.yaml               # Lint, test, build (golangci-lint, go test)
      release.yaml           # Semantic versioning + container publish
    ISSUE_TEMPLATE/
      bug_report.md
      feature_request.md
      plugin_request.md
    PULL_REQUEST_TEMPLATE.md
  charts/
    osmia/                 # Helm chart
      Chart.yaml
      values.yaml
      templates/
        deployment.yaml      # Controller
        rbac.yaml
        configmap.yaml
        secret.yaml          # Optional, for simple setups
        servicemonitor.yaml  # Prometheus ServiceMonitor
  cmd/
    osmia/
      main.go               # Controller entrypoint
  docs/
    getting-started.md
    architecture.md
    plugins/
      writing-a-plugin.md   # Polyglot guide (Go, Python, TypeScript)
      ticketing.md
      notifications.md
      secrets.md
      engines.md
    guardrails.md
    scaling.md
    security.md
  docker/
    controller/
      Dockerfile             # Multi-stage Go build → distroless
    engine-claude-code/
      Dockerfile
      entrypoint.sh
      hooks/                 # Guard rail hook scripts
      mcp/                   # MCP server for notifications
    engine-codex/
      Dockerfile
      entrypoint.sh
  examples/
    github-slack/            # Minimal: GitHub Issues + Slack
      values.yaml
      guardrails.md
    gitlab-teams/            # GitLab + Microsoft Teams
      values.yaml
    enterprise/              # Full enterprise setup with Vault
      values.yaml
    plugins/
      example-jira-python/   # Example third-party plugin in Python
      example-teams-ts/      # Example third-party plugin in TypeScript
  internal/
    controller/
      controller.go          # controller-runtime reconciler
      controller_test.go
    jobbuilder/
      builder.go             # ExecutionSpec -> V1Job translation
      builder_test.go
    taskrun/
      taskrun.go             # TaskRun state machine + idempotency
      taskrun_test.go
    watchdog/
      watchdog.go            # Progress watchdog loop
      watchdog_test.go
    config/
      config.go              # Configuration loading
    metrics/
      metrics.go             # Prometheus metrics
    costtracker/
      tracker.go             # Token/cost tracking
    promptbuilder/
      builder.go             # Prompt construction
  pkg/
    engine/
      engine.go              # ExecutionEngine interface + ExecutionSpec
      claudecode/
        engine.go            # Claude Code engine
        hooks.go             # Hook generation
      codex/
        engine.go            # OpenAI Codex engine
      aider/
        engine.go            # Aider engine
    plugin/
      host.go                # gRPC plugin host (hashicorp/go-plugin)
      ticketing/
        ticketing.go         # TicketingBackend interface
        github/
          github.go          # GitHub Issues (built-in)
      notifications/
        notifications.go     # NotificationChannel interface
        slack/
          slack.go           # Slack (built-in)
      approval/
        approval.go          # HumanApprovalBackend interface
        slack/
          slack.go           # Slack approval (built-in)
      secrets/
        secrets.go           # SecretsBackend interface
        k8s/
          k8s.go             # K8s Secrets (built-in)
      review/
        review.go            # ReviewBackend interface
        coderabbit/
          coderabbit.go      # CodeRabbit (built-in)
      scm/
        scm.go               # SCMBackend interface
        github/
          github.go          # GitHub (built-in)
        gitlab/
          gitlab.go          # GitLab (built-in)
  proto/                     # Protobuf definitions (source of truth)
    ticketing.proto
    notifications.proto
    approval.proto
    secrets.proto
    review.proto
    scm.proto
    engine.proto
  sdk/                       # Generated plugin SDKs
    python/                  # osmia-plugin-sdk (PyPI)
    go/                      # github.com/osmia/plugin-sdk-go
    typescript/              # @osmia/plugin-sdk (npm)
  tests/
    e2e/                     # End-to-end tests (kind cluster)
    integration/
      github_ticketing_test.go
      slack_notifications_test.go
  go.mod
  go.sum
  Makefile                   # build, test, lint, proto-gen, sdk-gen
  LICENCE
  README.md
  CHANGELOG.md
  CONTRIBUTING.md
  CODE_OF_CONDUCT.md
  SECURITY.md
```

---

## 9. Implementation Phases

### Phase 1: Core Framework, Abstractions & Security Baseline

**Goal:** Extract the generic framework from the existing Unitary codebase. Define all plugin interfaces with versioning. Establish security baseline from day one. Get the controller running with no concrete plugins (dry-run mode).

**Work items:**
- Create the new OSS repository on GitHub
- Define all abstract base classes with `interface_version` fields:
  - `ExecutionEngine` + `ExecutionSpec` (decoupled from K8s)
  - `TicketingBackend`
  - `NotificationChannel` + `HumanApprovalBackend` (separated concerns)
  - `SecretsBackend`
  - `ReviewBackend`
  - `SCMBackend`
- Implement the `TaskRun` state machine with idempotency keys
- Implement the `JobBuilder` that translates `ExecutionSpec` -> K8s `V1Job`
- Implement the gRPC plugin host (hashicorp/go-plugin with version handshake)
- Define protobuf service definitions for all plugin interfaces
- Generate Python and Go plugin SDKs from protobufs
- Extract and generalise the controller (remove Shortcut-specific code)
- Extract and generalise the job manager (structured result via `/workspace/result.json`)
- Extract and generalise the cost tracker
- Implement configuration loading from `osmia-config.yaml`
- Implement Prometheus metrics endpoint from the start
- Set up CI pipeline (golangci-lint, go test, buf lint for protobufs)
- Set up container builds (GitHub Actions) with:
  - cosign image signing (keyless via OIDC)
  - syft SBOM generation (CycloneDX)
  - SLSA Level 2 provenance attestations
- Set up structured logging with slog (JSON output)
- Write unit tests for all core components
- Write SECURITY.md with vulnerability disclosure process

### Phase 2: Claude Code Engine + GitHub + Slack

**Goal:** First working end-to-end flow. A GitHub Issue triggers a Claude Code job that creates a PR and notifies via Slack.

**Work items:**
- Implement `ClaudeCodeEngine` (BuildExecutionSpec, BuildPrompt)
- Build the Claude Code container image (Dockerfile, entrypoint, MCP server)
- Implement structured result output (`/workspace/result.json` with semantic exit codes)
- Implement guard rails system (hooks generation, blocked commands, blocked files)
- Implement built-in `GitHubTicketingBackend` (poll issues by label, update state, add comments)
- Implement built-in `SlackNotificationChannel` (fire-and-forget notifications)
- Implement built-in `SlackApprovalBackend` (event-driven human-in-the-loop via webhook callback)
- Implement built-in `K8sSecretsBackend`
- Implement built-in `GitHubSCMBackend` (PR creation via gh CLI)
- Implement `guardrails.md` injection into prompts
- Write integration tests with mocked GitHub/Slack APIs
- Create example third-party plugin in Python (example-jira-python) using the SDK
- Implement `osmia-plugin scaffold` CLI (generates working plugin skeleton with tests, Dockerfile, and CI config)
- Implement `osmia-plugin test` CLI (local testing harness that exercises the plugin interface without a running controller)
- Document plugin deployment patterns (custom image, init containers, Python pip)
- Implement plugin crash detection and restart with exponential backoff
- Create the Helm chart (basic, with multi-tenancy support and plugin init container support)
- Create Grafana dashboard JSON
- Write getting-started documentation
- End-to-end testing against a test repository

### Phase 3: OpenAI Codex Engine + GitLab + Aider

**Goal:** Second and third execution engines plus second SCM backend. Proves the abstraction layer works with meaningfully different engines.

**Work items:**
- Implement `CodexEngine` in Go (different prompt format, no hooks, different result parsing)
- Implement `AiderEngine` in Go (popular OSS agent, broadens community appeal)
- Build the Codex and Aider container images
- Implement guard rails for hookless engines (prompt-based + command wrapping)
- Implement `GitLabSCMBackend` (MR creation via glab CLI)
- Implement `CodeRabbitReviewBackend`
- Add engine selection logic (per-ticket or per-config)
- Write tests for all engines
- Update documentation

### Phase 4: Agent Teams & Scaling

**Goal:** Experimental multi-agent support and production scaling.

**Work items:**
- Experiment with Claude Code agent teams in-process mode inside K8s Jobs
- Implement team configuration (roles, task splitting)
- Implement `TeammateIdle` and `TaskCompleted` hook handlers
- Implement Karpenter NodePool examples and documentation
- Add KEDA scaling examples (scale on ticket queue depth)
- Implement cost budgets and alerts
- Implement multi-tenancy namespace isolation
- Write scaling documentation

### Phase 5: Community & Launch

**Goal:** OSS launch readiness.

**Work items:**
- Write comprehensive README with architecture diagrams
- Write CONTRIBUTING.md with plugin development guide
- Create issue templates (bug, feature, plugin request)
- Create example configurations (github-slack, gitlab-teams, enterprise)
- Write plugin development tutorial with versioning guide
- Set up GitHub Discussions
- Create a documentation site (MkDocs Material)
- Record a demo video / GIF for README
- Write launch blog post
- Submit to relevant aggregators (Hacker News, Reddit r/kubernetes, r/devops)

### Phase 6: Unitary Migration

**Goal:** Migrate Unitary's internal deployment to use the OSS framework with Shortcut/Slack plugins.

**Work items:**
- Create `osmia-plugin-shortcut` Python package using the plugin SDK
- Migrate existing `shortcut_watcher.py` logic into the gRPC plugin
- Migrate existing `coderabbit_watcher.py` into `ReviewBackend` plugin
- Create Unitary-specific Helm values
- Validate against Customer1 test environment
- Cut over from internal codebase to OSS + plugins
- Archive the internal codebase

---

## 10. Security Model

### 10.1 Threat Model

| Threat | Mitigation |
|--------|------------|
| Prompt injection via ticket description | Guard rails hooks block dangerous commands; `guardrails.md` sets boundaries; `PreToolUse` hooks validate all tool calls |
| Credential leakage | Secrets mounted as env vars (not files); guard rails block commits of `.env`/credentials; short-lived tokens via `apiKeyHelper`; prefer workload identity (IRSA/WIF) over static keys in production |
| Container escape | Non-root user; read-only root filesystem; no host mounts; securityContext with `runAsNonRoot`, `readOnlyRootFilesystem`, dropped capabilities |
| Lateral movement | NetworkPolicy restricts egress to explicit FQDN/CIDR allowlists; dedicated namespace; RBAC scoped to namespace |
| Supply chain (malicious plugins) | Plugins run as controller extensions with full K8s API access - document this risk prominently; future: plugin capability manifests declaring required permissions |
| Supply chain (container images) | All release images signed with cosign; SBOMs generated with syft; SLSA Level 2 provenance via GitHub Actions |
| Excessive resource consumption | Job `activeDeadlineSeconds`; cost budgets; `max_concurrent_jobs` limit; Karpenter CPU limits |
| Unauthorised repository access | `allowed_repos` guard rail; SCM tokens scoped to specific repos/groups |
| Cross-tenant data leakage | Namespace-per-tenant model (see section 10.5) |

### 10.2 Container Security

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
  seccompProfile:
    type: RuntimeDefault
```

### 10.3 Network Policy

The default NetworkPolicy restricts agent pods to HTTPS and SSH egress only. However, `to: []` permits any destination IP on those ports, which is effectively minimal restriction.

**For production deployments**, we strongly recommend using a CNI that supports FQDN-based egress policies (Cilium, Calico Enterprise) or routing through an egress proxy with an explicit allowlist:

```yaml
# Basic policy (ships by default) - limits ports but not destinations
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: osmia-agent-egress
spec:
  podSelector:
    matchLabels:
      app: osmia-agent
  policyTypes: ["Egress"]
  egress:
    - ports:
        - port: 443
          protocol: TCP
        - port: 22
          protocol: TCP  # Git SSH
```

```yaml
# Enterprise policy example (Cilium FQDN-based) - limits destinations
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: osmia-agent-egress-strict
spec:
  endpointSelector:
    matchLabels:
      app: osmia-agent
  egress:
    - toFQDNs:
        - matchName: "api.anthropic.com"
        - matchName: "api.openai.com"
        - matchName: "github.com"
        - matchName: "gitlab.com"
        - matchName: "slack.com"
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
    - toFQDNs:
        - matchName: "github.com"
        - matchName: "gitlab.com"
      toPorts:
        - ports:
            - port: "22"
              protocol: TCP
```

### 10.4 Image Signing & Provenance

All release images are signed and accompanied by provenance attestations. This is a Phase 1 requirement, not a later addition.

- **Image signing**: cosign keyless signing via GitHub Actions OIDC
- **SBOM generation**: syft generates CycloneDX SBOMs for each image
- **Provenance**: SLSA Level 2 via GitHub Actions artifact attestations
- **Verification**: documented in getting-started guide

```bash
# Users can verify image signatures
cosign verify ghcr.io/osmia/controller:v1.0.0
cosign verify-attestation ghcr.io/osmia/engine-claude-code:v1.0.0
```

### 10.5 Multi-Tenancy

For shared clusters serving multiple teams or organisations, Osmia supports namespace-per-tenant isolation:

```yaml
# Helm values for tenant isolation
tenancy:
  mode: "namespace-per-tenant"  # or "shared" (default)
  tenants:
    - name: "team-alpha"
      namespace: "osmia-alpha"
      ticketing:
        backend: github
        config:
          repo: "alpha-org/repos"
      secrets:
        secret_name: "osmia-alpha-secrets"
    - name: "team-beta"
      namespace: "osmia-beta"
      ticketing:
        backend: jira
        config:
          project: "BETA"
      secrets:
        secret_name: "osmia-beta-secrets"
```

Each tenant gets:
- Dedicated namespace with its own RBAC, NetworkPolicies, and ResourceQuotas
- Separate K8s Secrets (no cross-tenant secret access)
- Independent job limits and cost budgets
- Isolated Karpenter NodePool (optional, for compute isolation)

---

## 11. Competitive Positioning

### 11.1 vs OpenClaw

| Aspect | OpenClaw | Osmia |
|--------|----------|---------|
| Architecture | Local-first, single machine | Kubernetes-native, distributed |
| Isolation | Shared process, local files | Container per job, namespace isolation |
| Credential storage | Local files (Markdown/JSON) | K8s Secrets / Vault / AWS SM |
| Scaling | Single machine | Karpenter auto-scaling |
| Security model | Local process execution; plugin store without verification | Container isolation; signed images; RBAC; network policies |
| Enterprise readiness | Designed for individuals | Designed for organisations (RBAC, audit, multi-tenancy) |
| Plugin ecosystem | Centralised skill hub | Entry-point based, no central registry (by design - avoids supply chain risk) |

### 11.2 vs Gas Town

| Aspect | Gas Town | Osmia |
|--------|----------|---------|
| Multi-agent | 20-30 agents, complex role system | Single agent per job (teams experimental) |
| State management | Git-backed "Beads" | K8s Jobs + ConfigMaps |
| Merge conflicts | "Refinery" merge queue | One job per ticket (no conflicts by design) |
| Engine support | Claude Code only | Claude Code + Codex (extensible) |
| Ticketing | Git issues | Pluggable (GitHub, Jira, etc) |

### 11.3 vs OpenAI Codex (cloud)

| Aspect | Codex (cloud) | Osmia |
|--------|---------------|---------|
| Infrastructure | OpenAI-managed | Self-hosted K8s |
| Data residency | OpenAI's cloud | Your cluster, your data |
| Engine lock-in | OpenAI only | Multi-engine |
| Customisation | AGENTS.md | Full plugin system, guard rails, hooks |
| Cost model | Per-task pricing | BYO compute + API keys |
| Network access | OpenAI sandbox | Your VPC, your network policies |

---

## 12. Community Bootstrapping

### 12.1 Repository Setup

- **GitHub** (not GitLab) for maximum OSS visibility and contributor access
- Apache 2.0 licence
- GitHub Actions for CI/CD
- GitHub Container Registry (ghcr.io) for images
- GitHub Discussions for community Q&A
- GitHub Projects for roadmap tracking

### 12.2 Documentation Site

Use **MkDocs Material** (MIT-licensed, widely used in K8s ecosystem):

- Getting Started guide (5-minute quickstart with kind + GitHub Issues)
- Architecture overview with diagrams
- Plugin development guide
- Configuration reference
- Security guide
- FAQ

### 12.3 Contributing Guide

Key sections:
- Development environment setup (Go toolchain, kind, skaffold, buf for protobufs)
- Plugin development tutorial in Python (implement a simple ticketing backend using the SDK)
- Controller code style (golangci-lint, gofumpt, British English in comments and docs)
- PR process (conventional commits, required reviews)
- Issue triage labels

### 12.4 Launch Strategy

1. **Soft launch**: Publish repo, share in a few Slack/Discord communities for early feedback
2. **Blog post**: Technical deep-dive on the architecture and security model
3. **Hacker News**: "Show HN: Osmia - Kubernetes-native harness for AI coding agents"
4. **Reddit**: r/kubernetes, r/devops, r/MachineLearning
5. **X/Twitter**: Thread explaining the problem and approach, with demo GIF
6. **Conference talks**: KubeCon, PyCon UK (submit CFPs)

### 12.5 First-Party Plugins vs Community

**Ships with core (maintained by Osmia team):**
- GitHub Issues ticketing
- Slack notifications
- K8s Secrets
- Claude Code engine
- Codex engine
- GitHub SCM
- GitLab SCM
- CodeRabbit review

**Community plugins (examples to seed):**
- Jira ticketing
- Linear ticketing
- Monday.com ticketing
- Microsoft Teams notifications
- Discord notifications
- Telegram notifications
- HashiCorp Vault secrets
- AWS Secrets Manager secrets
- 1Password secrets

---

## 13. Open Questions

1. **Container registry**: Should we publish to ghcr.io, Docker Hub, or both?
2. **Helm chart distribution**: OCI registry (ghcr.io) or a dedicated Helm repo?
3. **Multi-repo tickets**: How should we handle tickets that span multiple repositories? (Gas Town's approach of parallel agents on the same repo is one option; another is sequential jobs.)
4. **Cost attribution**: Should the controller integrate with cloud billing APIs (AWS Cost Explorer, etc) for actual cost tracking beyond token estimates?
5. **Plugin capability manifests**: Should we require plugins to declare their permission requirements (e.g. "needs K8s API access", "needs egress to api.jira.com") so operators can audit what a plugin does before enabling it?
6. **Managed offering**: Is there a future where Unitary offers Osmia as a managed SaaS? This would influence licence choice (Apache 2.0 allows competitors to do this too).
7. **CRD vs in-memory state**: Should `TaskRun` be a proper Kubernetes CRD (survives controller restarts, visible via `kubectl`) or an in-memory structure backed by ConfigMaps/annotations (simpler to implement, no CRD installation required)?

---

## 14. Dependencies & Prerequisites

| Dependency | Purpose | Version |
|------------|---------|---------|
| Go | Controller runtime | >= 1.23 |
| controller-runtime | K8s operator framework | >= 0.19 |
| client-go | K8s API | >= 0.31 |
| hashicorp/go-plugin | gRPC plugin host | >= 1.6 |
| protobuf / grpc-go | Plugin interface definitions | >= 1.35 / >= 1.68 |
| prometheus/client_golang | Metrics | >= 1.20 |
| slog (stdlib) | Structured logging | (stdlib) |
| Claude Code CLI | Execution engine | Latest |
| Codex CLI | Execution engine | Latest |
| Helm | Chart packaging | >= 3.14 |
| Karpenter | Node auto-scaling | >= 1.0 |

**Plugin SDK dependencies (for plugin authors):**

| SDK | Language | Package |
|-----|----------|---------|
| osmia-plugin-sdk | Python | `pip install osmia-plugin-sdk` |
| plugin-sdk-go | Go | `go get github.com/osmia/plugin-sdk-go` |
| @osmia/plugin-sdk | TypeScript | `npm install @osmia/plugin-sdk` |

---

## 15. Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Claude Code agent teams doesn't work in K8s Jobs | Medium | Low | Feature-flagged; single-agent mode is the reliable default |
| Codex/Aider CLI interface changes break our adapter | Medium | Medium | Pin versions; integration tests; abstract enough to adapt |
| Duplicate jobs on controller restart | High | High | TaskRun idempotency keys; check-before-create pattern |
| Low OSS adoption | Medium | Low | Focus on quality docs and quickstart; Unitary uses it regardless |
| Security vulnerability discovered post-launch | Low | High | SECURITY.md with disclosure process; signed images; SBOM; dependency audits |
| gRPC plugin complexity deters plugin authors | Medium | Medium | Ship Python/Go/TS SDKs with examples; provide plugin scaffolding CLI tool |
| Unitary team unfamiliar with Go | Medium | Medium | Claude builds the controller; Unitary plugins stay in Python via SDK; team should invest in Go familiarity for controller debugging, code review, and incident response; consider pairing sessions and a Go style guide early |
| Plugin interface breaks existing plugins | Medium | Medium | Explicit `interface_version`; migration guides; semver the interfaces |
| Third-party plugins with excessive permissions | Medium | High | Document plugin trust model; future: capability manifests |
| Human-in-the-loop timeouts block resources | Medium | Medium | Event-driven approval (NeedsHuman state); job can be paused/resumed |

When building, use a team. Use Sonnet 4.6 for implementation tasks and Opus 4.6 for planning.

