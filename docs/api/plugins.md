# Plugin gRPC API Reference

All RoboDev plugin interfaces are defined as protobuf services in `proto/`.
The proto files are the **source of truth** — generated Go stubs, Python stubs,
and TypeScript stubs are all derived from them. Do not edit generated files
directly; regenerate them with `make sdk-gen` instead.

Plugins communicate with the controller over gRPC using the
[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin) subprocess
model. Each plugin binary is spawned as a child process, connects over a local
Unix socket, and negotiates its interface version via the `Handshake` RPC
before any other calls are made.

For a guide on writing and deploying plugins, see
[Writing a Plugin](../plugins/writing-a-plugin.md).

---

## Services overview

| Service | Interface type | Proto file | Go package |
|---|---|---|---|
| `TicketingBackend` | Ticketing | `proto/ticketing.proto` | `github.com/unitaryai/robodev/proto/v1` |
| `NotificationChannel` | Notifications | `proto/notifications.proto` | `github.com/unitaryai/robodev/proto/v1` |
| `HumanApprovalBackend` | Approval | `proto/approval.proto` | `github.com/unitaryai/robodev/proto/v1` |
| `SecretsBackend` | Secrets | `proto/secrets.proto` | `github.com/unitaryai/robodev/proto/v1` |
| `SCMBackend` | Source control | `proto/scm.proto` | `github.com/unitaryai/robodev/proto/v1` |
| `ReviewBackend` | Code review | `proto/review.proto` | `github.com/unitaryai/robodev/proto/v1` |
| `ExecutionEngine` | Engine | `proto/engine.proto` | `github.com/unitaryai/robodev/proto/v1` |

All services share the package declaration `robodev.v1` and the Go package
`github.com/unitaryai/robodev/proto/v1`.

---

## Handshake pattern

Every service includes a `Handshake` RPC. The controller calls it at startup
and after any plugin restart to negotiate the interface version before making
any functional calls.

```protobuf
rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
```

| Message | Field | Type | Description |
|---|---|---|---|
| `HandshakeRequest` | `interface_version` | `int32` | Version the controller expects. |
| `HandshakeResponse` | `interface_version` | `int32` | Version the plugin implements. |
| `HandshakeResponse` | `plugin_name` | `string` | Human-readable plugin identifier (e.g. `"jira"`). |
| `HandshakeResponse` | `plugin_version` | `string` | Semver version of the plugin binary (e.g. `"1.2.0"`). |

If the plugin's `interface_version` does not match what the controller expects,
the controller refuses to load the plugin, logs a structured error, and marks it
permanently unhealthy. Restarting cannot resolve a version mismatch.

The current `interface_version` for most services is **1**. SCM is version **2** (bumped when `ListReviewComments`, `ReplyToComment`, `ResolveThread`, and `GetDiff` were added).

---

## Shared message types (`proto/common.proto`)

The following types are defined in `proto/common.proto` and referenced by
multiple services.

### `Ticket`

Represents a unit of work from an external issue tracker.

| Field | Type | Description |
|---|---|---|
| `id` | `string` | Unique identifier in the external system (e.g. `"42"`, `"PROJ-123"`). |
| `title` | `string` | Ticket summary or title. |
| `description` | `string` | Full ticket body. May contain markdown. |
| `ticket_type` | `string` | Work classifier (e.g. `"bug_fix"`, `"dependency_upgrade"`). Used to select guard rail profiles. |
| `labels` | `repeated string` | Tags or labels from the external system. |
| `repo_url` | `string` | Repository URL associated with the ticket. |
| `external_url` | `string` | Web URL to view the ticket in the originating system. |
| `raw` | `google.protobuf.Struct` | Original unprocessed data from the external system. |

### `TaskResult`

Structured outcome of an agent execution, written to `/workspace/result.json`
by the engine.

| Field | Type | Description |
|---|---|---|
| `success` | `bool` | Whether the agent completed successfully. |
| `merge_request_url` | `string` | URL of the created pull/merge request, if any. |
| `branch_name` | `string` | Git branch the agent worked on. |
| `summary` | `string` | Human-readable description of what the agent did. |
| `token_usage` | `TokenUsage` | Token consumption for cost accounting. |
| `cost_estimate_usd` | `double` | Estimated cost in US dollars. |
| `exit_code` | `int32` | `0` = success, `1` = agent failure, `2` = guard rail blocked. |

