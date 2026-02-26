# RoboDev

RoboDev is a Kubernetes-native AI coding agent harness that orchestrates autonomous developer agents (e.g. Claude Code, Codex) to perform maintenance and development tasks on codebases at scale.

The goal of this OSS project is to provide an enterprise-grade, security-first harness built on Kubernetes primitives (isolation, RBAC, auditability, observability, scaling).

## Status

This repository is in early scaffolding phase.

What exists today:

- A Go module with a minimal controller entrypoint (`cmd/robodev/main.go`)
- Core package scaffolding:
  - `internal/taskrun`: a TaskRun state machine with validated transitions + tests
  - `internal/config`: YAML config loader + tests
  - `internal/metrics`: Prometheus metrics scaffolding
  - `pkg/engine`: execution engine interfaces (Claude Code / Codex adapters will implement these)
- CI workflow (`.github/workflows/ci.yaml`) for lint/test/build
- Helm chart skeleton (`charts/robodev/`) for deploying the controller
- Documentation stubs under `docs/`
- Community files: `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`

## Quick start (dev)

Prereqs:
- Go 1.23+

Run locally:

```bash
make test
make build
./bin/robodev --help
```

## Configuration

The controller expects a YAML config file (see `internal/config`) typically mounted as `robodev-config.yaml`.

## Roadmap (near term)

- Implement TaskRun persistence in Kubernetes (CRD or ConfigMap-backed)
- Implement execution engines:
  - Claude Code engine (headless K8s Job runner)
  - Codex engine
- Implement first-party plugins:
  - GitHub Issues ticketing
  - Slack notifications + human approval
  - Kubernetes Secrets backend
  - GitHub SCM backend
- Add guardrails:
  - controller-level constraints
  - Claude Code hooks + `guardrails.md` prompt injection

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

Apache 2.0. See [LICENCE](LICENCE).
