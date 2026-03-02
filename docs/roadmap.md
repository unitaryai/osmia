# RoboDev Feature Roadmap

This document tracks the strategic feature roadmap for RoboDev. It complements `docs/improvements-plan.md` (which covers the initial 13 items) with the next wave of features to position RoboDev at the leading edge of agentic harnesses.

Tick items off as they are implemented and merged.

---

## Status Legend

- [ ] Not started
- [x] Complete
- **In Progress** — marked in the item description when actively being worked on

---

## Improvements Plan (Original 13 Items)

> Tracked in detail in `docs/improvements-plan.md`. Summary status here.

- [x] **1. Webhook Receiver & Event-Driven Ingestion** — GitHub, GitLab, Slack, Shortcut, generic handlers
- [x] **2. Agent Sandbox Integration (gVisor / Warm Pools)** — kernel-level isolation, warm pool CRD
- [x] **3. OpenCode Execution Engine** — BYOM terminal-native agent
- [x] **4. Cline CLI Execution Engine** — headless CI/CD mode, MCP support
- [x] **5. Shortcut.com Ticketing Backend** — REST API v3 integration
- [x] **6. Telegram Notification Channel** — Bot API notifications
- [x] **7. Linear Ticketing Backend** — GraphQL API integration
- [x] **8. Discord Notification Channel** — webhook-based notifications
- [x] **9. HashiCorp Vault Secrets Backend** — K8s auth, KV v2
- [x] **10. Task-Scoped Secret Resolution** — multi-backend, alias system, audit trail
- [x] **11. NetworkPolicy & Security Hardening** — agent/controller NetworkPolicy, PDB
- [ ] **12. Plugin SDKs (Python, Go, TypeScript)** — generated from protobuf definitions
- [x] **13. Local Development Mode (Docker Compose)** — Docker execution backend

**Score: 12 / 13 complete**

---

## Strategic Roadmap

### Phase A — Foundation

#### 1. Enhanced Claude Code Engine: Structured Output, Tool Control, Model Fallback

**Priority:** Critical
**Scope:** Medium (4-6 files)
**Dependencies:** None

Extend the Claude Code engine with production-ready CLI flags for structured output, model fallback, tool whitelisting, and guard rail injection.

- [x] Add `--output-format stream-json` / `--json-schema` for type-safe structured `TaskResult` output
- [x] Add `--fallback-model` support (e.g. `haiku`) for automatic failover
- [x] Add `--no-session-persistence` for stateless container execution
- [x] Add `--append-system-prompt` for guard rail injection (separates guard rails from task prompt)
- [x] Add `--tools` / `--allowedTools` / `--disallowedTools` driven by task profile config
- [x] Add functional options: `WithFallbackModel`, `WithToolWhitelist`, `WithJSONSchema`
- [x] Extend `EngineConfig` with `FallbackModel`, `ToolWhitelist`, `JSONSchema` fields
- [x] Extend `ClaudeCodeConfig` YAML fields (`fallback_model`, `tool_whitelist`, `json_schema`)
- [x] Extend prompt builder task profiles to drive tool whitelist per task type
- [x] Extend table-driven tests in `pkg/engine/claudecode/engine_test.go`

---

#### 3. Engine Fallback Chains and Auto-Selection

**Priority:** High
**Scope:** Medium (5-7 files)
**Dependencies:** None (all 5 engines already exist)

If Claude Code fails, retry with Cline. If Cline rate-limits, try Aider. Transforms RoboDev from a single-engine orchestrator into a resilient execution platform.

- [x] Add `FallbackEngines []string` to `EnginesConfig`
- [x] Create `internal/controller/engine_selector.go` with `EngineSelector` interface
- [x] Implement default selection: ticket label override → `[default] + fallback_engines`
- [x] Modify `ProcessTicket` to store engine list on TaskRun
- [x] Modify job failure handler to retry with next engine before exhausting retries
- [x] Add `EngineAttempts []string` and `CurrentEngine string` to TaskRun
- [x] Write `tests/integration/engine_fallback_test.go`

---

#### 6. TDD-Driven Agent Workflow Mode

**Priority:** Medium
**Scope:** Small (2-3 files)
**Dependencies:** Item 1 (structured output)

Structure the agent's workflow: write failing test → implement → verify tests pass. Produces verifiably correct output.

- [x] Add `WorkflowMode` field to task profiles in prompt builder
- [x] Implement `tdd` workflow: inject structured test-first instructions
- [x] Implement `review-first` workflow mode
- [x] Add `Workflow string` to `TaskProfileConfig` (`""` | `"tdd"` | `"review-first"`)
- [x] Verify `tests_passed`, `tests_failed`, `tests_added` flow from JSON schema to watchdog

---

### Phase B — Streaming

#### 2. Real-Time Agent Streaming via stream-json

**Priority:** Critical
**Scope:** Large (8-10 files)
**Dependencies:** Item 1

Replace heartbeat polling with real-time NDJSON telemetry from Claude Code's `stream-json` output format. No other harness streams agent progress back to the control plane.

