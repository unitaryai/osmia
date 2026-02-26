# Ticketing Backend Interface

## Overview

The ticketing backend is the primary input source for RoboDev. It polls an issue tracker for tickets ready to be processed, manages their lifecycle state transitions (in-progress, complete, failed), and posts progress comments. The controller calls `PollReadyTickets` on every reconciliation cycle to discover new work.

## Interface Summary

| Property | Value |
|---|---|
| Proto definition | `proto/ticketing.proto` |
| Go interface | `pkg/plugin/ticketing/ticketing.go` |
| Interface version | `1` |
| Role in lifecycle | Entry point — the controller calls `PollReadyTickets` every poll interval |
| Criticality | Critical — the controller cannot operate without a ticketing backend |

## Go Interface

```go
type Backend interface {
    PollReadyTickets(ctx context.Context) ([]Ticket, error)
    MarkInProgress(ctx context.Context, ticketID string) error
    MarkComplete(ctx context.Context, ticketID string, result engine.TaskResult) error
    MarkFailed(ctx context.Context, ticketID string, reason string) error
    AddComment(ctx context.Context, ticketID string, comment string) error
    Name() string
    InterfaceVersion() int
}
```

## Ticket Struct

The `Ticket` struct represents a unit of work from an external issue tracker:

```go
type Ticket struct {
    ID          string            // Unique identifier (e.g. issue number or Jira key).
    Title       string            // Short summary of the task.
    Description string            // Full task description (used as agent prompt input).
    TicketType  string            // Task category (e.g. "bug", "enhancement", "refactor").
    Labels      []string          // Labels or tags from the source system.
    RepoURL     string            // Repository the agent should work on.
    ExternalURL string            // Link back to the original issue for humans.
    Raw         map[string]any    // Unstructured data from the source system.
}
```

The `Raw` field allows backends to pass through arbitrary metadata from the source system. Engines and other plugins can inspect `Raw` for backend-specific fields (e.g. Jira priority, custom fields).

## RPC Methods

### Handshake

Version negotiation called once at plugin startup. The controller verifies that the plugin implements a compatible interface version before using it.

```protobuf
rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
```

### PollReadyTickets

Retrieves tickets that are ready for processing. The controller calls this on every reconciliation cycle (default: every 30 seconds).

```protobuf
rpc PollReadyTickets(PollReadyTicketsRequest) returns (PollReadyTicketsResponse);

message PollReadyTicketsRequest {
  repeated string labels = 1;    // Filter tickets by these labels.
  int32 max_results = 2;         // Maximum number of tickets to return.
}

message PollReadyTicketsResponse {
  repeated Ticket tickets = 1;
}
```

**Implementation guidance:**

- Return only tickets in a "ready" state (e.g., open issues with the `robodev` label that have not already been picked up).
- Respect `max_results` to avoid overwhelming the controller when there is a large backlog.
- The controller handles deduplication via idempotency keys, but filtering already-in-progress tickets at the source is more efficient and reduces unnecessary API calls.
- Consider caching the last poll result to avoid redundant API requests when the source system has not changed.

### MarkInProgress

Transitions a ticket to in-progress state. Called after the controller creates a TaskRun and launches a K8s Job.

```protobuf
rpc MarkInProgress(MarkInProgressRequest) returns (MarkInProgressResponse);

message MarkInProgressRequest {
  string ticket_id = 1;
}
```

**Implementation guidance:**

- Typically this adds a label (e.g., `in-progress`) or moves the ticket to a "doing" column/status.
- This operation must be **idempotent** — calling it twice for the same ticket must not fail or produce side effects.
- Remove the trigger label (e.g., `robodev`) if appropriate, so the ticket is not picked up again on the next poll.

### MarkComplete

Transitions a ticket to complete with the task result. Called when the agent job finishes successfully.

```protobuf
rpc MarkComplete(MarkCompleteRequest) returns (MarkCompleteResponse);

message MarkCompleteRequest {
  string ticket_id = 1;
  TaskResult result = 2;
}
```

The `TaskResult` message includes:

| Field | Type | Description |
|---|---|---|
| `success` | `bool` | Whether the task completed successfully |
| `merge_request_url` | `string` | URL of the created pull request (may be empty) |
| `branch_name` | `string` | The branch containing the agent's changes |
| `summary` | `string` | Human-readable summary of what was done |
| `token_usage` | `TokenUsage` | Input and output token counts |
| `cost_estimate_usd` | `double` | Estimated cost in US dollars |
| `exit_code` | `int32` | 0=success, 1=agent failure, 2=guard rail blocked |

**Implementation guidance:**

- Post a summary comment with a link to the PR before closing the ticket, so humans can easily find the result.
- The `merge_request_url` may be empty if the engine did not create a PR (e.g., the task was a linting fix committed directly, or the quality gate blocked PR creation).
- Include cost and token usage in the comment if your organisation tracks AI spend.

### MarkFailed

