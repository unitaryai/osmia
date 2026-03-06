# Execution Engines

!!! tip "New to engines?"
    For a plain-language comparison and decision tree, see [Engines Explained](../concepts/engines.md). This page covers the detailed technical reference.

## Overview

Execution engines wrap AI coding tools (Claude Code, OpenAI Codex, Aider) and produce engine-agnostic `ExecutionSpec` structs that the JobBuilder translates into Kubernetes Jobs. This decoupling enables testing without a cluster, supports multiple AI tools from a single controller, and opens the door to non-K8s runtimes in future.

## Interface Summary

| Property | Value |
|---|---|
| Proto definition | `proto/engine.proto` |
| Go interface | `pkg/engine/engine.go` |
| Interface version | `1` |
| Role in lifecycle | Called after guard rail validation to produce the K8s Job spec |

## Go Interface

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
    ID          string            // Unique task identifier.
    TicketID    string            // Source ticket identifier.
    Title       string            // Short summary (used as prompt heading).
    Description string            // Full task description (main prompt content).
    RepoURL     string            // Repository the agent should work on.
    Labels      []string          // Labels from the source ticket.
    Metadata    map[string]string // Additional key-value pairs.
}
```

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

The output — everything needed to create a K8s Job:

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

## Built-in Engines

### Claude Code

The primary and recommended engine. Runs the [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) in headless mode with full hook-based guard rail support.

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
    claude_code:
      image: "ghcr.io/unitaryai/engine-claude-code:v2.1.0"
      max_turns: 50
      model: "claude-sonnet-4-6"
      timeout_seconds: 3600
      fallback_model: haiku              # used when primary model is overloaded
      no_session_persistence: true       # disable session state between turns
      append_system_prompt: "Always run tests before committing."
      tool_whitelist:                    # only these tools are available
        - Bash
        - Read
        - Write
        - Edit
      tool_blacklist:                    # these tools are blocked
        - WebSearch
      json_schema: '{"type":"object","properties":{"success":{"type":"boolean"},"summary":{"type":"string"}},"required":["success","summary"]}'
      resource_requests:
        cpu: "500m"
        memory: "512Mi"
      resource_limits:
        cpu: "2"
        memory: "2Gi"
      skills:                            # custom skills — see Skills section below
        - name: create-changelog
          inline: |
            # Create Changelog
            Generate a CHANGELOG.md entry for the changes made.
        - name: review-checklist
          path: /opt/robodev/skills/review-checklist.md
      agent_teams:                       # experimental — see Agent Teams section below
        enabled: false
        mode: in-process
        max_teammates: 3
```

