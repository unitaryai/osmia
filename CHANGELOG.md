# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### Phase 2 Wiring — Diagnosis, Calibration, Routing, Estimator, SCM Router, Transcript

**Diagnosis subsystem** (`internal/diagnosis/`, `internal/controller/controller.go`)
- `WithDiagnosis(analyser, retryBuilder)` option wires the causal failure analyser into the controller
- `handleJobFailed` now runs `Analyser.Analyse` before deciding whether to retry; `ShouldRetry` skips retry when the same failure mode recurs or when `InfraFailure` is diagnosed; `RetryBuilder.Build` enriches the retry prompt with corrective instructions and optionally switches engine based on diagnosis
- `DiagnosisRecord` appended to `tr.DiagnosisHistory` on every diagnosis cycle
- Fixed: `launchRetryJob` method added — same-engine retries were previously left in `StateRetrying` indefinitely with no new job being created

**Watchdog + adaptive calibration** (`internal/watchdog/`, `internal/controller/controller.go`)
- `WithWatchdog(wd)` and `WithWatchdogCalibration(cal, pr)` options wire the watchdog and calibration pipeline
- Watchdog background loop started inside `Reconciler.Run` via `wd.Start`
- `ConsumeStreamEvent` wired into the stream reader for real-time telemetry updates
- Per-task-run heartbeat tracking (`heartbeats`, `heartbeatSeqs` maps) feeds `watchdog.Check`
- `recordTaskOutcome` calls `calibrator.Record` + `profileResolver.RefreshProfile` after every terminal task run
- `main.go` initialises `Calibrator`, `MemoryProfileStore`, `ProfileResolver` and uses `NewWithCalibration` constructor

**Intelligent routing** (`internal/routing/`, `internal/controller/controller.go`)
- `WithIntelligentSelector(sel)` option; `recordTaskOutcome` calls `RecordOutcome` on completion
- `main.go` initialises `MemoryFingerprintStore` + `IntelligentSelector` when `routing.enabled: true`

**Cost/duration estimator** (`internal/estimator/`, `internal/controller/controller.go`)
- `WithEstimator(predictor, scorer)` option; `ProcessTicket` calls `ComplexityScorer.Score` + `Predictor.Predict` before job creation; auto-rejects when predicted cost exceeds the configured threshold
- `recordTaskOutcome` calls `estimatorPredictor.RecordOutcome` on every terminal task run
- `main.go` initialises with in-memory store when `estimator.enabled: true`

**Multi-SCM routing — item 22** (`internal/scmrouter/`)
- New `internal/scmrouter` package: `Router.For(repoURL)` selects the correct backend by matching the repo URL host against configured glob patterns; falls back to the first backend when no pattern matches
- `WithSCMRouter(router)` option on `Reconciler`
- `main.go`: checks `cfg.SCM.Backends` array first; falls back to legacy single-backend `cfg.SCM.Backend` for backwards compatibility
- `SCMBackendEntry` struct and `Backends []SCMBackendEntry` added to `SCMConfig` in `internal/config/config.go`

**Transcript storage — item 21** (`pkg/plugin/transcript/`)
- New `pkg/plugin/transcript` package: `TranscriptSink` interface with `Append` and `Flush`
- New `pkg/plugin/transcript/local`: `LocalSink` writes NDJSON audit files (one `.jsonl` per task run) to a configured directory
- `WithTranscriptSink(sink)` option on `Reconciler`; stream reader wires `Append` as an event processor; transcript flushed when stream forwarder exits
- `AuditConfig` + `TranscriptConfig` added to `internal/config/config.go`; local backend activated when `audit.transcript.backend: local` and `path` are set

#### Agent Stream Logging Processor (`internal/agentstream/logging.go`)
- New `NewLoggingEventProcessor(logger *slog.Logger) StreamEventProcessor` — logs each NDJSON stream event as a human-readable structured `slog` line in the controller's own logs, giving operators a clean view of agent activity without touching (or losing) the raw pod logs
- Log format per event type: `tool_use` → INFO `"agent tool call"` with `tool` and `input` (first 80 chars of args); `content` → DEBUG `"agent content"` with `role` only (content text deliberately elided to avoid logging LLM output); `result` → INFO `"agent result"` with `success`, `summary`, `mr_url`; `cost` → INFO `"agent cost"` with token counts and USD; system/unknown → DEBUG `"agent system event"`
- Comprehensive table-driven tests covering all event types, nil-Parsed guards, content elision at both INFO and DEBUG levels, and the 80-character args truncation boundary

