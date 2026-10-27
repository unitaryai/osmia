# Execution engines

!!! tip "New to engines?"
    For a plain-language comparison and decision tree, see [Engines explained](../concepts/engines.md). This page covers the detailed technical reference.

## Overview

Execution engines wrap AI coding tools (Claude Code, OpenAI Codex, Aider and others) and produce engine-agnostic `ExecutionSpec` structs that the JobBuilder translates into Kubernetes Jobs. Because the spec is separate from the Job, you can test engines without a cluster, run several AI tools from one controller, and add non-Kubernetes runtimes later.

## Interface summary

| Property | Value |
|---|---|
| Proto definition | `proto/engine.proto` |
| Go interface | `pkg/engine/engine.go` |
| Interface version | `1` |
| Role in lifecycle | Called after guard rail validation to produce the K8s Job spec |

## Go interface

```go
type ExecutionEngine interface {
    // BuildExecutionSpec translates a task into a container spec.
    BuildExecutionSpec(task Task, config EngineConfig) (*ExecutionSpec, error)

    // BuildPrompt constructs the task prompt for the AI agent.
    BuildPrompt(task Task) (string, error)

    // Name returns the unique engine identifier.
    Name() string

    // InterfaceVersion returns the interface version.
    InterfaceVersion() int
}
```

### Task

The input to all engine methods, populated from the ticketing backend:

```go
type Task struct {
    ID              string            // Unique task identifier.
    TicketID        string            // Source ticket identifier.
    TaskRunID       string            // Per-run unique identifier for storage isolation.
    Title           string            // Short summary (used as prompt heading).
    Description     string            // Full task description (main prompt content).
    RepoURL         string            // Repository the agent should work on.
    Labels          []string          // Labels from the source ticket.
    Metadata        map[string]string // Additional key-value pairs.
    MemoryContext   string            // Prior-knowledge injected from episodic memory.
    PriorBranchName string            // Git branch from a previous attempt (git-based continuation).
    SessionID       string            // Claude Code session ID for --resume (session-persistence continuation).
}
```

`PriorBranchName` and `SessionID` represent two continuation strategies that can coexist:

| Field | Strategy | Who sets it | How it works |
|---|---|---|---|
| `PriorBranchName` | Git-based (default) | Controller, on retries | Retry prompt includes prior branch; agent reads `git log` to understand prior work |
| `SessionID` | Session persistence (opt-in) | Controller, on retry jobs only | `--resume <id>` restores the full conversation; the `## Continuation` prompt section is suppressed |

On first attempts, the controller does not set either field. When session persistence is enabled, the engine itself derives a deterministic session ID from `TaskRunID` and launches Claude Code with `--session-id <id>`. On subsequent retries the controller passes the known session ID via `SessionID`, and the engine switches to `--resume <id>` to continue the prior conversation.

### EngineConfig

Runtime configuration passed to the engine:

```go
type EngineConfig struct {
    Image            string    // Container image override.
    TimeoutSeconds   int       // Active deadline for the K8s Job.
    ResourceRequests Resources // CPU and memory requests.
    ResourceLimits   Resources // CPU and memory limits.
    Env              map[string]string // Additional environment variables.
}
```

### ExecutionSpec

The output: everything needed to create a K8s Job:

```go
type ExecutionSpec struct {
    Image                 string            // Container image to run.
    Command               []string          // Entrypoint command and arguments.
    Env                   map[string]string // Plain-text environment variables.
    SecretEnv             map[string]string // Key=env var name, Value=K8s Secret name.
    ResourceRequests      Resources
    ResourceLimits        Resources
    Volumes               []VolumeMount
    ActiveDeadlineSeconds int               // Hard timeout for the Job.
}

// VolumeMount — volume source priority: PVCName > ConfigMapName > emptyDir.
type VolumeMount struct {
    Name          string // Volume name.
    MountPath     string // Path inside the container.
    ReadOnly      bool
    SubPath       string // Mount a single key/subdirectory.
    ConfigMapName string // Back the volume with a ConfigMap.
    ConfigMapKey  string // Project only this key (used with ConfigMapName).
    PVCName       string // Back the volume with a PersistentVolumeClaim.
}
```

### TaskResult

The structured outcome of a completed task, written to `/workspace/result.json`:

```go
type TaskResult struct {
    Success         bool        // Whether the task completed successfully.
    MergeRequestURL string      // URL of the created pull request.
    BranchName      string      // The branch containing changes.
    Summary         string      // Human-readable summary.
    TokenUsage      *TokenUsage // Input/output token counts.
    CostEstimateUSD float64     // Estimated cost in US dollars.
    ExitCode        int         // 0=success, 1=agent failure, 2=guard rail blocked.
}
```

## Built-in engines

### Claude Code

The primary and recommended engine. Runs the [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) in headless mode, with hook-based guard rails.

| Property | Value |
|---|---|
| Engine name | `claude-code` |
| Package | `pkg/engine/claudecode/` |
| Default image | `ghcr.io/unitaryai/engine-claude-code:latest` |
| Default timeout | 7200 seconds (2 hours) |
| API key secret | `anthropic-api-key` |
| Guard rails | Pre-tool-use hooks via `hooks.json` |
| Max agentic turns | 50 (configurable) |