Transitions a ticket to failed state. Called when the agent job fails and retries are exhausted.

```protobuf
rpc MarkFailed(MarkFailedRequest) returns (MarkFailedResponse);

message MarkFailedRequest {
  string ticket_id = 1;
  string reason = 2;
}
```

**Implementation guidance:**

- Post the failure reason as a comment on the ticket so humans can investigate.
- Consider adding a label (e.g., `robodev-failed`) rather than closing the ticket, so it can be retried manually by re-adding the trigger label.
- Include enough context in the reason for a human to understand what went wrong without consulting controller logs.

### AddComment

Posts a progress comment on the ticket. Called during long-running tasks to provide visibility.

```protobuf
rpc AddComment(AddCommentRequest) returns (AddCommentResponse);

message AddCommentRequest {
  string ticket_id = 1;
  string comment = 2;
}
```

**Implementation guidance:**

- Keep comments concise and informative. Avoid flooding the ticket with low-value updates.
- Consider formatting comments with markdown (most issue trackers support it).
- This method is fire-and-forget — errors are logged but do not block the controller.

## Built-in: GitHub Issues

The GitHub Issues backend (`pkg/plugin/ticketing/github/`) polls GitHub Issues via the GitHub REST API.

### Configuration

```yaml
config:
  ticketing:
    backend: github
    config:
      owner: "your-org"
      repo: "your-repo"
      labels:
        - "robodev"
      token_secret: "robodev-github-token"
```

### Behaviour

| Method | GitHub Action |
|---|---|
| `PollReadyTickets` | `GET /repos/{owner}/{repo}/issues?labels=robodev&state=open` |
| `MarkInProgress` | Adds the `robodev-in-progress` label, removes the `robodev` label |
| `MarkComplete` | Posts a comment with the PR link and summary, then closes the issue |
| `MarkFailed` | Posts a comment with the failure reason, adds the `robodev-failed` label |
| `AddComment` | `POST /repos/{owner}/{repo}/issues/{number}/comments` |

### Required Permissions

The GitHub personal access token (or GitHub App installation token) needs the following scopes:

| Scope | Reason |
|---|---|
| `repo` | Read/write access to repository contents (for agent work) |
| `issues` | Read/write access to issues (for polling and lifecycle management) |
| `pull_requests` | Create pull requests (used by the SCM backend, not the ticketing backend directly) |

For production deployments, prefer **GitHub App installation tokens** (1-hour expiry, scoped to specific repositories) over long-lived personal access tokens.

## Writing a Custom Ticketing Backend

See the [Writing a Plugin](writing-a-plugin.md) guide for complete examples in Go, Python, and TypeScript. Key design considerations:

### Idempotency

All state transition methods (`MarkInProgress`, `MarkComplete`, `MarkFailed`) must be idempotent. The controller may call them more than once due to:

- Network timeouts followed by retries.
- The same ticket appearing in consecutive polls before `MarkInProgress` takes effect.
- Controller restarts during processing.

### Filtering

Return only actionable tickets from `PollReadyTickets`. The more precise your filtering, the less work the controller does. For example:

- Exclude tickets that already have an `in-progress` or `robodev-failed` label.
- Only return tickets from repositories in the allowed list (if your backend supports server-side filtering).
- Limit results to recent tickets to avoid processing stale backlog.

### Error Handling

Return appropriate gRPC error codes:

| Code | When to use |
|---|---|
| `NOT_FOUND` | Ticket does not exist or has been deleted |
| `UNAVAILABLE` | The external API is temporarily down |
| `PERMISSION_DENIED` | Token is invalid or lacks required scopes |
| `INVALID_ARGUMENT` | Ticket ID is malformed |

### Timeouts

Respect the `context.Context` deadline. All RPCs should complete within a reasonable timeout (10–30 seconds). If your ticket source has slow API responses, implement client-side timeouts and retries within the plugin.

### Rate Limiting

If your ticket source has API rate limits (e.g., GitHub's 5,000 requests/hour), implement backoff in the plugin rather than relying on the controller. Consider:

- Caching poll results for a short period.
- Exponential backoff on rate-limit responses (HTTP 429).
- Tracking remaining rate limit quota via response headers.

## Protobuf Definition

The complete protobuf service is defined in `proto/ticketing.proto`:

```protobuf
service TicketingBackend {
    rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
    rpc PollReadyTickets(PollReadyTicketsRequest) returns (PollReadyTicketsResponse);
    rpc MarkInProgress(MarkInProgressRequest) returns (MarkInProgressResponse);
    rpc MarkComplete(MarkCompleteRequest) returns (MarkCompleteResponse);
    rpc MarkFailed(MarkFailedRequest) returns (MarkFailedResponse);
    rpc AddComment(AddCommentRequest) returns (AddCommentResponse);
}
```

See `proto/common.proto` for the shared `Ticket`, `TaskResult`, and `HandshakeRequest`/`HandshakeResponse` message definitions.