#### Multi-Workflow Shortcut Support (`pkg/plugin/ticketing/shortcut/shortcut.go`)
- New exported `WorkflowMapping` struct pairing a trigger state name with an in-progress state name
- New `WithWorkflowMappings(mappings []WorkflowMapping) Option` — configures multiple trigger states; `PollReadyTickets` issues one API call per trigger state and merges/deduplicates results by story ID
- `Init` resolves `triggerStateID` for every mapping; backward-compatible: single `WithWorkflowStateName`/`WithInProgressStateName` pair is automatically synthesised into a single mapping
- `MarkInProgress` now selects the correct in-progress state by matching the story's current `workflow_state_id` against the mapping whose trigger state it was found in; falls back to the first mapping with a warning if no match is found
- `WorkflowStateID()` returns the first mapping's resolved trigger state ID (for webhook filtering)
- New `WorkflowMappings() []WorkflowMapping` accessor
- New `pollQuery` helper to avoid duplicating query-build/parse logic across multiple trigger states
- `cmd/robodev/main.go` `initShortcutBackend` reads the `workflows` array from the Shortcut config map and calls `WithWorkflowMappings`; the legacy `workflow_state_name` / `in_progress_state_name` keys continue to work unchanged
- New tests: multi-mapping Init, multi-workflow poll merge, overlap deduplication, mapping selection in `MarkInProgress`

#### Configurable Code Review Gate (`internal/config/config.go`, `internal/controller/controller.go`)
- New `CodeReviewConfig` struct with `enabled`, `backend`, `wait_for_comments`, `timeout_minutes`, `token_secret` fields; added as `code_review:` top-level key in `Config`
- Controller `handleJobComplete` now respects `config.CodeReview.Enabled`: when `false` (the default) no review wait occurs; when `true` and a `reviewBackend` is wired, calls `ReviewDiff` with a configurable timeout (default 15 minutes) and logs the outcome before proceeding
- New `ShortcutWorkflow` struct (`trigger_state`, `in_progress_state`) exported from `internal/config/` for typed config parsing

#### Roadmap (`docs/roadmap.md`)
- Added Phase K (near-term) items 21–23: Transcript Storage & Audit Log, Multi-SCM Backend Routing, Skills/Subagents/Per-Task MCP Plugins
- Added Phase L (longer-term) items 24–25: Non-Standard Task Types (requires design doc), Supervisor Agent (requires design doc)
- Updated summary table

#### All Plugin Backends Wired into Controller (`cmd/robodev/main.go`)
- **Linear ticketing**: `initLinearBackend` reads token secret, team_id, state_filter, labels, and exclude_labels from config
- **Discord notifications**: `initDiscordChannel` reads webhook_url from config
- **Telegram notifications**: `initTelegramChannel` reads token secret, chat_id, and optional thread_id from config
- **Slack approval**: `initApprovalBackend` reads channel_id and token secret; controller gains `WithApprovalBackend(approval.Backend)` option
- **GitHub/GitLab SCM**: `initSCMBackend` reads token secret and optional base_url; controller gains `WithSCMBackend(scm.Backend)` option
- **CodeRabbit review**: `initReviewBackend` reads api_key_secret; controller gains `WithReviewBackend(review.Backend)` option
- **K8s/Vault secrets resolver**: `initSecretsResolver` iterates `cfg.SecretResolver.Backends` and registers each backend by scheme; controller gains `WithSecretsResolver(*secretresolver.Resolver)` option
- **Aider/Codex engines**: wired in when `cfg.Engines.Aider` / `cfg.Engines.Codex` are non-nil
- **`config.ApprovalConfig`** struct added to top-level `Config` (mirrors ReviewConfig / SCMConfig pattern)
- Notifications loop refactored from if/else chain to switch so Discord and Telegram have equal first-class handling
- **Startup wiring test** added (`cmd/robodev/init_test.go`, 20 tests) — calls every init function directly with a fake Kubernetes client to verify that all supported backend strings reach their init function rather than falling through to the unsupported-backend error branch

#### Shortcut Backend Wired into Controller (`cmd/robodev/main.go`)
- `initShortcutBackend` helper function reads `token_secret`, `workflow_state_name`, `in_progress_state_name`, `owner_mention_name`, and `exclude_labels` from config, fetches the API token from a Kubernetes Secret, and calls `Init()` to resolve human-readable names to Shortcut API identifiers at startup
- `"shortcut"` case added to the ticketing backend selection block — previously only `"github"` was wired in
- Webhook server automatically configured with `WithShortcutTargetStateID` when the Shortcut backend is active, so only story transitions to the trigger state generate webhook events

