# Architecture

> **Note:** This document will be expanded as the implementation progresses. For the full technical plan, see `oss-plan.md`.

## Overview

RoboDev is a Kubernetes-native controller that orchestrates AI coding agents to perform development tasks autonomously. It follows the operator pattern: a reconciliation loop polls for work, creates Kubernetes Jobs, and manages their lifecycle.

## Core Components

### Controller (`internal/controller/`)

The controller-runtime reconciler that drives the main loop:

1. Poll the ticketing backend for ready tickets
2. Create a TaskRun for each ticket (with idempotency)
3. Build an ExecutionSpec via the chosen engine
4. Translate the ExecutionSpec into a Kubernetes Job
5. Monitor job progress via heartbeats
6. Collect results and update the ticket

### TaskRun State Machine (`internal/taskrun/`)

Each task progresses through a well-defined state machine:

```
Queued → Running → Succeeded
                 → Failed → Retrying → Running
                 → NeedsHuman → Running (after human responds)
                 → TimedOut
```

### Execution Engine (`pkg/engine/`)

The `ExecutionEngine` interface abstracts AI coding tools. Each engine produces an `ExecutionSpec` — a runtime-agnostic description of what container to run, with what environment and resources.

### Plugin System (`pkg/plugin/`)

All external integrations are abstracted behind plugin interfaces:

- **TicketingBackend** — polls for work (GitHub Issues, Jira, etc.)
- **NotificationChannel** — fire-and-forget notifications (Slack, Teams, etc.)
- **HumanApprovalBackend** — event-driven human-in-the-loop
- **SecretsBackend** — secret retrieval (K8s Secrets, Vault, etc.)
- **SCMBackend** — source control operations (GitHub, GitLab, etc.)
- **ReviewBackend** — automated code review (CodeRabbit, etc.)

Built-in plugins are compiled into the controller binary. Third-party plugins communicate over gRPC via hashicorp/go-plugin.

## Security Model

See [security.md](security.md) for the full threat model and mitigations.