### `TokenUsage`

| Field | Type | Description |
|---|---|---|
| `input_tokens` | `int64` | Number of input tokens consumed. |
| `output_tokens` | `int64` | Number of output tokens consumed. |

---

## `TicketingBackend` (`proto/ticketing.proto`)

Ticketing backends poll external issue trackers for ready tickets and manage
ticket lifecycle transitions throughout the TaskRun state machine. Built-in
backends (GitHub Issues) are compiled into the controller binary. External
backends (Jira, Linear, Shortcut) run as gRPC subprocesses.

### RPCs

| Method | Request | Response | Description |
|---|---|---|---|
| `Handshake` | `HandshakeRequest` | `HandshakeResponse` | Version negotiation at startup. |
| `PollReadyTickets` | `PollReadyTicketsRequest` | `PollReadyTicketsResponse` | Returns tickets labelled and ready for processing. Called on a configurable interval (default 30 s). |
| `MarkInProgress` | `MarkInProgressRequest` | `MarkInProgressResponse` | Transitions the ticket to an in-progress state (labels, assignees, status). Called when the controller creates a Kubernetes Job. |
| `MarkComplete` | `MarkCompleteRequest` | `MarkCompleteResponse` | Transitions the ticket to completed and attaches the execution result. Called on agent success. |
| `MarkFailed` | `MarkFailedRequest` | `MarkFailedResponse` | Transitions the ticket to failed with a human-readable reason. Called on agent failure or watchdog termination. |
| `AddComment` | `AddCommentRequest` | `AddCommentResponse` | Posts a comment on the ticket. Used for progress updates and diagnostic information. |

### Key message types

**`PollReadyTicketsRequest`**

| Field | Type | Description |
|---|---|---|
| `labels` | `repeated string` | Filter to tickets matching all specified labels. |
| `max_results` | `int32` | Maximum tickets per poll (backend default if zero). |

**`PollReadyTicketsResponse`**

| Field | Type | Description |
|---|---|---|
| `tickets` | `repeated Ticket` | Tickets ready for automated processing. |

**`MarkCompleteRequest`**

| Field | Type | Description |
|---|---|---|
| `ticket_id` | `string` | External system identifier. |
| `result` | `TaskResult` | Structured execution outcome. |

**`MarkFailedRequest`**

| Field | Type | Description |
|---|---|---|
| `ticket_id` | `string` | External system identifier. |
| `reason` | `string` | Human-readable failure reason. |

**`AddCommentRequest`**

| Field | Type | Description |
|---|---|---|
| `ticket_id` | `string` | External system identifier. |
| `comment` | `string` | Comment text. Markdown supported if the backend supports it. |

`MarkInProgressResponse`, `MarkCompleteResponse`, `MarkFailedResponse`, and
`AddCommentResponse` are intentionally empty; errors are conveyed via gRPC
status codes.

---

## `NotificationChannel` (`proto/notifications.proto`)

Notification channels are fire-and-forget: they deliver messages to external
systems (Slack, Discord, Teams, email) but do not wait for or handle responses.
For interactive workflows requiring human input, use `HumanApprovalBackend`.

### RPCs

| Method | Request | Response | Description |
|---|---|---|---|
| `Handshake` | `HandshakeRequest` | `HandshakeResponse` | Version negotiation at startup. |
| `Notify` | `NotifyRequest` | `NotifyResponse` | Sends a free-form message associated with a ticket. Used for progress updates and warnings. |
| `NotifyStart` | `NotifyStartRequest` | `NotifyStartResponse` | Signals that the controller has begun processing a ticket (a Kubernetes Job has been created). |
| `NotifyComplete` | `NotifyCompleteRequest` | `NotifyCompleteResponse` | Signals that processing has finished (success or failure). |

### Key message types

**`NotifyRequest`**

| Field | Type | Description |
|---|---|---|
| `message` | `string` | Notification text. May contain markdown. |
| `ticket` | `Ticket` | Ticket context. |

