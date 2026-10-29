# Causal failure diagnosis

When a task fails, Osmia does not retry it blindly. The causal diagnosis subsystem classifies *why* the failure happened and writes corrective instructions into the retry prompt, so the next attempt starts with more information.

## Failure modes

The analyser classifies every failed task into one of eight modes:

| Mode | What it means |
|---|---|
| `infra_failure` | OOM kill, timeout, network unreachable, disk full |
| `permission_blocked` | File system or network permission denied |
| `dependency_missing` | Missing module, unresolved import, package not found |
| `test_misunderstanding` | Agent modified tests incorrectly or misread test expectations |
| `wrong_approach` | Agent edited unrelated files or produced changes unrelated to the task |
| `scope_creep` | Agent changed far more files than the task warranted |
| `model_confusion` | High oscillation in tool calls (repeated undo/redo patterns) |
| `unknown` | No recognised pattern matched |

Each diagnosis includes a confidence score (0–1) and the specific evidence strings that triggered the classification.

## How it works

The analyser processes three sources of signal from the failed run:

1. The event stream: tool call inputs and outputs from the agent pod NDJSON stream.
2. The watchdog reason: any reason string the progress watchdog recorded at termination.
3. The task result summary: the structured result the engine emits at exit.

Pattern matching runs in priority order. Infrastructure failures are checked first because retrying an OOM-killed job without changing resources is futile. Permission and dependency failures follow. Watchdog-specific patterns (thrashing, looping) map to `model_confusion`.

For `infra_failure`, the retry is suppressed entirely. The controller records the diagnosis and parks the task rather than spending tokens on a retry that cannot succeed.

## Prescriptions

For every other failure mode, the `RetryBuilder` adds a corrective instruction to the retry prompt. Examples:

- `dependency_missing`: *"A previous attempt failed due to a missing dependency. Before writing any code, verify that all required packages are installed and importable."*
- `test_misunderstanding`: *"A previous attempt modified tests incorrectly. Read the failing test carefully before making any changes. Do not alter test assertions unless the task explicitly requires it."*
- `scope_creep`: *"A previous attempt changed too many files. Focus only on the files directly relevant to the task."*

The prescription is appended after the original task prompt, so the agent receives both the full task context and the corrective guidance.

## Engine switching

When the diagnosis suggests the failure is engine-specific (for example, `model_confusion` on an engine with known oscillation issues), the `RetryBuilder` can switch to a different engine for the retry attempt. Engine switching is off by default and is turned on for all failure modes with `enable_engine_switch`.

## Configuration

```yaml
diagnosis:
  enabled: true
  enable_engine_switch: true   # allow a retry on the fallback engine
```

## Relationship to the watchdog

The watchdog detects problems *during* a run and intervenes early. Causal diagnosis runs *after* a run has failed and informs the retry. The watchdog tries to salvage a run in progress; diagnosis tries to make the next attempt succeed.

## Further reading

- [Guard Rails Overview](guardrails-overview.md): how diagnosis fits into the broader safety architecture
- [Configuration Reference](../getting-started/configuration.md): diagnosis config options
