# Guard rails overview

Guard rails are safety boundaries that stop AI agents doing things they should not. Osmia has six independent layers. If one fails, the others can still catch the problem.

## The six layers

```mermaid
graph LR
    A["📥 Incoming Ticket"] --> B["1. Controller<br/>Validation"]
    B --> C["2. Engine<br/>Hooks"]
    C --> D["3. Repository<br/>Rules"]
    D --> E["4. Task<br/>Profiles"]
    E --> F["5. Quality<br/>Gate"]
    F --> G["6. Progress<br/>Watchdog"]
    G --> H["✅ Safe Output"]

    style B fill:#7c4dff,color:#fff
    style C fill:#7c4dff,color:#fff
    style D fill:#7c4dff,color:#fff
    style E fill:#7c4dff,color:#fff
    style F fill:#7c4dff,color:#fff
    style G fill:#7c4dff,color:#fff
```

### Layer 1: Controller validation

When: before any agent starts working.
What: the controller checks the ticket against configurable rules before creating a job.

| Check | What it prevents |
|---|---|
| Allowed repositories | Agent working on repos it shouldn't touch |
| Allowed task types | Agent performing task categories that aren't approved |
| Concurrent job limit | Too many agents running at once (resource exhaustion) |
| Blocked file patterns | Agent modifying sensitive files (injected into engine config) |

A ticket that fails validation is rejected immediately and marked as failed, with the reason.

### Layer 2: Engine hooks

When: during agent execution, before every tool call.
What: scripts that intercept and block dangerous operations in real time.

!!! info "Only Claude Code supports hook-based guard rails"
    Other engines (Codex, Aider, OpenCode, Cline) use prompt-based rules, which are advisory and not enforced.

Blocked operations include:

- Destructive commands: `rm -rf /`, `sudo`, `eval`, `chmod 777`
- Remote code execution: `curl | bash`, `wget | sh`
- Destructive SCM operations: `git push --force` to main/master
- Writes to sensitive files: `.env`, `*.pem`, `*.key`

If a hook blocks an operation, the agent sees the rejection and adjusts its approach.

### Layer 3: Repository rules

When: read by the agent from the target repository during execution.
What: a `guardrails.md` or `CLAUDE.md` file in the target repository that tells the agent what it must and must not do.

!!! note "Advisory, not enforced by the controller"
    The controller does not currently inject these files into the agent prompt. Claude Code reads `CLAUDE.md` automatically when it starts work; other engines do not. Prompt-builder injection for all engines is on the roadmap.

Example `guardrails.md` (for Claude Code via `CLAUDE.md`):

```markdown
## Never Do
- Never modify CI/CD pipeline configuration files
- Never change database migration files
- Never alter authentication or authorisation logic

## Always Do
- Always run the full test suite before creating a PR
- Always add tests for new functionality
```

These rules are repo-specific and maintained by the repository owners.

### Layer 4: Task profiles

When: applied when the controller selects the engine configuration.
What: different task types get different cost and duration budgets.

```yaml
guardrails:
  task_profiles:
    documentation:
      allowed_file_patterns: ["*.md", "docs/**"]
      max_cost_per_job: 10.0
    bug_fix:
      blocked_file_patterns: ["**/migrations/**", "**/auth/**"]
      max_cost_per_job: 50.0
```

!!! note "Config schema only: file pattern enforcement not yet wired"
    `max_cost_per_job` and `max_job_duration_minutes` overrides per task type are read from config. However, `allowed_file_patterns` and `blocked_file_patterns` at the profile level are not yet enforced at runtime. Only the global `blocked_file_patterns` is injected into engine hooks. Per-profile enforcement is on the roadmap.

### Layer 5: Quality gate

When: after the agent finishes, before the PR is created.
What: a separate review step that checks the agent's output.

The quality gate can:

- Scan for accidentally committed secrets
- Check for OWASP security patterns
- Verify the agent followed guard rail instructions
- Check dependency changes for known CVEs

On failure, it can retry with feedback, block the PR, or notify a human.

### Layer 6: Progress watchdog

When: continuously, while the agent is running.
What: monitors agent behaviour and terminates unproductive agents.

