# RoboDev Feature Roadmap

This document tracks what to work on next, in priority order. Completed work is
archived at the bottom. See `docs/improvements-plan.md` for the original 13-item
plan (12/13 complete).

---

## Status Legend

- [ ] Not started
- [x] Complete
- **In Progress** — marked in the item description when actively being worked on

---

## Current Priority Order

```
1. High-Priority Upcoming               — item 10 (dashboard); item 20 complete ✅
2. Live-backend E2E validation          — long-running / real-GitHub items below
3. Design-First (ADR before code)       — items 24, 25
4. Infrastructure                       — items 9 (plugin SDKs), 11 (docs — in progress)
```

---

## Item 20: PR/MR Comment Response ✅ (2026-03-04)

RoboDev now monitors open pull/merge requests it creates and spawns follow-up
jobs to address actionable review feedback.

**Implemented:**

- `pkg/plugin/scm` — `ReviewComment` type; `ListReviewComments`, `ReplyToComment`,
  `ResolveThread` added to the `Backend` interface
- `pkg/plugin/scm/github` — GitHub REST implementation of the three new methods
  (`ListReviewComments` merges review + issue comment endpoints; `ReplyToComment`
  attempts review reply then falls back to issue comment; `ResolveThread` is a
  no-op since GitHub REST does not support thread resolution)
- `pkg/plugin/scm/gitlab` — GitLab REST implementation (notes endpoint;
  discussion-aware reply; PUT `resolved: true` for thread resolution)
- `internal/reviewpoller` — new package:
  - `types.go` — `Classification`, `ClassifiedComment`, `TrackedPR`, `FollowUpRequest`
  - `classifier.go` — `RuleBasedClassifier` (keyword + bot author heuristics) and
    `LLMClassifier` (ChainOfThought with rule-based fallback)
  - `poller.go` — background `Poller` with `Register`, `DrainFollowUps`, `Start`
- `internal/config` — `ReviewResponseConfig` struct + validation
- `internal/taskrun` — `ParentTicketID`, `ReviewCommentID`, `ReviewThreadID`,
  `ReviewPRURL` fields on `TaskRun`
- `internal/controller` — `reviewPoller` field, `WithReviewPoller` option,
  `handleFollowUpComplete`, `processFollowUpTask`, `scmFor` helper; drain in
  `reconcileOnce`; register in `handleJobComplete`
- `cmd/robodev/main.go` — review response subsystem wiring
- `tests/integration/review_response_test.go` — 9 integration tests

**Configuration** (`robodev-config.yaml`):

```yaml
review_response:
  enabled: true
  poll_interval_minutes: 5
  min_severity: warning        # info | warning | error
  max_follow_up_jobs: 3        # per PR
  reply_to_comments: true
  resolve_threads: false       # GitLab only; no-op on GitHub REST
  llm_classifier: false        # set true to enable LLM-backed classification
```

---

## 1. Live-Backend E2E Validation — Remaining Gaps

The fake-agent E2E workflow test suite (Phase 5) covers all subsystem interactions
end-to-end against a real kind cluster. Two live tests (`make e2e-live-test`) now
run against the real Shortcut + GitLab + Claude Code stack. The items below require
extended live workloads (50+ tasks, 3-engine comparison) and will be validated once
a staging environment is available.

---

### Live Test Suite ✅ (initial coverage)

- [x] Happy path — story created → Claude Code picks up → MR opened → story done (`TestLiveHappyPath`)
- [x] Graceful clone failure — invalid repo URL → Claude Code handles error → story done with failure description (`TestLiveGracefulCloneFailure`)

Key finding from initial live testing: Claude Code handles clone failures at the
application level (exits 0, writes error description). `MarkFailed` / `robodev-failed`
label is triggered by K8s-level failures only (watchdog termination, OOM kill, etc.).

Run with: `make e2e-live-test` (requires `make live-up` + valid secrets)

