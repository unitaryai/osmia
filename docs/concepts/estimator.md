# Predictive Cost Estimation

Before Osmia launches a task, the cost estimator predicts how much it will cost and how long it will take. This gives operators visibility into expensive tasks before they run — and optionally blocks tasks that exceed a configured threshold until a human approves them.

## How It Works

Estimation runs in two stages:

### 1. Complexity Scoring

The task is scored across four dimensions, each normalised to [0, 1]:

| Dimension | What it measures |
|---|---|
| `description_complexity` | Length and vocabulary of the ticket description |
| `task_type_complexity` | Task category — bug fix, refactor, feature, migration, etc. |
| `repo_size` | Number of files in the repository |
| `label_complexity` | Labels attached to the ticket |

A weighted average produces an `overall` complexity score. The weights are configurable.

### 2. k-NN Prediction

The overall score and the selected engine are used to query a SQLite store of historical task outcomes. The k=5 nearest neighbours (by complexity score and engine) are retrieved, and P25/P75 percentile ranges are computed for cost and duration:

```
Predicted cost:     $2.10 – $4.80   (P25–P75)
Predicted duration: 8 – 22 minutes  (P25–P75)
Confidence:         0.8              (based on sample count)
```

Confidence increases with the number of matching historical outcomes, up to 1.0 at k=5 neighbours. With fewer than 2 neighbours, the predictor falls back to engine-specific cold-start defaults from configuration, with a confidence of 0.1.

Every task outcome — actual cost, actual duration, complexity score, engine used — is fed back into the store after completion, so predictions improve continuously as the system accumulates history.

## Auto-Rejection Threshold

When `max_predicted_cost_per_job` is configured, tasks whose predicted cost exceeds the threshold are automatically rejected before a job is created. The controller calls `MarkFailed` on the ticket, logs a warning, and emits a Prometheus metric. The ticket is not held for human approval — it is failed immediately.

```yaml
estimator:
  enabled: true
  max_predicted_cost_per_job: 10.00   # USD — auto-reject above this
```

This is distinct from `max_cost_per_job` in the guard rails, which is a hard runtime ceiling enforced by the watchdog. The estimator threshold fires *before* the job starts; the guard rail fires *during* execution.

## Cold-Start Defaults

For engines with no historical data, the predictor uses configurable per-engine defaults scaled by complexity:

```yaml
estimator:
  default_cost_per_engine:
    claude-code:
      low: 1.00
      high: 8.00
    codex:
      low: 0.50
      high: 5.00
  default_duration_per_engine:
    claude-code:
      low_minutes: 5
      high_minutes: 45
```

## Prometheus Metrics

| Metric | Description |
|---|---|
| `osmia_estimator_predictions_total` | Total predictions issued |
| `osmia_estimator_predicted_cost` | Histogram of midpoint predicted costs |
| `osmia_estimator_auto_rejections_total` | Tasks auto-rejected for exceeding the cost threshold |

## Further Reading

- [Configuration Reference](../getting-started/configuration.md) — full estimator config options
- [Guard Rails Overview](guardrails-overview.md) — how the cost ceiling relates to the estimator gate
