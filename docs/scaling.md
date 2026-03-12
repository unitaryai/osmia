# Scaling

## Overview

Osmia is designed for horizontal scaling on Kubernetes. The controller runs as a single replica, whilst agent jobs scale independently via Karpenter node provisioning and configurable concurrency limits.

## Controller High Availability

!!! note "Planned feature"
    Leader election (multiple controller replicas with automatic failover) is not yet implemented. The controller currently runs as a single replica. HA support via controller-runtime Kubernetes Lease objects is on the roadmap.

For now, run a single controller replica. Agent jobs are independent Kubernetes Jobs and are unaffected by a controller restart — in-flight jobs complete normally.

## Karpenter Integration

Osmia agent jobs are ephemeral, CPU-bound workloads. Karpenter provisions dedicated nodes on demand when jobs are created and scales down when they complete.

### Recommended NodePool

See `examples/karpenter/nodepool.yaml` for a production-ready configuration.

Key design decisions:
- **Spot instances** for cost savings (jobs are stateless and retryable)
- **Taints** to isolate agent workloads from production services
- **`WhenEmpty` consolidation** to avoid disrupting running jobs
- **Instance type diversity** across c6i/c7i/m6i for spot availability
- **CPU limits** to cap maximum infrastructure spend

### Tolerations

Agent jobs include tolerations for the `osmia.io/agent` taint by default:

```yaml
tolerations:
  - key: osmia.io/agent
    value: "true"
    effect: NoSchedule
```

## Concurrency Limits

The controller enforces a maximum number of concurrent jobs to prevent runaway scaling:

```yaml
guardrails:
  max_concurrent_jobs: 10
```

When the limit is reached, new tickets are queued until running jobs complete.

## Cost Controls

Multiple layers of cost control are available:

1. **Per-job cost ceiling** — `max_cost_per_job` terminates jobs exceeding the budget
2. **Job duration limit** — `max_job_duration_minutes` sets a hard timeout
3. **Progress watchdog** — detects and terminates unproductive agents early
4. **Karpenter CPU limits** — caps total compute across all Osmia nodes
5. **Cost velocity alerts** — warns when spending exceeds thresholds

## KEDA Scaling

For advanced scaling scenarios, KEDA can trigger scaling based on ticket queue depth:

```yaml
# See examples/keda/scaledobject.yaml
```

Note: The controller currently runs as a single replica (leader election is on the roadmap). KEDA is most useful for alerting or for scaling downstream resources.

## Multi-Tenancy

For shared clusters, Osmia defines a tenancy configuration schema. Namespace-per-tenant runtime isolation is on the roadmap; the config structure and RBAC recommendations are in place:

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

When namespace-per-tenant isolation is implemented, each tenant will receive:
- Dedicated namespace with its own RBAC, NetworkPolicies, and ResourceQuotas
- Separate Kubernetes Secrets (no cross-tenant secret access)
- Independent job limits and cost budgets
- Isolated Karpenter NodePool (optional, for compute isolation)

!!! note "Tenancy runtime isolation is on the roadmap"
    The `tenancy` config block is parsed and stored but has no runtime effect today. All jobs currently run in the controller's own namespace regardless of tenant configuration.

## Observability

### Prometheus Metrics

The controller exposes the following metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `osmia_taskruns_total` | Counter | Total task runs by state |
| `osmia_active_jobs` | Gauge | Currently running jobs |
| `osmia_taskrun_duration_seconds` | Histogram | Job execution duration |
| `osmia_plugin_errors_total` | Counter | Plugin errors by plugin |

### Grafana Dashboard

A Grafana dashboard JSON model is included in the Helm chart for visualising Osmia metrics.

### Structured Logging

All controller logs are structured JSON via Go's `slog`, suitable for log aggregation systems (Elasticsearch, Loki, CloudWatch Logs).
