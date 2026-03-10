# API Reference

Osmia exposes two distinct API surfaces:

| API | Description | Reference |
|---|---|---|
| **Webhook HTTP API** | An HTTP server that receives events from GitHub, GitLab, Shortcut, and Slack and feeds them into the controller as tickets. | [Webhook API](webhooks.md) |
| **Plugin gRPC API** | A set of protobuf services that plugins implement to integrate with external ticketing systems, notification channels, secrets vaults, SCM providers, review tools, and execution engines. | [Plugin gRPC API](plugins.md) |

## Webhook HTTP API

The webhook server is an optional component. When enabled, it listens for
inbound events and converts them into `Ticket` records that the controller
reconciles. Each source has its own endpoint under `/webhooks/`:

```
POST /webhooks/github
POST /webhooks/gitlab
POST /webhooks/slack
POST /webhooks/shortcut
POST /webhooks/generic
```

All endpoints require a configured signing secret and validate signatures
before processing the request body. See [Webhook API](webhooks.md) for the
full reference.

## Plugin gRPC API

All plugin interfaces are defined as protobuf services in `proto/` — this is
the source of truth. The generated Go stubs and client/server wrappers live in
`pkg/plugin/`. SDKs for Go, Python, and TypeScript are in `sdk/` and can be
regenerated at any time with:

```bash
make sdk-gen
```

See [Plugin gRPC API](plugins.md) for the full service reference, including
all RPCs and message types.