- [x] Create `internal/agentstream/events.go` — event types (`ToolCallEvent`, `ContentDeltaEvent`, `CostEvent`, `ResultEvent`)
- [x] Create `internal/agentstream/reader.go` — K8s pod log streaming, NDJSON parsing
- [x] Create `internal/agentstream/forwarder.go` — forward events to watchdog and notification channels
- [x] Update Claude Code engine to use `--output-format stream-json --verbose` when streaming enabled
- [x] Add `StreamingSource` input to watchdog alongside existing heartbeat source
- [x] Start stream reader goroutine per active Claude Code job in controller
- [x] Non-streaming engines (Codex, Aider, OpenCode, Cline) fall back to heartbeat mechanism
- [x] Add optional live progress forwarding to notification channels (`notifications.live_updates: true`)
- [x] Write unit tests for NDJSON parser and event types
- [x] Write integration tests for stream reader with mock pod logs

---

### Phase C — Isolation

#### 4. Agent Sandbox Integration (gVisor / Warm Pools)

**Priority:** High
**Scope:** Large (10+ files)
**Dependencies:** Cluster needs agent-sandbox controller installed

Native gVisor integration via `kubernetes-sigs/agent-sandbox` for kernel-level isolation and warm pools for sub-second cold starts.

- [x] Add `ExecutionConfig` with `Backend string` (`"job"` | `"sandbox"` | `"local"`) to config
- [x] Create `internal/sandboxbuilder/builder.go` — emits `Sandbox` CRs instead of `batch/v1.Job`
- [x] Implement `SandboxClaim` abstraction against alpha API changes
- [x] Add warm pool config per engine (different images need separate pools)
- [x] RuntimeClass defaults to gVisor, Kata as opt-in
- [x] Update controller to select builder based on `config.Execution.Backend`
- [x] Add `templates/runtimeclass-gvisor.yaml` to Helm chart (gated by `sandbox.enabled`)
- [x] Add `templates/sandboxwarmpool.yaml` to Helm chart (gated by `sandbox.warmPool.enabled`)
- [x] Add `sandbox` section to `values.yaml`
- [x] Add environment variable stripping to each engine entrypoint (read API keys into memory, delete from `os.environ`)
- [x] Write integration tests for sandbox builder (CRD generation only)

---

### Phase D — Governance

#### 7. Governance: Approval Workflows and Audit Trail

**Priority:** Medium
**Scope:** Medium (5-7 files)
**Dependencies:** Webhook server (already complete)

Wire the existing approval interface, add persistent TaskRun storage, and build approval gates for enterprise governance.

- [x] Wire approval backend in controller; add approval gate checks before job creation (`require_approval_before_start`)
- [x] Add approval gate check before marking complete (`require_approval_before_merge`)
- [x] Handle Slack interactive message callbacks to resolve pending approvals
- [x] Create `internal/taskrun/store.go` — `TaskRunStore` interface (`Save`, `Get`, `List`, `ListByTicketID`)
- [x] Implement in-memory store (default)
- [ ] Implement SQLite store (local mode)
- [ ] Implement PostgreSQL store (production)
- [x] Add `ApprovalGates []string` and `ApprovalCostThresholdUSD` to guard rails config
- [x] Add `TaskRunStore` config section (`backend`, `sqlite.path`, `postgres.*`)
- [ ] Extend secret resolver audit logging to write to TaskRunStore

---

### Phase E — Access

#### 8. Local Development Mode (Docker Compose)

**Priority:** Medium
**Scope:** Medium (5-7 files)
**Dependencies:** None

A `docker compose up` experience that dramatically lowers the barrier to adoption.

- [x] Create `internal/jobbuilder/docker.go` — `DockerBuilder` implementing `JobBuilder` using Docker API
- [x] Support `execution.backend: local` in config to trigger DockerBuilder
- [x] Select builder based on `execution.backend` in `cmd/robodev/main.go`
- [x] Create `docker-compose.yaml` — controller + webhook server
- [x] Extend noop ticketing backend with file-watcher mode (reads tasks from YAML file)
- [x] Add `compose-up` / `compose-down` Makefile targets
- [x] Write quickstart guide for Docker Compose mode (`docs/getting-started/docker-compose.md`)

---

#### 5. Multi-Agent Coordination Layer (Phase 1 — In-Process Teams)

**Priority:** High
**Scope:** Large (10+ files)
**Dependencies:** Items 1 and 4

Orchestrate cross-engine teams where Claude Code plans and Aider executes. Phase 1 covers in-process Claude Code teams only.

- [x] Support `--agents` flag in Claude Code engine when `AgentTeamsConfig.Enabled` is true
- [x] Populate `AgentTeamsConfig` with `Agents map[string]AgentDef` and `MaxTeammates int`
- [x] Generate agent definitions from task type in prompt builder (e.g. `coder` + `reviewer` for bug-fix)
- [x] Write integration tests for team-enabled engine spec generation

