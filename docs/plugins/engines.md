# Execution Engines

## Overview

Execution engines wrap AI coding tools (Claude Code, OpenAI Codex, Aider) and produce engine-agnostic `ExecutionSpec` structs that the JobBuilder translates into Kubernetes Jobs. This decoupling enables testing without a cluster and opens the door to non-K8s runtimes.

## Interface

```go
type ExecutionEngine interface {
    BuildExecutionSpec(task Task, config EngineConfig) (*ExecutionSpec, error)
    BuildPrompt(task Task) (string, error)
    Name() string
    InterfaceVersion() int
}
```

## Built-in Engines

### Claude Code

The primary engine. Runs Claude Code CLI in headless mode.

```yaml
engines:
  default: claude-code
  claude-code:
    image: "ghcr.io/robodev-inc/engine-claude-code:latest"
    auth:
      method: api_key
      api_key_secret: "robodev-anthropic-key"
    agent_teams:
      enabled: false
      mode: "in-process"
      max_teammates: 3
```

**Features:**
- Full hooks support (PreToolUse, PostToolUse, Stop)
- Guard rail enforcement via hooks
- Heartbeat telemetry for progress watchdog
- MCP server support for human interaction
- Agent teams (experimental)

**Invocation:**
```bash
claude -p "prompt" --output-format json --max-turns 50 --dangerously-skip-permissions
```

### OpenAI Codex

Runs OpenAI's Codex CLI in full-auto mode.

```yaml
engines:
  codex:
    image: "ghcr.io/robodev-inc/engine-codex:latest"
    auth:
      method: api_key
      api_key_secret: "robodev-openai-key"
```

**Key differences from Claude Code:**
- No hooks system — guard rails are embedded in the prompt
- Uses `AGENTS.md` instead of `CLAUDE.md` for repository context
- No MCP support

**Invocation:**
```bash
codex --quiet --approval-mode full-auto --full-stdout "prompt"
```

### Aider

Runs the Aider coding assistant.

```yaml
engines:
  aider:
    image: "ghcr.io/robodev-inc/engine-aider:latest"
    auth:
      method: api_key
      api_key_secret: "robodev-anthropic-key"
```

**Key differences:**
- Uses `.aider/conventions.md` for repository context
- Guard rails via prompt only
- Supports both Anthropic and OpenAI API keys

**Invocation:**
```bash
aider --yes --no-git --message "prompt"
```

## Engine Selection

The controller selects engines in this order:
1. Per-ticket engine override (via ticket metadata or label)
2. Default engine from config (`engines.default`)
3. Falls back to `claude-code`

## TaskResult

All engines write a structured result to `/workspace/result.json`:

```json
{
  "success": true,
  "merge_request_url": "https://github.com/org/repo/pull/42",
  "branch_name": "robodev/fix-login-bug",
  "summary": "Fixed the login validation bug and added tests",
  "exit_code": 0
}
```

Exit codes: `0` = success, `1` = agent failure, `2` = guard rail blocked.
