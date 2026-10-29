# Webhook API reference

Osmia optionally runs an HTTP webhook server that accepts events from GitHub,
GitLab, Shortcut and Slack. When an event arrives, Osmia validates it,
parses it into one or more `Ticket` records and forwards them to the
controller's reconciliation loop.

The webhook server is implemented in `internal/webhook/`. It is separate from
the plugin gRPC API; see [Plugin gRPC API](plugins.md) for the plugin system.

---

## Configuration

| Configuration key | Default | Description |
|---|---|---|
| `webhook.port` | `8080` | TCP port the webhook server listens on. |
| `webhook.<source>.secret` | None | Per-source signing secret, where `<source>` is `github`, `gitlab`, `slack`, `shortcut`, `generic` or `incident_io`. Required for all sources except Shortcut (optional) and Generic (depends on `auth_mode`). |
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
`X-Hub-Signature-256` header in the format `sha256=<hex-digest>`. Osmia
rejects requests that do not carry a valid signature for the configured secret.
The signature is validated before the JSON body is parsed (fail-fast).

### Supported events

| `X-GitHub-Event` header | `action` field | Behaviour |
|---|---|---|
| `issues` | `opened` | Ingests the issue as a ticket. |
| `issues` | `labeled` | Ingests the issue as a ticket. |
| All other event types | n/a | Acknowledged (`200 OK`) but not forwarded. |

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
      { "name": "osmia" },
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

GitLab sends a shared secret in the `X-Gitlab-Token` header. Osmia performs
a constant-time string comparison of this header value against the configured
secret. The token is validated before the request body is read (fail-fast).

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
    { "title": "osmia" }
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

Shortcut signature validation is optional. If a secret is configured under
`webhook.shortcut.secret`, the `X-Shortcut-Signature` header is validated
using HMAC-SHA256. The header value may be sent with or without a `sha256=`
prefix. Both formats are accepted.

If no secret is configured, all well-formed requests are accepted. Configure a
secret in production.

### Supported events

Shortcut delivers a list of `actions` in each webhook payload. Osmia
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

Osmia also checks that `X-Slack-Request-Timestamp` is within
5 minutes of the current server time. Requests with older timestamps are
rejected to prevent replay attacks.

### Supported events

The Slack handler processes interactive component payloads. Payloads may
arrive as:

- JSON body (`Content-Type: application/json`)
- URL-encoded form data with a `payload` field (`Content-Type: application/x-www-form-urlencoded`)

Actions with an `action_id` prefixed `osmia_approval_` are recognised as
approval callbacks and routed directly to the approval handler. They are not
forwarded as tickets, because that would create spurious task runs. Only
non-approval Slack interactions (slash commands, other button actions) are
forwarded as tickets with `ticket_type: "slack_interaction"`.

### Example payload (interactive message action)

```json
{
  "type": "block_actions",
  "actions": [
    {
      "action_id": "osmia_approval_42-1_0",
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

The generic endpoint supports three authentication modes, configured via
`webhook.generic.auth_mode`:

| `auth_mode` | Mechanism |
|---|---|
| `hmac` | HMAC-SHA256 of the request body, sent in the header specified by `webhook.generic.signature_header` (default: `X-Webhook-Signature`). The value may include a `sha256=` prefix. |
| `bearer` | `Authorization: Bearer <secret>` header, validated by constant-time comparison against the configured secret. |
| `svix` | Verified by the official Svix Go library: five-minute replay window, `svix-*` and enterprise `webhook-*` header prefixes, `whsec_`-prefixed secrets. Use this for sources that sign with Svix conventions (e.g. Stripe, OpenAI, Linear). |

### Field mapping

The generic handler accepts a JSON body and maps fields to the ticket schema
using `webhook.generic.field_mapping`. Keys are dot-notation JSON paths (e.g.
`issue.title`); values are ticket field names. Supported target fields:

| Target field | Description |
|---|---|
| `id` | Ticket identifier. Required. Requests producing no `id` are rejected. |
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

There is no automatic mapping. Fields are copied into the ticket only through
`field_mapping`, and a request whose mapping produces no `id` is rejected with
`400 missing ticket id in payload`. A flat payload still needs a mapping, even
when its keys already have the right names:

```yaml
field_mapping:
  id: id
  title: title
  description: description
  repo_url: repo_url