**Phase 2 (future — not tracked here):**
- Multi-pod teams via `internal/teamcoordinator/`
- Multi-job decomposition for compound tasks
- `ParentTaskRunID` for sub-task tracking
- Agent Relay SDK as communication sidecar

---

### Phase F — Ecosystem

#### 9. Plugin SDKs (Python, Go, TypeScript)

**Priority:** Medium
**Scope:** Large (separate repositories)
**Dependencies:** Protobuf definitions (already complete in `proto/`)

Generated SDKs so third-party plugin authors don't need to implement raw gRPC.

- [ ] Configure `buf.gen.yaml` for multi-language stub generation
- [ ] **Python SDK** (`unitaryai/robodev-plugin-sdk-python`)
  - [ ] Generate Python gRPC stubs
  - [ ] Build base classes with `scaffold`/`serve`/`test` CLI commands
  - [ ] Include example plugins (noop ticketing, file-based ticketing, webhook notification)
- [ ] **Go SDK** (`unitaryai/robodev-plugin-sdk-go`)
  - [ ] Generate Go gRPC stubs (separate module)
  - [ ] Build thin wrapper with hashicorp/go-plugin boilerplate
  - [ ] Include example plugins
- [ ] **TypeScript SDK** (`unitaryai/robodev-plugin-sdk-ts`)
  - [ ] Generate TypeScript gRPC stubs
  - [ ] Build base classes wrapping grpc-js
  - [ ] Include example plugins
- [ ] Publish SDK documentation to `docs/plugins/`

---

### Phase G — Observability

#### 10. Agent Dashboard (Web UI)

**Priority:** High
**Scope:** Large (new service)
**Dependencies:** Items 2 (streaming) and 7 (audit trail/TaskRun store)

A web dashboard for real-time agent observability and control. Shows live agent status, task run history, streaming progress, cost tracking, and provides manual controls (approve, cancel, retry).

**Approach options:**
- **Grafana-based** — Use existing Prometheus metrics + Loki logs + custom Grafana panels. Lowest effort, leverages existing metrics infrastructure. Limited interactivity (read-only, no approve/cancel actions).
- **Custom lightweight UI** — Go backend serving a React/Next.js frontend. Full control over UX, interactive approval buttons, live streaming via SSE/WebSocket. More effort but purpose-built for the use case.
- **Hybrid** — Grafana for metrics/dashboards + a thin custom control plane UI for interactive actions (approvals, cancellation, manual task submission). Best of both worlds.

**Minimum viable features:**
- [ ] Real-time TaskRun status view (queued, running, succeeded, failed, needs-human)
- [ ] Live streaming progress per agent (tool calls, token usage, cost) from agentstream events
- [ ] Task run history with filtering by engine, ticket, status, date range
- [ ] Cost tracking dashboard (per-task, per-engine, daily/weekly aggregates)
- [ ] Manual controls: approve pending tasks, cancel running tasks, retry failed tasks
- [ ] Engine health overview (which engines are available, fallback chain status)

**If custom UI:**
- [ ] Create `cmd/robodev-dashboard/` — separate binary or embed in controller
- [ ] Add `/api/v1/` REST endpoints: `GET /taskruns`, `GET /taskruns/:id`, `POST /taskruns/:id/approve`, `POST /taskruns/:id/cancel`, `GET /taskruns/:id/stream` (SSE)
- [ ] Frontend: React + Tailwind or similar lightweight stack
- [ ] WebSocket/SSE endpoint for live streaming events from agentstream

**If Grafana-based:**
- [ ] Create Grafana dashboard JSON provisioning in `charts/robodev/dashboards/`
- [ ] Add Loki log aggregation for structured slog output
- [ ] Custom Grafana panels for TaskRun state machine visualisation
- [ ] Alert rules for cost velocity, stalled agents, failed tasks

---

### Phase H — Documentation

#### 11. Documentation Site

**Priority:** High
**Scope:** Medium (new site, existing content)
**Dependencies:** None

A polished, searchable documentation site that makes RoboDev look production-grade. First impressions matter — a good docs site is the difference between "interesting project" and "I'm deploying this on Monday."

**Approach options:**
- **Docusaurus** — React-based, MDX support, versioning, search, dark mode out of the box. Used by most major OSS projects. Easy to deploy on Vercel/Netlify/GitHub Pages.
- **Astro Starlight** — Newer, faster, built for docs. Excellent DX, automatic sidebar from file structure, i18n support, lighter than Docusaurus.
- **MkDocs Material** — Python-based, gorgeous Material Design theme, built-in search, mermaid diagrams. Simpler than Docusaurus, very popular in the Go/K8s ecosystem.

