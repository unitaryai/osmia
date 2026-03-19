# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.7] - 2026-03-19

### Fixed

- Agent pods were stuck in `ContainerCreating` indefinitely because the
  Claude Code engine hardcoded the API key secret name as `osmia-anthropic-key`,
  while the actual Kubernetes Secret is named `osmia-anthropic`. Added an
  `APIKeySecret` field to `EngineConfig` so the secret name is configurable.
  Wired the value from `engines.claude-code.auth.api_key_secret` in the Helm
  values via a new `baseEngineConfig` helper in the controller, which is used
  at all job creation sites (initial, retry, continuation, fallback, follow-up,
  tournament). The engine falls back to `osmia-anthropic-key` if no override
  is configured.

## [0.3.6] - 2026-03-19

### Fixed

- Controller service account was missing `persistentvolumeclaims` RBAC
  permissions. The `per-taskrun-pvc` session store calls the K8s API to create
  PVCs but the ClusterRole only granted access to `jobs`, `pods`, `configmaps`,
  and `secrets`. Added `get`, `list`, `watch`, `create`, `delete` on
  `persistentvolumeclaims`.

## [0.3.5] - 2026-03-18

### Fixed

- Agent pods were stuck in `Pending` because the per-TaskRun session PVC was
  never created before the K8s Job referenced it. `PerTaskRunPVCStore.Prepare`
  was implemented but never called. The controller now holds a `sessionStore`
  reference (wired via `WithSessionStore`) and calls `prepareSession` before
  every `BuildExecutionSpec` call — covering initial jobs, retries,
  continuations, fallbacks, review follow-ups, and tournament candidates.

## [0.3.4] - 2026-03-18

### Fixed

- Helm chart now defaults to `Recreate` deployment strategy instead of
  `RollingUpdate`. With a `ReadWriteOnce` PVC mounted, rolling updates cause
  a `Multi-Attach` error because the new pod starts before the old one releases
  the volume. `Recreate` terminates the old pod first. Configurable via
  `deploymentStrategy` in values.

## [0.3.3] - 2026-03-17

### Fixed

- Helm chart defaulted `leaderElection.enabled` to `true`, passing
  `--leader-elect=true` to the controller binary which does not support that
  flag. This caused the pod to exit immediately on startup. Changed default to
  `false` with a comment noting leader election is not yet implemented (see
  `docs/roadmap.md`).

## [0.3.2] - 2026-03-17

### Fixed

- Helm chart version was stuck at 0.2.0 — `Chart.yaml` was not bumped for
  v0.3.0 or v0.3.1, so the chart-releaser never published new versions to
  the Helm repository. ArgoCD and `helm pull` could not find the chart.
  `Chart.yaml` now tracks the release version.

## [0.3.1] - 2026-03-16

### Fixed