### Fixed

- Add environment variable stripping to Codex, Cline, and OpenCode engine entrypoints (previously only Claude Code had it)
- Fix mermaid diagram in `docs/concepts/prm.md` that failed to render on GitHub (replaced `≥`/`<` with safe alternatives)
- Fix sandbox status contradiction in `docs/roadmap.md` (summary said not started, but Phase C items were all complete)
- Update Docker Compose quickstart and Documentation Site status in `docs/roadmap.md` to reflect shipped work
- Sync all 13 items in `docs/improvements-plan.md` with actual implementation status (12 items were marked "Planned" despite being implemented)

### Added

#### DSPy-Inspired LLM Abstraction (`internal/llm/`)
- New `internal/llm/` package providing a typed, composable LLM abstraction inspired by DSPy
- `Signature` type defining typed input/output fields for structured LLM interactions
- `Module` interface with `Predict` (single LLM call) and `ChainOfThought` (step-by-step reasoning) implementations
- `Adapter` converting signatures + inputs into formatted prompts and parsing structured JSON responses
- `Client` interface with `AnthropicClient` implementation using `net/http` only (no external SDK)
- `Budget` tracker with per-subsystem spend limits, token counting, and concurrent-safe access
- Full table-driven unit test suite for all types, modules, adapter, client (with `httptest` server), and budget

#### PRM and Memory Controller Integration
- **PRM wired into controller**: `WithPRMConfig` reconciler option, PRM evaluator lifecycle (creation in `startStreamReader`, cleanup in `handleJobComplete`/`handleJobFailed`), real-time event processing via `WithEventProcessor`, hint recording with Prometheus metrics
- **Memory wired into controller**: `WithMemory` reconciler option accepting graph, extractor, and query engine; knowledge extraction on task completion and failure via background goroutines; memory context query before execution spec building
- **Memory context in prompts**: `MemoryContext` field on `engine.Task`, propagated through `BuildPrompt`, `BuildPromptWithProfile`, and `BuildPromptWithTeams` in the prompt builder template
- **Memory initialisation in main.go**: SQLite store opening, graph loading, extractor and query engine creation, background decay goroutine with configurable interval and prune threshold, graceful store close on shutdown
- **PRM initialisation in main.go**: Config mapping from `config.PRMConfig` to `prm.Config`, logger wiring
- **Backwards compatible**: PRM disabled by default (`prm.enabled: false`), Memory disabled by default (`memory.enabled: false`); no behavioural change when features are off
- New controller unit tests: `TestWithPRMConfig`, `TestWithMemory`, `TestProcessTicketWithMemoryContext`, `TestHandleJobCompleteWithMemory`, `TestHandleJobFailedWithMemory`
- New integration tests: `tests/integration/prm_controller_test.go` (PRM wiring, disabled no-op), `tests/integration/memory_controller_test.go` (memory extraction wiring, context injection, disabled no-op)

#### Bleeding-Edge Agentic Engineering Features (Scaffolding)

> **Note:** PRM and Memory are now wired into the controller and functional when enabled. The remaining five features (Diagnosis, Routing, Estimator, Tournament, Adaptive Watchdog) have complete packages with types, core logic, unit tests, and integration tests, but are not yet wired into the controller. See `docs/roadmap.md` Phase I for the full integration plan.

##### Controller-Level Process Reward Model (Feature 2) — Real-Time Agent Coaching
- New `internal/prm/` package: rule-based step scoring from tool call patterns, trajectory tracking with pattern detection (sustained decline, plateau, oscillation, recovery), and intervention decision logic (continue/nudge/escalate)
- `Scorer` evaluates rolling windows of tool calls: penalises repetition, rewards productive patterns (read→edit, edit→test), and tracks tool diversity
- `Trajectory` detects patterns across score history with configurable window length
- `InterventionDecider` triggers soft nudges (hint file at `/workspace/.robodev-hint.md`) or watchdog escalation based on score thresholds and trajectory patterns
- `Evaluator` ties scoring, trajectory, and intervention into a single entry point wired into the streaming pipeline via `WithEventProcessor`
- `PRMConfig` in controller config: `evaluation_interval`, `window_size`, `score_threshold_nudge`, `score_threshold_escalate`, `hint_file_path`, `max_budget_usd`
- Prometheus metrics: `prm_step_scores` histogram, `prm_interventions_total` counter by action, `prm_trajectory_patterns_total` counter by pattern