---

### End-to-End Tests (Live Backend) — Extended Coverage

- [ ] PRM with live Claude Code agent — verify scoring and interventions fire at correct thresholds
- [ ] Memory across 50+ tasks — verify accumulation, decay, and prompt injection resistance
- [ ] Adaptive watchdog across 15+ tasks — verify calibration reduces false positives
- [ ] Diagnosis on intentionally failing tasks — correct classification, enriched retry
- [ ] Routing across 20+ tasks on 3 engines — verify convergence to optimal engine selection
- [ ] Cost estimator — validate predictions against actuals within 2×
- [ ] 3-engine tournament on a real GitHub issue — verify judge selects best solution
- [ ] All features enabled simultaneously — no conflicts or race conditions

---

## 2. High-Priority Upcoming

---

### 10. Agent Dashboard (Web UI)

**Priority:** High
**Scope:** Large (new service)
**Dependencies:** Real-time streaming (done), TaskRun store (done)

A web dashboard for real-time agent observability and control.

**Approach options:**
- **Grafana-based** — Prometheus metrics + Loki logs + custom panels. Lowest effort,
  read-only. Good for internal ops teams.
- **Custom UI** — Go backend + React/Next.js. Interactive approve/cancel/retry, live
  streaming via SSE/WebSocket. More effort but purpose-built.
- **Hybrid** — Grafana for metrics + thin custom UI for interactive actions.

**Minimum viable features:**
- [ ] Real-time TaskRun status view (queued, running, succeeded, failed, needs-human)
- [ ] Live streaming progress per agent (tool calls, token usage, cost)
- [ ] Task run history with filtering by engine, ticket, status, date range
- [ ] Cost tracking dashboard (per-task, per-engine, daily/weekly aggregates)
- [ ] Manual controls: approve, cancel, retry
- [ ] Engine health overview

**If custom UI:**
- [ ] `cmd/robodev-dashboard/` — separate binary or embedded in controller
- [ ] `/api/v1/` REST endpoints: `GET /taskruns`, `POST /taskruns/:id/approve`,
  `GET /taskruns/:id/stream` (SSE)
- [ ] Frontend: React + Tailwind or equivalent

**If Grafana-based:**
- [ ] Dashboard JSON provisioning in `charts/robodev/dashboards/`
- [ ] Loki log aggregation for structured slog output
- [ ] Alert rules for cost velocity, stalled agents, failed tasks

---

## 3. Design-First — ADR Required Before Implementation

These items have a clear problem and rough direction but need a design document or ADR
agreed before writing code.

---

### 24. Non-Standard Task Types (Analysis, Reporting, Review)

**Priority:** Medium
**Scope:** Large (controller, prompt builder, execution spec)
**Dependencies:** Task profiles (partially implemented), prompt builder

Tasks like "review open MRs and report which need approval" do not fit the standard
clone-fix-push-MR flow. They need read-only execution and a ticket comment + notification
as output rather than a merge request.

**Design questions before implementation:**

1. **Execution mode taxonomy**: `clone_push_mr` (today) | `read_only` (no git clone) |
   `api_read` (no workspace, just SCM API access)
2. **Result handler taxonomy**: `open_mr` (today) | `comment_and_notify` (post summary as
   ticket comment + notify channels)
3. **Profile dispatch**: label-based (`robodev:analysis`) or story-type-based?
4. **Prompt design**: what system prompt makes a read-only analysis task produce a
   well-structured summary?

**Rough sketch (validate design first):**
- Extend `TaskProfileConfig` with `ExecutionMode` and `ResultHandler` fields
- Update `BuildExecutionSpec` in all engines to skip git clone for `read_only` mode
- Add `result_handler` dispatch in `handleJobComplete`
- Update prompt builder to inject different system prompt per execution mode

---

### 25. Supervisor Agent (or: PRM V2 with Strategic Oversight)

