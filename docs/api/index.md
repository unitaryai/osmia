# API reference

Osmia has two API surfaces:

| API | Description | Reference |
|---|---|---|
| Webhook HTTP API | An HTTP server that receives events from GitHub, GitLab, Shortcut, Slack, incident.io and generic sources and passes them to the controller as tickets. | [Webhook API](webhooks.md) |
| Plugin gRPC API | Protobuf services that plugins implement to connect ticketing systems, notification channels, secrets vaults, SCM providers, review tools and execution engines. | [Plugin gRPC API](plugins.md) |

## Webhook HTTP API

The webhook server is optional. When enabled, it listens for
inbound events and converts them into `Ticket` records that the controller
reconciles. Each source has its own endpoint under `/webhooks/`:

```
POST /webhooks/github
POST /webhooks/gitlab
POST /webhooks/slack
POST /webhooks/shortcut
POST /webhooks/generic
POST /webhooks/incident-io
```

Every endpoint validates a signature or token before processing the request
body, with two exceptions: Shortcut checks its signature only when a secret is
configured, and the generic endpoint depends on its `auth_mode`. See [Webhook API](webhooks.md) for the
full reference.

## Plugin gRPC API

All plugin interfaces are defined as protobuf services in `proto/`, which is
the source of truth. The generated Go stubs and client/server wrappers live in
`pkg/plugin/`. SDKs for Go, Python and TypeScript are in `sdk/` and can be
regenerated with:

```bash
make sdk-gen
```

See [Plugin gRPC API](plugins.md) for the full service reference, including
all RPCs and message types.
