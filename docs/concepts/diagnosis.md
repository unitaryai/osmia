# Causal Failure Diagnosis

When a task fails, Osmia does not just retry it blindly. The causal diagnosis subsystem classifies *why* the failure happened and generates targeted corrective instructions that are injected into the retry prompt — so the next attempt starts with a more informed approach.

## Failure Modes

The analyser classifies every failed task into one of eight modes:

| Mode | What it means |
|---|---|
| `infra_failure` | OOM kill, timeout, network unreachable, disk full |
| `permission_blocked` | File system or network permission denied |
| `dependency_missing` | Missing module, unresolved import, package not found |
| `test_misunderstanding` | Agent modified tests incorrectly or misread test expectations |
| `wrong_approach` | Agent edited unrelated files or produced changes unrelated to the task |
| `scope_creep` | Agent changed far more files than the task warranted |
| `model_confusion` | High oscillation in tool calls — repeated undo/redo patterns |
| `unknown` | No recognised pattern matched |

Each diagnosis includes a confidence score (0–1) and the specific evidence strings that triggered the classification.

## How It Works

The analyser processes three sources of signal from the failed run:

1. **Event stream** — tool call inputs and outputs from the agent pod NDJSON stream
2. **Watchdog reason** — any reason string recorded by the progress watchdog at termination
3. **Task result summary** — the structured result emitted by the engine at exit

Pattern matching runs in priority order. Infrastructure failures are checked first because retrying an OOM-killed job without changing resources is futile. Permission and dependency failures follow. Watchdog-specific patterns (thrashing, looping) map to `model_confusion`.

For `infra_failure`, the retry is suppressed entirely — the controller records the diagnosis and parks the task rather than burning tokens on a doomed retry.

## Prescriptions

For every other failure mode, the `RetryBuilder` prepends a corrective instruction to the retry prompt. Examples:

- **`dependency_missing`** — *"A previous attempt failed due to a missing dependency. Before writing any code, verify that all required packages are installed and importable."*
- **`test_misunderstanding`** — *"A previous attempt modified tests incorrectly. Read the failing test carefully before making any changes. Do not alter test assertions unless the task explicitly requires it."*
- **`scope_creep`** — *"A previous attempt changed too many files. Focus only on the files directly relevant to the task."*

The prescription is appended after the original task prompt, so the agent receives both the full task context and the corrective guidance.

## Engine Switching

When the diagnosis suggests the failure is engine-specific (for example, `model_confusion` on an engine with known oscillation issues), the `RetryBuilder` can optionally switch to a different engine for the retry attempt. This is configured per failure mode in the diagnosis config.

## Configuration

```yaml
diagnosis:
  enabled: true
  switch_engine_on:
    - model_confusion   # retry with fallback engine when confused
```

## Relationship to the Watchdog

The watchdog detects problems *during* a run and intervenes early. Causal diagnosis runs *after* a run has failed and informs the retry. They are complementary: the watchdog tries to salvage a run in progress; diagnosis tries to make the next attempt succeed.

## Further Reading

- [Guard Rails Overview](guardrails-overview.md) — how diagnosis fits into the broader safety architecture
- [Configuration Reference](../getting-started/configuration.md) — diagnosis config options