**Priority:** Medium
**Scope:** Medium–Large
**Dependencies:** PRM (`internal/prm/`), agentstream `Forwarder`, `internal/llm/`

The watchdog detects quantitative failure modes and terminates. The PRM scores each tool
call and nudges. Neither can reason about *whether the agent is pursuing the right approach*.

A supervisor adds LLM-based qualitative oversight: "you're correctly implementing a cache
but the ticket asked for pagination."

**Key design question — standalone package vs PRM V2:**

The PRM already has `StreamEventProcessor`, sliding window, hint file writer, and
intervention logic. The cleanest path is probably to extend the PRM `Evaluator` with an
optional LLM scoring backend (PRM V2) rather than a parallel `internal/supervisor/` package.

**Resolve before implementation:**
1. PRM V2 extension vs standalone `internal/supervisor/` — pick one
2. Full task description + codebase context, or only recent event window?
3. Can it trigger `NeedsHuman`, or only write hints?
4. Anti-thrashing: how to avoid over-correcting an on-track agent?
5. What constitutes "severely off-track" vs what PRM escalation already handles?

---

## 4. Infrastructure

---

### 9. Plugin SDKs (Python, Go, TypeScript)

**Priority:** Medium
**Scope:** Large (separate repositories)
**Dependencies:** Protobuf definitions (complete in `proto/`)

- [ ] Configure `buf.gen.yaml` for multi-language stub generation
- [ ] **Python SDK** (`unitaryai/robodev-plugin-sdk-python`) — gRPC stubs, base classes,
  `scaffold`/`serve`/`test` CLI, example plugins
- [ ] **Go SDK** (`unitaryai/robodev-plugin-sdk-go`) — gRPC stubs (separate module),
  hashicorp/go-plugin boilerplate, example plugins
- [ ] **TypeScript SDK** (`unitaryai/robodev-plugin-sdk-ts`) — gRPC stubs, grpc-js wrapper,
  example plugins
- [ ] Publish SDK documentation to `docs/plugins/`

---

### 11. Documentation Site *(In Progress)*

**Priority:** High
**Framework:** MkDocs Material

- [x] Landing page, getting started, architecture overview, configuration reference
- [x] Engine guides, plugin development guide, security model, deployment guides
- [x] Search + dark mode
- [ ] API reference (webhook endpoints, protobuf service definitions)
- [ ] Changelog and migration guides
- [ ] CI/CD pipeline for automatic deployment on merge to main
- [ ] Custom domain (`docs.robodev.dev` or similar)
- [ ] `make docs-serve` / `make docs-build` targets

---

## 5. Completed

Everything below is implemented and merged.

---

### Active Integration — Phase 5 ✅

E2E workflow pipeline tests with fake-agent binary:

| Test | Validates | Status |
|------|-----------|--------|
| `TestWorkflowHappyPath` | Ticket → K8s Job → NDJSON stream → `StateSucceeded` + result | ✅ |
| `TestWorkflowJobFailure` | Non-zero exit → retry exhaustion → `StateFailed` + `MarkFailed` | ✅ |
| `TestWorkflowEngineChainFallback` | Primary engine fails → fallback engine completes → `StateSucceeded` | ✅ |
| `TestWorkflowPRMHintDelivery` | Looping agent triggers PRM nudge intervention and logs | ✅ |
| `TestWorkflowWatchdogTermination` | Cost-thrashing agent terminated by watchdog → `StateFailed` | ✅ |
| `TestWorkflowSequentialTasksMemory` | Memory extracted after task 1; injected into task 2 prompt | ✅ |
| `TestWorkflowTournamentEndToEnd` | 2 candidates + 1 judge → judge selects winner → `MarkComplete` | ✅ |

Infrastructure added:
- `hack/fake-agent/` — standalone Go module; `Dockerfile` (scratch, UID 10000)
- `make fake-agent-image` / `make fake-agent-load` / `make e2e-workflow-test` / `make e2e-workflow-test-verbose`

