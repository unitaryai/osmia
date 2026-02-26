# CLAUDE.md — RoboDev

## Project Overview

RoboDev is a Kubernetes-native AI coding agent harness that orchestrates autonomous developer agents (Claude Code, OpenAI Codex, Aider) to perform maintenance and development tasks on codebases at scale. It is Apache 2.0 licensed, enterprise-grade, and security-first.

The full technical plan is in `oss-plan.md`. The product requirements are in `oss-prd.md`. Refer to these when you need architectural context or implementation details.

## Language & Stack

- **Controller**: Go (>= 1.23) using controller-runtime, client-go, hashicorp/go-plugin
- **Plugin interfaces**: Protobuf/gRPC (source of truth in `proto/`)
- **Plugin SDKs**: Generated from protobufs — Python, Go, TypeScript
- **Build**: Makefile targets for build, test, lint, proto-gen, sdk-gen
- **Container images**: Multi-stage Docker builds, distroless base images
- **Deployment**: Helm chart in `charts/robodev/`

## Code Style & Conventions

- Run `gofumpt` on all Go files before committing (stricter superset of gofmt)
- Use `golangci-lint run` to check for issues — fix all warnings
- Use **British English** in all comments, documentation, error messages, and user-facing strings (e.g. "colour" not "color", "organisation" not "organization", "licence" not "license")
- Use Go's `slog` (standard library) for all logging — structured, JSON output
- Follow standard Go project layout: `cmd/`, `internal/`, `pkg/`
- `internal/` is for packages only used by the controller
- `pkg/` is for packages that external consumers (plugins, SDKs) may import
- Prefer table-driven tests
- All exported types, functions, and methods must have doc comments
- Error messages should be lowercase, no trailing punctuation (Go convention)

## Directory Structure

Follow the structure defined in section 8 of `oss-plan.md`. The key packages are:

```
cmd/robodev/           — Main entrypoint
internal/controller/   — controller-runtime reconciler
internal/jobbuilder/   — ExecutionSpec -> K8s Job translation
internal/taskrun/      — TaskRun state machine + idempotency
internal/watchdog/     — Progress watchdog loop
internal/config/       — Configuration loading
internal/metrics/      — Prometheus metrics
pkg/engine/            — ExecutionEngine interface + built-in engines
pkg/plugin/            — gRPC plugin host + all plugin interfaces
proto/                 — Protobuf definitions (source of truth for all interfaces)
```

## Testing

- Write unit tests for every package (`*_test.go` alongside the source)
- Use table-driven tests with subtests (`t.Run`)
- Use `testify/assert` and `testify/require` for assertions
- Integration tests go in `tests/integration/`
- E2E tests (requiring a kind cluster) go in `tests/e2e/`
- Run `go test ./...` to execute all unit tests
- Aim for meaningful coverage of core logic — state machines, builders, reconciliation loops

## Protobuf & gRPC

- All plugin interfaces are defined as protobuf services in `proto/`
- The Go interfaces in `pkg/plugin/*/` must match the protobuf definitions
- Use `buf` for linting and generating protobuf code
- Every service must include a `Handshake` RPC with `interface_version`

## Security Principles

- This is a security-first project. Every design decision should consider the threat model in section 10 of `oss-plan.md`
- Never log secrets or API keys
- Validate all external input (ticket descriptions, plugin responses, webhook payloads)
- Use context.Context for cancellation and timeouts on all I/O operations
- All container specs must include restrictive securityContext (runAsNonRoot, readOnlyRootFilesystem, drop ALL capabilities)
- Prefer workload identity (IRSA/WIF) patterns over static credentials where applicable

## Dependencies

- Use Go modules. Run `go mod tidy` after adding/removing imports
- Prefer standard library where possible
- Key dependencies: controller-runtime, client-go, hashicorp/go-plugin, grpc-go, prometheus/client_golang
- Do not add dependencies without good reason — this is an OSS project and every dependency is an attack surface

## Git Conventions

- Use conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`
- Keep commits focused — one logical change per commit
- Write descriptive commit messages explaining *why*, not just *what*

## What NOT to Do

- Do not modify `oss-plan.md` or `oss-prd.md` — these are reference documents
- Do not introduce Python into the controller — the controller is Go only
- Do not bypass the plugin interface abstraction — all external integrations go through the defined interfaces
- Do not hard-code configuration values — use `robodev-config.yaml` and environment variables
- Do not add Kubernetes CRD types until explicitly decided (see open question 7 in the plan)

## Changelog
- keep a CHANGELOG.md in the root dir and update it regularly. 