##### Cross-Task Episodic Memory with Temporal Knowledge Graph (Feature 3)
- New `internal/memory/` package: persistent knowledge graph accumulating structured lessons from every TaskRun across all engines, repos, and tenants
- Node types: `Fact` (with temporal validity and confidence decay), `Pattern` (recurring observations), `EngineProfile` (per-engine capability data)
- Edge relations: `relates_to`, `contradicts`, `supersedes` with weighted connections
- SQLite-backed storage (`modernc.org/sqlite`, pure Go) with auto-migration on startup
- Heuristic knowledge extractor: harvests facts from task outcomes, watchdog events, engine fallbacks, and tool call patterns
- Temporal query engine: retrieves relevant facts weighted by recency, confidence, and decay rate with cross-tenant isolation
- `MemoryConfig` in controller config: `store_path`, `decay_interval_hours`, `prune_threshold`, `max_facts_per_query`, `tenant_isolation`
- Prometheus metrics: `memory_nodes_total` gauge by type, `memory_queries_total` counter, `memory_extractions_total` counter by outcome, `memory_confidence_distribution` histogram

##### Self-Healing Retry with Causal Diagnosis (Feature 4)
- New `internal/diagnosis/` package: structured failure diagnosis pipeline replacing blind retries with informed corrective action
- Failure mode classifier: `WrongApproach`, `DependencyMissing`, `TestMisunderstanding`, `ScopeCreep`, `PermissionBlocked`, `ModelConfusion`, `InfraFailure` — rule-based pattern matching on tool call sequences, event content, and watchdog reasons
- Template-based prescription generator: per-failure-mode corrective instructions using safe `text/template` (prevents prompt injection from agent output)
- Retry builder: composes original prompt + diagnosis prescription + optional engine switch recommendation
- `DiagnosisHistory []DiagnosisRecord` field on TaskRun prevents repeating the same diagnosis
- `DiagnosisConfig` in controller config: `max_diagnoses_per_task`, `enable_engine_switch`
- Prometheus metrics: `diagnosis_total` counter by failure mode, `diagnosis_engine_switches_total` counter, `diagnosis_retry_success_total` counter

##### Adaptive Watchdog Calibration (Feature 5)
- New `internal/watchdog/calibrator.go`: running percentile statistics (P50, P90, P99) per (repo pattern, engine, task type) for key telemetry signals — token consumption rate, tool call frequency, file change rate, cost velocity, duration, consecutive identical tools
- New `internal/watchdog/profiles.go`: calibrated threshold profiles with cold-start fallback (minimum 10 completed TaskRuns before overriding static defaults), best-match profile resolution (exact → partial → global → static)
- `AdaptiveCalibrationConfig` in watchdog config: `min_sample_count`, `percentile_threshold` (p50/p90/p99), `cold_start_fallback`
- Prometheus metrics: `watchdog_calibrated_threshold` gauge by (signal, repo_pattern, engine, task_type), `watchdog_calibration_samples` gauge, `watchdog_calibration_overrides_total` counter

##### Engine Fingerprinting and Intelligent Task Routing (Feature 6)
- New `internal/routing/` package: statistical engine profiles built from historical task outcomes, replacing static fallback chains with data-driven routing
- `EngineFingerprint` with Laplace-smoothed success rates across dimensions: task type, repo language, repo size, complexity
- `IntelligentSelector` implementing `EngineSelector` interface: weighted composite scoring with epsilon-greedy exploration (default ε=0.1) to discover new engine capabilities
- In-memory fingerprint store with thread-safe concurrent access
- `RoutingConfig` in controller config: `epsilon_greedy`, `min_samples_for_routing`, `store_path`
- Prometheus metrics: `routing_engine_selected_total` counter by engine, `routing_exploration_total` counter, `routing_fingerprint_samples` gauge by engine, `routing_success_rate` gauge by (engine, dimension, value)

##### Predictive Cost and Duration Estimation (Feature 7)
- New `internal/estimator/` package: pre-execution cost ($) and duration (minutes) prediction based on task complexity and historical data
- Multi-dimensional complexity scorer: description complexity, label complexity, repo size, task type complexity → weighted composite score
- k-nearest-neighbours predictor (k=5): finds similar historical tasks, returns [P25, P75] cost/duration ranges with confidence based on sample count
- Cold-start defaults per engine when insufficient historical data
- New guard rail: `max_predicted_cost_per_job` — auto-reject tasks predicted to exceed cost threshold
- `EstimatorConfig` in controller config: `max_predicted_cost_per_job`, `default_cost_per_engine`, `default_duration_per_engine`
- Prometheus metrics: `estimator_predictions_total` counter, `estimator_predicted_cost` histogram, `estimator_auto_rejections_total` counter, `estimator_prediction_accuracy` histogram