Test suite hardening (post-merge fixes):
- Job list queries filtered by `LabelTaskRunID` label to prevent stale-job count failures across runs
- `workflowFakeEngine.BuildExecutionSpec` now calls `BuildPrompt` (mirrors real engine pattern) so `MemoryContext` is correctly captured in `TestWorkflowSequentialTasksMemory`
- `make e2e-workflow-test` switched to quiet mode (failures only) + `-count=1` to prevent cached results

---

### Active Integration — Phase 4 ✅

SQLite persistence, security hardening, LLM V2 upgrades, and integration tests:

| Subsystem | Status |
|-----------|--------|
| SQLite persistence — routing (`SQLiteFingerprintStore`) | ✅ WAL mode, upserts, persistence tests |
| SQLite persistence — estimator (`SQLiteEstimatorStore`) | ✅ kNN similarity in Go, persistence tests |
| SQLite persistence — watchdog (`SQLiteProfileStore`) | ✅ composite PK `(repo_pattern, engine, task_type)` |
| Memory graph tenant isolation | ✅ `ListNodes`/`DeleteNode` tenant params; cross-tenant `SaveEdge` rejected; adversarial tests |
| Diagnosis prompt injection defence | ✅ `sanitiseForPrompt`; XML delimiters in retry builder |
| Tournament judge prompt injection defence | ✅ `CANDIDATE-DIFF-BEGIN/END` comment markers |
| Config validation | ✅ `validate.go`; `Config.Validate()` called in `Load()` |
| Rate-limited LLM client | ✅ `RateLimitedClient` with configurable RPS |
| PRM LLM scorer V2 | ✅ `LLMScorer` with `ChainOfThought` + V1 fallback |
| Memory LLM extractor V2 | ✅ `LLMExtractor` merging LLM + V1 results |
| Diagnosis LLM analyser V2 | ✅ `LLMAnalyser` with failure-mode validation + V1 fallback |
| Integration tests — 8 subsystem scenarios | ✅ `tests/integration/subsystems_test.go` |
| Integration tests — all-features smoke test | ✅ `tests/integration/all_features_test.go` |

---

### Active Integration — Phase 3 ✅

Tournament coordinator, PRM hint file writer:

| Subsystem | Status |
|-----------|--------|
| Tournament coordinator wiring (item 18) | ✅ `launchTournament` / `handleCandidateComplete` / `launchJudge` / `handleJudgeComplete` |
| PRM hint file writer | ✅ `writeHintFile` via K8s exec; `cleanupHintFile`; `validateHintPath` |

---

### Near-Term Items — All Complete ✅

| # | Feature | Notes |
|---|---------|-------|
| 21 | Transcript Storage & Audit Log | `TranscriptSink` interface; local filesystem sink; wired into controller agentstream |
| 22 | Multi-SCM Backend Routing | `internal/scmrouter` package; host-pattern routing; backward-compatible config |
| 23 | Skills, Subagents & Per-Task MCP Plugins | `Skill` + `SkillEnvVars`; base64 env var delivery via `setup-claude.sh` |

---

### Active Integration — Phase 2 ✅

Diagnosis, calibration, routing, estimator, SCM router, and transcript all wired into the
live controller and `main.go`:

| Subsystem | Status |
|-----------|--------|
| Causal diagnosis (item 14) | ✅ Wired — `WithDiagnosis`; enriched retry prompts in `handleJobFailed` |
| Adaptive watchdog calibration (item 15) | ✅ Wired — `WithWatchdog` + `WithWatchdogCalibration`; `ConsumeStreamEvent` in stream reader |
| Intelligent routing (item 16) | ✅ Wired — `WithIntelligentSelector`; `RecordOutcome` after every terminal run |
| Predictive cost estimation (item 17) | ✅ Wired — `WithEstimator`; auto-reject in `ProcessTicket`; `RecordOutcome` after terminal run |
| Multi-SCM routing (item 22) | ✅ Wired — `WithSCMRouter`; `cfg.SCM.Backends` array in `main.go` |
| Transcript storage (item 21) | ✅ Wired — `WithTranscriptSink`; `Append` in stream reader; `Flush` on completion |
| Bug fix: `launchRetryJob` | ✅ Same-engine retries were transitioning to `StateRetrying` with no new job created |