- Agent NetworkPolicy now matches agent pods. Both `JobBuilder` and
  `SandboxBuilder` add `app.kubernetes.io/component: agent` and
  `app.kubernetes.io/managed-by: osmia` labels to Jobs and pod templates,
  matching the selectors in `networkpolicy-agent.yaml`. Previously the
  NetworkPolicy deployed but matched zero pods. (#12)

## [0.3.0] - 2026-03-13

### Added

#### User-Prompted Continuation on Turn Exhaustion

When a Claude Code agent exhausts `--max-turns`, the controller can now pause
and ask the operator (via Slack) whether to continue or stop, rather than
auto-retrying or failing silently.

How it works:

- The controller detects turn exhaustion (`ToolCallsTotal >= ConfiguredMaxTurns`)
  at job completion.
- The TaskRun transitions to `NeedsHuman` with gate type `continuation`.
- An approval request is sent to the Slack approval channel with **Continue**
  and **Stop** buttons, including the turn count, cost, and any progress summary.
- On approval, a new pod resumes the session via `--resume <session-id>`
  (requires session persistence). `ContinuationCount` is incremented; `RetryCount`
  is not affected.
- On rejection, the TaskRun transitions to `Failed` with the operator's username
  and progress summary recorded in the failure reason.
- The Slack webhook handler now recognises `"stop"` as a rejection value
  (alongside `"reject"` and `"deny"`).

New configuration fields:

```yaml
engines:
  claude-code:
    continuation_prompt: true    # opt-in (default false)
    max_continuations: 3         # maximum operator approvals per TaskRun (default 3)
```

New `TaskRun` fields: `ContinuationCount`, `MaxContinuations`, `ConfiguredMaxTurns`.

Requires: session persistence enabled + an approval backend configured.

#### Session Persistence for Agent Continuations

Retry pods can now resume Claude Code sessions with full conversation history via
`--resume <session-id>` instead of starting fresh and relying on git diffs alone.

Three storage backends are available:

- **`shared-pvc`** — a single ReadWriteMany PVC with per-TaskRun subdirectories (simplest to operate)
- **`per-taskrun-pvc`** — a dynamically created/deleted PVC per TaskRun (stronger isolation)
- **`s3`** — stub; full implementation pending init-container support in ExecutionSpec

`~/.claude/` (conversation history) is persisted, and the workspace can also be
persisted depending on the configured backend, allowing retry pods to skip the
git-clone step when the workspace is retained.

New configuration:

```yaml
engines:
  claude-code:
    session_persistence:
      enabled: true
      backend: shared-pvc
      pvc_name: osmia-agent-sessions
      ttl_minutes: 1440
```

New Helm values: `sessionPersistence.enabled`, `sessionPersistence.backend`,
`sessionPersistence.sharedPVC`, `sessionPersistence.perTaskRunPVC`, `sessionPersistence.ttlMinutes`.

New package: `internal/sessionstore` (`SharedPVCStore`, `PerTaskRunPVCStore`,
`S3Store`, `Cleaner`).
New interface: `pkg/engine.SessionStore`.

New field: `Task.TaskRunID` — isolates session storage per execution attempt so
retries of the same ticket do not share session data.

New field: `Task.SessionID` — set by the controller on retry jobs so the engine
knows to use `--resume` rather than `--session-id`.

New field: `TaskRun.SessionID` — stores the session ID assigned to the first job.

New event type: `agentstream.SystemEvent` — parses the system init event emitted
by Claude Code at startup, capturing the session ID for belt-and-braces tracking.

New field: `VolumeMount.PVCName` — allows the jobbuilder to back a volume with a
PersistentVolumeClaim instead of emptyDir or ConfigMap.

### Breaking Changes

#### Removal of `no_session_persistence` flag

The `no_session_persistence` configuration field has been removed from
`ClaudeCodeEngineConfig` and `EngineConfig`. The `--no-session-persistence` CLI
flag was closed as NOT_PLANNED by Anthropic and removed from the Claude Code CLI,
so passing it causes the agent to error.

**Migration:** Remove `no_session_persistence: true` from any `osmia-config.yaml`
files. Session persistence is now disabled by default and must be explicitly
opted in via `session_persistence.enabled: true`.

### Changed

#### Project Renamed from RoboDev to Osmia

The project has been renamed from **RoboDev** to **Osmia** (the mason bee genus — solitary but incredibly efficient builders). All identifiers, paths, and references have been updated throughout the codebase:

- Go module path: `github.com/unitaryai/robodev` → `github.com/unitaryai/osmia`
- Kubernetes label domain: `robodev.io/` → `osmia.io/`
- Environment variable prefix: `ROBODEV_` → `OSMIA_`
- Prometheus metrics namespace: `robodev_` → `osmia_`
- Container image paths: `ghcr.io/unitaryai/robodev/` → `ghcr.io/unitaryai/osmia/`
- Binary name: `robodev` → `osmia`
- Helm chart: `charts/robodev/` → `charts/osmia/`
- Python SDK package: `robodev-plugin-sdk` / `from robodev.plugin` → `osmia-plugin-sdk` / `from osmia.plugin`
- TypeScript SDK package: `@unitaryai/robodev-plugin-sdk` → `@unitaryai/osmia-plugin-sdk`
- Config file: `robodev-config.yaml` → `osmia-config.yaml`
- Hint file: `.robodev-hint.md` → `.osmia-hint.md`

### Added

#### AWS Secrets Manager Backend

- **Built-in AWS Secrets Manager secrets backend.** New `aws-secrets-manager` backend (`pkg/plugin/secrets/awssm/`) reads secrets from AWS Secrets Manager via the AWS SDK v2 default credential chain. Supports IRSA on EKS, cross-account access via STS AssumeRole, configurable cache TTL (default 5 minutes), and the `secret-name#json-field` key format. URI scheme: `aws-sm://`. Configured via `secret_resolver.backends` in `robodev-config.yaml`.

#### Session Continuation After Max Turns

- **Retry jobs now continue from prior work rather than starting from scratch.** When a Claude Code agent hits `--max-turns` and the pod exits, the next retry agent clones the branch that was pushed during the previous session instead of re-cloning from the default branch.
- **Deterministic branch naming.** The base prompt now instructs the agent to create and push to `osmia/<ticket-id>` throughout execution, committing frequently rather than waiting until the end. This branch name is predictable even if `result.json` was never written (e.g. pod killed before the stop hook ran).
- **`Task.PriorBranchName` field.** The `engine.Task` struct has a new `PriorBranchName` field. When set, `BuildPrompt` emits a `## Continuation` section that clones that branch with `--depth=50` and asks the agent to review prior commits before continuing.
- **`launchRetryJob` branch propagation.** The controller now sets `PriorBranchName` from `tr.Result.BranchName` when available, or falls back to `osmia/<ticketID>` for retries where the pod was killed before writing `result.json`.

#### Configurable Max Turns for Claude Code

- **`max_turns` is now configurable** for the Claude Code engine. Set `engines.claude_code.max_turns` in your config to override the default of 50 turns. Increase this for tasks that require more steps, such as large refactors or multi-file changes. The value is passed directly to Claude Code's `--max-turns` flag.

#### ConfigMap-Backed Skills

- **Skills can now be loaded from Kubernetes ConfigMaps.** Set `configmap` (and optionally `key`) on a skill config entry instead of `inline` or `path`. The ConfigMap is volume-mounted into the agent container and copied to `~/.claude/skills/` at startup. This avoids bloating the controller config with large Markdown files and allows teams to manage skills independently.

#### Sub-Agent Support

- **New `sub_agents` configuration** for Claude Code sub-agents. Sub-agents are lightweight helpers within a single session, using the official `--agents` flag format (`{"name": {"description":"...", ...}}`). Supports inline prompts and ConfigMap-backed prompts (mounted to `~/.claude/agents/`). Full feature set: model selection, tool allow/deny lists, permission modes, max turns, skills, and background mode. This is a separate feature from agent teams (which spawn multiple independent instances).

#### ConfigMap Volume Support

- **`engine.VolumeMount` now supports ConfigMap sources.** New `ConfigMapName`, `ConfigMapKey`, and `SubPath` fields allow the jobbuilder to emit ConfigMap volume sources instead of emptyDir. This is the foundation for ConfigMap-backed skills and sub-agents.

#### Proto/Plugin Consistency

- **`proto/scm.proto` updated to match Go interface.** Added `ListReviewComments`, `ReplyToComment`, `ResolveThread`, and `GetDiff` RPCs with their request/response message types. Third-party SCM plugins written against the protobuf API can now implement the full v2 contract.
- **Plugin host now validates interface versions at load time.** `LoadPlugin` checks the plugin's declared `interface_version` against the controller's expected version before spawning the subprocess. Mismatched plugins are rejected immediately with a structured error instead of silently loading.

#### Agent Teams Fixed and Wired

- **Agent teams are now properly wired in the controller.** Previously, `WithTeamsConfig` was never called in `main.go`. Agent teams now correctly set `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`, `CLAUDE_CODE_MAX_TEAMMATES`, and pass `--teammate-mode` to the Claude CLI. The incorrect `--agents` flag (which is a sub-agents feature, not an agent teams feature) has been removed. Agent teams and sub-agents are separate, complementary features that can be used simultaneously.

#### Documentation

- **Skills and agent teams documented.** The engine reference (`docs/plugins/engines.md`) now covers custom skills (inline and image-bundled), agent teams configuration (modes, default agents per task type), and all previously-undocumented Claude Code fields (`fallback_model`, `tool_whitelist`, `tool_blacklist`, `json_schema`, `no_session_persistence`, `append_system_prompt`). The configuration reference (`docs/getting-started/configuration.md`) includes expanded examples. The `setup-claude.sh` startup flow is also described.

#### Approval Flow End-to-End

- **Full approval flow wiring.** Slack approval buttons (`osmia_approval_{taskRunID}_{i}`) are now routed through a new `ApprovalHandler` interface on the webhook server, which delegates to `Reconciler.ResolveApproval`. Pre-start approval launches the job; pre-merge approval completes the task. Rejections transition to Failed and mark the ticket.
- **`RequestApproval` called at both gates.** The controller now calls the approval backend's `RequestApproval` at both the pre-start and pre-merge gates so humans actually receive the notification.
- **`NeedsHuman → Failed` state transition.** The TaskRun state machine now allows rejection from the NeedsHuman state.
- **Watchdog monitors NeedsHuman task runs.** The watchdog loop now iterates NeedsHuman runs (for unanswered-human timeout) and cancels pending approval requests on termination.

#### Intelligent Routing Wired into Engine Selection

- **`IntelligentSelector` is now the primary engine selector** when routing is enabled. It uses the default engine selector as its fallback via a new `SetFallback` method, ensuring cold-start tasks still get a sensible engine order.

#### Real Diff for Code Review Gate

- **`GetDiff` added to `scm.Backend` interface.** GitHub and GitLab SCM backends now implement `GetDiff` using their respective compare APIs. The `baseBranch` parameter is resolved automatically: GitHub uses `HEAD` (the repo's default branch) when empty; GitLab fetches the project's `default_branch` from the API. The review gate fetches the actual diff before calling `ReviewDiff`, instead of passing an empty string. `scm.InterfaceVersion` bumped from 1 to 2 to signal the contract change to external plugins.

#### Runtime Cost Enforcement

- **`max_cost_per_job` watchdog rule.** A new `checkTotalCost` rule in the watchdog terminates jobs whose cumulative cost exceeds `guardrails.max_cost_per_job`. Cost data is fed from the live stream: `CostEvent.CostUSD` is propagated to `TaskRun.CostUSD` via `ConsumeStreamEvent`, and `buildHeartbeat` uses it for each watchdog tick. Falls back to the result-level cost for agents that do not emit live cost events.

- **Slack approval callback failures now return 500.** When `ResolveApproval` fails, the webhook returns a non-2xx status so Slack retries the delivery instead of silently losing the callback.

### Fixed

#### Local Development

- **`make local-up` now boots cleanly on kind without extra Helm overrides.** The local controller image is built and loaded under the chart's `ghcr.io/unitaryai/osmia/controller` repository, and the dev Helm overlay now leaves ticketing unconfigured so the controller falls back to the built-in noop backend for credential-free smoke testing.

#### Security / Correctness

- **Slack approval action ID mismatch resolved.** The webhook previously filtered `osmia_approve_*` / `osmia_reject_*` prefixes, which never matched the actual `osmia_approval_*` format generated by the Slack approval backend. Callbacks are now correctly detected, parsed, and routed.

- **GitHub polling no longer picks up pull requests.** The `/issues` endpoint returns both issues and pull requests. Items with a non-nil `pull_request` field are now filtered out before the polling backend emits tickets, preventing Osmia from treating a labelled PR as a task.

- **Webhook trigger labels auto-derived from ticketing config.** When `webhook.github.trigger_labels` is not explicitly set and `ticketing.backend` is `"github"`, the webhook now falls back to the `labels` list from the GitHub ticketing backend config. Deployments that do not use the GitHub polling backend must set `trigger_labels` explicitly if label gating is desired.

- **Webhook adapter propagates processing errors.** `HandleWebhookEvent` now returns the first error encountered rather than always returning `nil`. This causes the webhook server to respond with a non-2xx status so senders (GitHub, GitLab) will retry the delivery.

- **Code review gate is now actually a gate.** When `code_review.enabled: true` and the review backend returns `passed: false`, the TaskRun is now transitioned to `Failed` and the ticket is marked failed. Previously the controller logged the outcome and then unconditionally succeeded the run.

- **Intelligent routing no longer panics on cold start.** `IntelligentSelector.SelectEngines` previously called `s.fallback.SelectEngines` with a nil receiver when fingerprint data was insufficient. A nil-fallback guard now returns the available engine list in order instead of panicking.

- **Routing advertises only configured engines.** The `availableEngines` list in the routing initialisation was built by ranging over a `map[string]bool` without checking the boolean value, so unconfigured engine names were always included. Only engines whose configuration is non-nil (or whose name matches `engines.default`) are now advertised.

- **Label removal is now URL-safe.** `removeLabel` in the GitHub ticketing backend now uses `url.PathEscape` on the label name, preventing broken DELETE calls for labels with spaces or special characters.

- **Removed unused `RequireHumanApprovalBeforeMR` config field.** Approval uses `ApprovalGates` instead.

#### Documentation

- Corrected wrong config keys in README and getting-started guides: `max_cost_per_task_usd` → `max_cost_per_job`, `max_duration_minutes` → `max_job_duration_minutes`.
- Fixed `runAsUser: 1000` → `10000` in security docs.
- Changed "Seven layered" → "Six layered" in README; removed prompt injection from layers list (it is a threat, not a layer).
- Moved PRM from "Coming Soon" to implemented in guardrails overview.
- Added note that engine hooks only apply to Claude Code.
- Added note that only the `memory` TaskRun store backend is currently supported.
- Fixed secondary docs overclaiming guardrails.md injection and task-profile file-pattern enforcement: `architecture.md`, `security.md`, `concepts/guardrails-overview.md`, `getting-started/kubernetes.md` all now accurately describe layers 3 & 4 as advisory/on-roadmap.
- Fixed `docs/scaling.md` contradiction: KEDA note no longer claims leader election is active; multi-tenancy section notes that namespace-per-tenant runtime isolation is on the roadmap.
- Fixed `docs/plugins/secrets.md` overclaiming: "Third-party plugins are available" replaced with accurate status. 1Password and Azure KV backends are not implemented; documented as planned or community-implementable. Added built-in Vault backend documentation (was undocumented). Added External Secrets Operator integration guide for AWS Secrets Manager.
- Added built-in AWS Secrets Manager backend to roadmap (`docs/roadmap.md`).
- Fixed Slack webhook API docs: action ID prefix corrected from `osmia_approve_` to `osmia_approval_`; clarified that approval callbacks are routed to the approval handler, not forwarded as tickets.
- Fixed `docs/concepts/engines.md`: tournament warning no longer calls `max_predicted_cost_per_job` an "approval gate" — it is an auto-rejection threshold.

---

## [0.2.0] — 2026-03-05

### Added

#### Helm Chart Persistence

- New `persistence` values block — when `persistence.enabled: true`, a PVC is
  provisioned and mounted at `/data` for SQLite stores (memory, routing, watchdog).
  Uses the cluster default storage class; override via `persistence.storageClass`.
- Memory and routing subsystems now survive pod restarts when persistence is enabled.

#### Memory and PRM in Live Deployments

- `memory` and `prm` subsystems are now enabled via `values-live-local.yaml` and
  confirmed working end-to-end against a real kind cluster.
- Controller now logs `memory context injected into prompt` (with fact/insight/issue
  counts) when prior knowledge is retrieved and passed to an agent.
- Controller logs `memory extraction completed` with node and edge counts after each
  successful task run.

#### Shortcut: Configurable Completed State

- New `completed_state_name` config key for the Shortcut backend. When set, `MarkComplete`
  transitions stories to that named state (e.g. `"Ready for Review"`) rather than
  always using the first done-type state in the workflow.

### Fixed

#### Helm Chart RBAC

- Added `pods/exec` with `create` verb to the controller `ClusterRole`. Previously the
  PRM hint file injection and cleanup would fail with a 403 — this is now resolved.

### Changed

#### CI Workflow

- Added `workflow_dispatch` trigger to the CI workflow so it can be manually run
  against any branch without requiring a push event.

### Documentation

- **Configuration Reference** — expanded ticketing section with full config reference
  for GitHub Issues, Shortcut, and Linear, including all keys and field tables.
- **Plugins / Ticketing** — added Shortcut and Linear built-in backend sections with
  configuration examples, behaviour tables, and permissions notes.
- **Setup Guide: Linear + Slack** — new step-by-step guide covering API key creation,
  team ID lookup, label setup, secrets, config, deploy, and troubleshooting.
- **Setup Guide: Shortcut + Slack** — added `completed_state_name`, `exclude_labels`,
  and multi-workflow tip.
- **Roadmap** — moved Item 20 to completed section; marked documentation CI/CD items done;
  removed stale note about intelligence subsystems being unwired.
- Fixed broken anchor link in `index.md` pointing to the Competitive Execution section.

---

### Added

#### PR/MR Comment Response (Item 20)

- **`pkg/plugin/scm`** — `ReviewComment` type; `ListReviewComments`,
  `ReplyToComment`, and `ResolveThread` added to the `Backend` interface.
- **`pkg/plugin/scm/github`** — GitHub REST implementation: `ListReviewComments`
  merges review-comment and issue-comment endpoints sorted by creation time;
  `ReplyToComment` tries the review reply endpoint and falls back to issue comment;
  `ResolveThread` is a documented no-op (GitHub REST does not support thread resolution).
- **`pkg/plugin/scm/gitlab`** — GitLab REST implementation: `ListReviewComments`
  fetches notes with `system: false` filter; `ReplyToComment` resolves discussion ID
  then posts a discussion note; `ResolveThread` calls `PUT …/discussions/{id}` with
  `resolved: true`.
- **`internal/reviewpoller`** — new package implementing the review response polling
  subsystem:
  - `types.go` — `Classification` enum, `ClassifiedComment`, `TrackedPR`, `FollowUpRequest`.
  - `classifier.go` — `RuleBasedClassifier` (bot-author ignore list, keyword tiers for
    informational/warning/error) and `LLMClassifier` (ChainOfThought with rule-based
    fallback; disabled by default).
  - `poller.go` — `Poller` with `Register`, `DrainFollowUps`, and `Start` (background
    ticker); routes SCM calls via `scmBackend` or `scmRouter`; enforces `MaxFollowUpJobs`
    per PR; optionally replies to comments acknowledging receipt.
- **`internal/config`** — `ReviewResponseConfig` struct with `enabled`,
  `poll_interval_minutes`, `min_severity`, `max_follow_up_jobs`, `reply_to_comments`,
  `resolve_threads`, `llm_classifier`; validation rejects invalid `min_severity` values.
- **`internal/taskrun`** — `ParentTicketID`, `ReviewCommentID`, `ReviewThreadID`,
  `ReviewPRURL` fields on `TaskRun` for follow-up lifecycle tracking.
- **`internal/controller`** — `reviewPoller` field, `WithReviewPoller` option,
  `handleFollowUpComplete` (posts ticket comment, replies to review comment, resolves
  thread), `processFollowUpTask` (builds and launches a K8s follow-up Job), and
  `scmFor` helper; drain in `reconcileOnce`; register in `handleJobComplete`.
- **`cmd/osmia/main.go`** — review response subsystem wiring under `review_response.enabled`.
- **`tests/integration/review_response_test.go`** — 9 integration tests covering
  bot-ignore, requires-action, informational, follow-up emission, processed-ID
  idempotency, max-follow-up limit, merged-PR untracking, reply-on-action, and
  config validation.

#### E2E Workflow Pipeline Tests

- **`hack/fake-agent/`** — standalone Go module with a pure-stdlib fake agent binary.
  Reads `OSMIA_SCENARIO` and emits a pre-scripted NDJSON event stream, then exits with the
  appropriate code. Scenarios: `success`, `loop`, `thrash`, `fail`, `tournament_a`,
  `tournament_b`, `judge`. Built into a scratch container image (UID 10000, read-only
  root FS) via `hack/fake-agent/Dockerfile`.
- **`tests/e2e/workflow_helpers_test.go`** — shared helpers for workflow E2E tests:
  `workflowFakeEngine`, `workflowSecondEngine`, `mockWorkflowTicketing`,
  `testLogWriter`, and utility functions (`ensureNamespace`, `waitForTicketComplete`,
  `waitForTicketFailed`, `runReconcilerInBackground`, etc.).
- **`tests/e2e/workflow_test.go`** — seven end-to-end workflow tests that exercise the
  full pipeline against a real kind cluster (real K8s Jobs, real pod log streaming):
  - `TestWorkflowHappyPath` — ticket → Job → NDJSON stream → `StateSucceeded`
  - `TestWorkflowJobFailure` — non-zero exit → retry exhaustion → `StateFailed`
  - `TestWorkflowEngineChainFallback` — primary fails → fallback engine succeeds
  - `TestWorkflowPRMHintDelivery` — looping agent triggers PRM nudge intervention
  - `TestWorkflowWatchdogTermination` — cost-thrashing agent terminated by watchdog
  - `TestWorkflowSequentialTasksMemory` — episodic memory injected into second task
  - `TestWorkflowTournamentEndToEnd` — 2 candidates + 1 judge → winner selected
- **Makefile** — new targets `fake-agent-image`, `fake-agent-load`, and
  `e2e-workflow-test` (`FAKE_AGENT_IMAGE` variable; runs `TestWorkflow*` suite).

#### SQLite Persistence for Routing, Estimator, and Watchdog Stores

- **`internal/routing/sqlite.go`** — `SQLiteFingerprintStore` implementing `FingerprintStore`;
  schema: `fingerprints(engine_name PK, data_json, updated_at)`; uses a shadow JSON struct to
  avoid serialising the embedded `sync.RWMutex` in `EngineFingerprint`.
- **`internal/estimator/sqlite.go`** — `SQLiteEstimatorStore` implementing `EstimatorStore`;
  schema: `prediction_outcomes` with index on `engine`; kNN similarity search runs in Go using
  Euclidean distance over the complexity feature vector.
- **`internal/watchdog/sqlite.go`** — `SQLiteProfileStore` implementing `ProfileStore`;
  schema: `calibrated_profiles` with composite primary key `(repo_pattern, engine, task_type)`.
- All three stores use WAL mode, `INSERT OR REPLACE` upserts, and `modernc.org/sqlite` (no CGO).
- Config: `StoragePath` added to `RoutingConfig` and `EstimatorConfig`; `CalibrationStorePath`
  added to `AdaptiveCalibrationConfig`. When empty the existing in-memory store is used.
- `cmd/osmia/main.go` conditionally constructs SQLite stores when the path is non-empty.
- Unit tests (`sqlite_test.go` in each package) cover save/get round-trips, upsert semantics,
  full-scan `List`, and persistence across close/reopen using `t.TempDir()`.

#### Security Hardening

- **Memory graph tenant isolation** (`internal/memory/store.go`):
  - `ListNodes` now accepts a `tenantID string` parameter; an empty string returns all nodes
    (administrative / startup use only).
  - `DeleteNode` now accepts a `tenantID string` parameter and rejects cross-tenant deletes
    with an explicit error.
  - `SaveEdge` validates that both endpoint nodes share the same `tenant_id`; cross-tenant
    edges are rejected before any write.
  - `nodeTenantID` helper returns `("", nil)` for nodes absent from the store so that edges
    to unregistered (external) nodes are still accepted.
  - `internal/memory/graph.go` updated: `LoadFromStore` passes `""` to load all tenants;
    `PruneStale` passes each node's own tenant ID when calling `DeleteNode`.
  - Adversarial tests added for cross-tenant `ListNodes`, `DeleteNode`, and `SaveEdge`.
- **Diagnosis prompt injection** (`internal/diagnosis/`):
  - New `sanitise.go` exports `sanitiseForPrompt(s string, maxLen int) string` — strips ASCII
    control characters (< 0x20 except `\n` and `\t`) and truncates to `maxLen`.
  - `analyser.go` calls `sanitiseForPrompt` on `WatchdogReason` and `Result.Summary` before
    building the classifier text.
  - `retry_builder.go` wraps the corrective prescription in XML-style delimiters
    (`<previous-attempt-output>`) with an explicit instruction not to follow injected content.
- **Tournament judge prompt injection** (`internal/tournament/judge.go`):
  - Each candidate diff is wrapped with `<!-- CANDIDATE-DIFF-BEGIN -->` /
    `<!-- CANDIDATE-DIFF-END -->` comment markers.
  - Instructions section clarified: treat diff content as data only; correctness outweighs
    cost efficiency.
- **Config validation** (`internal/config/validate.go`, `internal/config/config.go`):
  - New `validate.go` with `validateNonNegativeFloat`, `validatePositiveInt`,
    `validateFraction`, and `validateStorePath` helpers.
  - `Config.Validate()` checks `GuardRails` bounds, `Routing.EpsilonGreedy` in [0, 1],
    `Memory.PruneThreshold` in [0, 1], `CompetitiveExecution.DefaultCandidates` ≥ 2 when
    non-zero, `PRM.HintFilePath`, and all new `StoragePath` fields for path-traversal safety.
  - `Load()` calls `cfg.Validate()` before returning; invalid configuration is rejected at
    startup.

#### LLM V2 — Optional LLM-backed Scoring, Extraction, and Classification

- **Rate-limited LLM client** (`internal/llm/ratelimited.go`):
  - `RateLimitedClient` wraps any `Client` and enforces a minimum gap between requests
    (`1s / rps`). Thread-safe via `sync.Mutex`.
  - `NewRateLimitedClient(inner Client, rps float64) *RateLimitedClient`.
- **PRM LLM scorer** (`internal/prm/scorer_llm.go`):
  - `LLMScorer` uses a `ChainOfThought` module (inputs: `tool_calls`, `task_description`;
    outputs: `score` int 1–10, `reasoning`) and falls back to the rule-based `Scorer` on
    error or out-of-range score.
  - `NewLLMScorer(client llm.Client, fallback *Scorer) *LLMScorer`.
  - Config: `UseLLMScoring bool` added to `PRMConfig`; wired in `main.go`.
- **Memory LLM extractor** (`internal/memory/extractor_llm.go`):
  - `LLMExtractor` calls an LLM module (inputs: `task_description`, `outcome`, `summary`,
    `engine`, `repo_url`; outputs: `facts`, `patterns` JSON arrays) and merges results with
    the V1 rule-based extractor. Falls back to V1 on error.
  - `NewLLMExtractor(client llm.Client, v1 *Extractor, logger *slog.Logger) *LLMExtractor`.
  - Config: `UseLLMExtraction bool` added to `MemoryConfig`; wired in `main.go`.
- **Diagnosis LLM analyser** (`internal/diagnosis/analyser_llm.go`):
  - `LLMAnalyser` classifies failure modes via LLM (inputs: `watchdog_reason`,
    `result_summary`, `tool_call_count`, `files_changed`; outputs: `failure_mode`,
    `confidence`, `evidence`). Unknown mode strings trigger fallback to the rule-based
    `Analyser`.
  - `NewLLMAnalyser(client llm.Client, fallback *Analyser, logger *slog.Logger) *LLMAnalyser`.
  - Config: `UseLLMClassification bool` added to `DiagnosisConfig`; wired in `main.go`.

#### Integration Tests (`tests/integration/`)

- **`subsystems_test.go`** (`//go:build integration`) — 8 scenario tests:
  1. `TestPRMInterventions` — 10 identical tool calls → `ActionNudge` returned.
  2. `TestMemoryAccumulation` — extract from 10 fake task runs → `QueryForTask` returns non-empty context.
  3. `TestWatchdogCalibrationConverges` — 15 observations → `RefreshProfile` → calibrated
     thresholds differ from defaults.
  4. `TestDiagnosisOnLoopingCalls` — looping tool calls → `ModelConfusion` diagnosis; retry
     prompt contains injection-defence delimiters.
  5. `TestRoutingConvergence` — 20 outcomes (claude-code 90%, aider 25%) → `SelectEngines`
     picks claude-code as primary ≥ 8/10 times.
  6. `TestCostEstimatorAccuracy` — 10 outcomes → `Predict` returns cost within 3× of mean.
  7. `TestTournamentFlow` — `StartTournament` → 2× `OnCandidateComplete` → `BeginJudging` →
     `SelectWinner` → asserts `StateSelected`.
  8. `TestTournamentJudgeInjectionDefense` — prompt contains `CANDIDATE-DIFF-BEGIN` markers and
     injection instruction.
- **`all_features_test.go`** (`//go:build integration`) — `TestAllFeaturesEnabled` smoke test:
  wires PRM + memory + watchdog + routing + estimator + diagnosis + transcript + tournament
  into a `Reconciler`, processes two tickets, and asserts no panics with all task runs
  reaching a terminal state.

#### PRM Hint File Writer (`internal/controller/controller.go`)

- `writeHintFile(ctx, taskRunID, content string) error` — delivers PRM hint content directly
  to the running agent pod via the Kubernetes exec API (`remotecommand.NewSPDYExecutor`).
  Uses `tee` rather than a shell script to avoid injection. Caches pod names from the stream
  reader to avoid repeated pod lookups.
- `cleanupHintFile(ctx, taskRunID string)` — best-effort `rm -f` of the hint file on task
  completion (5 s timeout, errors are logged but not propagated).
- `cleanupPodName(taskRunID string)` — removes the cached pod name entry after task completion.
- `validateHintPath(path string) error` — rejects paths containing `..` components to prevent
  path traversal; called before every exec operation.
- `recordPRMHint` updated to deliver hints asynchronously (10 s timeout goroutine) instead of
  only logging.
- `buildK8sClient()` in `main.go` now returns `*rest.Config` alongside the client;
  `WithRestConfig(cfg)` option passes it to the controller.

#### Tournament Coordinator Wiring (`internal/controller/controller.go`, `cmd/osmia/main.go`)

- `ProcessTicket` now detects tournament-eligible tasks: when `tournamentCoordinator` is set,
  `competitive_execution.enabled=true`, `default_candidates >= 2`, and `len(engines) >= 2`,
  it calls `launchTournament` instead of launching a single job.
- `launchTournament(ctx, ticket)` — creates N parallel candidate `TaskRun`s + K8s Jobs,
  registers the tournament with `tournament.Coordinator.StartTournament`, records role/ID
  maps, and starts stream readers for claude-code candidates. The first candidate is stored
  under the standard idempotency key (`ticketID-1`) to prevent double-processing.
- `handleCandidateComplete(ctx, tr, tournamentID)` — transitions the candidate to Succeeded,
  calls `OnCandidateComplete`, and triggers `launchJudge` when the early-termination threshold
  is met. Lagging candidates' stream readers are cancelled before judging begins.
- `launchJudge(ctx, tournamentID)` — pre-generates the judge task run ID, calls `BeginJudging`
  atomically (preventing duplicate judges under concurrent candidate completions), builds a
  judge prompt via `tournament.JudgePromptBuilder`, and launches the judge K8s Job.
- `handleJudgeComplete(ctx, tr, tournamentID)` — parses a `JudgeDecision` JSON object from the
  judge's result summary (extracting the first `{...}` block to tolerate surrounding prose),
  calls `SelectWinner`, marks the ticket complete with the winner's result, and sends
  notifications. Defaults to candidate 0 when parsing fails.
- `handleJobComplete` and `handleJobFailed` both dispatch via `taskRunRole` map to the above
  handlers before normal flow; both call `cleanupHintFile` and `cleanupPodName` on completion.
- `WithTournamentCoordinator(c)` option wired in `main.go` when
  `config.CompetitiveExecution.Enabled` is true.
- `go.sum` updated for `github.com/gorilla/websocket`, `github.com/moby/spdystream`, and
  `github.com/mxk/go-flowrate` (transitive deps of `k8s.io/client-go/tools/remotecommand`).

#### Item 23 — Skills, Subagents, and Per-Task MCP Plugins

**Custom skills for Claude Code** (`pkg/engine/claudecode/skills.go`, `pkg/engine/claudecode/engine.go`)
- New `Skill` struct with `Name`, `Inline`, and `Path` fields
- `SkillEnvVars(skills []Skill) map[string]string` converts skills to container environment
  variables: inline skills are base64-encoded into `CLAUDE_SKILL_INLINE_<NAME>`; path-based
  skills use `CLAUDE_SKILL_PATH_<NAME>`
- `WithSkills(skills []Skill)` functional option on `ClaudeCodeEngine`; skill env vars are
  injected into `ExecutionSpec.Env` during `BuildExecutionSpec`

**Configuration** (`internal/config/config.go`)
- New `SkillConfig` struct with `Name`, `Path`, `Inline` fields added to
  `ClaudeCodeEngineConfig` as `Skills []SkillConfig`
- `MCPServers []string` added to `TaskProfileConfig` for future per-profile MCP server overrides

**Container startup** (`docker/engine-claude-code/setup-claude.sh`)
- `setup-claude.sh` now reads `CLAUDE_SKILL_INLINE_*` and `CLAUDE_SKILL_PATH_*` env vars and
  writes the corresponding Markdown files to `~/.claude/skills/<name>.md` before starting
  the agent, making them available as `/skill-name` invocations

**Main wiring** (`cmd/osmia/main.go`)
- `cfg.Engines.ClaudeCode.Skills` translated to `claudecode.Skill{}` slice and passed to
  `claudecode.New(claudecode.WithSkills(...))` at startup

**Tests** (`pkg/engine/claudecode/skills_test.go`, `engine_test.go`)
- 8 unit tests covering inline encoding, path skills, mixed skills, empty skill handling,
  base64 round-trip, env var key generation, and execution spec integration

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
- `cmd/osmia/main.go` `initShortcutBackend` reads the `workflows` array from the Shortcut config map and calls `WithWorkflowMappings`; the legacy `workflow_state_name` / `in_progress_state_name` keys continue to work unchanged
- New tests: multi-mapping Init, multi-workflow poll merge, overlap deduplication, mapping selection in `MarkInProgress`

#### Configurable Code Review Gate (`internal/config/config.go`, `internal/controller/controller.go`)
- New `CodeReviewConfig` struct with `enabled`, `backend`, `wait_for_comments`, `timeout_minutes`, `token_secret` fields; added as `code_review:` top-level key in `Config`
- Controller `handleJobComplete` now respects `config.CodeReview.Enabled`: when `false` (the default) no review wait occurs; when `true` and a `reviewBackend` is wired, calls `ReviewDiff` with a configurable timeout (default 15 minutes) and logs the outcome before proceeding
- New `ShortcutWorkflow` struct (`trigger_state`, `in_progress_state`) exported from `internal/config/` for typed config parsing

#### Roadmap (`docs/roadmap.md`)
- Added Phase K (near-term) items 21–23: Transcript Storage & Audit Log, Multi-SCM Backend Routing, Skills/Subagents/Per-Task MCP Plugins
- Added Phase L (longer-term) items 24–25: Non-Standard Task Types (requires design doc), Supervisor Agent (requires design doc)
- Updated summary table

#### All Plugin Backends Wired into Controller (`cmd/osmia/main.go`)
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
- **Startup wiring test** added (`cmd/osmia/init_test.go`, 20 tests) — calls every init function directly with a fake Kubernetes client to verify that all supported backend strings reach their init function rather than falling through to the unsupported-backend error branch

#### Shortcut Backend Wired into Controller (`cmd/osmia/main.go`)
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
- `InterventionDecider` triggers soft nudges (hint file at `/workspace/.osmia-hint.md`) or watchdog escalation based on score thresholds and trajectory patterns
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
- Ticket label override (`osmia:engine:<name>`) for per-ticket engine selection
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
- `TeamsConfig` with enabled, mode, and max_teammates
- `TeamsFlags` generating `--teammate-mode` CLI flag
- `TeamsEnvVars` setting `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS` and `CLAUDE_CODE_MAX_TEAMMATES`
- `WithTeamsConfig` functional option on Claude Code engine
- Team coordination prompt section in `BuildPromptWithTeams`

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
- Slack approval callback parsing (`osmia_approve_*` / `osmia_reject_*` actions)
- `ApprovalGates` and `ApprovalCostThresholdUSD` in guard rails config

#### Local Development Mode (Docker Compose)
- DockerBuilder (`internal/jobbuilder/docker.go`) implementing `controller.JobBuilder` for local Docker execution — produces K8s Job objects annotated with `osmia.io/execution-backend: local`
- Builder selection in `cmd/osmia/main.go`: reads `execution.backend` config to choose between standard ("job"), sandbox, or local Docker builder
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
- Slack webhook handler now parses `osmia_approve_*` and `osmia_reject_*` action IDs from approval callbacks, extracting task run IDs and logging structured approval/rejection events (stub for future resolution wiring)
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
- Webhook server wired into `cmd/osmia/main.go` with graceful shutdown support
- New `WebhookConfig` in controller configuration for per-source secrets

#### Task-Scoped Secret Resolution
- Secret resolver (`internal/secretresolver/`) parsing `<!-- osmia:secrets -->` HTML comment blocks and `osmia:secret:` label prefixes
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
- Added client-side label exclusion to prevent re-pickup of in-progress and failed issues (default: `in-progress`, `osmia-failed`)
- New functional options: `WithAssignee`, `WithMilestone`, `WithState`, `WithExcludeLabels`
- Labels filter is now optional — omitting it enables assignee-only or milestone-only workflows
- Refactored `PollReadyTickets` URL construction to use `url.Values` for safer query parameter encoding

#### Live End-to-End Testing
- Wired up `cmd/osmia/main.go` with full backend initialisation: K8s client, GitHub ticketing, Claude Code engine, job builder, and Slack notifications
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
- Grafana dashboard JSON (charts/osmia/dashboards/)
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
- Four newcomer-facing concept pages: What is Osmia?, TaskRun Lifecycle, Engines Explained, Guard Rails Overview
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