##### Competitive Execution with Tournament Selection (Feature 1)
- New `internal/tournament/` package: launch N parallel K8s Jobs with different engines/strategies, judge results, and select the best solution
- `Coordinator` manages tournament lifecycle: start parallel candidates, track completions, trigger early termination, launch judge, select winner
- `JudgeBuilder` constructs judge Jobs with side-by-side diff comparison prompts and structured JSON output
- Tournament-aware TaskRun fields: `TournamentID`, `CandidateIndex`, `TournamentState` (Competing/Judging/Selected/Eliminated)
- `CompetitiveExecutionConfig` in controller config: `default_candidates`, `judge_engine`, `early_termination_threshold`, `max_concurrent_tournaments`
- Prometheus metrics: `tournament_total` counter, `tournament_candidates_total` counter by engine, `tournament_winner_engine_total` counter by engine, `tournament_cost_total` histogram, `tournament_duration_seconds` histogram

#### Strategic Roadmap Phase A-E (Items 1-8)

##### Enhanced Claude Code Engine (Item 1)
- `DefaultTaskResultSchema` const with JSON schema for structured task result output
- Functional options pattern: `WithFallbackModel`, `WithToolWhitelist`, `WithJSONSchema`
- Conditional CLI flag construction: `--output-format stream-json`, `--json-schema`, `--fallback-model`, `--no-session-persistence`, `--append-system-prompt`, `--allowedTools`, `--disallowedTools`
- Extended `EngineConfig` with `FallbackModel`, `ToolWhitelist`, `ToolBlacklist`, `JSONSchema`, `AppendSystemPrompt`, `NoSessionPersistence`, `StreamingEnabled` fields
- Extended `ClaudeCodeEngineConfig` with matching YAML fields

##### Real-Time Agent Streaming (Item 2)
- New `internal/agentstream/` package: NDJSON event types (`ToolCallEvent`, `ContentDeltaEvent`, `CostEvent`, `ResultEvent`), K8s pod log stream reader, event forwarder to watchdog and notifications
- Watchdog `ConsumeStreamEvent` method for live tool call tracking, cost monitoring, and heartbeat updates from streaming agents
- Controller stream reader lifecycle: starts per active Claude Code job, cancels on completion/failure
- `StreamingConfig` with `Enabled` and `LiveNotifications` fields
- `--verbose` flag added when streaming is enabled for richer event data

##### Engine Fallback Chains (Item 3)
- `EngineSelector` interface with `DefaultEngineSelector` implementation
- Ticket label override (`robodev:engine:<name>`) for per-ticket engine selection
- `FallbackEngines []string` in `EnginesConfig` for ordered fallback chain
- `EngineAttempts` and `CurrentEngine` tracking on TaskRun
- Automatic fallback to next engine in `handleJobFailed` before exhausting retries
- `launchFallbackJob` helper with unique job IDs per attempt

##### Agent Sandbox Integration (Item 4)
- `SandboxBuilder` implementing `JobBuilder` with gVisor/Kata RuntimeClass, SandboxClaim annotations, and warm pool labels
- `ExecutionConfig` with `Backend` ("job"/"sandbox"/"local") and `SandboxConfig` (runtime class, warm pool, env stripping)
- Helm templates: `runtimeclass-gvisor.yaml` and `sandboxwarmpool.yaml` (gated by `sandbox.enabled`)
- Environment variable stripping in Claude Code entrypoint (guarded by `ENV_STRIPPING`)

##### Multi-Agent Coordination Phase 1 (Item 5)
- `AgentDef` config type with role, model, and instructions
- `BuildAgentFlags` generating `--agents` JSON from config or task-type defaults (bug_fix: coder+reviewer, feature: coder+reviewer+tester)
- `WithTeamsConfig` functional option on Claude Code engine
- Team coordination prompt section in `BuildPromptWithTeams`
- `CLAUDE_CODE_MAX_TEAMMATES` environment variable

##### TDD Workflow Mode (Item 6)
- `WorkflowInstructions()` function returning structured instructions for "tdd" and "review-first" modes
- `TaskProfileConfig` with `Workflow`, `ToolWhitelist`, `ToolBlacklist` fields
- Workflow instructions injected between task description and guard rails in prompt template
- `BuildPromptWithProfile` method for profile-aware prompt construction

