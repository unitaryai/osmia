# Webhook API Reference

RoboDev optionally runs an HTTP webhook server that accepts events from GitHub,
GitLab, Shortcut, and Slack. When an event is received, it is validated,
parsed into one or more `Ticket` records, and forwarded to the controller's
reconciliation loop for processing.

The webhook server is implemented in `internal/webhook/`. It is separate from
the plugin gRPC API; see [Plugin gRPC API](plugins.md) for the plugin system.

---

## Configuration

| Configuration key | Default | Description |
|---|---|---|
| `webhook.port` | `8080` | TCP port the webhook server listens on. |
| `webhook.secrets.<source>` | — | Per-source signing secret. Required for all sources except Shortcut (optional) and Generic (depends on `auth_mode`). |
| `webhook.path_prefix` | `/webhooks` | Base path prepended to all route registrations. Not configurable at present; all routes are hard-coded under `/webhooks/`. |

A health-check endpoint is available at `GET /healthz` and returns `200 OK`
with body `ok`. This is suitable for Kubernetes liveness and readiness probes.

---

## GitHub

### Endpoint

```
POST /webhooks/github
```

### Authentication

GitHub signs every delivery using HMAC-SHA256. The signature is sent in the
`X-Hub-Signature-256` header in the format `sha256=<hex-digest>`. RoboDev
rejects requests that do not carry a valid signature for the configured secret.
The signature is validated **before** the JSON body is parsed (fail-fast).

### Supported events

| `X-GitHub-Event` header | `action` field | Behaviour |
|---|---|---|
| `issues` | `opened` | Ingests the issue as a ticket. |
| `issues` | `labeled` | Ingests the issue as a ticket. |
| All other event types | — | Acknowledged (`200 OK`) but not forwarded. |

### Example payload

```json
{
  "action": "opened",
  "issue": {
    "number": 42,
    "title": "Fix null pointer in auth middleware",
    "body": "The `AuthMiddleware` panics when the `Authorization` header is absent.",
    "html_url": "https://github.com/example/repo/issues/42",
    "labels": [
      { "name": "robodev" },
      { "name": "bug" }
    ]
  },
  "repository": {
    "full_name": "example/repo",
    "html_url": "https://github.com/example/repo"
  }
}
```

The `issue.number` becomes the ticket `id`, `issue.title` the title,
`issue.body` the description, and `repository.html_url` the `repo_url`.

---

## GitLab

### Endpoint

```
POST /webhooks/gitlab
```

### Authentication

GitLab sends a shared secret in the `X-Gitlab-Token` header. RoboDev performs
a constant-time string comparison of this header value against the configured
secret. The token is validated **before** the request body is read (fail-fast).

### Supported events

| `object_kind` field | Behaviour |
|---|---|
| `issue` | Ingests the issue as a ticket with `ticket_type: "issue"`. |
| `merge_request` | Ingests the merge request as a ticket with `ticket_type: "merge_request"`. |
| All other values | Acknowledged (`200 OK`) but not forwarded. |

### Example payload

```json
{
  "object_kind": "issue",
  "object_attributes": {
    "iid": 7,
    "title": "Upgrade dependency — bump grpc-go to v1.63",
    "description": "grpc-go v1.63 fixes a critical memory leak.",
    "url": "https://gitlab.example.com/group/project/-/issues/7",
    "action": "open",
    "state": "opened"
  },
  "project": {
    "web_url": "https://gitlab.example.com/group/project",
    "path_with_namespace": "group/project"
  },
  "labels": [
    { "title": "robodev" }
  ]
}
```

The `object_attributes.iid` becomes the ticket `id`, `object_attributes.title`
the title, `object_attributes.description` the description, and
`project.web_url` the `repo_url`.

---

## Shortcut

### Endpoint

```
POST /webhooks/shortcut
```

### Authentication

Shortcut signature validation is **optional**. If a secret is configured under
`webhook.secrets.shortcut`, the `X-Shortcut-Signature` header is validated
using HMAC-SHA256. The header value may be sent with or without a `sha256=`
prefix — both formats are accepted.

If no secret is configured, all well-formed requests are accepted. It is
strongly recommended to configure a secret in production.

### Supported events

Shortcut delivers a list of `actions` in each webhook payload. RoboDev
processes actions where:

- `entity_type` is `"story"`, and
- `action` is `"update"`.

If `webhook.shortcut_target_state_id` is configured, only story updates where
the `workflow_state_id` changed to that specific state ID are forwarded. This
prevents unrelated story edits (description changes, comments, etc.) from
reaching the controller unnecessarily.

### Example payload

