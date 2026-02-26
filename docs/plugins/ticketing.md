# Ticketing Backend Interface

## Overview

The ticketing backend is the primary input source for RoboDev. It polls an issue tracker for tickets ready to be processed, updates their state as work progresses, and adds comments with results.

## Interface

```go
type Backend interface {
    PollReadyTickets(ctx context.Context) ([]Ticket, error)
    MarkInProgress(ctx context.Context, ticketID string) error
    MarkComplete(ctx context.Context, ticketID string, result TaskResult) error
    MarkFailed(ctx context.Context, ticketID string, reason string) error
    AddComment(ctx context.Context, ticketID string, comment string) error
    Name() string
    InterfaceVersion() int
}
```

## Built-in: GitHub Issues

The GitHub Issues backend polls a repository for issues matching configured labels and state.

### Configuration

```yaml
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
|--------|--------------|
| `PollReadyTickets` | GET `/repos/{owner}/{repo}/issues?labels=robodev&state=open` |
| `MarkInProgress` | Add "in-progress" label, remove "robodev" label |
| `MarkComplete` | Close the issue, add comment with result summary |
| `MarkFailed` | Add "robodev-failed" label, add comment with reason |
| `AddComment` | POST `/repos/{owner}/{repo}/issues/{number}/comments` |

## Writing a Custom Ticketing Backend

See [writing-a-plugin.md](writing-a-plugin.md) for a full guide. The [example Jira plugin](../../examples/plugins/example-jira-python/) demonstrates a Python implementation.

## Protobuf Definition

The protobuf service is defined in `proto/ticketing.proto`:

```protobuf
service TicketingBackend {
    rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
    rpc PollReadyTickets(PollReadyTicketsRequest) returns (PollReadyTicketsResponse);
    rpc MarkInProgress(MarkInProgressRequest) returns (Empty);
    rpc MarkComplete(MarkCompleteRequest) returns (Empty);
    rpc MarkFailed(MarkFailedRequest) returns (Empty);
    rpc AddComment(AddCommentRequest) returns (Empty);
}
```