##### Approval Workflows and Audit Trail (Item 7)
- `TaskRunStore` interface with `Save`, `Get`, `List`, `ListByTicketID` methods
- `MemoryStore` thread-safe in-memory implementation
- Pre-start and pre-merge approval gates in controller
- Queued → NeedsHuman state transition for approval holds
- Slack approval callback parsing (`robodev_approve_*` / `robodev_reject_*` actions)
- `ApprovalGates` and `ApprovalCostThresholdUSD` in guard rails config

#### Local Development Mode (Docker Compose)
- DockerBuilder (`internal/jobbuilder/docker.go`) implementing `controller.JobBuilder` for local Docker execution — produces K8s Job objects annotated with `robodev.io/execution-backend: local`
- Builder selection in `cmd/robodev/main.go`: reads `execution.backend` config to choose between standard ("job"), sandbox, or local Docker builder
- Noop ticketing file-watcher mode: `NewWithTaskFile` constructor reads tasks from a local YAML file, enabling local development without a real ticketing provider
- `FileTask` struct for YAML task definitions with ID, title, description, repo URL, and labels
- `docker-compose.yaml` for running the controller outside Kubernetes with config and workspace volume mounts
- Makefile targets: `compose-up` and `compose-down` for Docker Compose lifecycle
- Table-driven tests for DockerBuilder (backend annotation, security context, volumes, env vars, resources)
- Tests for noop file-watcher (valid YAML, empty file, malformed YAML, missing file, in-progress filtering)

#### Governance: Approval Workflows, Audit Trail, and TaskRun Store
- TaskRunStore interface (`internal/taskrun/store.go`) with `Save`, `Get`, `List`, and `ListByTicketID` methods for persistent TaskRun storage
- MemoryStore in-memory implementation of TaskRunStore with thread-safe operations
- Approval gates configuration: `approval_gates` list and `approval_cost_threshold_usd` in GuardRailsConfig (values: "pre_start", "high_cost", "pre_merge")
- Pre-start approval gate: when "pre_start" is configured, TaskRun transitions Queued → NeedsHuman instead of launching a K8s Job, requiring human approval before execution begins
- Pre-merge approval gate: when "pre_merge" is configured, completed jobs transition Running → NeedsHuman instead of marking the ticket complete, requiring human approval before merge
- TaskRunStoreConfig with backend selection ("memory", "sqlite", "postgres") and SQLite path configuration — future durable backends can implement the TaskRunStore interface
- Reconciler now persists TaskRun state to the store after every state transition (creation, approval hold, running, completion, failure, retry)
- `WithTaskRunStore` reconciler option; defaults to MemoryStore when not provided
- Slack webhook handler now parses `robodev_approve_*` and `robodev_reject_*` action IDs from approval callbacks, extracting task run IDs and logging structured approval/rejection events (stub for future resolution wiring)
- Queued → NeedsHuman added as a valid state transition in the TaskRun state machine
- Table-driven tests for MemoryStore (save/get, list, filter by ticket ID, not-found error)
- Controller tests for pre-start approval gate blocking job creation, pre-merge gate holding completion, and store persistence verification
- Config loading tests for new governance fields (approval gates, cost threshold, taskrun store backend)

#### Integration Test Suite
- Comprehensive three-tier integration test architecture: Tier 1 (E2E against Kind cluster), Tier 2 (in-process with fake K8s client), Tier 3 (in-process, no K8s)
- No-op ticketing backend (`pkg/plugin/ticketing/noop/`) as fallback for webhook-only and test deployments — prevents nil-pointer panics when no ticketing backend is configured
- Tier 3 tests: engine spec validation across all 5 engines, JobBuilder security hardening verification
- Tier 2 tests: full reconciler pipeline, webhook-to-reconciler integration, TaskRun state machine lifecycle, guard rails with real reconciler wiring, secret resolver parsing and policy enforcement
- Tier 1 E2E tests: webhook signature validation (GitHub HMAC-SHA256), container security contexts, Helm resource verification, NetworkPolicy rules, Prometheus metrics endpoints
- Test orchestration script (`hack/run-integration-tests.sh`) producing markdown reports suitable for `claude -p` analysis
- Test Helm values overlay (`hack/values-test.yaml`) with webhook secrets and NetworkPolicy enabled
- Makefile targets: `deploy-test`, `integration-test`, `test-report`, `test-all`