#### Configuration

```yaml
config:
  engines:
    default: claude-code
    claude-code:
      image: "ghcr.io/unitaryai/engine-claude-code:v2.1.0"
      max_turns: 50
      fallback_model: haiku              # used when primary model is overloaded
      append_system_prompt: "Always run tests before committing."
      tool_whitelist:                    # only these tools are available
        - Bash
        - Read
        - Write
        - Edit
      tool_blacklist:                    # these tools are blocked
        - WebSearch
      json_schema: '{"type":"object","properties":{"success":{"type":"boolean"},"summary":{"type":"string"}},"required":["success","summary"]}'
      skills:                            # custom skills (see Skills section below)
        - name: create-changelog
          inline: |
            # Create Changelog
            Generate a CHANGELOG.md entry for the changes made.
        - name: review-checklist
          path: /opt/osmia/skills/review-checklist.md
        - name: deploy-guide
          configmap: deploy-skills       # load from a Kubernetes ConfigMap
          key: deploy-guide.md           # optional, defaults to <name>.md
      sub_agents:                        # see Sub-Agents section below
        - name: reviewer
          description: "Reviews code changes for correctness"
          prompt: "You are a code reviewer. Check for bugs, security issues, and style."
          model: haiku
        - name: architect
          description: "System architecture reviewer"
          configmap: architect-agent      # load prompt from ConfigMap
      agent_teams:                       # experimental multi-instance collaboration
        enabled: false
        mode: in-process                 # required for headless K8s containers
        max_teammates: 3
```

