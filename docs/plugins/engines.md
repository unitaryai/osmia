# Execution Engines

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
| Default image | `ghcr.io/robodev-inc/engine-claude-code:latest` |
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
      image: "ghcr.io/robodev-inc/engine-claude-code:v2.1.0"
      max_turns: 50
      model: "claude-sonnet-4-6"
      timeout_seconds: 3600
      resource_requests:
        cpu: "500m"
        memory: "512Mi"
      resource_limits:
        cpu: "2"
        memory: "2Gi"
```

#### Command

The engine generates a `claude` CLI invocation in print mode with JSON output:

```bash
claude \
  --print \
  --output-format json \
  --max-turns 50 \
  --model claude-sonnet-4-6 \
  "<prompt>"
```

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
| `ANTHROPIC_API_KEY` | K8s Secret `anthropic-api-key` | API authentication |
| `ROBODEV_TASK_ID` | Controller | Unique task identifier |
| `ROBODEV_TICKET_ID` | Controller | Source ticket identifier |
| `ROBODEV_REPO_URL` | Ticket | Repository to work on |
| `CLAUDE_CODE_MAX_TURNS` | Engine config | Maximum agentic turns |

#### Volume Mounts

| Mount | Path | Writable | Purpose |
|---|---|---|---|
| `workspace` | `/workspace` | Yes | Repository checkout and working directory |
| `config` | `/config` | No | Guard rail hooks and configuration |

---

### OpenAI Codex

Runs the [OpenAI Codex CLI](https://github.com/openai/codex) in fully autonomous mode.

| Property | Value |
|---|---|
| Engine name | `codex` |
| Package | `pkg/engine/codex/` |
| Default image | `ghcr.io/robodev-inc/engine-codex:latest` |
| Default timeout | 7200 seconds (2 hours) |
| API key secret | `openai-api-key` |
| Guard rails | Prompt-embedded rules |

#### Configuration

```yaml
config:
  engines:
    default: codex
    codex:
      image: "ghcr.io/robodev-inc/engine-codex:v1.0.0"
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

> **Important:** Prompt-embedded guard rails are advisory — the AI model may not always respect them. For stricter enforcement, use the Claude Code engine with hook-based guards, or rely on the quality gate for post-completion validation.

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
| Default image | `ghcr.io/robodev-inc/engine-aider:latest` |
| Default timeout | 7200 seconds (2 hours) |
| API key secret | `anthropic-api-key` (default) or `openai-api-key` |
| Guard rails | Prompt-embedded rules |

#### Configuration

```yaml
config:
  engines:
    default: aider
    aider:
      image: "ghcr.io/robodev-inc/engine-aider:v1.0.0"
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

## Engine Selection

The controller selects engines in this order:

1. **Per-ticket override** — if the ticket metadata or labels specify an engine (e.g., a `engine:codex` label), that engine is used.
2. **Default engine** — the `engines.default` configuration value.
3. **Fallback** — `claude-code` if no default is configured.

## Comparison Matrix

| Criterion | Claude Code | Codex | Aider |
|---|---|---|---|
| Guard rail enforcement | Hook-based (deterministic) | Prompt-based (advisory) | Prompt-based (advisory) |
| Agent teams support | Yes (experimental) | No | No |
| Multi-model support | Anthropic only | OpenAI only | Anthropic + OpenAI |
| Agentic turns limit | Configurable (`max_turns`) | N/A | N/A |
| Repository context file | `CLAUDE.md` | `AGENTS.md` | `.aider/conventions.md` |
| Heartbeat telemetry | Via PostToolUse hook | Not built-in | Not built-in |
| MCP server support | Yes | No | No |

**Recommendation:** Use Claude Code as the default engine for its superior guard rail enforcement via hooks and built-in heartbeat telemetry. Use Codex or Aider when you need OpenAI models or have specific tool preferences.

## Writing a Custom Engine

To add a new engine, create a new package under `pkg/engine/` and implement the `ExecutionEngine` interface:

```go
package myengine

import (
    "fmt"
    "strings"

    "github.com/robodev-inc/robodev/pkg/engine"
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