#### Webhook Receiver & Event-Driven Ingestion
- HTTP webhook server (`internal/webhook/`) with route handlers for GitHub, GitLab, Slack, Shortcut, and a configurable generic handler
- GitHub HMAC-SHA256 signature validation (`X-Hub-Signature-256`), issue event parsing for `opened` and `labeled` actions
- GitLab secret token validation (`X-Gitlab-Token`), issue and merge request event parsing
- Slack signing secret validation (`X-Slack-Signature`) with 5-minute replay attack prevention
- Shortcut webhook handler with optional HMAC signature validation and story state change parsing
- Generic webhook handler with configurable HMAC or bearer token auth and dot-notation JSON field mapping
- Webhook server wired into `cmd/robodev/main.go` with graceful shutdown support
- New `WebhookConfig` in controller configuration for per-source secrets

#### Task-Scoped Secret Resolution
- Secret resolver (`internal/secretresolver/`) parsing `<!-- robodev:secrets -->` HTML comment blocks and `robodev:secret:` label prefixes
- Policy engine validating environment variable names against allowed/blocked glob patterns, URI scheme restrictions, and tenant scoping
- Multi-backend resolver dispatching secrets by URI scheme (`vault://`, `k8s://`, `alias://`) to registered backends
- Structured audit logging (secret names only, never values) for compliance and debugging
- New `SecretResolverConfig` and `VaultSecretsConfig` types in controller configuration

#### HashiCorp Vault Secrets Backend
- Vault backend (`pkg/plugin/secrets/vault/`) implementing `secrets.Backend` interface
- Kubernetes auth method: reads ServiceAccount token and authenticates to Vault's `/v1/auth/kubernetes/login` endpoint
- KV v2 secret reads with token caching for performance
- Uses `net/http` directly — no external Vault client library dependency

#### OpenCode Execution Engine
- OpenCode engine (`pkg/engine/opencode/`) implementing `ExecutionEngine` for the OpenCode CLI
- Command: `opencode --non-interactive --message <prompt>`, context file: `AGENTS.md`
- Supports Anthropic, OpenAI, and Google model providers
- OpenCode engine Dockerfile (`docker/engine-opencode/`) with `node:22-slim` base, OpenCode CLI, and non-root user
- OpenCode entrypoint script with repo cloning, multi-provider support, and semantic exit codes
- Makefile targets: `docker-build-engine-opencode`, `docker-build-dev-engine-opencode`

#### Cline Execution Engine
- Cline engine (`pkg/engine/cline/`) implementing `ExecutionEngine` for the Cline CLI
- Command: `cline --headless --task <prompt> --output-format json`, context file: `.clinerules`
- Supports Anthropic, OpenAI, Google, and AWS Bedrock model providers
- Optional MCP (Model Context Protocol) support via `WithMCPEnabled` option and `--mcp` flag
- Cline engine Dockerfile (`docker/engine-cline/`) with `node:22-slim` base, headless CLI, and non-root user
- Cline entrypoint script with repo cloning, MCP toggle (`MCP_ENABLED`), JSON output, and semantic exit codes
- Makefile targets: `docker-build-engine-cline`, `docker-build-dev-engine-cline`

#### Shortcut Ticketing Backend
- Shortcut.com backend (`pkg/plugin/ticketing/shortcut/`) implementing `ticketing.Backend`
- REST API v3 integration with story search, label management, comments, and completion
- Auth via `Shortcut-Token` header, configurable workflow state ID and label filtering

#### Linear Ticketing Backend
- Linear backend (`pkg/plugin/ticketing/linear/`) implementing `ticketing.Backend`
- GraphQL API integration with issue queries, state transitions, comments, and label management
- Auth via raw API key, configurable team ID, state filter, and label filtering

#### Telegram Notification Channel
- Telegram channel (`pkg/plugin/notifications/telegram/`) implementing `notifications.Channel`
- Bot API `sendMessage` endpoint with Markdown formatting and optional topic-based thread support
- Builder options: `WithAPIURL`, `WithHTTPClient`, `WithThreadID`

#### Discord Notification Channel
- Discord channel (`pkg/plugin/notifications/discord/`) implementing `notifications.Channel`
- Webhook-based with colour-coded rich embeds (green success, red failure, blue info)
- No auth library needed — webhook URL contains the token