| Field | Type | Default | Description |
|---|---|---|---|
| `image` | string | `ghcr.io/unitaryai/engine-claude-code:latest` | Container image override |
| `fallback_model` | string | (none) | Model to use when the primary is overloaded (e.g. `haiku`) |
| `append_system_prompt` | string | (none) | Extra text appended to Claude Code's system prompt |
| `session_persistence` | SessionPersistenceConfig | disabled | Opt-in session-state persistence between retries (see [Session persistence](#session-persistence)) |
| `tool_whitelist` | []string | (none) | Only allow these Claude Code tools (via `--allowedTools`) |
| `tool_blacklist` | []string | (none) | Block these Claude Code tools (via `--disallowedTools`) |
| `json_schema` | string | built-in TaskResult schema | JSON schema for structured output (via `--json-schema`) |
| `skills` | []SkillConfig | (none) | Custom skill files loaded into the agent (see [Skills](#skills)) |
| `sub_agents` | []SubAgentConfig | (none) | Sub-agent definitions (see [Sub-agents](#sub-agents)) |
| `agent_teams` | AgentTeamsConfig | disabled | Experimental multi-instance collaboration (see [Agent teams](#agent-teams)) |

The Job's time limit is not set per engine. It comes from
`guardrails.max_job_duration_minutes` and applies to every engine. CPU and
memory requests and limits for agent pods cannot be configured yet.

#### Command

The engine generates a `claude` CLI invocation in streaming JSON mode:

```bash
setup-claude.sh \
  -p "<prompt>" \
  --output-format stream-json \
  --max-turns 50 \
  --dangerously-skip-permissions \
  --verbose \
  --mcp-config /workspace/.mcp.json
```

The `setup-claude.sh` wrapper runs before `claude` to initialise the writable home directory, writing `~/.claude/settings.json`, `/workspace/.mcp.json` and any [skill files](#skills). It then `exec`s the real `claude` binary with the arguments above.

#### Guard rails (hooks)

Claude Code supports a [hooks system](https://docs.anthropic.com/en/docs/claude-code/hooks) that intercepts tool calls before execution. Osmia generates a `hooks.json` configuration file and mounts it into the agent container at `/config/hooks.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "command": "/config/guard-rail-check.sh \"$TOOL_INPUT\""
      },
      {
        "matcher": "Write|Edit",
        "command": "/config/file-pattern-check.sh \"$TOOL_INPUT\""
      }
    ],
    "PostToolUse": [
      {
        "command": "/config/heartbeat.sh"
      }
    ]
  }
}
```

The guard rail check scripts validate each tool call against:

- Destructive command detection: blocks `rm -rf`, `DROP TABLE`, `git push --force`, `sudo`, and similar dangerous commands.
- Blocked file patterns: prevents reading or writing files matching patterns in `blocked_file_patterns` (e.g., `*.env`, `*.key`, `*.pem`).
- Network restriction: optionally blocks `curl`, `wget`, and other network tools from contacting external hosts.

If a hook script exits with a non-zero code, Claude Code blocks the tool call and reports the violation to the agent, which can then adjust its approach.

The `PostToolUse` hook writes heartbeat telemetry to `/workspace/heartbeat.json` after every tool invocation, so the progress watchdog can monitor agent activity.

#### Environment variables

| Variable | Source | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | K8s Secret `osmia-anthropic-key` | API authentication |
| `OSMIA_TASK_ID` | Controller | Unique task identifier |
| `OSMIA_TICKET_ID` | Controller | Source ticket identifier |
| `OSMIA_REPO_URL` | Ticket | Repository to work on |
| `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` | Engine | Always set to `1` |
| `CLAUDE_SKILL_INLINE_<NAME>` | Engine | Base64-encoded inline skill content (see [Skills](#skills)) |
| `CLAUDE_SKILL_PATH_<NAME>` | Engine | Path to a skill file on the image or ConfigMap mount (see [Skills](#skills)) |
| `CLAUDE_SUBAGENT_PATH_<NAME>` | Engine | Path to a ConfigMap-backed sub-agent file (see [Sub-Agents](#sub-agents)) |
| `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS` | Engine | Set to `1` when agent teams are enabled |
| `CLAUDE_CODE_MAX_TEAMMATES` | Engine | Maximum teammate agents (agent teams) |

#### Volume mounts

| Mount | Path | Writable | Purpose |
|---|---|---|---|
| `workspace` | `/workspace` | Yes | Repository checkout and working directory |
| `config` | `/config` | No | Guard rail hooks and configuration |
| `home` | `/home/osmia` | Yes | Writable home directory (emptyDir) for `~/.claude/` config and skills |
| `tmp` | `/tmp` | Yes | Writable tmp (emptyDir) for Claude Code subprocess shell directories |

#### Skills

Skills are custom Markdown instruction files that the agent can invoke with `/skill-name` in its prompts. They are written to the agent's Claude configuration directory before it starts (the exact path is covered below).

Each skill has a `name` (lowercase letters, digits and hyphens only) and exactly one of:

- `inline`: the Markdown content directly in the config. The controller base64-encodes it and passes it as the `CLAUDE_SKILL_INLINE_<NAME>` environment variable.
- `path`: a path to a Markdown file on the container image (e.g. `/opt/osmia/skills/review-checklist.md`). The controller passes it as `CLAUDE_SKILL_PATH_<NAME>`.
- `configmap`: the name of a Kubernetes ConfigMap containing the skill. The controller mounts the ConfigMap as a volume at `/skills/<name>.md` and sets `CLAUDE_SKILL_PATH_<NAME>` to the mount path. Set `key` to pick a specific key within the ConfigMap (defaults to `<name>.md`).

A `configmap` skill may also set `multi_file: true` when the ConfigMap holds a whole skill directory rather than one file. See [Multi-file skills](#multi-file-skills) below.

At container startup, `setup-claude.sh` reads these environment variables, decodes or copies the files, and writes each skill to `<config-dir>/skills/<name>/SKILL.md`. The `<NAME>` suffix is converted to lowercase with hyphens (for example `CLAUDE_SKILL_INLINE_CREATE_CHANGELOG` becomes `skills/create-changelog/SKILL.md`).

`<config-dir>` is `$CLAUDE_CONFIG_DIR` when session persistence is enabled, and `~/.claude` otherwise. Claude Code reads user-scoped skills from whichever of those is in force and does not fall back between them, so the skill must be written to the one it will read.

The skill and sub-agent directories are cleared on every pod start and rebuilt from the environment variables. That happens whether or not any skills are configured, so removing the last one still clears what was there. A skill removed from the configuration disappears on the next run rather than lingering on a persisted volume. Do not edit skill files inside a running pod: the next start deletes them.

Example: inline skill

```yaml
engines:
  claude-code:
    skills:
      - name: create-changelog
        inline: |
          # Create Changelog

          When asked to create a changelog entry:
          1. Read the existing CHANGELOG.md
          2. Determine the next version number from git tags
          3. Add a new section with today's date
          4. List all changes since the last release
```

Example: image-bundled skill

```yaml
engines:
  claude-code:
    skills:
      - name: security-review
        path: /opt/osmia/skills/security-review.md
```

Example: ConfigMap-backed skill

```yaml
engines:
  claude-code:
    skills:
      - name: deploy-guide
        configmap: deploy-skills    # K8s ConfigMap name
        key: deploy-guide.md        # optional, defaults to <name>.md
```

Create the ConfigMap separately:

```bash
kubectl create configmap deploy-skills \
  --from-file=deploy-guide.md=./skills/deploy-guide.md
```

ConfigMap skills suit large skill files, or teams that manage their own skills separately from the controller configuration.

#### Multi-file skills

A skill can be a directory rather than a single file: a short `SKILL.md` plus reference files it points at. The agent reads `SKILL.md` up front and pulls in a reference file only when it needs that detail, so a large skill does not use up context on every run.

Set `multi_file: true` and give the ConfigMap one key per file. The whole ConfigMap is mounted as a directory and every Markdown file in it is copied into the skill directory. `key` is ignored.

```yaml
engines:
  claude-code:
    skills:
      - name: incident-classifier
        configmap: incident-classifier-skill
        multi_file: true
```

Create the ConfigMap from a directory:

```bash
kubectl create configmap incident-classifier-skill \
  --from-file=./skills/incident-classifier/
```

The directory must contain a `SKILL.md`. The agent has no entry point without one, and `setup-claude.sh` logs a warning if it is missing. Only `.md` files are copied, so a `README` or a script sitting in the same directory is ignored.

ConfigMaps hold at most 1 MiB in total. A skill larger than that needs an image-bundled `path` skill instead.

To bundle skills into the container image, add them to your custom Dockerfile:

```dockerfile
FROM ghcr.io/unitaryai/engine-claude-code:latest
COPY skills/ /opt/osmia/skills/
```

#### Sub-agents

Sub-agents let the main Claude Code agent hand specialised subtasks to other agents during execution. Each sub-agent has its own system prompt, model, tool restrictions and permission mode. Sub-agents use Claude Code's official `--agents` flag and `~/.claude/agents/` directory.

Sub-agents can be defined inline (prompt in the config) or loaded from a Kubernetes ConfigMap (for large prompts or independent management).

Configuration:

```yaml
engines:
  claude-code:
    sub_agents:
      # Inline sub-agent: prompt is in the config.
      - name: reviewer
        description: "Reviews code changes for correctness and style"
        prompt: |
          You are a code reviewer. Check for:
          - Bugs and logic errors
          - Security vulnerabilities
          - Style and convention violations
        model: haiku
        tools:
          - Read
          - Grep
          - Glob
        max_turns: 10

      # ConfigMap-backed sub-agent: prompt loaded from a volume.
      - name: architect
        description: "Reviews system architecture decisions"
        configmap: architect-agent    # K8s ConfigMap name
        key: architect.md             # optional, defaults to <name>.md
        model: opus

      # Background sub-agent.
      - name: linter
        description: "Runs linting in the background"
        prompt: "Run the project linter and report issues."
        background: true
```

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | (none) | Sub-agent identifier (required) |
| `description` | string | (none) | Short summary of the sub-agent's purpose (required) |
| `prompt` | string | (none) | Inline system prompt (mutually exclusive with `configmap`) |
| `model` | string | inherit | Model to use: `sonnet`, `opus`, `haiku`, or `inherit` |
| `tools` | []string | (none) | Tool allowlist |
| `disallowed_tools` | []string | (none) | Tool denylist |
| `permission_mode` | string | `default` | One of `default`, `acceptEdits`, `dontAsk`, `bypassPermissions`, `plan` |
| `max_turns` | int | (none) | Maximum agentic turns |
| `skills` | []string | (none) | Skill names to preload |
| `background` | bool | `false` | Run as a background process |
| `configmap` | string | (none) | Load prompt from this Kubernetes ConfigMap |
| `key` | string | `<name>.md` | Key within the ConfigMap |

How it works:

- Inline sub-agents are serialised as JSON and passed via `--agents '{"name": {"description":"...", "prompt":"...", ...}}'`.
- ConfigMap sub-agents are volume-mounted at `/subagents/<name>.md` and copied to `~/.claude/agents/<name>.md` by `setup-claude.sh` at container startup. Claude Code discovers agent files in this directory on its own.

Creating a ConfigMap for a sub-agent:

```bash
kubectl create configmap architect-agent \
  --from-file=architect.md=./agents/architect.md
```

The Markdown file can include YAML frontmatter for metadata (model, tools, etc.) following the [Claude Code sub-agents specification](https://code.claude.com/docs/en/sub-agents).

#### Agent teams

!!! info "Experimental"
    Agent teams are an experimental Claude Code feature (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`). The API may change in future Claude Code releases.

Agent teams start several independent Claude Code instances that collaborate through shared task lists and messages between agents, coordinated by a team lead. The team lead creates teammates as the task needs them, so you do not define agents in advance.

This differs from [sub-agents](#sub-agents), which are lightweight helpers inside a single Claude Code session. You can use both at the same time.

Configuration:

```yaml
engines:
  claude-code:
    agent_teams:
      enabled: true
      mode: in-process        # required for headless K8s containers (no tmux)
      max_teammates: 3
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Activate agent teams mode |
| `mode` | string | `in-process` | Teammate execution mode. Use `in-process` for headless containers (no tmux required). Alternative: `tmux` for interactive environments |
| `max_teammates` | int | `3` | Maximum number of teammate agents the team lead can spawn |

When enabled, the engine:

1. Sets `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` and `CLAUDE_CODE_MAX_TEAMMATES=<n>` in the container environment.
2. Appends `--teammate-mode <mode>` to the Claude CLI command.

The team lead then decides how many teammates to create, what roles they have and how to split the work.

#### Session persistence

By default, each agent pod starts with a clean `~/.claude/` directory (emptyDir volume). When a task hits `--max-turns` and retries, the new pod has no conversation history and must re-read git diffs to understand prior work.

Session persistence stores the `~/.claude/` directory (and optionally the workspace) in durable storage. Retry pods can then resume the exact conversation with `--resume <session-id>` and do not waste turns re-establishing context.

> Note: session persistence is opt-in. Git-based continuation (`PriorBranchName`) remains the default fallback.

Configuration:

```yaml
engines:
  claude-code:
    session_persistence:
      enabled: true
      backend: shared-pvc          # shared-pvc | per-taskrun-pvc | s3
      pvc_name: osmia-agent-sessions  # for shared-pvc backend
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Activate session persistence |
| `backend` | string | (none) | Storage backend: `shared-pvc`, `per-taskrun-pvc`, or `s3` |
| `pvc_name` | string | (none) | Name of the shared PVC (`shared-pvc` backend) |
| `storage_class` | string | cluster default | Storage class for dynamic PVCs (`per-taskrun-pvc` backend) |
| `storage_size` | string | `1Gi` | PVC size (`per-taskrun-pvc` backend) |
| `s3_bucket` | string | (none) | S3 bucket name (`s3` backend, not yet implemented) |
| `s3_prefix` | string | `osmia-sessions/` | S3 key prefix (`s3` backend) |
| `ttl_minutes` | int | `1440` | Session data retention after TaskRun completion (24 h) |

Backends:

- `shared-pvc`: mounts a single ReadWriteMany PVC. Each TaskRun gets an isolated subdirectory via `SubPath`. Simple and low-cost, but requires a storage class that supports RWX (e.g. NFS, EFS, Ceph).
- `per-taskrun-pvc`: creates a dedicated ReadWriteOnce PVC per TaskRun. Stronger isolation, and the controller deletes the PVC on cleanup.
- `s3`: not yet implemented. Will use an init container to download session data and a lifecycle hook to upload on exit.

How it works:

1. On first job launch, the controller generates a deterministic session ID from the TaskRun ID and passes it to the agent via `--session-id <id>`.
2. Claude Code stores all session JSONL files in `$CLAUDE_CONFIG_DIR` (the PVC-backed path) rather than the ephemeral emptyDir home volume.
3. When the agent hits `--max-turns`, the retry pod receives `task.SessionID` and the agent is invoked with `--resume <session-id>` instead of a fresh session.
4. The workspace directory is also persisted on the PVC (`OSMIA_WORKSPACE_DIR`), so `entrypoint.sh` skips the git-clone step on retry pods.

#### Storage cleanup

Session data is ephemeral: it only needs to exist while retries are possible. Once a TaskRun reaches a terminal state (Succeeded, Failed with retries exhausted, or TimedOut), its session data can be removed. A background cleaner goroutine (`internal/sessionstore/cleaner.go`) does this automatically. It runs inside the controller pod when session persistence is enabled.

How cleanup works:

The cleaner runs a sweep every hour (configurable) and removes session data older than `ttl_minutes` (default 1440 = 24 hours):

| Backend | What gets cleaned | How |
|---|---|---|
| `shared-pvc` | Subdirectories on the shared PVC | The controller pod mounts the shared PVC at `/data/sessions`. The cleaner lists subdirectories and removes any whose modification time is older than the TTL. |
| `per-taskrun-pvc` | Dedicated PVCs | The cleaner lists PVCs with the `osmia.io/task-run-id` label and deletes any whose creation timestamp is older than the TTL. |
| `s3` | S3 objects by prefix | Not yet implemented. |

Details:

- PVC phase is intentionally not checked. When an agent pod finishes and is deleted, its PVC stays in the `Bound` phase, because Kubernetes does not move it to `Released` on its own. If the cleaner filtered by phase, PVCs would accumulate indefinitely. Age-based deletion is safe because the TTL guarantees the TaskRun is terminal and no pod is referencing the PVC.
- The `Cleanup()` method on each store is idempotent. Calling it multiple times (e.g. after a controller restart) is safe: deleting an already-removed PVC is treated as success.
- The controller pod must mount the shared PVC for the `shared-pvc` cleaner to work. The Helm chart handles this automatically: when `sessionPersistence.backend` is `shared-pvc`, the deployment template adds a `session-pvc` volume and mounts it at `/data/sessions`. If you deploy without the Helm chart, ensure the controller pod has the shared PVC mounted and pass the mount path as `PVCRootDir` in the `CleanerConfig`.
- `per-taskrun-pvc` cleanup requires RBAC. The controller's service account needs `list` and `delete` permissions on `persistentvolumeclaims` in the target namespace. The Helm chart's default RBAC rules include these permissions.

Tuning the TTL:

Set `sessionPersistence.ttlMinutes` in `values.yaml`. A shorter TTL reduces storage usage but risks deleting session data before a long-running retry completes. A longer TTL is safer but accumulates more data. The default of 24 hours is conservative: most tasks finish their retries within minutes.

```yaml
sessionPersistence:
  enabled: true
  backend: per-taskrun-pvc
  ttlMinutes: 720   # 12 hours, more aggressive cleanup
```

What happens if cleanup fails?

The cleaner logs warnings and continues to the next item. Failed deletions are retried on the next sweep (1 hour later). No session data is critical. In the worst case, stale PVCs occupy storage until manually removed. You can monitor cleanup via the controller's structured logs:

```text
level=INFO msg="per-taskrun-pvc cleaner: deleted stale session PVC" pvc=osmia-session-tr-abc123
level=WARN msg="per-taskrun-pvc cleaner: failed to delete stale PVC" pvc=osmia-session-tr-xyz error="..."
```

Helm chart: set `sessionPersistence.enabled: true` and configure the chosen backend in `values.yaml`. The chart will create the shared PVC when `backend: shared-pvc` is selected.

#### Continuation prompts

When an agent exhausts `--max-turns`, the default behaviour is to auto-retry using git-based continuation (the retry agent clones the prior branch and reads `git log`). User-prompted continuation replaces that with an interactive pause: the controller sends a Slack message asking the operator whether to continue or stop, and only resumes if the operator approves.

Prerequisites:

- Session persistence must be enabled (so the retry pod can resume via `--resume <session-id>` rather than re-reading git history).
- An approval backend must be configured (currently Slack).

How it works:

1. The agent pod exits cleanly after hitting `--max-turns`. The controller detects that `ToolCallsTotal >= ConfiguredMaxTurns` and the task did not succeed.
2. The TaskRun transitions to `NeedsHuman` with gate type `continuation`.
3. The controller sends an approval request containing the turn count, cost so far, any progress summary, and two buttons: Continue and Stop.
4. If the operator clicks Continue, the controller increments `ContinuationCount`, creates a new pod with `--resume <session-id>`, and transitions back to `Running`.
5. If the operator clicks Stop (or the message times out), the TaskRun transitions to `Failed` with a reason that includes the operator's name and any progress summary.

Configuration:

```yaml
config:
  engines:
    claude-code:
      continuation_prompt: true      # enable user-prompted continuation
      max_continuations: 3           # maximum times the operator can approve (default 3)
      session_persistence:
        enabled: true
        backend: per-taskrun-pvc
```

Interaction with retries:

`ContinuationCount` and `RetryCount` are independent. A continuation resumes the existing session from where it paused. It does not count as a retry and does not use a retry slot. If a resumed session then fails for a different reason (e.g. a tool error), the normal retry mechanism applies separately.

Operator experience:

The Slack message sent to the approval channel looks like:

```text
Task tr-abc123 has exhausted its turn limit (50 turns, $0.42).
Progress: "Implemented the feature, tests passing, PR not yet raised."

Continuation 1 of 3. Approve to resume the session where it left off.

[Continue]  [Stop]
```

Clicking Continue resumes immediately. Clicking Stop marks the TaskRun as failed and records the operator's username in the failure reason.

> Note: user-prompted continuation requires the Claude Code engine and session persistence. The git-based continuation strategy is used for all other engines.

---

### OpenAI Codex

Runs the [OpenAI Codex CLI](https://github.com/openai/codex) in fully autonomous mode.

| Property | Value |
|---|---|
| Engine name | `codex` |
| Package | `pkg/engine/codex/` |
| Default image | `ghcr.io/unitaryai/engine-codex:latest` |
| Default timeout | 7200 seconds (2 hours) |
| API key secret | `openai-api-key` |
| Guard rails | Prompt-embedded rules |

#### Configuration

```yaml
config:
  engines:
    default: codex
    codex:
      image: "ghcr.io/unitaryai/engine-codex:v1.0.0"
```

#### Command

```bash
codex \
  --quiet \
  --approval-mode full-auto \
  --full-stdout \
  "<prompt>"
```

#### Guard rails (prompt-embedded)

Codex does not support a hooks system. Guard rails are appended directly to the task prompt:

```
## Guard Rails

You MUST follow these rules strictly:
- Do NOT execute destructive commands (e.g. rm -rf /, drop database, etc.)
- Do NOT modify or read files matching sensitive patterns (*.env, **/secrets/**, *.key, *.pem)
- Do NOT make network requests to external services other than the repository remote
- Do NOT install packages or dependencies without explicit instructions to do so
- Do NOT push commits directly; stage and commit changes locally only
```

!!! warning "Prompt-based guard rails are advisory"
    The AI model may not always respect prompt-embedded rules. For stricter enforcement, use the Claude Code engine with hook-based guards, or rely on the quality gate for post-completion validation.

#### Repository context

Codex reads repository conventions from `AGENTS.md` (rather than `CLAUDE.md`). If an `AGENTS.md` file is in the repository root, Codex uses it for coding conventions and project structure.

#### Environment variables

| Variable | Source | Description |
|---|---|---|
| `OPENAI_API_KEY` | K8s Secret `openai-api-key` | API authentication |
| `OSMIA_TASK_ID` | Controller | Unique task identifier |
| `OSMIA_TICKET_ID` | Controller | Source ticket identifier |
| `OSMIA_REPO_URL` | Ticket | Repository to work on |

---

### Aider

Runs the [Aider CLI](https://aider.chat/) for AI-assisted coding. Aider supports several LLM providers. Osmia can configure it to use either Anthropic or OpenAI models.

| Property | Value |
|---|---|
| Engine name | `aider` |
| Package | `pkg/engine/aider/` |
| Default image | `ghcr.io/unitaryai/engine-aider:latest` |
| Default timeout | 7200 seconds (2 hours) |
| API key secret | `anthropic-api-key` (default) or `openai-api-key` |
| Guard rails | Prompt-embedded rules |

#### Configuration

```yaml
config:
  engines:
    default: aider
    aider:
      image: "ghcr.io/unitaryai/engine-aider:v1.0.0"
      provider: "anthropic"   # or "openai"
```

#### Command

```bash
aider \
  --yes \
  --no-git \
  --message "<prompt>"
```

The `--no-git` flag is used because Osmia manages git operations via the SCM backend, not via Aider's built-in git support.

#### Model provider

Aider supports both Anthropic and OpenAI models. Configure the provider in the engine configuration:

| Provider | Environment Variable | Secret Name |
|---|---|---|
| `anthropic` (default) | `ANTHROPIC_API_KEY` | `anthropic-api-key` |
| `openai` | `OPENAI_API_KEY` | `openai-api-key` |

#### Repository context

Aider reads coding conventions from `.aider/conventions.md` (rather than `CLAUDE.md` or `AGENTS.md`). If an `.aider.conf.yml` file is in the repository root, Aider uses it for additional configuration (model selection, editor settings, etc.).

#### Guard rails (prompt-embedded)

Like Codex, Aider does not support a hooks system. Guard rails are appended directly to the task prompt, identical in content to the Codex guard rails above.

### OpenCode

OpenCode is a terminal-based AI coding agent. Osmia runs it in headless mode inside Kubernetes Jobs.

Package: `pkg/engine/opencode/`

| Property | Value |
|---|---|
| Engine name | `opencode` |
| Default image | `ghcr.io/unitaryai/engine-opencode:latest` |
| Command | `opencode --non-interactive --message <prompt>` |
| Interface version | `1` |

#### Configuration

```yaml
engines:
  default: opencode
  opencode:
    image: ghcr.io/unitaryai/engine-opencode:v1.0.0  # optional override
    auth:
      method: api_key
      api_key_secret: anthropic-credentials
    provider: anthropic  # or "openai", "google"
```

#### Providers

| Provider | Environment Variable | K8s Secret Key |
|---|---|---|
| `anthropic` (default) | `ANTHROPIC_API_KEY` | `anthropic-api-key` |
| `openai` | `OPENAI_API_KEY` | `openai-api-key` |
| `google` | `GOOGLE_API_KEY` | `google-api-key` |

#### Repository context

OpenCode reads coding conventions from `AGENTS.md` in the repository root.

#### Guard rails (prompt-embedded)

OpenCode does not support a hooks system. Guard rails are appended directly to the task prompt.

### Cline

!!! warning "Community template: no pre-built image"
    Cline is a VS Code extension with no published headless CLI. The Go engine implementation (`pkg/engine/cline/`) and the Dockerfile (`docker/engine-cline/`) are provided as a community contribution template. No pre-built container image is published for Cline. Configuring `cline` as your engine will result in an image pull failure until a working headless integration is contributed. See [Contributing](../contributing.md) if you want to help.

Cline is an AI coding agent with optional MCP (Model Context Protocol) and AWS Bedrock support. When a headless CLI becomes available, Osmia can run it inside Kubernetes Jobs using the implementation in `pkg/engine/cline/`.

Package: `pkg/engine/cline/`

| Property | Value |
|---|---|
| Engine name | `cline` |
| Default image | `ghcr.io/unitaryai/engine-cline:latest` *(not yet published)* |
| Command | `cline --headless --task <prompt> --output-format json` |
| Interface version | `1` |

#### Configuration

```yaml
engines:
  default: cline
  cline:
    image: ghcr.io/unitaryai/engine-cline:v1.0.0  # optional override
    auth:
      method: api_key
      api_key_secret: anthropic-credentials
    provider: anthropic  # or "openai", "google", "bedrock"
    mcp_enabled: true    # append --mcp flag
```

#### Providers

| Provider | Environment Variable | K8s Secret Key |
|---|---|---|
| `anthropic` (default) | `ANTHROPIC_API_KEY` | `anthropic-api-key` |
| `openai` | `OPENAI_API_KEY` | `openai-api-key` |
| `google` | `GOOGLE_API_KEY` | `google-api-key` |
| `bedrock` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | `aws-access-key-id`, `aws-secret-access-key` |

#### Repository context

Cline reads project-specific instructions from `.clinerules` in the repository root.

#### MCP support

When `mcp_enabled: true` is set in the engine configuration, the `--mcp` flag is appended to the Cline command, which turns on Model Context Protocol integration for tool use.

#### Guard rails (prompt-embedded)

Cline does not support a hooks system. Guard rails are appended directly to the task prompt.

## Engine selection

The controller selects engines in this order:

1. Per-ticket override: if the ticket metadata or labels specify an engine (e.g., a `engine:codex` label), that engine is used.
2. Default engine: the `engines.default` configuration value.
3. Fallback: `claude-code` if no default is configured.

## Comparison matrix

| Criterion | Claude Code | Codex | Aider | OpenCode | Cline |
|---|---|---|---|---|---|
| Guard rail enforcement | Hook-based (deterministic) | Prompt-based (advisory) | Prompt-based (advisory) | Prompt-based (advisory) | Prompt-based (advisory) |
| Sub-agent support | Yes (official) | No | No | No | No |
| Multi-model support | Anthropic only | OpenAI only | Anthropic + OpenAI | Anthropic + OpenAI + Google | Anthropic + OpenAI + Google + Bedrock |
| Agentic turns limit | Configurable (`max_turns`) | N/A | N/A | N/A | N/A |
| Repository context file | `CLAUDE.md` | `AGENTS.md` | `.aider/conventions.md` | `AGENTS.md` | `.clinerules` |
| Heartbeat telemetry | Via PostToolUse hook | Not built-in | Not built-in | Not built-in | Not built-in |
| MCP server support | Yes | No | No | No | Yes (via `--mcp` flag) |
| Pre-built image | ✅ | ✅ | ✅ | ✅ | ❌ Community template |

Recommendation: use Claude Code as the default engine, because it enforces guard rails through hooks and has built-in heartbeat telemetry. Use Codex or Aider when you need OpenAI models or have specific tool preferences. OpenCode supports Google models. Cline is a community template without a published image.

## Writing a custom engine

To add a new engine, create a new package under `pkg/engine/` and implement the `ExecutionEngine` interface:

```go
package myengine

import (
    "fmt"
    "strings"

    "github.com/unitaryai/osmia/pkg/engine"
)

const (
    engineName       = "my-engine"
    interfaceVersion = 1
    defaultImage     = "ghcr.io/myorg/my-engine:latest"
)

// MyEngine implements engine.ExecutionEngine.
type MyEngine struct{}

func New() *MyEngine { return &MyEngine{} }

func (e *MyEngine) Name() string           { return engineName }
func (e *MyEngine) InterfaceVersion() int   { return interfaceVersion }

func (e *MyEngine) BuildExecutionSpec(task engine.Task, config engine.EngineConfig) (*engine.ExecutionSpec, error) {
    if task.ID == "" {
        return nil, fmt.Errorf("task ID must not be empty")
    }

    prompt, err := e.BuildPrompt(task)
    if err != nil {
        return nil, fmt.Errorf("building prompt: %w", err)
    }

    image := config.Image
    if image == "" {
        image = defaultImage
    }

    return &engine.ExecutionSpec{
        Image:   image,
        Command: []string{"my-cli", "--prompt", prompt},
        Env: map[string]string{
            "OSMIA_TASK_ID":   task.ID,
            "OSMIA_TICKET_ID": task.TicketID,
            "OSMIA_REPO_URL":  task.RepoURL,
        },
        SecretEnv: map[string]string{
            "MY_API_KEY": "my-api-key-secret",
        },
        Volumes: []engine.VolumeMount{
            {Name: "workspace", MountPath: "/workspace"},
            {Name: "config", MountPath: "/config", ReadOnly: true},
        },
        ActiveDeadlineSeconds: config.TimeoutSeconds,
    }, nil
}

func (e *MyEngine) BuildPrompt(task engine.Task) (string, error) {
    if task.Title == "" {
        return "", fmt.Errorf("task title must not be empty")
    }

    var b strings.Builder
    b.WriteString("# Task: ")
    b.WriteString(task.Title)
    b.WriteString("\n\n")

    if task.Description != "" {
        b.WriteString("## Description\n\n")
        b.WriteString(task.Description)
        b.WriteString("\n\n")
    }

    b.WriteString("## Instructions\n\n")
    b.WriteString("Complete the task above. Work in /workspace.\n")
    b.WriteString("Write result.json to /workspace/result.json when finished.\n")

    return b.String(), nil
}
```

Register the engine with the controller at startup:

```go
reconciler := controller.NewReconciler(cfg, logger,
    controller.WithEngine(claudecode.New()),
    controller.WithEngine(codex.New()),
    controller.WithEngine(aider.New()),
    controller.WithEngine(myengine.New()),
)
```

### Testing

Write table-driven tests for both `BuildExecutionSpec` and `BuildPrompt`:

```go
func TestMyEngine_BuildExecutionSpec(t *testing.T) {
    tests := []struct {
        name    string
        task    engine.Task
        config  engine.EngineConfig
        wantErr bool
    }{
        {
            name: "valid task produces spec",
            task: engine.Task{ID: "1", Title: "Fix bug"},
            config: engine.EngineConfig{TimeoutSeconds: 3600},
        },
        {
            name:    "empty ID returns error",
            task:    engine.Task{Title: "Fix bug"},
            wantErr: true,
        },
    }

    e := New()
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            spec, err := e.BuildExecutionSpec(tt.task, tt.config)
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.NotEmpty(t, spec.Image)
            assert.NotEmpty(t, spec.Command)
        })
    }
}
```

## Protobuf definition

The complete protobuf service is defined in `proto/engine.proto`. Note that engines are currently built-in only (Go), but the protobuf definition exists for future support of third-party engines via gRPC.

```protobuf
service ExecutionEngine {
    rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
    rpc BuildExecutionSpec(BuildExecutionSpecRequest) returns (ExecutionSpec);
    rpc BuildPrompt(BuildPromptRequest) returns (BuildPromptResponse);
}
```
