# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### Phase 1: Core Framework & Abstractions
- Protobuf definitions for all 7 plugin interfaces: ticketing, notifications, approval, secrets, review, SCM, engine (plus shared common.proto)
- buf.yaml and buf.gen.yaml for protobuf linting and Go/gRPC code generation
- Makefile targets: `proto-lint` and `proto-gen` for protobuf workflow
- All plugin Go interfaces: ticketing.Backend, notifications.Channel, approval.Backend, secrets.Backend, review.Backend, scm.Backend
- gRPC plugin host (pkg/plugin/host.go) using hashicorp/go-plugin with version handshake, crash detection, and restart with exponential backoff
- JobBuilder (internal/jobbuilder) translating ExecutionSpec to K8s batch/v1.Job with security contexts, tolerations, and labels
- Progress watchdog (internal/watchdog) with anomaly detection rules: loop detection, thrashing, stall, cost velocity, telemetry failure
- Cost tracker (internal/costtracker) with per-engine token rates and budget checking
- Prompt builder (internal/promptbuilder) with guard rails injection and task profile support
- Controller reconciliation loop with ticket polling, idempotency, guard rails validation, job lifecycle management, and retry logic
- Health endpoints (/healthz, /readyz) and Prometheus metrics serving in main.go
- Graceful shutdown with signal handling (SIGTERM, SIGINT)

#### Phase 2: Claude Code Engine + GitHub + Slack
- Claude Code execution engine (pkg/engine/claudecode) with BuildExecutionSpec, BuildPrompt, and hooks generation
- Claude Code hooks system for guard rails: PreToolUse blockers, PostToolUse heartbeat, Stop handler
- GitHub Issues ticketing backend (pkg/plugin/ticketing/github) with REST API integration
- GitHub SCM backend (pkg/plugin/scm/github) for branch and pull request management
- Kubernetes Secrets backend (pkg/plugin/secrets/k8s) with secret retrieval and env var building
- Slack notification channel (pkg/plugin/notifications/slack) with Block Kit formatted messages
- Slack approval backend (pkg/plugin/approval/slack) with interactive messages and callback handling

#### Phase 2D: Dockerfiles & Helm Chart
- Multi-stage controller Dockerfile (golang:1.23-alpine builder to distroless runtime)
- Claude Code engine Dockerfile with Node.js, Claude Code CLI, git, gh, python3, guard rail hooks
- Claude Code entrypoint.sh with repo cloning, CLAUDE.md injection, and semantic exit codes
- Guard rail hooks: block-dangerous-commands.sh (PreToolUse/Bash) and block-sensitive-files.sh (PreToolUse/Write|Edit)
- OpenAI Codex engine Dockerfile with Node.js, Codex CLI, and full-auto entrypoint
- Helm chart: plugin init container support via values.yaml plugins array and shared emptyDir volume
- Helm chart: metrics Service, leader election flag, and post-install NOTES.txt
- Makefile targets: docker-build-controller, docker-build-engine-claude-code, docker-build-engine-codex, docker-build, helm-lint
- .dockerignore for clean container builds

#### Phase 3: Codex + Aider + GitLab + CodeRabbit
- OpenAI Codex execution engine (pkg/engine/codex) with prompt-based guard rails
- Aider execution engine (pkg/engine/aider) with conventions.md support
- Engine registry (pkg/engine/registry.go) for engine selection and management
- GitLab SCM backend (pkg/plugin/scm/gitlab) with merge request support
- CodeRabbit review backend (pkg/plugin/review/coderabbit) for quality gate integration

#### Phase 4: Agent Teams, Scaling & Multi-Tenancy
- Claude Code agent teams configuration (pkg/engine/claudecode/teams.go) with in-process mode
- Multi-tenancy support via namespace-per-tenant model in config
- Quality gate configuration with security checks
- Progress watchdog configuration in main config
- Extended engine configs with auth settings (API key, Bedrock, Vertex, setup-token)
- Karpenter NodePool example (examples/karpenter/) with spot instances and taints
- KEDA ScaledObject example (examples/keda/) for Prometheus-based scaling
- Example configurations: github-slack, gitlab-teams, enterprise (with full feature set)
- Example third-party plugins: Jira (Python) and Microsoft Teams (TypeScript)
- Grafana dashboard JSON (charts/robodev/dashboards/)
- Scaling documentation (docs/scaling.md)

#### Phase 5: Community & Documentation
- Comprehensive README with architecture diagram, quick start, and full feature overview
- Guard rails documentation (docs/guardrails.md) covering all six layers
- Plugin interface documentation: ticketing, notifications, secrets, engines
- Expanded CI pipeline: protobuf linting, Helm linting, Docker build verification
- Release workflow with cosign image signing and syft SBOM generation
- GitHub issue templates (bug report, feature request, plugin request)
- Pull request template with checklist
- Comprehensive architecture documentation with system diagrams and component details
- Full security documentation with threat model, defence in depth, and container hardening
- Getting started guide with step-by-step quick start, configuration reference, and troubleshooting
- Plugin development guide covering Go built-in and gRPC third-party plugin authoring

#### Infrastructure
- Go module and core skeleton: controller entrypoint, config loading, TaskRun state machine, Prometheus metrics, ExecutionEngine interface
- CI pipeline with lint, test, build, proto-lint, helm-lint, docker-build jobs
- Helm chart with deployment, RBAC, ConfigMap, ServiceMonitor, Service templates
- Community files: CONTRIBUTING.md, CODE_OF_CONDUCT.md, SECURITY.md
- Comprehensive table-driven tests for all packages (20 test suites)