#### NetworkPolicy & Security Hardening
- Agent NetworkPolicy (`networkpolicy-agent.yaml`): deny all ingress, egress allowed to DNS (53), HTTPS (443), SSH (22) only
- Controller NetworkPolicy (`networkpolicy-controller.yaml`): allow webhook and metrics ingress, egress to DNS, HTTPS, and K8s API
- PodDisruptionBudget (`pdb.yaml`): configurable `minAvailable` / `maxUnavailable`, defaults to `minAvailable: 1`
- All templates gated by `networkPolicy.enabled` and `pdb.enabled` values
- Webhook Service template (`service-webhook.yaml`) exposing webhook port separately from metrics
- Webhook Ingress template (`ingress.yaml`) with className, annotations, hosts, paths, and TLS support
- Webhook container port added to controller Deployment template (conditional on `webhook.enabled`)
- New Helm values: `webhook` (with `service`, `ingress` sub-config), `networkPolicy`, `pdb` sections

#### GitHub Backend Filtering
- GitHub ticketing backend now supports filtering by assignee, milestone, and issue state in addition to labels
- Added client-side label exclusion to prevent re-pickup of in-progress and failed issues (default: `in-progress`, `robodev-failed`)
- New functional options: `WithAssignee`, `WithMilestone`, `WithState`, `WithExcludeLabels`
- Labels filter is now optional — omitting it enables assignee-only or milestone-only workflows
- Refactored `PollReadyTickets` URL construction to use `url.Values` for safer query parameter encoding

#### Live End-to-End Testing
- Wired up `cmd/robodev/main.go` with full backend initialisation: K8s client, GitHub ticketing, Claude Code engine, job builder, and Slack notifications
- Controller now reads secrets from Kubernetes at startup (GitHub token, Slack token) using config-driven secret references
- Added `hack/setup-secrets.sh` interactive script for provisioning K8s secrets (GitHub token, Anthropic API key, Slack bot token)
- Added `hack/values-live.yaml` Helm values overlay for live testing with real backends (conservative guardrails: $10 cost cap, 30min timeout, max 2 jobs)
- Added Makefile targets: `setup-secrets`, `live-deploy`, `live-up`, `live-redeploy` for full live testing workflow
- In-cluster and kubeconfig fallback for K8s client creation (supports both deployed and local dev)

#### Local Development & E2E Testing
- Kind cluster configuration (`hack/kind-config.yaml`) with two-node topology and host port mapping
- Local dev Helm values overlay (`hack/values-dev.yaml`) with `pullPolicy: Never`, disabled leader election, and NodePort access
- End-to-end smoke test suite (`tests/e2e/`) covering deployment readiness, health endpoints, metrics, RBAC, and resource creation
- Makefile targets for full local workflow: `check-prereqs`, `kind-create`, `kind-delete`, `docker-build-dev`, `kind-load`, `deploy`, `undeploy`, `local-up`, `local-down`, `local-redeploy`, `e2e-test`, `logs`
- Configurable service type in Helm chart (supports NodePort for local development)
- Local development workflow documentation in CONTRIBUTING.md

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
- Expanded plugin interface docs: ticketing (269 lines), notifications (246 lines), secrets (261 lines), engines (500 lines) with full RPC coverage, implementation guidance, and design considerations

#### Documentation Site (MkDocs Material)
- MkDocs Material documentation site with deep purple/amber theme, dark mode, search, and code copy
- Landing page with feature cards, Mermaid architecture diagram, and full project layout reference
- Dual quick start paths: Docker Compose (K8s newbies) and Kubernetes (experienced users)
- Four newcomer-facing concept pages: What is RoboDev?, TaskRun Lifecycle, Engines Explained, Guard Rails Overview
- Full configuration reference page documenting all config sections from `internal/config/config.go`
- Troubleshooting guide covering controller, agent, webhook, notification, and watchdog issues
- Mermaid diagrams: system architecture, TaskRun state machine, job lifecycle sequence, guard rail layers, engine decision tree
- Community section with contributing guide, code of conduct, security policy, roadmap, and changelog
- GitHub Actions workflow for automatic deployment to GitHub Pages on push to main
- Makefile targets: `docs-serve` and `docs-build` for local development
- Admonition boxes added to plugin documentation for tips, warnings, and interface info
- Existing docs updated: ASCII diagrams replaced with Mermaid, internal links fixed for MkDocs compatibility
- `getting-started.md` split into `getting-started/` subdirectory (kubernetes, docker-compose, configuration, troubleshooting)

#### Infrastructure
- Go module and core skeleton: controller entrypoint, config loading, TaskRun state machine, Prometheus metrics, ExecutionEngine interface
- CI pipeline with lint, test, build, proto-lint, helm-lint, docker-build jobs
- Helm chart with deployment, RBAC, ConfigMap, ServiceMonitor, Service templates
- Community files: CONTRIBUTING.md, CODE_OF_CONDUCT.md, SECURITY.md
- Comprehensive table-driven tests for all packages (20 test suites)
