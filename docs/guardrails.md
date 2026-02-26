# Guard Rails

## Overview

RoboDev provides layered safety boundaries — guard rails — to ensure autonomous AI agents operate within enterprise-approved limits. Guard rails are applied at multiple levels for defence in depth.

## 1. Controller-Level Guards

Applied before a job is created. Configured in `robodev-config.yaml`:

```yaml
guardrails:
  max_cost_per_job: 50.00
  max_concurrent_jobs: 5
  max_job_duration_minutes: 120
  allowed_repos:
    - "org/frontend-*"
    - "org/backend-*"
  blocked_file_patterns:
    - "*.env"
    - "**/secrets/**"
    - "**/credentials/**"
  require_human_approval_before_mr: false
  allowed_task_types:
    - "dependency_upgrade"
    - "test_fix"
    - "bug_fix"
    - "documentation"
```

### What Each Guard Does

| Guard | Effect |
|-------|--------|
| `max_cost_per_job` | Terminates jobs exceeding the USD budget |
| `max_concurrent_jobs` | Queues new tickets when limit is reached |
| `max_job_duration_minutes` | Sets `activeDeadlineSeconds` on K8s Jobs |
| `allowed_repos` | Rejects tickets for repositories not matching glob patterns |
| `blocked_file_patterns` | Injected into engine hooks to prevent modification |
| `require_human_approval_before_mr` | Pauses before PR creation for human sign-off |
| `allowed_task_types` | Rejects tickets with disallowed task types |

## 2. Engine-Level Guards (Claude Code Hooks)

Applied inside the execution container via Claude Code's hooks system. RoboDev generates a `settings.json` file mounted into the container:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/robodev/hooks/block-dangerous-commands.sh"
          }
        ]
      },
      {
        "matcher": "Write|Edit",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/robodev/hooks/block-sensitive-files.sh"
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/opt/robodev/hooks/heartbeat.sh"
          }
        ]
      }
    ]
  }
}
```

### Blocked Commands

The `block-dangerous-commands.sh` hook blocks:
- `rm -rf /` and similar destructive commands
- `curl | bash`, `wget | bash` (remote code execution)
- `eval` with untrusted input
- `sudo` (privilege escalation)
- `chmod 777` (insecure permissions)
- `git push --force` to main/master

### Blocked Files

The `block-sensitive-files.sh` hook blocks writes to:
- `.env*` files
- `**/credentials/**`
- `**/secrets/**`
- `*.pem`, `*.key` (private keys)

Custom patterns can be added via the `BLOCKED_FILE_PATTERNS` environment variable.

## 3. Custom Guard Rails via Markdown

Users can provide a `guardrails.md` file that is appended to every prompt sent to the execution engine:

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

This file is mounted from a ConfigMap and injected by the prompt builder.

## 4. Per-Task-Type Permission Profiles

Different task types carry different risk profiles:

```yaml
guardrails:
  task_profiles:
    dependency_upgrade:
      allowed_file_patterns:
        - "pyproject.toml"
        - "requirements*.txt"
      max_cost_per_job: 30.00
      max_job_duration_minutes: 60

    bug_fix:
      blocked_file_patterns:
        - "**/migrations/**"
        - "**/auth/**"
      max_cost_per_job: 50.00

    documentation:
      allowed_file_patterns:
        - "*.md"
        - "docs/**"
      blocked_commands:
        - "git push"
      max_cost_per_job: 10.00
```

The controller selects the profile based on ticket labels or the `ticket_type` field from the ticketing backend.

## 5. Quality Gate

An optional post-completion review that runs as a separate K8s Job:

```yaml
quality_gate:
  enabled: true
  mode: "post-completion"
  engine: claude-code
  max_cost_per_review: 5.00
  security_checks:
    scan_for_secrets: true
    check_owasp_patterns: true
    verify_guardrail_compliance: true
    check_dependency_cves: true
  on_failure: "retry_with_feedback"
```

The quality gate is read-only — it cannot modify the repository.

## 6. Progress Watchdog

Detects agents that are stalled, looping, or unproductive during execution:

```yaml
progress_watchdog:
  enabled: true
  check_interval_seconds: 60
  min_consecutive_ticks: 2
  research_grace_period_minutes: 5
  loop_detection_threshold: 10
  thrashing_token_threshold: 80000
  stall_idle_seconds: 300
  cost_velocity_max_per_10_min: 15.00
  unanswered_human_timeout_minutes: 30
```

### Detection Rules

| Rule | Detects | Action |
|------|---------|--------|
| Loop detection | Same tool call repeated N times | Terminate with feedback |
| Thrashing | High token use, no file changes | Warn, then terminate |
| Stall | No tool calls for N seconds | Terminate |
| Cost velocity | Spending > $X per 10 minutes | Warn |
| Telemetry failure | Heartbeat stopped advancing | Warn |
| Unanswered human | NeedsHuman with no response | Terminate and notify |

All terminate actions require the anomaly to persist for at least `min_consecutive_ticks` checks to prevent false positives.
