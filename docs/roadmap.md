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
1. Near-Term (bounded, unblocked)     — items 21, 22, 23
2. Active Integration (wire it in)    — Phase I: diagnosis, routing, estimator, tournament, watchdog
3. High-Priority Upcoming             — items 20 (PR/MR comments), 10 (dashboard)
4. Design-First (ADR before code)     — items 24, 25
5. Infrastructure                     — items 9 (plugin SDKs), 11 (docs — in progress)
```

---

## 1. Near-Term — Bounded, Unblocked

These were identified during live testing. Designs are clear and all dependencies
are already in place.

---

### 21. Transcript Storage & Audit Log

**Priority:** High
**Scope:** Medium (4–6 files)
**Dependencies:** agentstream `Forwarder` (event pipeline already exists)

Agent transcripts are currently ephemeral pod logs — once the K8s Job is GC'd they are
gone. Add a `TranscriptSink` interface that buffers NDJSON events and flushes to object
storage (S3, GCS, or local filesystem) on completion.

**Design:** `oss-plan.md §3.6` already describes this pattern. Implement as a
`StreamEventProcessor` registered on the `Forwarder`.

- [ ] Add `TranscriptSink` interface in `pkg/plugin/transcript/transcript.go`
  - `Append(event *StreamEvent) error`
  - `Flush(ctx context.Context, taskRunID string) error`
- [ ] `pkg/plugin/transcript/local/local.go` — local filesystem sink; dev/test and Docker Compose mode
- [ ] `pkg/plugin/transcript/s3/s3.go` — S3-compatible object storage (AWS S3, MinIO, Ceph)
- [ ] `pkg/plugin/transcript/gcs/gcs.go` — Google Cloud Storage
- [ ] Register sink as a `StreamEventProcessor`; buffer events in-memory, flush on result event or explicit `Flush` call
- [ ] Wire into controller: create per-TaskRun sink in `startStreamReader`, call `Flush` in `handleJobComplete` and `handleJobFailed`
- [ ] Add `AuditConfig` to `internal/config/config.go`:
  ```yaml
  audit:
    transcript_storage:
      backend: s3          # s3 | gcs | local | disabled
      bucket: robodev-transcripts
      prefix: "transcripts/{year}/{month}/{task_run_id}/"
      credentials_secret: aws-credentials
  ```
- [ ] Unit tests: mock sink; verify all event types are buffered and flushed correctly
- [ ] Integration test: run a task with local sink, verify transcript file is written

**Key files:** `pkg/plugin/transcript/`, `internal/agentstream/forwarder.go`,
`internal/controller/controller.go`, `internal/config/config.go`

---

### 22. Multi-SCM Backend Routing (GitLab + GitHub simultaneously)

**Priority:** High
**Scope:** Medium (3–5 files)
**Dependencies:** Both `pkg/plugin/scm/github/` and `pkg/plugin/scm/gitlab/` already implement the same interface

Today the controller holds a single `scmBackend`. Teams that use GitLab for private
repos and GitHub for public repos cannot configure both simultaneously. The `RepoURL`
field on every ticket provides the host needed to route to the correct backend.

**Design:** Replace the single `scmBackend` field on the `Reconciler` with a `SCMRouter`
that selects the correct backend by matching the ticket's `RepoURL` against a configured
host pattern.

- [ ] Add `SCMBackendConfig` and update `SCMConfig` in `internal/config/config.go`:
  ```yaml
  scm:
    backends:
      - backend: gitlab
        match: "gitlab.com"     # exact host or glob pattern
        config:
          token_secret: gitlab-token
      - backend: github
        match: "github.com"
        config:
          token_secret: github-token
  ```
- [ ] Create `internal/scmrouter/` package with `Router` struct:
  - `For(repoURL string) (scm.Backend, error)` — selects backend by matching URL host
    against each entry's `match` pattern (exact host or `filepath.Match`-style glob)
  - Falls back to the first configured backend if no match
- [ ] Update `Reconciler` to hold `scmRouter *scmrouter.Router` instead of `scmBackend scm.Backend`
- [ ] Update `cmd/robodev/main.go` to initialise multiple backends and construct the router
- [ ] Backward compatibility: single `backend` + `config` at the top level still works
  (treated as a single-entry backends list)
- [ ] Unit tests for `scmrouter.Router.For` (exact match, glob match, no match fallback, empty URL)
- [ ] Integration test: two-backend config, verify correct backend selected per URL

**Key files:** `internal/scmrouter/`, `internal/config/config.go`,
`internal/controller/controller.go`, `cmd/robodev/main.go`

---

### 23. Skills, Subagents, and Per-Task MCP Plugins ✅

**Priority:** Medium
**Scope:** Small–Medium (3–4 files)
**Dependencies:** Claude Code engine (`pkg/engine/claudecode/`)

- [x] Add `SkillConfig` struct + `Skills []SkillConfig` to `ClaudeCodeEngineConfig` in
  `internal/config/config.go`. Both `path` (bundled on image) and `inline` modes supported.
- [x] Wire skills into `BuildExecutionSpec` via `SkillEnvVars()`: inline skills are
  base64-encoded into `CLAUDE_SKILL_INLINE_<NAME>`; path skills use `CLAUDE_SKILL_PATH_<NAME>`.
  `setup-claude.sh` decodes and writes them to `~/.claude/skills/<name>.md` at startup.
- [x] Add `MCPServers []string` to `TaskProfileConfig` — field added for operator config;
  full runtime merging with the workspace MCP config is tracked as a separate task.
- [x] Unit tests for `SkillEnvVars` (inline, path, multi-skill, empty, encoding round-trip)
  and skill env var injection tests in `engine_test.go`.

**Pending (integration test + MCP merging):**
- [ ] Integration test: run a job with skills configured, verify `~/.claude/skills/*.md` appear
- [ ] Per-profile MCP server merging: extend `setup-claude.sh` to append profile servers to
  the workspace MCP config when `ROBODEV_MCP_SERVERS` env var is set

**Key files:** `internal/config/config.go`, `pkg/engine/claudecode/skills.go`,
`pkg/engine/claudecode/engine.go`, `docker/engine-claude-code/setup-claude.sh`,
`cmd/robodev/main.go`

---

## 2. Active Integration — Wire the Scaffolded Features In

Five packages are fully scaffolded (types, logic, unit tests, integration tests) but not
yet wired into the live controller. PRM and Memory are already integrated and serve as
the reference pattern.

**Reference pattern** (already done for PRM and Memory):
1. Add field + functional option to `Reconciler`
2. Wire into the appropriate lifecycle hook (`ProcessTicket`, `handleJobFailed`, etc.)
3. Initialise in `cmd/robodev/main.go` behind a config flag
4. E2E test

### Controller Wiring Checklist

- [ ] Add `calibrator`, `analyser`, `intelligentSelector`, `estimator`, `tournamentCoordinator` fields to `Reconciler`
- [ ] Add functional options: `WithCalibrator`, `WithDiagnosis`, `WithRouting`, `WithEstimator`, `WithTournament`
- [ ] Wire estimator into `ProcessTicket` — run prediction before approval gate, surface in `HumanQuestion`
- [ ] Wire routing — replace `DefaultEngineSelector` with `IntelligentSelector` when `config.Routing.Enabled`
- [ ] Wire diagnosis into `handleJobFailed` — run before retry/fallback decision, use enriched prompt
- [ ] Wire calibrator recording into `handleJobComplete`/`handleJobFailed`
- [ ] Wire tournament coordinator into `ProcessTicket` for tournament-eligible tasks

### Main Entrypoint Wiring Checklist

- [ ] Initialise `watchdog.Calibrator` when `config.ProgressWatchdog.AdaptiveCalibration.Enabled`
- [ ] Initialise `diagnosis.Analyser` when `config.Diagnosis.Enabled`
- [ ] Initialise `routing.IntelligentSelector` + `routing.MemoryFingerprintStore` when `config.Routing.Enabled`
- [ ] Initialise `estimator.Predictor` + `estimator.MemoryEstimatorStore` when `config.Estimator.Enabled`
- [ ] Initialise `tournament.Coordinator` when `config.CompetitiveExecution.Enabled`

### PRM Hint File Writer

The PRM already decides to write hints; the actual file delivery to the agent pod is not
yet implemented.

- [ ] Create volume writer that accesses the shared workspace PVC
- [ ] Write `HintContent` to `/workspace/.robodev-hint.md` (or configured `hint_file_path`)
- [ ] Handle concurrent writes (multiple PRM evaluations for the same TaskRun)
- [ ] Clean up hint files on task completion

### Persistence Layer

Replace in-memory stores with durable persistence.

- [ ] Implement `routing.SQLiteFingerprintStore` (reuse memory's SQLite DB)
- [ ] Implement `estimator.SQLiteEstimatorStore` (reuse memory's SQLite DB)
- [ ] Persist calibrator observations (SQLite table)
- [ ] Verify `memory.SQLiteStore` handles concurrent writes, migration idempotency, corruption recovery
- [ ] Test data survives controller restarts

### LLM Integration (V2 upgrades)

Replace rule-based heuristics with LLM-powered reasoning. `internal/llm/` is complete —
this is prompt engineering work.

- [ ] **PRM V2**: scoring prompt — given recent tool calls, rate productivity 1–10 with
  reasoning. Iterate on real agent transcripts until reliable.
- [ ] **Memory V2**: extraction prompt — given TaskRun data, extract structured facts.
  Handle empty results, hallucinated facts, duplicate knowledge.
- [ ] **Diagnosis V2**: classification prompt — given failure transcript, classify failure
  mode and generate prescription. Must resist prompt injection from agent output.
- [ ] **Tournament Judge**: judging prompt — given N diffs, select best with reasoning.
  Test with real side-by-side diffs.
- [ ] Rate limiting for LLM scoring calls (avoid overwhelming the API during active TaskRuns)

### Security Hardening

- [ ] PRM hint file path — verify path traversal is impossible (`../` in configured path)
- [ ] Memory graph tenant isolation — adversarial tests: tenant A cannot read tenant B's facts
- [ ] Diagnosis templates — verify agent output cannot escape templates into injected prompts
- [ ] Tournament judge prompt — verify candidate diffs cannot inject instructions into the judge
- [ ] LLM scoring prompts — verify agent stream events cannot manipulate PRM scores
- [ ] Config validation — reject negative thresholds, path traversal in file paths

### End-to-End Tests

- [ ] PRM with live Claude Code agent — verify scoring and interventions fire at correct thresholds
- [ ] Memory across 50+ tasks — verify accumulation, decay, and prompt injection
- [ ] Adaptive watchdog for 15+ tasks — verify calibration reduces false positives
- [ ] Diagnosis on intentionally failing tasks — correct classification, enriched retry
- [ ] Routing across 20+ tasks on 3 engines — verify convergence to optimal selection
- [ ] Cost estimator — validate predictions against actuals within 2×
- [ ] 3-engine tournament on a real GitHub issue — verify judge selects best solution
- [ ] All 7 features enabled simultaneously — no conflicts or race conditions

---

## 3. High-Priority Upcoming

---

### 20. PR/MR Comment Response (GitHub + GitLab)

**Priority:** High
**Scope:** Large (10+ files)
**Dependencies:** SCM backends (already built), TaskRun store, controller reconciler

After RoboDev opens a pull/merge request, reviewers (human and AI agents — CodeRabbit,
Copilot Review, Gemini Code Assist) may leave comments. This feature enables RoboDev to
monitor those comments and create targeted follow-up jobs to address actionable feedback,
turning a single-pass agent into a review-responsive loop.

- [ ] Extend SCM plugin interface:
  - `ListReviewComments(ctx, prURL) ([]ReviewComment, error)`
  - `ReplyToComment(ctx, prURL, commentID, body string) error`
  - `ResolveThread(ctx, prURL, threadID string) error`
- [ ] Implement for GitHub — REST API (`/pulls/{pr}/comments`, `/pulls/{pr}/reviews`)
- [ ] Implement for GitLab — REST API (`/merge_requests/{iid}/notes`, discussions API)
- [ ] New `internal/reviewpoller/` — monitors open PRs created by RoboDev (tracked in TaskRunStore)
- [ ] Comment classifier via `internal/llm/` — ignore / informational / requires-action
- [ ] Follow-up task generator: create new TaskRun with original description + comment context
- [ ] Reply-and-resolve: post acknowledgement comment, call `ResolveThread` via SCM backend
- [ ] Config section `review_response`: `enabled`, `min_severity`, `max_follow_up_jobs`, `poll_interval_minutes`
- [ ] Integration tests with mocked SCM backends
- [ ] E2E test: open PR → add comment → verify follow-up job created → verify thread resolved

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
- **Hybrid** — Grafana for metrics + thin custom UI for interactive actions. Best of both.

**Minimum viable features:**
- [ ] Real-time TaskRun status view (queued, running, succeeded, failed, needs-human)
- [ ] Live streaming progress per agent (tool calls, token usage, cost)
- [ ] Task run history with filtering by engine, ticket, status, date range
- [ ] Cost tracking dashboard (per-task, per-engine, daily/weekly aggregates)
- [ ] Manual controls: approve, cancel, retry
- [ ] Engine health overview

**If custom UI:**
- [ ] `cmd/robodev-dashboard/` — separate binary or embedded in controller
- [ ] `/api/v1/` REST endpoints: `GET /taskruns`, `POST /taskruns/:id/approve`, `GET /taskruns/:id/stream` (SSE)
- [ ] Frontend: React + Tailwind or equivalent

**If Grafana-based:**
- [ ] Dashboard JSON provisioning in `charts/robodev/dashboards/`
- [ ] Loki log aggregation for structured slog output
- [ ] Alert rules for cost velocity, stalled agents, failed tasks

---

## 4. Design-First — ADR Required Before Implementation

These items have a clear problem and rough direction but need a design document or ADR
agreed before writing code. The scope and approach are non-trivial enough that getting
the design wrong would be costly to undo.

---

### 24. Non-Standard Task Types (Analysis, Reporting, Review)

**Priority:** Medium
**Scope:** Large (requires controller, prompt builder, execution spec changes)
**Dependencies:** Task profiles (partially implemented), prompt builder

Tasks like "review open MRs and report which need approval" do not fit the standard
clone-fix-push-MR flow. They need read-only execution and a ticket comment + notification
as output rather than a merge request.

**Design questions before implementation:**

1. **Execution mode taxonomy**: `clone_push_mr` (today) | `read_only` (no git clone) | `api_read` (no workspace, just SCM API access)
2. **Result handler taxonomy**: `open_mr` (today) | `comment_and_notify` (post summary as ticket comment + notify channels)
3. **Profile dispatch**: label-based (`robodev:analysis`) or story-type-based?
4. **Prompt design**: what system prompt makes a read-only analysis task produce a well-structured summary?

**Rough implementation sketch (draft only — validate design first):**

- Extend `TaskProfileConfig` with `ExecutionMode string` and `ResultHandler string`
- Update `BuildExecutionSpec` in all engines to skip git clone for `read_only` mode
- Add `result_handler` dispatch in `handleJobComplete`
- Update prompt builder to inject a different system prompt for analysis vs fix tasks

---

### 25. Supervisor Agent (or: PRM V2 with Strategic Oversight)

**Priority:** Medium
**Scope:** Medium–Large
**Dependencies:** PRM (`internal/prm/`), agentstream `Forwarder`, `internal/llm/`

**What it solves that the watchdog and PRM don't:**

The watchdog detects quantitative failure modes — stuck, looping, thrashing — and
terminates. The PRM scores each tool call against productivity heuristics and nudges.
Neither can reason about *whether the agent is pursuing the right approach*.

A supervisor adds LLM-based qualitative oversight: "you're correctly implementing a cache
but the ticket asked for pagination." The agent may be passing every watchdog threshold
while doing the wrong thing entirely.

**Key design question — standalone package vs PRM V2:**

The PRM already has the scaffolding for this: `StreamEventProcessor`, sliding window,
hint file writer, intervention logic. The cleanest implementation is probably to extend
the PRM's `Evaluator` with an optional LLM scoring backend (already noted as "PRM V2"
in the I-6 integration checklist) rather than building a parallel `internal/supervisor/`
package with duplicated infrastructure.

**Resolve before implementation:**

1. PRM V2 extension vs standalone `internal/supervisor/` package — pick one
2. Should the supervisor have access to the full task description + codebase context,
   or only the recent event window?
3. Should it be able to trigger `NeedsHuman` (ask the human) or only write hints?
4. Anti-thrashing mechanism: how do we avoid over-correcting an agent that is actually
   on the right track but taking an unfamiliar path?
5. What does "severely off-track" mean precisely, and how does escalation to the watchdog
   differ from what the PRM's escalation already does?

**Rough design (if standalone — validate against PRM V2 option first):**

- `SupervisorAgent` implements `StreamEventProcessor` — subscribes to agentstream events
- Sliding window of the last N tool calls and their outcomes
- Every M tool calls, sends window to a cheap LLM (Haiku) with a structured prompt
- If off-track: writes steering hint to `/workspace/.robodev-hint.md`
- If severely off-track: escalates to watchdog with diagnosis string
- Budget-enforced via `internal/llm/` (`max_budget_usd` per supervised job)

---

## 5. Infrastructure

---

### 9. Plugin SDKs (Python, Go, TypeScript)

**Priority:** Medium
**Scope:** Large (separate repositories)
**Dependencies:** Protobuf definitions (complete in `proto/`)

Generated SDKs so third-party plugin authors don't need to implement raw gRPC.

- [ ] Configure `buf.gen.yaml` for multi-language stub generation
- [ ] **Python SDK** (`unitaryai/robodev-plugin-sdk-python`) — gRPC stubs, base classes, `scaffold`/`serve`/`test` CLI, example plugins
- [ ] **Go SDK** (`unitaryai/robodev-plugin-sdk-go`) — gRPC stubs (separate module), hashicorp/go-plugin boilerplate, example plugins
- [ ] **TypeScript SDK** (`unitaryai/robodev-plugin-sdk-ts`) — gRPC stubs, grpc-js wrapper, example plugins
- [ ] Publish SDK documentation to `docs/plugins/`

---

### 11. Documentation Site *(In Progress)*

**Priority:** High
**Framework:** MkDocs Material (selected)

- [x] Landing page, getting started, architecture overview, configuration reference
- [x] Engine guides, plugin development guide, security model, deployment guides
- [x] Search + dark mode
- [ ] API reference (webhook endpoints, protobuf service definitions)
- [ ] Changelog and migration guides
- [ ] CI/CD pipeline for automatic deployment on merge to main
- [ ] Custom domain (`docs.robodev.dev` or similar)
- [ ] `make docs-serve` / `make docs-build` targets

---

## 6. Completed

Everything below is implemented and merged.

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

### Strategic Features — Completed

| # | Feature | Notes |
|---|---------|-------|
| 1 | Enhanced Claude Code Engine | Structured output, tool control, model fallback |
| 2 | Real-Time Agent Streaming | NDJSON stream-json; agentstream package |
| 3 | Engine Fallback Chains | Ordered fallback with per-ticket override |
| 4 | Agent Sandbox Integration | gVisor + Kata + warm pools |
| 5 | Multi-Agent Coordination (Phase 1) | In-process Claude Code teams |
| 6 | TDD Workflow Mode | `tdd` and `review-first` workflow modes |
| 7 | Approval Workflows & Audit Trail | pre_start + pre_merge gates; in-memory store |
| 8 | Local Development Mode | Docker Compose + DockerBuilder |
| 12 | PRM — Real-Time Agent Coaching | ✅ Integrated; V2 (LLM scoring) pending |
| 13 | Episodic Memory | ✅ Integrated; V2 (LLM extraction) pending |
| 19 | Shortcut webhook state filtering | `WithShortcutTargetStateID` in webhook handler |

---

## Summary Table

| # | Feature | Priority | Status |
|---|---------|----------|--------|
| 21 | Transcript Storage & Audit Log | High | Not started |
| 22 | Multi-SCM Backend Routing | High | Not started |
| 23 | Skills, Subagents & Per-Task MCP Plugins | Medium | Not started |
| 14 | Causal Diagnosis (Self-Healing Retry) | High | Scaffolding complete · Integration pending |
| 15 | Adaptive Watchdog Calibration | High | Scaffolding complete · Integration pending |
| 16 | Engine Fingerprinting + Routing | Medium | Scaffolding complete · Integration pending |
| 17 | Predictive Cost Estimation | Medium | Scaffolding complete · Integration pending |
| 18 | Competitive Execution (Tournament) | Medium | Scaffolding complete · Integration pending |
| 20 | PR/MR Comment Response | High | Not started |
| 10 | Agent Dashboard | High | Not started |
| 24 | Non-Standard Task Types | Medium | Not started — design doc required |
| 25 | Supervisor Agent / PRM V2 | Medium | Not started — design doc required |
| 9 | Plugin SDKs | Medium | Not started |
| 11 | Documentation Site | High | **In progress** |