| Field | Type | Default | Description |
|---|---|---|---|
| `image` | string | `ghcr.io/unitaryai/engine-claude-code:latest` | Container image override |
| `timeout_seconds` | int | `7200` | Active deadline for the K8s Job |
| `fallback_model` | string | — | Model to use when the primary is overloaded (e.g. `haiku`) |
| `no_session_persistence` | bool | `false` | Disable Claude Code session persistence between turns |
| `append_system_prompt` | string | — | Extra text appended to Claude Code's system prompt |
| `tool_whitelist` | []string | — | Only allow these Claude Code tools (via `--allowedTools`) |
| `tool_blacklist` | []string | — | Block these Claude Code tools (via `--disallowedTools`) |
| `json_schema` | string | built-in TaskResult schema | JSON schema for structured output (via `--json-schema`) |
| `skills` | []SkillConfig | — | Custom skill files loaded into the agent — see [Skills](#skills) |
| `agent_teams` | AgentTeamsConfig | disabled | Experimental agent teams — see [Agent Teams](#agent-teams-experimental) |

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

The `setup-claude.sh` wrapper runs before `claude` to initialise the writable home directory — writing `~/.claude/settings.json`, `/workspace/.mcp.json`, and any [skill files](#skills). It then `exec`s the real `claude` binary with the arguments above.

#### Guard Rails (Hooks)

Claude Code supports a [hooks system](https://docs.anthropic.com/en/docs/claude-code/hooks) that intercepts tool calls before execution. RoboDev generates a `hooks.json` configuration file and mounts it into the agent container at `/config/hooks.json`:

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

- **Destructive command detection** — blocks `rm -rf`, `DROP TABLE`, `git push --force`, `sudo`, and similar dangerous commands.
- **Blocked file patterns** — prevents reading or writing files matching patterns in `blocked_file_patterns` (e.g., `*.env`, `*.key`, `*.pem`).
- **Network restriction** — optionally blocks `curl`, `wget`, and other network tools from contacting external hosts.

If a hook script exits with a non-zero code, Claude Code blocks the tool call and reports the violation to the agent, which can then adjust its approach.

The `PostToolUse` hook writes heartbeat telemetry to `/workspace/heartbeat.json` after every tool invocation, enabling the progress watchdog to monitor agent activity.

#### Environment Variables

| Variable | Source | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | K8s Secret `robodev-anthropic-key` | API authentication |
| `ROBODEV_TASK_ID` | Controller | Unique task identifier |
| `ROBODEV_TICKET_ID` | Controller | Source ticket identifier |
| `ROBODEV_REPO_URL` | Ticket | Repository to work on |
| `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` | Engine | Always set to `1` |
| `CLAUDE_SKILL_INLINE_<NAME>` | Engine | Base64-encoded inline skill content (see [Skills](#skills)) |
| `CLAUDE_SKILL_PATH_<NAME>` | Engine | Path to a skill file on the image (see [Skills](#skills)) |
| `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS` | Engine | Set to `1` when agent teams are enabled |
| `CLAUDE_CODE_MAX_TEAMMATES` | Engine | Maximum teammate agents (when teams are enabled) |

#### Volume Mounts

| Mount | Path | Writable | Purpose |
|---|---|---|---|
| `workspace` | `/workspace` | Yes | Repository checkout and working directory |
| `config` | `/config` | No | Guard rail hooks and configuration |
| `home` | `/home/robodev` | Yes | Writable home directory (emptyDir) for `~/.claude/` config and skills |
| `tmp` | `/tmp` | Yes | Writable tmp (emptyDir) for Claude Code subprocess shell directories |

#### Skills

Skills are custom Markdown instruction files that the agent can invoke via `/skill-name` in its prompts. They are written to `~/.claude/skills/<name>.md` before the agent starts.

Each skill has a `name` (lowercase letters, digits, and hyphens only) and exactly one of:

- **`inline`** — the Markdown content directly in the config. The controller base64-encodes it and passes it as the `CLAUDE_SKILL_INLINE_<NAME>` environment variable.
- **`path`** — a path to a Markdown file on the container image (e.g. `/opt/robodev/skills/review-checklist.md`). The controller passes it as `CLAUDE_SKILL_PATH_<NAME>`.

At container startup, `setup-claude.sh` reads these environment variables, decodes/copies the files, and writes them to `~/.claude/skills/`. The `<NAME>` suffix is converted to lowercase with hyphens (e.g. `CLAUDE_SKILL_INLINE_CREATE_CHANGELOG` → `~/.claude/skills/create-changelog.md`).

**Example — inline skill:**

```yaml
engines:
  claude_code:
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

**Example — image-bundled skill:**

```yaml
engines:
  claude_code:
    skills:
      - name: security-review
        path: /opt/robodev/skills/security-review.md
```

To bundle skills into the container image, add them to your custom Dockerfile:

```dockerfile
FROM ghcr.io/unitaryai/engine-claude-code:latest
COPY skills/ /opt/robodev/skills/
```

#### Agent Teams (Experimental)

Agent teams allow splitting a task across multiple Claude Code sub-agents running in-process within a single K8s Job pod. This is useful for large tasks where parallel work (e.g. one agent writes code while another writes tests) can reduce total execution time.

!!! warning "Experimental feature"
    Agent teams use Claude Code's experimental `--agents` flag. The feature may change or be removed in future Claude Code releases.

**Configuration:**

```yaml
engines:
  claude_code:
    agent_teams:
      enabled: true
      mode: in-process           # only supported mode
      max_teammates: 3           # cap on parallel agents
      agents:                    # optional — overrides defaults
        coder:
          role: "Write code to implement the feature"
          model: opus
        reviewer:
          role: "Review code changes for correctness"
          model: haiku
        tester:
          role: "Write and run tests for the new feature"
          model: sonnet
```

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Enable agent teams mode |
| `mode` | string | `in-process` | Execution mode (only `in-process` is supported) |
| `max_teammates` | int | `3` | Maximum number of teammate agents |
| `agents` | map[string]AgentDef | — | Named agent definitions. When omitted, defaults are generated from task type |

Each `AgentDef` has:

| Field | Type | Description |
|---|---|---|
| `role` | string | Description of what the agent is responsible for |
| `model` | string | Optional model override (e.g. `opus`, `haiku`, `sonnet`) |
| `instructions` | string | Optional additional instructions for the agent |

**Default agents by task type:**

When no `agents` map is provided, RoboDev generates default teams based on the `task_type` metadata from the ticket:

| Task Type | Agents |
|---|---|
| `bug_fix` | `coder` (opus) + `reviewer` (haiku) |
| `feature` | `coder` (opus) + `reviewer` (haiku) + `tester` (sonnet) |
| Other | No agents — runs as a single agent |

When enabled, the engine sets `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` and `CLAUDE_CODE_MAX_TEAMMATES=<n>` in the container environment, and appends `--agents <JSON>` to the Claude CLI command with the agent definitions.

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
      timeout_seconds: 3600
      resource_requests:
        cpu: "500m"
        memory: "512Mi"
      resource_limits:
        cpu: "2"
        memory: "2Gi"
```

#### Command

```bash
codex \
  --quiet \
  --approval-mode full-auto \
  --full-stdout \
  "<prompt>"
```

#### Guard Rails (Prompt-Embedded)

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

#### Repository Context

Codex reads repository conventions from `AGENTS.md` (rather than `CLAUDE.md`). If an `AGENTS.md` file is present in the repository root, Codex will use it for coding conventions and project structure guidance.

#### Environment Variables

| Variable | Source | Description |
|---|---|---|
| `OPENAI_API_KEY` | K8s Secret `openai-api-key` | API authentication |
| `ROBODEV_TASK_ID` | Controller | Unique task identifier |
| `ROBODEV_TICKET_ID` | Controller | Source ticket identifier |
| `ROBODEV_REPO_URL` | Ticket | Repository to work on |

---

### Aider

Runs the [Aider CLI](https://aider.chat/) for AI-assisted coding. Aider supports multiple LLM providers — RoboDev can configure it to use either Anthropic or OpenAI models.

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
      timeout_seconds: 3600
      resource_requests:
        cpu: "500m"
        memory: "512Mi"
      resource_limits:
        cpu: "2"
        memory: "2Gi"
```

#### Command

```bash
aider \
  --yes \
  --no-git \
  --message "<prompt>"
```

The `--no-git` flag is used because RoboDev manages git operations via the SCM backend, not via Aider's built-in git support.

#### Model Provider

Aider supports both Anthropic and OpenAI models. Configure the provider in the engine configuration:

| Provider | Environment Variable | Secret Name |
|---|---|---|
| `anthropic` (default) | `ANTHROPIC_API_KEY` | `anthropic-api-key` |
| `openai` | `OPENAI_API_KEY` | `openai-api-key` |

#### Repository Context

Aider reads coding conventions from `.aider/conventions.md` (rather than `CLAUDE.md` or `AGENTS.md`). If an `.aider.conf.yml` file is present in the repository root, Aider will use it for additional configuration (model selection, editor settings, etc.).

#### Guard Rails (Prompt-Embedded)

Like Codex, Aider does not support a hooks system. Guard rails are appended directly to the task prompt, identical in content to the Codex guard rails above.

### OpenCode

OpenCode is a terminal-based AI coding agent. RoboDev runs it in headless mode inside Kubernetes Jobs.

**Package:** `pkg/engine/opencode/`

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

#### Repository Context

OpenCode reads coding conventions from `AGENTS.md` in the repository root.

#### Guard Rails (Prompt-Embedded)

OpenCode does not support a hooks system. Guard rails are appended directly to the task prompt.

### Cline

!!! warning "Community template — no pre-built image"
    Cline is a VS Code extension with no published headless CLI. The Go engine implementation (`pkg/engine/cline/`) and the Dockerfile (`docker/engine-cline/`) are provided as a community contribution template. **No pre-built container image is published for Cline.** Configuring `cline` as your engine will result in an image pull failure until a working headless integration is contributed. See [Contributing](../contributing.md) if you want to help.

Cline is an AI coding agent with optional MCP (Model Context Protocol) and AWS Bedrock support. When a headless CLI becomes available, RoboDev can run it inside Kubernetes Jobs using the implementation in `pkg/engine/cline/`.

**Package:** `pkg/engine/cline/`

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

#### Repository Context

Cline reads project-specific instructions from `.clinerules` in the repository root.

#### MCP Support

When `mcp_enabled: true` is set in the engine configuration, the `--mcp` flag is appended to the Cline command, enabling Model Context Protocol integration for tool use.

#### Guard Rails (Prompt-Embedded)

Cline does not support a hooks system. Guard rails are appended directly to the task prompt.

## Engine Selection

The controller selects engines in this order:

1. **Per-ticket override** — if the ticket metadata or labels specify an engine (e.g., a `engine:codex` label), that engine is used.
2. **Default engine** — the `engines.default` configuration value.
3. **Fallback** — `claude-code` if no default is configured.

## Comparison Matrix

| Criterion | Claude Code | Codex | Aider | OpenCode | Cline |
|---|---|---|---|---|---|
| Guard rail enforcement | Hook-based (deterministic) | Prompt-based (advisory) | Prompt-based (advisory) | Prompt-based (advisory) | Prompt-based (advisory) |
| Agent teams support | Yes (experimental) | No | No | No | No |
| Multi-model support | Anthropic only | OpenAI only | Anthropic + OpenAI | Anthropic + OpenAI + Google | Anthropic + OpenAI + Google + Bedrock |
| Agentic turns limit | Configurable (`max_turns`) | N/A | N/A | N/A | N/A |
| Repository context file | `CLAUDE.md` | `AGENTS.md` | `.aider/conventions.md` | `AGENTS.md` | `.clinerules` |
| Heartbeat telemetry | Via PostToolUse hook | Not built-in | Not built-in | Not built-in | Not built-in |
| MCP server support | Yes | No | No | No | Yes (via `--mcp` flag) |
| Pre-built image | ✅ | ✅ | ✅ | ✅ | ❌ Community template |

**Recommendation:** Use Claude Code as the default engine for its superior guard rail enforcement via hooks and built-in heartbeat telemetry. Use Codex or Aider when you need OpenAI models or have specific tool preferences. OpenCode supports Google models. Cline is a community template without a published image.

## Writing a Custom Engine

To add a new engine, create a new package under `pkg/engine/` and implement the `ExecutionEngine` interface:

```go
package myengine

import (
    "fmt"
    "strings"

    "github.com/unitaryai/robodev/pkg/engine"
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
            "ROBODEV_TASK_ID":   task.ID,
            "ROBODEV_TICKET_ID": task.TicketID,
            "ROBODEV_REPO_URL":  task.RepoURL,
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

## Protobuf Definition

The complete protobuf service is defined in `proto/engine.proto`. Note that engines are currently built-in only (Go), but the protobuf definition exists for future support of third-party engines via gRPC.

```protobuf
service ExecutionEngine {
    rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
    rpc BuildExecutionSpec(BuildExecutionSpecRequest) returns (ExecutionSpec);
    rpc BuildPrompt(BuildPromptRequest) returns (BuildPromptResponse);
}
```