```mermaid
graph TD
    A["Watchdog Tick<br/>(every 60s)"] --> B{"Loop<br/>detected?"}
    B -->|Yes, 2+ ticks| C["Terminate with feedback"]
    B -->|No| D{"Thrashing?<br/>High tokens, no progress"}
    D -->|Yes, 2+ ticks| E["Warn → Terminate"]
    D -->|No| F{"Stalled?<br/>No activity"}
    F -->|Yes, 2+ ticks| G["Terminate"]
    F -->|No| H{"Cost velocity<br/>too high?"}
    H -->|Yes| I["Warn"]
    H -->|No| J["✅ Healthy"]
```

| Detection rule | What it catches | Default threshold |
|---|---|---|
| Loop detection | Agent calling the same tool with the same arguments repeatedly | 10 consecutive identical calls |
| Thrashing | High token consumption without file changes | 80,000 tokens without progress |
| Stall | No tool calls despite heartbeat advancing | 300 seconds idle |
| Cost velocity | Spending rate too high | $15 USD per 10 minutes |
| Telemetry failure | Heartbeat stopped advancing | 3 stale ticks |
| Unanswered human | `NeedsHuman` state with no response | 30 minutes |

!!! tip "Avoiding false positives"
    All detection rules require the anomaly to persist for `min_consecutive_ticks` (default: 2) before action is taken. New TaskRuns also get a `research_grace_period` (default: 5 minutes) during which thrashing detection is relaxed, since agents often consume many tokens during initial code analysis.

### Layer 7: Real-time agent coaching (PRM)

When: continuously, while the agent is running (alongside the watchdog).
What: evaluates agent productivity at each tool call and intervenes with guidance before problems escalate.

!!! info "Only applies to Claude Code"
    The PRM operates on the NDJSON event stream which is only available from the Claude Code engine. Other engines are not scored by the PRM.

The Process Reward Model (PRM) reads the NDJSON event stream from Claude Code. It scores each evaluation window of tool calls on a 1-10 scale, tracks the score trajectory over time, and decides on interventions:

- Score ≥ 7: the agent is productive, so continue.
- Score 4-6 with a negative trend: write a hint file (`/workspace/.osmia-hint.md`) with targeted guidance, such as "Your approach appears to be oscillating. Try committing to a single strategy."
- Score ≤ 3 with sustained decline: escalate to the watchdog for termination.

Unlike the watchdog, which detects anomalies in raw telemetry, the PRM evaluates *productivity patterns*: whether the agent is making progress toward the goal, not only whether it is alive.

## Coming soon

> **Status:** Scaffolding complete, integration pending. See `docs/roadmap.md` Phase I.

### Layer 8: Adaptive watchdog calibration

When: applied automatically once enough historical data accumulates.
What: replaces static watchdog thresholds with adaptive per-(repo, engine, task_type) thresholds.

A bug fix in a small Python script has very different "normal" telemetry from a TypeScript monorepo refactor. Static thresholds either miss real anomalies in simple tasks or give false positives on complex ones. Adaptive calibration learns what "normal" looks like for each context from historical TaskRun data.

Cold-start safety: a minimum of 10 completed TaskRuns for a given context is required before calibrated thresholds override static defaults.

## How the layers work together

For example, a ticket asks the agent to "update the deployment manifest":

1. Controller validation checks that the repo is in `allowed_repos`. ✅ Pass.
2. Engine hooks would block writes to `.github/workflows/**` if configured. ✅ Blocked if needed.
3. The repository `guardrails.md` says "Never modify CI/CD pipeline configuration", so the agent should avoid it (advisory).
4. The task profile for the `infrastructure` type defines a lower cost budget. ✅ Enforced. (File pattern enforcement is on the roadmap.)
5. The quality gate scans the output and flags it if deployment files were changed. ✅ Caught.
6. The watchdog stops the agent looping endlessly while it looks for the right file. ✅ Monitored.

If the agent ignores layer 3 (prompt-based), layers 2 and 5 still catch the problem.

## Further reading

- [Guard Rails (Detailed)](../guardrails.md): configuration reference for all guard rail options
- [Security Model](../security.md): threat model and defence-in-depth architecture
- [Configuration Reference](../getting-started/configuration.md): how to configure guard rails in your values
