# Scaling

## Overview

Osmia runs on Kubernetes. The controller is a single replica, while agent jobs scale independently through Karpenter node provisioning and a configurable concurrency limit.

## Controller high availability

!!! note "Planned feature"
    Leader election (multiple controller replicas with automatic failover) is not yet implemented. The controller currently runs as a single replica. HA support via controller-runtime Kubernetes Lease objects is on the roadmap.

For now, run a single controller replica. Agent jobs are independent Kubernetes Jobs and are unaffected by a controller restart. In-flight jobs complete normally.

## Karpenter integration

Osmia agent jobs are ephemeral, CPU-bound workloads. Karpenter provisions dedicated nodes on demand when jobs are created and scales down when they complete.

### Recommended NodePool

See `examples/karpenter/nodepool.yaml` for an example configuration.

The example makes these choices:

- On-Demand instances. Spot is cheaper, but a reclaimed node loses the agent's progress and every token it has spent, so the example NodePool in `examples/karpenter/nodepool.yaml` requires On-Demand.
- Taints, to keep agent workloads apart from production services.
- `WhenEmpty` consolidation, so running jobs are not disrupted.
- A mix of c6i, c7i and m6i instance types, to improve availability.
- CPU limits, to cap infrastructure spend.

### Tolerations

Agent jobs include tolerations for the `osmia.io/agent` taint by default:

```yaml
tolerations:
  - key: osmia.io/agent
    value: "true"
    effect: NoSchedule
```

## Concurrency limits

The controller caps the number of concurrent jobs so scaling cannot run away:

```yaml
guardrails:
  max_concurrent_jobs: 10
```

When the limit is reached, new tickets are queued until running jobs complete.

## Cost controls

Cost is limited in several places:

1. Per-job cost ceiling: `max_cost_per_job` terminates jobs that exceed the budget.
2. Job duration limit: `max_job_duration_minutes` sets a hard timeout.
3. Progress watchdog: detects and terminates unproductive agents early.
4. Karpenter CPU limits: cap total compute across all Osmia nodes.
5. Cost velocity alerts: warn when spending exceeds thresholds.

## KEDA scaling

KEDA can trigger scaling based on ticket queue depth:

```yaml
# See examples/keda/scaledobject.yaml
```

Note: The controller currently runs as a single replica (leader election is on the roadmap). KEDA is most useful for alerting or for scaling downstream resources.

## Multi-tenancy

For shared clusters, Osmia defines a tenancy configuration schema. Runtime isolation per tenant namespace is on the roadmap. The config structure and RBAC recommendations exist already:

```yaml
tenancy:
  mode: "namespace-per-tenant"
  tenants:
    - name: "team-alpha"
      namespace: "osmia-alpha"
      ticketing:
        backend: github
        config:
          repo: "alpha-org/repos"
```

When namespace-per-tenant isolation is implemented, each tenant will get:

- Dedicated namespace with its own RBAC, NetworkPolicies, and ResourceQuotas
- Separate Kubernetes Secrets (no cross-tenant secret access)
- Independent job limits and cost budgets
- Isolated Karpenter NodePool (optional, for compute isolation)

!!! note "Tenancy runtime isolation is on the roadmap"
    The `tenancy` config block is parsed and stored but has no runtime effect today. All jobs currently run in the controller's own namespace regardless of tenant configuration. See the [roadmap](roadmap.md#namespace-per-tenant-isolation) for planned work.

## Observability

### Prometheus metrics

The controller exposes the following metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `osmia_taskruns_total` | Counter | Total task runs by state |
| `osmia_active_jobs` | Gauge | Currently running jobs |
| `osmia_taskrun_duration_seconds` | Histogram | Job execution duration |
| `osmia_plugin_errors_total` | Counter | Plugin errors by plugin |

### Grafana dashboard

The Helm chart includes a Grafana dashboard JSON model for Osmia metrics.

### Structured logging

All controller logs are structured JSON via Go's `slog`, suitable for log aggregation systems (Elasticsearch, Loki, CloudWatch Logs).