**Site structure:**
- [x] Landing page with hero, feature highlights, and quick start (`docs/index.md`)
- [x] Getting Started guide (K8s deploy, Docker Compose local mode, first task) (`docs/getting-started/`)
- [x] Architecture overview with diagrams (controller, engines, plugins, state machine) (`docs/architecture.md`)
- [x] Configuration reference (full YAML schema with examples) (`docs/getting-started/configuration.md`)
- [x] Engine guides (Claude Code, Codex, Aider, OpenCode, Cline) with comparison matrix (`docs/plugins/engines.md`)
- [x] Plugin development guide (ticketing, notifications, secrets, approval, review, SCM) (`docs/plugins/writing-a-plugin.md`)
- [x] Security model documentation (threat model, gVisor, NetworkPolicy, guard rails) (`docs/concepts/guardrails-overview.md`)
- [ ] API reference (webhook endpoints, protobuf service definitions)
- [x] Deployment guides (Helm chart reference, production hardening, multi-tenancy) (`docs/getting-started/kubernetes.md`)
- [ ] Changelog and migration guides
- [x] Search functionality (MkDocs Material built-in search)
- [x] Dark mode support (MkDocs Material palette toggle)

**Infrastructure:**
- [x] Choose site framework (Docusaurus / Astro Starlight / MkDocs Material) — **MkDocs Material** selected
- [x] Set up `docs/` directory with `mkdocs.yml` configuration
- [ ] CI/CD pipeline for automatic deployment on merge to main
- [ ] Custom domain setup (docs.robodev.dev or similar)
- [ ] Add `make docs-serve` / `make docs-build` targets

---

---

## Bleeding-Edge Agentic Engineering Features

Seven new subsystems that move RoboDev from a standard K8s operator into an intelligent orchestration platform. The core packages and unit tests are complete (scaffolding phase). The next work is **full controller integration** — wiring these packages into the live reconciliation loop, prompt builder, and main entrypoint.

### Current Status: PRM and Memory Integrated, Five More Pending

**PRM**, **Memory**, and the **LLM abstraction** are now fully wired into the controller and functional when enabled in configuration. The remaining five features (Diagnosis, Routing, Estimator, Tournament, Adaptive Watchdog) have complete packages with types, core logic, unit tests, and integration tests, but are not yet wired into the controller.

---

### 12. Controller-Level Process Reward Model (PRM) — Real-Time Agent Coaching

**Status:** ✅ Integrated into controller
**Package:** `internal/prm/`
**Priority:** Critical (most novel feature)

Evaluates agent behaviour at each tool call using the NDJSON stream. Scores agent productivity, tracks trajectory patterns, and decides interventions (soft nudge via hint file, or watchdog escalation).

- [x] `Scorer` — rule-based step scoring from tool call patterns (repetition penalty, productive pattern bonuses, diversity tracking)
- [x] `Trajectory` — pattern detection: sustained decline, plateau, oscillation, recovery
- [x] `InterventionDecider` — threshold-based decision logic (continue/nudge/escalate)
- [x] `Evaluator` — orchestrates scorer + trajectory + decider
- [x] `PRMConfig` in `internal/config/config.go`
- [x] Prometheus metrics (`prm_step_scores`, `prm_interventions_total`, `prm_trajectory_patterns_total`)
- [x] `WithEventProcessor` hook on `agentstream.Forwarder`
- [x] Unit tests (table-driven) for all components
- [x] Integration test (`tests/integration/prm_test.go`)

**Integration completed:**

- [x] Wire `prm.Evaluator` into `startStreamReader()` in controller — create evaluator per TaskRun, pass via `WithEventProcessor`
- [x] Implement hint recording — PRM interventions are logged + recorded on the TaskRun with Prometheus metrics
- [x] Add `WithPRMConfig` functional option to `Reconciler` struct
- [x] Initialise PRM in `cmd/robodev/main.go` when `config.PRM.Enabled` is true
- [x] Clean up PRM evaluators on job completion and failure
- [x] Unit tests and integration tests for controller wiring

**Future work:**

- [ ] Wire escalation into watchdog — when PRM escalates, signal the watchdog to terminate the Job with diagnostic feedback
- [ ] Pod-level hint delivery — write hints to the agent pod via projected ConfigMap volume
- [ ] V2: Replace rule-based scorer with LLM-based scoring via `internal/llm/` (prompt engineering + budget enforcement)
- [ ] Test PRM under concurrent TaskRuns (race condition coverage)
- [ ] E2E test: run a real agent, verify PRM scores and interventions fire

---

### 13. Cross-Task Episodic Memory with Temporal Knowledge Graph

**Status:** ✅ Integrated into controller
**Package:** `internal/memory/`
**Priority:** Critical (the compounding brain)

Persistent knowledge graph accumulating structured lessons from every TaskRun across all engines, repos, and tenants. Facts have temporal validity and confidence decay.

- [x] Node types: `Fact`, `Pattern`, `EngineProfile` with temporal metadata
- [x] Edge relations: `relates_to`, `contradicts`, `supersedes`
- [x] `Graph` — thread-safe core with temporal decay and pruning
- [x] `SQLiteStore` — pure Go SQLite via `modernc.org/sqlite`, auto-migration
- [x] `Extractor` — heuristic post-task knowledge extraction
- [x] `QueryForTask` — temporal-weighted retrieval with tenant isolation
- [x] `MemoryConfig` in `internal/config/config.go`
- [x] Prometheus metrics (`memory_nodes_total`, `memory_queries_total`, `memory_extractions_total`, `memory_confidence_distribution`)
- [x] Unit tests and integration test