**`NotifyStartRequest`**

| Field | Type | Description |
|---|---|---|
| `ticket` | `Ticket` | Ticket being processed. |
| `task_run_id` | `string` | Unique identifier for this execution attempt. |
| `engine` | `string` | Engine name (e.g. `"claude-code"`, `"codex"`). |

**`NotifyCompleteRequest`**

| Field | Type | Description |
|---|---|---|
| `ticket` | `Ticket` | Ticket that finished processing. |
| `task_run_id` | `string` | Unique identifier for this execution attempt. |
| `result` | `TaskResult` | Structured execution outcome. |

---

## `HumanApprovalBackend` (`proto/approval.proto`)

Approval backends handle bidirectional human-in-the-loop interactions. When an
agent requires human input (clarification, authorisation to proceed), the
controller transitions the TaskRun to `NeedsHuman` and delegates the question
to the approval backend. The backend delivers the question (e.g. via a Slack
interactive message) and the human's response resumes the flow via the webhook
server's Slack endpoint.

### RPCs

| Method | Request | Response | Description |
|---|---|---|---|
| `Handshake` | `HandshakeRequest` | `HandshakeResponse` | Version negotiation at startup. |
| `RequestApproval` | `RequestApprovalRequest` | `RequestApprovalResponse` | Dispatches a question or approval request to a human. Returns once the request is sent, not when the human responds. |
| `CancelPending` | `CancelPendingRequest` | `CancelPendingResponse` | Cancels any outstanding approval requests for a task run. Called when the watchdog terminates a `NeedsHuman` job. |

### Key message types

**`RequestApprovalRequest`**

| Field | Type | Description |
|---|---|---|
| `question` | `string` | Text to present to the human decision-maker. |
| `ticket` | `Ticket` | Ticket context. |
| `task_run_id` | `string` | Used to correlate the human response back to the correct TaskRun. |
| `options` | `repeated string` | Valid response choices. Free-text if empty. |

**`RequestApprovalResponse`**

| Field | Type | Description |
|---|---|---|
| `request_id` | `string` | Backend-specific identifier for this approval request. Used for cancellation and callback correlation. |

**`CancelPendingRequest`**

| Field | Type | Description |
|---|---|---|
| `task_run_id` | `string` | Execution attempt whose pending requests should be cancelled. |

---

## `SecretsBackend` (`proto/secrets.proto`)

Secrets backends abstract over different secret storage systems (Kubernetes
Secrets, HashiCorp Vault, AWS Secrets Manager, 1Password, External Secrets
Operator) to provide a uniform way of injecting credentials into agent
execution containers.

### RPCs

| Method | Request | Response | Description |
|---|---|---|---|
| `Handshake` | `HandshakeRequest` | `HandshakeResponse` | Version negotiation at startup. |
| `GetSecret` | `GetSecretRequest` | `GetSecretResponse` | Retrieves a single secret value by key. |
| `GetSecrets` | `GetSecretsRequest` | `GetSecretsResponse` | Retrieves multiple secrets in one call. More efficient than repeated `GetSecret` calls. |
| `BuildEnvVars` | `BuildEnvVarsRequest` | `BuildEnvVarsResponse` | Translates secret references into Kubernetes-native `EnvVar` specifications, enabling backends to use native `secretKeyRef` where possible. |

### Key message types

**`GetSecretRequest`** / **`GetSecretResponse`**

| Field | Type | Description |
|---|---|---|
| `key` | `string` | Backend-specific secret reference (e.g. `"namespace/secret-name/key"` for Kubernetes, `"secret/data/path#field"` for Vault). |
| `value` | `string` | Resolved secret value. Handle securely; never log. |

**`SecretRef`**

| Field | Type | Description |
|---|---|---|
| `env_name` | `string` | Environment variable name to set in the container. |
| `secret_key` | `string` | Backend-specific secret reference. |

**`EnvVar`** (returned by `BuildEnvVars`)

| Field | Type | Description |
|---|---|---|
| `name` | `string` | Environment variable name. |
| `value` | `string` | Direct value (mutually exclusive with `secret_ref`). |
| `secret_ref` | `SecretKeyRef` | Kubernetes `secretKeyRef` (mutually exclusive with `value`). |