```

---

## incident.io

The incident.io endpoint routes webhook events through a parallel
reconciler entry point (`Reconciler.ProcessIncidentEvent`) rather than
the SCM ticketing pipeline that backs the other endpoints, so the
controller does not require a repository or merge-request lifecycle for
these events. The endpoint is intended for triage and similar use cases
where the agent reads context and posts to chat rather than shipping
code.

### Endpoint

```http
POST /webhooks/incident-io
```

### Authentication

Only Svix-style signing is currently supported (which is what
incident.io emits). Configure via `webhook.incident_io`:

| `auth_mode` | Mechanism |
|---|---|
| `svix` | Verified by the official Svix Go library: five-minute replay window, `svix-*` and enterprise `webhook-*` header prefixes, `whsec_`-prefixed secrets. |

### Supported events

| Event type | Notes |
|---|---|
| `public_incident.incident_created_v2` | Fires when a new incident is opened. The wrapped body is a flat `IncidentV2` object. |
| `public_incident.incident_status_updated_v2` | Fires on incident status transitions. The wrapped body is `{ incident, message, new_status, previous_status }`. |

Other event types are rejected with 400. To extend this, add a constant and a case in
`internal/webhook/incident.go`.

### Fields surfaced to the agent

On dispatch, `ProcessIncidentEvent` builds the agent's `engine.Task` from
the parsed `IncidentV2`. Beyond the incident's name (`Task.Title`),
summary (`Task.Description`) and permalink (`Task.TicketURL`), the
following fields are surfaced to the agent:

Labels (`Task.Labels`, short enum-like categoricals):

| Label | Source | When emitted |
|---|---|---|
| `osmia:source:incident-io` | constant | always |
| `osmia:event:<event_type>` | `event_type` | always |
| `osmia:incident-status:<category>` | `incident_status.category` (`triage` / `live` / `learning` / `closed` / …) | when non-empty |
| `osmia:mode:<mode>` | `mode` (`standard` / `test`) | when non-empty |
| `osmia:incident-reference:<reference>` | `reference` (e.g. `INC-1368`) | when non-empty |

Description block, appended after the incident summary, when the
incident was alert-driven (`creator.alert` populated):

```
## Underlying alert

<alert.title> (alert id: <alert.id>)
```

Other fields are parsed but not surfaced through labels or the
description today: `severity.name`, `incident_type.name`,
`custom_field_entries`, `slack_channel_id`, `slack_channel_name`,
`most_recent_update_message` and so on. They are either operator-defined
(names, custom field structure varies per deployment) or sentinel-
guarded (`slack_channel_id: "not_created_yet"` on the `created` event).
The full parsed `IncidentV2` is available to downstream consumers via
`IncidentEvent.Raw`. Skills needing these can either parse them from
`Raw` or fetch the incident from the incident.io API on demand once an
incident.io MCP / REST integration is wired into the agent.

### Dispatch configuration

A separate top-level `incident_triage` block controls how parsed events
are dispatched to an engine:

| Field | Description |
|---|---|
| `engine` | Engine name to dispatch to (matched against `engine.Name()`). Defaults to `claude-code` when unset. |
| `append_system_prompt` | Injected into the per-call `engine.EngineConfig.AppendSystemPrompt`. Useful for directing the agent to invoke a classification skill or to avoid repository-shaped reasoning. |
| `slack_channel_id` | Slack channel for the incident-triage agent Job's MCP-based notifications. The ticketing flow's channel is configured under `notifications.channels`; this field is the equivalent for incident triage. A single Osmia deployment can run both flows side by side with separate channels and bot tokens. When empty, the incident flow falls back to the first configured channel under `notifications.channels`, which is useful only if you want both flows in the same channel. |
| `slack_token_secret` | Kubernetes Secret backing `SLACK_BOT_TOKEN` for the incident-triage agent Job. Pair with `slack_channel_id` when the channel belongs to a different Slack app/bot than the one used by ticketing runs. The Secret is probed for the well-known keys `SLACK_BOT_TOKEN` and `SLACK_TOKEN`, falling back to the literal `token` key. When empty, the bot token from the first configured Slack channel under `notifications.channels` is used. |

### Example configuration

```yaml
webhook:
  incident_io:
    auth_mode: svix
    secret: "${INCIDENT_IO_WEBHOOK_SECRET}"

incident_triage:
  engine: claude-code
  append_system_prompt: |
    You are an incident triage agent. Invoke /incident-classifier and
    follow it. Do not clone repositories, modify code, push branches,
    or open merge requests.
  # Slack channel + bot for the incident flow. The ticketing flow's
  # channel lives under `notifications.channels` above; the two run
  # side-by-side with separate destinations. When omitted, the incident
  # flow shares the ticketing channel.
  slack_channel_id: "C0XXXINCIDENT"
  slack_token_secret: "incident-triage-slack-bot"