```json
{
  "actions": [
    {
      "id": 1001,
      "entity_type": "story",
      "action": "update",
      "name": "Refactor authentication service",
      "app_url": "https://app.shortcut.com/example/story/1001",
      "changes": {
        "description": {
          "old": "",
          "new": "Extract auth logic into a dedicated service with proper unit tests."
        },
        "workflow_state_id": {
          "old": 500000020,
          "new": 500000021
        }
      }
    }
  ]
}
```

The action `id` becomes the ticket `id`, `name` the title, and the new
`description` value (if present) the description.

---

## Slack

### Endpoint

```
POST /webhooks/slack
```

### Authentication

Slack uses a versioned HMAC-SHA256 scheme. The signature is computed over the
string `v0:<X-Slack-Request-Timestamp>:<raw-body>` using the signing secret,
and sent in `X-Slack-Signature` as `v0=<hex-digest>`.

RoboDev additionally validates that the `X-Slack-Request-Timestamp` is within
**5 minutes** of the current server time. Requests with older timestamps are
rejected to prevent replay attacks.

### Supported events

The Slack handler processes **interactive component** payloads. Payloads may
arrive as:

- JSON body (`Content-Type: application/json`)
- URL-encoded form data with a `payload` field (`Content-Type: application/x-www-form-urlencoded`)

Actions with an `action_id` prefixed `robodev_approval_` are recognised as
approval callbacks and routed directly to the approval handler. They are **not**
forwarded as tickets — doing so would create spurious task runs. Only
non-approval Slack interactions (slash commands, other button actions) are
forwarded as tickets with `ticket_type: "slack_interaction"`.

### Example payload (interactive message action)

```json
{
  "type": "block_actions",
  "actions": [
    {
      "action_id": "robodev_approval_42-1_0",
      "value": "approved"
    }
  ],
  "user": {
    "id": "U01AB2CD3EF",
    "username": "alice"
  },
  "channel": {
    "id": "C01AB2CD3EF"
  },
  "trigger_id": "12345.67890.abcdef"
}
```

---

## Generic HTTP

### Endpoint

```
POST /webhooks/generic
```

### Authentication

The generic endpoint supports two authentication modes, configured via
`webhook.generic.auth_mode`:

| `auth_mode` | Mechanism |
|---|---|
| `hmac` | HMAC-SHA256 of the request body, sent in the header specified by `webhook.generic.signature_header` (default: `X-Webhook-Signature`). The value may include a `sha256=` prefix. |
| `bearer` | `Authorization: Bearer <secret>` header, validated by constant-time comparison against the configured secret. |

### Field mapping

The generic handler accepts a JSON body and maps fields to the ticket schema
using `webhook.generic.field_mapping`. Keys are dot-notation JSON paths (e.g.
`issue.title`); values are ticket field names. Supported target fields:

| Target field | Description |
|---|---|
| `id` | Ticket identifier. **Required** — requests producing no `id` are rejected. |
| `title` | Ticket title. |
| `description` | Ticket description (markdown accepted). |
| `ticket_type` | Ticket type classifier (e.g. `"bug_fix"`, `"feature"`). |
| `repo_url` | Repository URL. |
| `external_url` | Web URL to view the ticket in the originating system. |

### Example configuration

```yaml
webhook:
  generic:
    auth_mode: hmac
    secret: "your-signing-secret"
    signature_header: "X-My-System-Signature"
    field_mapping:
      "item.id": id
      "item.title": title
      "item.body": description
      "item.repo": repo_url
```

### Example payload

Given the mapping above, a minimal triggering payload would be:

```json
{
  "item": {
    "id": "TASK-999",
    "title": "Add rate limiting to the public API",
    "body": "The public API has no rate limiting. Implement token-bucket rate limiting.",
    "repo": "https://github.com/example/repo"
  }
}
```

Alternatively, without field mapping configured, a flat payload with top-level
`title`, `description`, and `repo_url` fields will work when the mapping keys
match those names directly.

---

## Security

!!! warning "Always configure signing secrets"
    Running the webhook server without secrets means any actor who can reach the
    endpoint can inject arbitrary tickets. Configure a secret for every source
    you enable and rotate secrets regularly.

- Signatures are validated using `crypto/hmac` constant-time comparison to
  prevent timing attacks.
- The GitHub handler validates the signature **before** JSON parsing so that
  malformed payloads cannot cause a denial-of-service via parsing overhead.
- The Slack handler validates both the HMAC signature and the request timestamp
  to prevent replay attacks.
- All external input (ticket titles, descriptions) is passed through to the
  agent as prompt context. Ensure your guard rail profiles (see
  [Guard Rails](../concepts/guardrails-overview.md)) restrict what the agent is
  permitted to do with untrusted input.
