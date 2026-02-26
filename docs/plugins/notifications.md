# Notification Channel Interface

## Overview

Notification channels send fire-and-forget messages to external systems (Slack, Microsoft Teams, Discord, email, etc.). They are used to inform humans about agent activity — when tasks start, complete, or fail. Notifications are one-way; for interactive human-in-the-loop flows (approvals, questions), see the `HumanApprovalBackend` interface.

## Interface Summary

| Property | Value |
|---|---|
| Proto definition | `proto/notifications.proto` |
| Go interface | `pkg/plugin/notifications/notifications.go` |
| Interface version | `1` |
| Role in lifecycle | Called at task start, completion, and for ad-hoc progress messages |
| Criticality | Non-critical — notification failures are logged but do not block the controller |

## Go Interface

```go
type Channel interface {
    // Notify sends a free-form notification message associated with a ticket.
    Notify(ctx context.Context, message string, ticket ticketing.Ticket) error

    // NotifyStart sends a notification that an agent has begun working on a ticket.
    NotifyStart(ctx context.Context, ticket ticketing.Ticket) error

    // NotifyComplete sends a notification that an agent has finished working
    // on a ticket, including the task result summary.
    NotifyComplete(ctx context.Context, ticket ticketing.Ticket, result engine.TaskResult) error

    // Name returns the unique identifier for this channel (e.g. "slack", "teams").
    Name() string

    // InterfaceVersion returns the version of the NotificationChannel interface
    // that this channel implements.
    InterfaceVersion() int
}
```

## RPC Methods

### Handshake

Version negotiation called once at plugin startup.

```protobuf
rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
```

### Notify

Sends a free-form notification message associated with a ticket. Used for progress updates, warnings, and ad-hoc messages during task execution.

```protobuf
rpc Notify(NotifyRequest) returns (NotifyResponse);

message NotifyRequest {
  string message = 1;       // The notification text.
  Ticket ticket = 2;        // The associated ticket (for context).
  string task_run_id = 3;   // The TaskRun ID for correlation.
}
```

**Implementation guidance:**

- Include the ticket title and a link to the original issue in the message for context.
- Support markdown or rich formatting if the target platform allows it.

### NotifyStart

Sends a notification that an agent has begun working on a ticket. Called after the controller creates the K8s Job and transitions the TaskRun to `Running`.

```protobuf
rpc NotifyStart(NotifyStartRequest) returns (NotifyStartResponse);

message NotifyStartRequest {
  Ticket ticket = 1;
  string task_run_id = 2;
  string engine = 3;        // The engine being used (e.g., "claude-code").
}
```

**Implementation guidance:**

- Include the engine name so humans know which AI tool is working.
- Link to the original ticket for easy navigation.

### NotifyComplete

Sends a notification that an agent has finished working on a ticket, including the task result summary.

```protobuf
rpc NotifyComplete(NotifyCompleteRequest) returns (NotifyCompleteResponse);

message NotifyCompleteRequest {
  Ticket ticket = 1;
  string task_run_id = 2;
  TaskResult result = 3;    // The structured task result.
}
```

**Implementation guidance:**

- Differentiate success from failure visually (e.g., green vs red colour, different icons).
- Include the PR/MR link when the task succeeded.
- Include the failure reason when the task failed.
- Optionally include cost and token usage for visibility.

## Error Handling

Notification channels are **non-critical**. The controller's reconciliation loop does not fail or pause if a notification cannot be delivered:

```go
for _, n := range r.notifiers {
    if err := n.NotifyStart(ctx, ticket); err != nil {
        r.logger.ErrorContext(ctx, "notification failed",
            "channel", n.Name(),
            "error", err,
        )
    }
}
```

Errors are logged and tracked via the `robodev_plugin_errors_total` Prometheus metric with the plugin name as a label. If a notification channel is persistently failing, the plugin health checker will mark it as unhealthy, but the controller continues operating.

## Multiple Channels

RoboDev supports multiple notification channels simultaneously. All configured channels receive all events:

```yaml
config:
  notifications:
    channels:
      - backend: slack
        config:
          channel_id: "C0123456789"
          token_secret: "robodev-slack-token"
      - backend: teams
        config:
          webhook_url_secret: "robodev-teams-webhook"
```

## Built-in: Slack

The Slack notification channel sends formatted messages using the Slack Web API with Block Kit formatting.

### Configuration

```yaml
config:
  notifications:
    channels:
      - backend: slack
        config:
          channel_id: "C0123456789"
          token_secret: "robodev-slack-token"
```

### Message Format

| Event | Slack Message |
|---|---|
| `NotifyStart` | Agent started working on: **{title}** using `{engine}` |
| `NotifyComplete` (success) | Completed **{title}** — [View PR]({url}). Cost: ${cost}. Summary: {summary} |
| `NotifyComplete` (failure) | Failed on **{title}**: {summary} |
| `Notify` | Free-form text with ticket context |

### Required Setup

1. Create a Slack app at [api.slack.com/apps](https://api.slack.com/apps).
2. Add the `chat:write` bot scope.
3. Install the app to your workspace.
4. Copy the bot token (`xoxb-...`) into a Kubernetes Secret.
5. Invite the bot to the target channel.

> **Security note:** Store the bot token in a Kubernetes Secret and reference it via the `token_secret` field rather than placing it directly in `values.yaml`.

## Writing a Custom Notification Channel

When implementing a custom notification channel, follow these guidelines:

### Speed

Notification methods should be fast (< 5 seconds). The controller calls notifications synchronously during the reconciliation loop. If the external system is slow, consider:

- Buffering notifications and sending them asynchronously.
- Using webhooks (HTTP POST) rather than polling-based APIs.
- Setting aggressive HTTP client timeouts.

### No Retries from the Controller

The controller does not retry failed notifications. If delivery reliability is critical, implement retry logic within your plugin:

```go
func (n *MyNotifier) Notify(ctx context.Context, message string, ticket ticketing.Ticket) error {
    var lastErr error
    for attempt := 0; attempt < 3; attempt++ {
        if err := n.send(ctx, message); err != nil {
            lastErr = err
            time.Sleep(time.Duration(attempt+1) * time.Second)
            continue
        }
        return nil
    }
    return lastErr
}
```

### Rich Formatting

Use the ticket and result data to build informative messages. Good notifications include:

- **Ticket title** and link to the original issue.
- **Engine name** (so humans know which AI tool is working).
- **PR/MR link** on success.
- **Failure reason** on failure.
- **Cost estimate** and **duration** for spend tracking.
- **Token usage** for capacity planning.

### Rate Limiting

If your notification target has rate limits (e.g., Slack: 1 message per second per channel), implement throttling in the plugin. Consider using a rate limiter:

```go
limiter := rate.NewLimiter(rate.Every(time.Second), 1)
limiter.Wait(ctx) // blocks until a slot is available
```

### Context Propagation

Respect the `context.Context` for cancellation. If the controller is shutting down, notification calls should return promptly rather than blocking on slow network requests.

## Protobuf Definition

The complete protobuf service is defined in `proto/notifications.proto`:

```protobuf
service NotificationChannel {
    rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
    rpc Notify(NotifyRequest) returns (NotifyResponse);
    rpc NotifyStart(NotifyStartRequest) returns (NotifyStartResponse);
    rpc NotifyComplete(NotifyCompleteRequest) returns (NotifyCompleteResponse);
}
```

See `proto/common.proto` for the shared `Ticket`, `TaskResult`, and `HandshakeRequest`/`HandshakeResponse` message definitions.