**`SecretKeyRef`**

| Field | Type | Description |
|---|---|---|
| `name` | `string` | Kubernetes Secret name. |
| `key` | `string` | Key within the Secret's data map. |

---

## `SCMBackend` (`proto/scm.proto`)

SCM backends handle git hosting operations — creating branches, opening
pull/merge requests, and querying their status. This abstracts over different
hosting platforms (GitHub, GitLab, Bitbucket) so the controller can manage code
changes without coupling to a specific provider.

### RPCs

| Method | Request | Response | Description | Since |
|---|---|---|---|---|
| `Handshake` | `HandshakeRequest` | `HandshakeResponse` | Version negotiation at startup. | v1 |
| `CreateBranch` | `CreateBranchRequest` | `CreateBranchResponse` | Creates a new git branch in the remote repository. | v1 |
| `CreatePullRequest` | `CreatePullRequestRequest` | `CreatePullRequestResponse` | Opens a pull request (GitHub) or merge request (GitLab) for the agent's changes. | v1 |
| `GetPullRequestStatus` | `GetPullRequestStatusRequest` | `GetPullRequestStatusResponse` | Retrieves the current state of a pull/merge request, including CI and review status. | v1 |
| `ListReviewComments` | `ListReviewCommentsRequest` | `ListReviewCommentsResponse` | Returns all review and general comments on a pull/merge request. | v2 |
| `ReplyToComment` | `ReplyToCommentRequest` | `ReplyToCommentResponse` | Posts a reply to an existing comment. | v2 |
| `ResolveThread` | `ResolveThreadRequest` | `ResolveThreadResponse` | Marks a review thread as resolved. No-op where unsupported (e.g. GitHub REST). | v2 |
| `GetDiff` | `GetDiffRequest` | `GetDiffResponse` | Returns the unified diff between two branches for code review. | v2 |

### Key message types

**`CreatePullRequestRequest`**

| Field | Type | Description |
|---|---|---|
| `repo_url` | `string` | Repository URL. |
| `title` | `string` | Pull request title. |
| `body` | `string` | Pull request description. Markdown supported. |
| `head_branch` | `string` | Source branch containing the changes. |
| `base_branch` | `string` | Target branch. Defaults to repository default if empty. |
| `labels` | `repeated string` | Labels to apply. |
| `draft` | `bool` | Create as a draft if `true`. |

**`GetPullRequestStatusResponse`**

| Field | Type | Description |
|---|---|---|
| `state` | `PullRequestState` | `OPEN`, `CLOSED`, `MERGED`, or `DRAFT`. |
| `mergeable` | `bool` | Whether the PR can be merged (no conflicts, all checks passing). |
| `ci_status` | `CIStatus` | `PENDING`, `PASSING`, or `FAILING`. |
| `review_status` | `ReviewStatus` | `PENDING`, `APPROVED`, or `CHANGES_REQUESTED`. |
| `pull_request_url` | `string` | Web URL of the pull/merge request. |

---

## `ReviewBackend` (`proto/review.proto`)

Review backends integrate with code review systems (CodeRabbit, native
LLM-based review, custom pipelines) to provide quality gates that evaluate
agent-produced diffs before they are merged. The quality gate is deliberately
read-only: it reviews output but cannot modify the repository.

### RPCs

| Method | Request | Response | Description |
|---|---|---|---|
| `Handshake` | `HandshakeRequest` | `HandshakeResponse` | Version negotiation at startup. |
| `ReviewDiff` | `ReviewDiffRequest` | `ReviewDiffResponse` | Submits a diff for code review. May return an immediate verdict (synchronous backends) or a `review_id` for a follow-up poll (asynchronous backends). |
| `GetGateResult` | `GetGateResultRequest` | `GetGateResultResponse` | Retrieves the quality gate result for a previously submitted review. |

### Key message types

**`ReviewDiffRequest`**

