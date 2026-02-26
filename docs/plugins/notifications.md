# Notification Channel Interface

## Overview

Notification channels send fire-and-forget messages to external systems (Slack, Teams, Discord, etc). They are used to inform humans about agent activity — not for interactive flows (see the [approval interface](../plugins/writing-a-plugin.md) for human-in-the-loop).

## Interface

```go
type Channel interface {
    Notify(ctx context.Context, message string, ticket Ticket) error
    NotifyStart(ctx context.Context, ticket Ticket) error
    NotifyComplete(ctx context.Context, ticket Ticket, result TaskResult) error
    Name() string
    InterfaceVersion() int
}
```

## Built-in: Slack

The Slack notification channel sends messages using the Slack Web API with Block Kit formatting.

### Configuration

```yaml
notifications:
  channels:
    - backend: slack
      config:
        channel_id: "C0123456789"
        token_secret: "robodev-slack-token"
```

### Messages

| Method | Message |
|--------|---------|
| `NotifyStart` | Agent started working on: {title} |
| `NotifyComplete` | Agent completed: {title} — {summary} + MR link |
| `Notify` | Custom message with ticket context |

## Protobuf Definition

See `proto/notifications.proto` for the full service definition.
