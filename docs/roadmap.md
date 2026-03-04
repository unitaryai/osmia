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
1. Active Integration (remaining gaps)  — SQLite persistence, LLM V2, security hardening, E2E tests
2. High-Priority Upcoming               — items 20 (PR/MR comments), 10 (dashboard)
3. Design-First (ADR before code)       — items 24, 25
4. Infrastructure                       — items 9 (plugin SDKs), 11 (docs — in progress)
```

---

## 1. Active Integration — Remaining Gaps

Phase 2 wired diagnosis, calibration, routing, estimator, SCM router, and transcript into
the live controller. Tournament coordinator and PRM hint writer are also complete. The
following gaps remain before the integration layer is fully production-ready.

---

### Persistence Layer

All learning stores currently use in-memory backends — routing fingerprints, cost
predictions, and calibrator observations are lost on controller restart.

- [ ] `routing.SQLiteFingerprintStore` — reuse memory's SQLite DB and schema pattern
- [ ] `estimator.SQLiteEstimatorStore` — ditto
- [ ] Persist calibrator observations in a SQLite table
- [ ] Verify `memory.SQLiteStore` handles concurrent writes, migration idempotency, and
  corruption recovery
- [ ] Confirm data survives controller restarts (integration test)

**Key files:** `internal/routing/`, `internal/estimator/`, `internal/watchdog/`

---

### LLM Integration — V2 Upgrades

Replace rule-based heuristics with LLM-powered reasoning. `internal/llm/` is complete —
this is prompt engineering + integration work.

- [ ] **PRM V2**: scoring prompt — given recent tool calls, rate productivity 1–10 with
  reasoning; iterate on real agent transcripts until reliable
- [ ] **Memory V2**: extraction prompt — given TaskRun data, extract structured facts;
  handle empty results, hallucinated facts, duplicates
- [ ] **Diagnosis V2**: classification prompt — given failure transcript, classify failure
  mode and generate prescription; must resist prompt injection from agent output
- [ ] **Tournament Judge**: judging prompt — given N diffs, select best with reasoning;
  test with real side-by-side diffs
- [ ] Rate limiting for LLM scoring calls (avoid overwhelming the API during active jobs)

---

### Security Hardening

- [x] PRM hint file path — `validateHintPath` rejects `..` components before every exec
- [ ] Memory graph tenant isolation — adversarial tests: tenant A cannot read tenant B's facts
- [ ] Diagnosis templates — verify agent output cannot escape into injected retry prompts
- [ ] Tournament judge prompt — verify candidate diffs cannot inject instructions into the judge
- [ ] LLM scoring prompts — verify agent stream events cannot manipulate PRM scores
- [ ] Config validation — reject negative thresholds, path traversal in file paths

---

### End-to-End Tests

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

### 20. PR/MR Comment Response (GitHub + GitLab)

**Priority:** High
**Scope:** Large (10+ files)
**Dependencies:** SCM backends (built), TaskRun store (built), controller reconciler

After RoboDev opens a pull/merge request, reviewers (human and AI — CodeRabbit,
Copilot Review, Gemini Code Assist) may leave comments. This feature enables RoboDev to
monitor those comments and spawn follow-up jobs to address actionable feedback, turning a
single-pass agent into a review-responsive loop.

- [ ] Extend SCM plugin interface:
  - `ListReviewComments(ctx, prURL) ([]ReviewComment, error)`
  - `ReplyToComment(ctx, prURL, commentID, body string) error`
  - `ResolveThread(ctx, prURL, threadID string) error`
- [ ] Implement for GitHub — REST API (`/pulls/{pr}/comments`, `/pulls/{pr}/reviews`)
- [ ] Implement for GitLab — REST API (`/merge_requests/{iid}/notes`, discussions API)
- [ ] New `internal/reviewpoller/` — monitors open PRs created by RoboDev (tracked in TaskRunStore)
- [ ] Comment classifier via `internal/llm/` — ignore / informational / requires-action
- [ ] Follow-up task generator: new TaskRun with original description + comment context
- [ ] Reply-and-resolve: post acknowledgement comment, call `ResolveThread` via SCM backend
- [ ] Config: `review_response.enabled`, `min_severity`, `max_follow_up_jobs`, `poll_interval_minutes`
- [ ] Integration tests with mocked SCM backends
- [ ] E2E: open PR → add comment → verify follow-up job created → verify thread resolved

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
| 12 | Plugin SDKs (Python, Go, TypeScript) | ⏳ Not started |
| 13 | Local Development Mode (Docker Compose) | ✅ |

---

## Summary Table

| # | Feature | Priority | Status |
|---|---------|----------|--------|
| — | Tournament coordinator wiring | High | ✅ Complete |
| — | PRM hint file writer | High | ✅ Complete |
| — | SQLite persistence (routing, estimator, calibrator) | Medium | Not started |
| — | LLM V2 upgrades (PRM, memory, diagnosis, judge) | Medium | Not started |
| — | Security hardening | High | Not started |
| — | E2E test suite | High | Not started |
| 20 | PR/MR Comment Response | High | Not started |
| 10 | Agent Dashboard | High | Not started |
| 24 | Non-Standard Task Types | Medium | Design doc required |
| 25 | Supervisor Agent / PRM V2 | Medium | Design doc required |
| 9 | Plugin SDKs | Medium | Not started |
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