| Field | Type | Description |
|---|---|---|
| `task_run_id` | `string` | Execution attempt identifier. |
| `ticket` | `Ticket` | Ticket context. |
| `diff` | `string` | Unified diff produced by the agent. Primary review input. |
| `branch_name` | `string` | Git branch containing the changes. |
| `repo_url` | `string` | Repository URL. |
| `result` | `TaskResult` | Structured execution result from the agent. |

**`GateResult`**

| Field | Type | Description |
|---|---|---|
| `passed` | `bool` | Whether the diff meets the quality bar. |
| `summary` | `string` | Human-readable overview of the review outcome. |
| `comments` | `repeated ReviewComment` | Specific review comments attached to the diff. |
| `security_findings` | `repeated SecurityFinding` | Security issues detected during review. |
| `score` | `int32` | Optional numeric quality score (0–100). |

**`Severity` enum**

| Value | Description |
|---|---|
| `SEVERITY_INFO` | Informational; no action required. |
| `SEVERITY_WARNING` | Potential issue that should be reviewed. |
| `SEVERITY_ERROR` | Definite issue that must be addressed. |
| `SEVERITY_CRITICAL` | Severe issue (e.g. security vulnerability) that blocks merging. |

---

## `ExecutionEngine` (`proto/engine.proto`)

Execution engines wrap AI coding tools (Claude Code, OpenAI Codex, Aider) and
translate tasks into engine-agnostic `ExecutionSpec` records. The core
`JobBuilder` (`internal/jobbuilder/`) then translates the spec into a
Kubernetes Job. This decoupling enables testing without a cluster and opens the
door to non-Kubernetes runtimes (Docker, Podman) in future.

### RPCs

| Method | Request | Response | Description |
|---|---|---|---|
| `Handshake` | `HandshakeRequest` | `HandshakeResponse` | Version negotiation at startup. |
| `BuildExecutionSpec` | `BuildExecutionSpecRequest` | `BuildExecutionSpecResponse` | Returns a runtime-agnostic execution specification — container image, command, environment variables, resource requirements, and volumes. |
| `BuildPrompt` | `BuildPromptRequest` | `BuildPromptResponse` | Constructs the task prompt for this engine. Different engines require different prompt formats (Claude Code uses markdown with `CLAUDE.md` context; Codex uses `AGENTS.md`). |

### Key message types

**`Task`**

| Field | Type | Description |
|---|---|---|
| `id` | `string` | Unique task identifier. |
| `ticket_id` | `string` | External ticket system identifier. |
| `title` | `string` | Task title. |
| `description` | `string` | Full task description. |
| `repo_url` | `string` | Repository URL to work on. |
| `labels` | `repeated string` | Labels used for guard rail profile selection. |
| `metadata` | `map<string, string>` | Additional engine-specific configuration. |

**`ExecutionSpec`**

| Field | Type | Description |
|---|---|---|
| `image` | `string` | Container image to run. |
| `command` | `repeated string` | Entrypoint command and arguments. |
| `env` | `map<string, string>` | Environment variables. |
| `secret_env` | `map<string, string>` | Environment variable names mapped to secret references. Resolved by the `SecretsBackend`. |
| `resource_requests` | `Resources` | Minimum CPU and memory. |
| `resource_limits` | `Resources` | Maximum CPU and memory. |
| `volumes` | `repeated VolumeMount` | Volumes to mount into the container. |
| `active_deadline_seconds` | `int32` | Maximum execution time before Kubernetes terminates the job. |

**`EngineConfig`**

| Field | Type | Description |
|---|---|---|
| `image` | `string` | Container image. |
| `resource_requests` | `Resources` | Minimum CPU and memory. |
| `resource_limits` | `Resources` | Maximum CPU and memory. |
| `timeout_seconds` | `int32` | Maximum execution duration. |
| `env` | `map<string, string>` | Additional environment variables. |

---

## SDK generation

Stubs for all three supported languages can be regenerated from the proto
sources at any time:

```bash
make sdk-gen
```

This runs `buf generate` with the appropriate templates for Go, Python, and
TypeScript. Generated files are written to `sdk/go/`, `sdk/python/`, and
`sdk/typescript/` respectively.

See [SDKs](../plugins/sdks.md) for how to use each SDK in a plugin
implementation.