---

### Active Integration — Phase 1 ✅

| Subsystem | Notes |
|-----------|-------|
| Phase 1 agent log filtering | `LoggingEventProcessor` in `agentstream/` |
| Multi-workflow Shortcut (item 4 from post-testing plan) | `workflows:` array; per-story state resolution |
| Code review config (item 7) | `code_review.enabled` guard in controller |
| PRM — Real-Time Agent Coaching (item 12) | Integrated; V2 (LLM scoring) pending |
| Episodic Memory (item 13) | Integrated; V2 (LLM extraction) pending |

---

### Original 13-Item Improvements Plan — 12/13 complete

| # | Feature | Status |
|---|---------|--------|
| 1 | Webhook Receiver & Event-Driven Ingestion | ✅ |
| 2 | Agent Sandbox Integration (gVisor / Warm Pools) | ✅ |
| 3 | OpenCode Execution Engine | ✅ |
| 4 | Cline CLI Execution Engine | ✅ |
| 5 | Shortcut.com Ticketing Backend | ✅ |
| 6 | Telegram Notification Channel | ✅ |
| 7 | Linear Ticketing Backend | ✅ |
| 8 | Discord Notification Channel | ✅ |
| 9 | HashiCorp Vault Secrets Backend | ✅ |
| 10 | Task-Scoped Secret Resolution | ✅ |
| 11 | NetworkPolicy & Security Hardening | ✅ |
| 12 | Plugin SDKs (Python, Go, TypeScript) | 🚧 In progress |
| 13 | Local Development Mode (Docker Compose) | ✅ |

---

## Summary Table

| # | Feature | Priority | Status |
|---|---------|----------|--------|
| — | Tournament coordinator wiring | High | ✅ Complete |
| — | PRM hint file writer | High | ✅ Complete |
| — | SQLite persistence (routing, estimator, calibrator) | Medium | ✅ Complete |
| — | LLM V2 upgrades (PRM, memory, diagnosis, judge) | Medium | ✅ Complete |
| — | Security hardening | High | ✅ Complete |
| — | E2E workflow suite (fake-agent, 7 tests) | High | ✅ Complete |
| — | E2E live-backend validation (initial 2 tests) | High | ✅ Complete |
| — | E2E live-backend extended coverage (50+ tasks) | High | In progress |
| 20 | PR/MR Comment Response | High | ✅ Complete |
| 10 | Agent Dashboard | High | Not started |
| 24 | Non-Standard Task Types | Medium | Design doc required |
| 25 | Supervisor Agent / PRM V2 | Medium | Design doc required |
| 9 | Plugin SDKs | Medium | 🚧 In progress |
| 11 | Documentation Site | High | **In progress** |
| 21 | Transcript Storage & Audit Log | High | ✅ Complete |
| 22 | Multi-SCM Backend Routing | High | ✅ Complete |
| 23 | Skills, Subagents & Per-Task MCP Plugins | Medium | ✅ Complete |
| 14 | Causal Diagnosis (Self-Healing Retry) | High | ✅ Complete |
| 15 | Adaptive Watchdog Calibration | High | ✅ Complete |
| 16 | Engine Fingerprinting + Routing | Medium | ✅ Complete |
| 17 | Predictive Cost Estimation | Medium | ✅ Complete |
| 18 | Competitive Execution (Tournament) | Medium | ✅ Complete |
| 19 | Shortcut webhook state filtering | Low | ✅ Complete |