```

### Example payload

```json
{
  "event_type": "public_incident.incident_created_v2",
  "public_incident.incident_created_v2": {
    "id": "01HZ0000000000000000000001",
    "reference": "INC-123",
    "name": "Database is degraded",
    "summary": "Customer reports the API is returning 500s",
    "permalink": "https://app.incident.io/incidents/123",
    "visibility": "public",
    "mode": "standard",
    "incident_status": {
      "id": "01HZ_status_live",
      "name": "Investigating",
      "category": "live",
      "rank": 10
    },
    "severity": {
      "id": "01HZ_sev_major",
      "name": "Major",
      "rank": 100
    },
    "slack_team_id": "T0123",
    "slack_channel_id": "C0123",
    "created_at": "2026-05-07T10:00:00Z",
    "updated_at": "2026-05-07T10:00:00Z"
  }
}
```

### Authoring the classifier skill

Osmia does not ship an incident classifier skill. Operators provide
one to suit their own incident taxonomy and chosen response surface
(Slack, an internal portal, an ITSM tool, etc.). The skill is an
ordinary Markdown file delivered to the agent via the engine's
[skills mechanism](../plugins/engines.md#skills);
`incident_triage.append_system_prompt` then directs the agent to
invoke it.

The `incident_triage` block points
the agent at a slash-command name, and a matching entry under
`engines.<engine>.skills` makes that command resolvable inside the
agent container:

```yaml
incident_triage:
  engine: claude-code
  append_system_prompt: |
    You are an incident triage agent. Invoke /incident-classifier
    for every incoming event and post your classification to the
    configured destination. Do not modify code, run kubectl, or close
    incidents directly.

engines:
  claude-code:
    skills:
      - name: incident-classifier
        configmap: incident-classifier-skill
        key: incident-classifier.md
```

Create the ConfigMap separately, by hand for a one-off or with your
configuration tool of choice:

```bash
kubectl create configmap incident-classifier-skill \
  --namespace osmia \
  --from-file=incident-classifier.md=./skills/incident-classifier.md
```

The agent then invokes the skill via `/incident-classifier`; pick a
different identifier if it suits your deployment, and update the name
in `engines.<engine>.skills[].name`, the file mounted into the
ConfigMap, and the `append_system_prompt` text to match.

#### What the skill should contain

The classifier skill is the agent's procedure for handling one
incident.io event. There is no fixed format, but in practice a skill
that produces consistent classifications covers:

1. Input contract. What the agent can rely on being in its prompt.
   The user prompt carries the incident's title, description (the
   incident summary), permalink and labels containing
   `osmia:source:incident-io` and `osmia:event:<event_type>`. See the
   [incident.io API documentation](https://api-docs.incident.io/) for
   the full shape of `IncidentV2` and the status-updated event if you
   need to derive additional fields from the description.
2. Classifications or output values. An enumerated set the agent
   must choose from, with the signals that distinguish them. The
   taxonomy is deployment-specific and intentionally not opinionated
   by Osmia.
3. Output contract. A precise structure for what the agent emits,
   typically a short human-readable summary plus a JSON object that
   downstream tooling can validate against a schema.
4. Bail-out conditions. If incident.io is configured to deliver
   all events (no severity, type or label filter), the skill must
   reject events outside its remit early, for example by returning a
   sentinel classification that downstream consumers route to a no-op.
5. Operational constraints. Whether the agent may take action
   (close incidents, restart workloads, page humans) or only observe.
   New deployments typically start observation-only to validate
   classification accuracy before any action paths are enabled.

#### Iterating on the skill

Because the skill is mounted from a ConfigMap, content updates do not
require redeploying the controller. Re-apply the ConfigMap, for
example with an idempotent dry-run and apply:

```bash
kubectl create configmap incident-classifier-skill \
  --namespace osmia \
  --from-file=incident-classifier.md=./skills/incident-classifier.md \
  -o yaml --dry-run=client | kubectl apply -f -
```

The next agent Job picks up the new content automatically; in-flight
Jobs continue with the version they started with.

If you prefer the skill to live in-tree with your deployment
manifests, the same content can be inlined under
`engines.<engine>.skills[].inline` instead. The trade-offs are
covered in the [Skills section](../plugins/engines.md#skills).

Without the YAML blocks, `POST /webhooks/incident-io` responds with 500
"not configured" and existing deployments are unaffected.

---

## Security

!!! warning "Always configure signing secrets"
    Running the webhook server without secrets means any actor who can reach the
    endpoint can inject arbitrary tickets. Configure a secret for every source
    you enable and rotate secrets regularly.

- Signatures are validated using `crypto/hmac` constant-time comparison to
  prevent timing attacks.
- The GitHub handler validates the signature before JSON parsing so that
  malformed payloads cannot cause a denial-of-service via parsing overhead.
- The Slack handler validates both the HMAC signature and the request timestamp
  to prevent replay attacks.
- All external input (ticket titles, descriptions) is passed through to the
  agent as prompt context. Ensure your guard rail profiles (see
  [Guard Rails](../concepts/guardrails-overview.md)) restrict what the agent is
  permitted to do with untrusted input.