**Integration completed:**

- [x] Wire extractor into `handleJobComplete` — extracts knowledge in background goroutine
- [x] Wire extractor into `handleJobFailed` — extracts failure patterns and engine capability facts
- [x] Wire query into `ProcessTicket` — queries memory before building execution spec
- [x] Add `MemoryContext` field to `engine.Task` — carries formatted prior knowledge
- [x] Extend prompt builder — all three `Build*` methods inject `MemoryContext` into prompt template
- [x] Add `WithMemory` functional option to `Reconciler` struct
- [x] Initialise SQLite store + graph + extractor + query engine in `cmd/robodev/main.go`
- [x] Implement periodic confidence decay goroutine
- [x] Implement periodic pruning of stale nodes below `PruneThreshold`
- [x] Graceful SQLite store close on shutdown
- [x] Unit tests and integration tests for controller wiring

**Future work:**

- [ ] Adversarial testing of cross-tenant isolation (verify tenant A cannot read tenant B's facts)
- [ ] Test SQLite store under concurrent writes, corruption recovery, migration idempotency
- [ ] Include provenance (source TaskRun ID) in injected facts
- [ ] V2: LLM-based extraction via `internal/llm/` replacing heuristic rules
- [ ] E2E test: run 10+ tasks, verify memory accumulates and injects relevant context

---

### 14. Self-Healing Retry with Causal Diagnosis

**Status:** Scaffolding complete · Integration pending
**Package:** `internal/diagnosis/`
**Priority:** High

When a task fails, runs a structured diagnosis pipeline on the stream transcript + watchdog reason + result. Classifies the failure mode, generates a targeted corrective instruction, and optionally switches engines.

- [x] Failure mode classifier: `WrongApproach`, `DependencyMissing`, `TestMisunderstanding`, `ScopeCreep`, `PermissionBlocked`, `ModelConfusion`, `InfraFailure`
- [x] Template-based prescription generator (safe `text/template`, prevents prompt injection)
- [x] Retry builder: composes original prompt + prescription + engine switch
- [x] `DiagnosisHistory []DiagnosisRecord` field on `TaskRun`
- [x] `DiagnosisConfig` in `internal/config/config.go`
- [x] Prometheus metrics (`diagnosis_total`, `diagnosis_engine_switches_total`, `diagnosis_retry_success_total`)
- [x] Unit tests and integration test

**Integration work remaining:**

- [ ] Wire analyser into `handleJobFailed` — run diagnosis before retry/fallback decision
- [ ] Use `RetryBuilder` output instead of plain retry — compose enriched prompt with prescription
- [ ] Enforce `DiagnosisHistory` deduplication — if same `FailureMode` diagnosed twice, go terminal
- [ ] Wire engine switch recommendation — when diagnosis suggests switch, modify the fallback chain
- [ ] Add `WithDiagnosis` functional option to `Reconciler` struct
- [ ] Initialise analyser in `cmd/robodev/main.go` when `config.Diagnosis.Enabled` is true
- [ ] Track `diagnosis_retry_success_total` — increment when a diagnosed retry succeeds
- [ ] V2: LLM-based diagnosis for richer classification
- [ ] E2E test: trigger a real failure, verify diagnosis classifies correctly and enriched retry succeeds

---

### 15. Adaptive Watchdog Calibration

**Status:** Scaffolding complete · Integration pending
**Package:** `internal/watchdog/` (extensions: `calibrator.go`, `profiles.go`)
**Priority:** High

Evolves the watchdog from static thresholds to per-(repo, engine, task_type) adaptive thresholds calibrated from historical telemetry.

- [x] `Calibrator` — running percentile statistics (P50, P90, P99) per profile key
- [x] `CalibratedProfile` — threshold profiles with cold-start fallback (min 10 samples)
- [x] Profile resolution: exact match → partial match → global fallback → static defaults
- [x] `AdaptiveCalibrationConfig` in watchdog config
- [x] Prometheus metrics (`watchdog_calibrated_threshold`, `watchdog_calibration_samples`, `watchdog_calibration_overrides_total`)
- [x] Unit tests and integration test

**Integration work remaining:**

- [ ] Modify `Watchdog.Check()` to accept a `*Calibrator` — resolve applicable profile before evaluating rules, use calibrated P90 thresholds when available
- [ ] Wire calibrator recording into `handleJobComplete` and `handleJobFailed` — feed final TaskRun telemetry as observations
- [ ] Add `WithCalibrator` option to `Watchdog` constructor
- [ ] Initialise calibrator in `cmd/robodev/main.go` and pass to watchdog when `config.ProgressWatchdog.AdaptiveCalibration.Enabled` is true
- [ ] Persist calibration data across controller restarts (SQLite or serialised file)
- [ ] Test calibration under varying load patterns (burst vs steady)
- [ ] E2E test: run 15+ tasks, verify calibration activates and adjusts thresholds

---

### 16. Engine Fingerprinting and Intelligent Task Routing

**Status:** Scaffolding complete · Integration pending
**Package:** `internal/routing/`
**Priority:** Medium

Builds statistical profiles of each engine's strengths/weaknesses from historical outcomes, then routes new tasks to the engine most likely to succeed.

- [x] `EngineFingerprint` — Laplace-smoothed success rates per dimension (task type, repo language, repo size, complexity)
- [x] `IntelligentSelector` implementing `EngineSelector` interface — epsilon-greedy exploration (ε=0.1)
- [x] `MemoryFingerprintStore` — in-memory, thread-safe
- [x] `RoutingConfig` in `internal/config/config.go`
- [x] Prometheus metrics (`routing_engine_selected_total`, `routing_exploration_total`, `routing_fingerprint_samples`, `routing_success_rate`)
- [x] Unit tests and integration test

**Integration work remaining:**

- [ ] Replace `DefaultEngineSelector` with `IntelligentSelector` in controller when `config.Routing.Enabled` is true
- [ ] Wire outcome recording into `handleJobComplete`/`handleJobFailed` — feed `TaskOutcome` to fingerprint store
- [ ] Add `WithIntelligentSelector` functional option to `Reconciler`
- [ ] Initialise fingerprint store in `cmd/robodev/main.go`
- [ ] Implement `SQLiteFingerprintStore` for persistence across restarts
- [ ] Populate `RoutingQuery` from ticket metadata (task type, repo language, repo size, complexity)
- [ ] E2E test: run 20+ tasks across engines, verify routing converges to better engine selection

---

### 17. Predictive Cost and Duration Estimation

**Status:** Scaffolding complete · Integration pending
**Package:** `internal/estimator/`
**Priority:** Medium

Pre-execution cost ($) and duration (minutes) prediction using task complexity dimensions and historical kNN.

- [x] `ComplexityScorer` — multi-dimensional scoring (description, label, repo size, task type)
- [x] `Predictor` — kNN (k=5) from historical data, returns [P25, P75] ranges
- [x] `MemoryEstimatorStore` — in-memory
- [x] Cold-start defaults per engine
- [x] `EstimatorConfig` in `internal/config/config.go`
- [x] Prometheus metrics (`estimator_predictions_total`, `estimator_predicted_cost`, `estimator_auto_rejections_total`, `estimator_prediction_accuracy`)
- [x] Unit tests and integration test

**Integration work remaining:**

- [ ] Wire estimator into `ProcessTicket` — run prediction before approval gate, include in `HumanQuestion` ("Predicted cost: $12-18, duration: 45-90 min")
- [ ] Implement `max_predicted_cost_per_job` guard rail — auto-reject tasks exceeding threshold
- [ ] Wire outcome recording into `handleJobComplete` — feed actual cost/duration for future predictions
- [ ] Add `WithEstimator` functional option to `Reconciler`
- [ ] Initialise estimator + store in `cmd/robodev/main.go`
- [ ] Implement `SQLiteEstimatorStore` for persistence
- [ ] Track prediction accuracy metric — compare predicted vs actual after completion
- [ ] E2E test: validate predictions against actuals for 20+ tasks

---

### 18. Competitive Execution with Tournament Selection

**Status:** Scaffolding complete · Integration pending
**Package:** `internal/tournament/`
**Priority:** Medium (capstone feature, depends on all others)

For high-value tasks, launches N parallel K8s Jobs with different engines. A judge Job compares results and selects the best solution.

- [x] `Tournament` struct — lifecycle state machine (Competing/Judging/Selected/Eliminated)
- [x] `Coordinator` — manages parallel TaskRuns linked by tournament ID
- [x] `JudgeBuilder` — constructs judge Job with side-by-side diff prompt
- [x] Tournament-aware TaskRun fields (`TournamentID`, `CandidateIndex`, `TournamentState`)
- [x] `CompetitiveExecutionConfig` in `internal/config/config.go`
- [x] Prometheus metrics (`tournament_total`, `tournament_candidates_total`, `tournament_winner_engine_total`, `tournament_cost_total`, `tournament_duration_seconds`)
- [x] Unit tests and integration test

**Integration work remaining:**

- [ ] Wire coordinator into `ProcessTicket` — detect tournament-eligible tasks (by label, priority, or config), delegate to coordinator
- [ ] Coordinator must create actual K8s Jobs for each candidate — currently a state machine without K8s interaction
- [ ] Implement git worktree isolation for candidates — each candidate works on a separate branch
- [ ] Wire `handleJobComplete` to route tournament members to coordinator (`OnCandidateComplete`)
- [ ] Wire judge Job creation via `JudgeBuilder` + `JobBuilder` (actual K8s Job, not just a struct)
- [ ] Wire winner selection — apply winning candidate's branch, eliminate losers
- [ ] Integrate PRM (Feature 12) for real-time candidate scoring during tournament
- [ ] Integrate memory (Feature 13) to provide judge with cross-engine context
- [ ] Integrate routing (Feature 16) to select candidate engines
- [ ] Integrate adaptive watchdog (Feature 15) for tournament-aware early termination
- [ ] Add `WithTournamentCoordinator` functional option to `Reconciler`
- [ ] Initialise coordinator in `cmd/robodev/main.go`
- [ ] E2E test: run a 3-engine tournament on a real task, verify judge selects and winning branch is applied

---

### Phase I — Full Integration (The Hard Part)

This phase wires all seven scaffolded features into the live controller. This is where the actual intelligence emerges — packages become features. Estimated 8-12 weeks.

#### I-1. Controller Wiring (~2 weeks)

Wire all new subsystems into the `Reconciler` struct with functional options and config-gated initialisation.

- [x] Add PRM fields to `Reconciler` struct: `prmConfig`, `prmEvaluators` map
- [x] Add Memory fields to `Reconciler` struct: `memoryGraph`, `memoryExtractor`, `memoryQuery`
- [x] Add functional options: `WithPRMConfig`, `WithMemory`
- [x] Wire PRM into `startStreamReader` flow via `WithEventProcessor`
- [x] Wire Memory extraction into `handleJobComplete` and `handleJobFailed`
- [x] Wire Memory query into `ProcessTicket` before building execution spec
- [x] PRM and Memory gated by config flags (disabled by default)
- [x] Backward compatibility: controller behaves identically when features disabled
- [ ] Add remaining fields: `calibrator`, `analyser`, `intelligentSelector`, `estimator`, `tournamentCoordinator`
- [ ] Add remaining functional options: `WithCalibrator`, `WithDiagnosis`, `WithRouting`, `WithEstimator`, `WithTournament`
- [ ] Wire estimator into `ProcessTicket`
- [ ] Wire routing into engine selection
- [ ] Wire diagnosis into `handleJobFailed`

#### I-2. Main Entrypoint Wiring (~1 week)

Initialise all components in `cmd/robodev/main.go` and pass to reconciler.

- [x] Initialise `memory.Graph` + `memory.SQLiteStore` when `config.Memory.Enabled`
- [x] Initialise PRM config when `config.PRM.Enabled`
- [x] Background decay goroutine for memory
- [x] Graceful shutdown for memory SQLite store
- [ ] Initialise `watchdog.Calibrator` when `config.ProgressWatchdog.AdaptiveCalibration.Enabled`
- [ ] Initialise `diagnosis.Analyser` when `config.Diagnosis.Enabled`
- [ ] Initialise `routing.IntelligentSelector` + `routing.MemoryFingerprintStore` when `config.Routing.Enabled`
- [ ] Initialise `estimator.Predictor` + `estimator.MemoryEstimatorStore` when `config.Estimator.Enabled`
- [ ] Initialise `tournament.Coordinator` when `config.CompetitiveExecution.Enabled`

#### I-3. Prompt Builder Integration (~1 week)

Wire memory query results into the prompt builder.

- [x] Add `MemoryContext` field to `engine.Task` and `promptData`
- [x] Extend all three `Build*` methods to populate `MemoryContext`
- [x] Add `{{.MemoryContext}}` to prompt template
- [ ] Include provenance (source TaskRun ID, confidence level) in injected facts
- [ ] Test prompt injection resistance — adversarial fact content must not escape template

#### I-4. PRM Hint File Writer (~1 week)

Implement the actual mechanism to write hint files into the agent's workspace.

- [ ] Create volume writer that accesses the shared workspace PVC
- [ ] Write `HintContent` to `/workspace/.robodev-hint.md` (or configured path)
- [ ] Handle concurrent writes (multiple PRM evaluations for the same TaskRun)
- [ ] Clean up hint files on task completion
- [ ] Ensure agents can read the hint file (some engines need explicit instruction to watch for it)

#### I-5. Persistence Layer (~2 weeks)

Replace in-memory stores with durable persistence for production use.

- [ ] Verify `memory.SQLiteStore` works end-to-end with real data (schema migration, concurrent access, corruption recovery)
- [ ] Implement `routing.SQLiteFingerprintStore` (reuse memory's SQLite DB)
- [ ] Implement `estimator.SQLiteEstimatorStore` (reuse memory's SQLite DB)
- [ ] Persist calibrator observations (serialised file or SQLite table)
- [ ] Test data survives controller restarts
- [ ] Consider shared SQLite DB vs separate files
- [ ] Add migration framework for schema evolution

#### I-6. LLM Integration (~3-4 weeks)

Replace rule-based heuristics with LLM-powered intelligence. The `internal/llm/` package is complete — this phase is prompt engineering work.

- [x] Implement DSPy-inspired LLM client abstraction (`internal/llm/`) — Signature, Module (Predict, ChainOfThought), Adapter, Client, Budget
- [x] Implement AnthropicClient using `net/http` only (no SDK dependency)
- [x] Implement budget enforcement for LLM calls (per-subsystem spend limits)
- [ ] **PRM V2**: Design scoring prompt — given recent tool calls, rate agent productivity 1-10 with reasoning. Iterate on real agent transcripts until reliable.
- [ ] **Memory V2**: Design extraction prompt — given TaskRun data, extract structured facts. Handle edge cases (empty results, hallucinated facts, duplicate knowledge).
- [ ] **Diagnosis V2**: Design classification prompt — given failure transcript, classify failure mode and generate prescription. Must resist prompt injection from agent output.
- [ ] **Tournament Judge**: Design judging prompt — given N diffs, select best with reasoning. Test with real side-by-side diffs.
- [ ] Rate limiting for LLM scoring calls (avoid overwhelming the API during active TaskRuns)

#### I-7. Security Hardening (~1-2 weeks)

Adversarial testing and security review of all new subsystems.

- [ ] PRM hint file path — verify path traversal is impossible (no `../` in configured path)
- [ ] Memory graph tenant isolation — adversarial tests proving tenant A cannot read tenant B's facts
- [ ] Diagnosis prescription templates — verify agent output cannot escape templates into injected prompts
- [ ] Tournament judge prompt — verify candidate diffs cannot inject instructions into the judge
- [ ] LLM scoring prompts — verify agent output in stream events cannot manipulate PRM scores
- [ ] SQLite database — verify no SQL injection via fact content or node IDs
- [ ] Config validation — reject invalid/dangerous config values (negative thresholds, path traversal in file paths)

#### I-8. End-to-End Testing (~2 weeks)

Full E2E tests against a real kind cluster with real agents.

- [ ] Run PRM with a live Claude Code agent — verify scoring and interventions fire at correct thresholds
- [ ] Run memory across 50+ tasks — verify knowledge accumulates, decays, and injects into prompts
- [ ] Run adaptive watchdog for 15+ tasks — verify calibration activates and reduces false positives
- [ ] Run diagnosis on intentionally failing tasks — verify correct classification and enriched retry
- [ ] Run routing across 20+ tasks on 3 engines — verify convergence to optimal engine selection
- [ ] Run cost estimator — validate predictions against actuals within 2x
- [ ] Run a 3-engine tournament on a real GitHub issue — verify judge selects best solution
- [ ] Full integration test: all 7 features enabled simultaneously, verify no conflicts or race conditions

---

## Implementation Order

```
Phase A (foundation):  1. Enhanced Claude Code Engine  ·  3. Engine Fallback Chains  ·  6. TDD Workflow Mode
Phase B (streaming):   2. Real-Time Agent Streaming
Phase C (isolation):   4. Agent Sandbox Integration
Phase D (governance):  7. Approval Workflows + Audit Trail
Phase E (access):      8. Local Development Mode  ·  5. Multi-Agent Coordination (Phase 1)
Phase F (ecosystem):   9. Plugin SDKs
Phase G (observe):    10. Agent Dashboard
Phase H (docs):       11. Documentation Site
Phase I (integration): Bleeding-edge feature integration (see below)
```

### Phase I Suggested Order

```
I-1. Controller wiring       (unblocks everything)
I-2. Main entrypoint wiring  (makes features configurable)
I-3. Prompt builder memory    (memory becomes useful)
I-4. PRM hint file writer     (PRM becomes useful)
I-5. Persistence layer        (features survive restarts)
I-6. LLM integration          (features become intelligent)
I-7. Security hardening       (features become safe)
I-8. End-to-end testing       (features become reliable)
```

---

## Summary

| # | Feature | Phase | Priority | Status |
|---|---------|-------|----------|--------|
| 1 | Enhanced Claude Code Engine | A | Critical | **Complete** |
| 2 | Real-Time Agent Streaming | B | Critical | **Complete** |
| 3 | Engine Fallback Chains | A | High | **Complete** |
| 4 | Agent Sandbox Integration | C | High | **Complete** |
| 5 | Multi-Agent Coordination (Phase 1) | E | High | **Complete** |
| 6 | TDD Workflow Mode | A | Medium | **Complete** |
| 7 | Approval Workflows + Audit Trail | D | Medium | **Complete** |
| 8 | Local Development Mode | E | Medium | **Complete** |
| 9 | Plugin SDKs | F | Medium | Not started |
| 10 | Agent Dashboard | G | High | Not started |
| 11 | Documentation Site | H | High | **In progress** (MkDocs Material site live, API ref + CI/CD remaining) |
| 12 | Controller PRM (Real-Time Coaching) | I | Critical | **Integrated** |
| 13 | Episodic Memory (Knowledge Graph) | I | Critical | **Integrated** |
| 14 | Causal Diagnosis (Self-Healing Retry) | I | High | **Scaffolding complete** |
| 15 | Adaptive Watchdog Calibration | I | High | **Scaffolding complete** |
| 16 | Engine Fingerprinting + Routing | I | Medium | **Scaffolding complete** |
| 17 | Predictive Cost Estimation | I | Medium | **Scaffolding complete** |
| 18 | Competitive Execution (Tournament) | I | Medium | **Scaffolding complete** |
| 19 | Shortcut webhook noise — filter story updates that don't transition to the target state | Backlog | Low | **Complete** (fixed in `internal/webhook/shortcut.go` via `WithShortcutTargetStateID`) |
