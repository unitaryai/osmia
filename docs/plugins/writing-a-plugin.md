# Writing a Plugin

## Overview

RoboDev is extended through **plugins** -- modular backends that integrate with external services. There are two plugin types:

1. **Built-in plugins** -- compiled directly into the controller binary (Go only). The GitHub Issues ticketing backend at `pkg/plugin/ticketing/github/` is an example.
2. **Third-party plugins** -- standalone processes communicating over gRPC via [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin). Written in any language. Recommended for organisation-specific integrations (Jira, PagerDuty, Slack).

Both types implement the same logical interface; they differ only in how they are compiled, deployed, and loaded.

## Plugin Interfaces

Every plugin implements one of six interfaces, defined as protobuf services in `proto/` with corresponding Go interfaces in `pkg/plugin/`:

| Interface | Protobuf Service | Description |
|-----------|-----------------|-------------|
| **Ticketing** | `TicketingBackend` | Polls issue trackers for ready tickets and manages lifecycle. See [ticketing.md](ticketing.md). |
| **Notifications** | `NotificationChannel` | Sends fire-and-forget notifications (Slack, Teams, email). See [notifications.md](notifications.md). |
| **Approval** | `HumanApprovalBackend` | Requests and receives human approval before critical operations. |
| **Secrets** | `SecretsBackend` | Retrieves secrets from external vaults at runtime. See [secrets.md](secrets.md). |
| **SCM** | `SCMBackend` | Source control operations -- cloning, branching, creating merge requests. |
| **Review** | `ReviewBackend` | Automated code review integration. |

All six include a `Handshake` RPC for version negotiation. Shared message types (`HandshakeRequest`, `HandshakeResponse`, `Ticket`, `TaskResult`) are in `proto/common.proto`.

## Architecture

Third-party plugins use the **hashicorp/go-plugin subprocess model**. The controller spawns each plugin as a child process and communicates over gRPC on a local socket. Benefits:

- **Language independence** -- any language with gRPC support.
- **Process isolation** -- a crashing plugin does not bring down the controller.
- **Automatic restart** -- the `Host` (`pkg/plugin/host.go`) restarts failed plugins with exponential backoff.

### Lifecycle

1. **Startup** -- The `Host` spawns each plugin binary via `exec.Command` and establishes a gRPC connection.
2. **Handshake** -- The controller sends its expected `interface_version`. If the plugin responds with an incompatible version, loading is refused.
3. **Health monitoring** -- The `Host` tracks health status. Unresponsive plugins are marked unhealthy.
4. **Restart with backoff** -- Failed plugins are restarted up to `max_plugin_restarts` times (default: 3) with configurable backoff (default: 1s, 5s, 30s). After the maximum, the plugin is permanently unhealthy.
5. **Shutdown** -- `Host.Shutdown()` kills all plugin subprocesses.

The controller verifies plugin identity using a magic cookie:

```go
goplugin.HandshakeConfig{
    ProtocolVersion:  1,
    MagicCookieKey:   "ROBODEV_PLUGIN",
    MagicCookieValue: "robodev",
}
```

## Writing a Go Plugin (Built-in)

### Step 1: Implement the Interface

The ticketing interface (`pkg/plugin/ticketing/ticketing.go`) is representative of all six:

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

Create a new package under `pkg/plugin/ticketing/yourbackend/` and add a compile-time assertion:

```go
package yourbackend

var _ ticketing.Backend = (*YourBackend)(nil)

type YourBackend struct {
    apiURL string
    token  string
    logger *slog.Logger
}

func (b *YourBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
    // Call your tracker's API and return matching tickets.
    return nil, nil
}

func (b *YourBackend) Name() string         { return "yourbackend" }
func (b *YourBackend) InterfaceVersion() int { return ticketing.InterfaceVersion }
// ... implement remaining methods ...
```

### Step 2: Register with the Controller

Register your backend in the controller's initialisation logic so it is selected when `ticketing.backend: yourbackend` is configured. See `pkg/plugin/ticketing/github/github.go` for the pattern.

### Step 3: Write Tests

Use table-driven tests with `testify` and `httptest.Server` to mock external APIs:

```go
func TestYourBackend_PollReadyTickets(t *testing.T) {
    tests := []struct {
        name      string
        response  string
        wantCount int
        wantErr   bool
    }{
        {name: "returns matching tickets", response: `[{"id":"1"}]`, wantCount: 1},
        {name: "handles API error", response: "", wantErr: true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Set up httptest.Server, create backend, call PollReadyTickets.
        })
    }
}
```

## Writing a gRPC Plugin (Third-party)

### Step 1: Generate Stubs from Protobuf

The protobuf definitions live in `proto/`. Use `buf` to generate stubs:

```bash
buf generate proto/                            # Go stubs
buf generate proto/ --template buf.gen.python.yaml   # Python stubs
buf generate proto/ --template buf.gen.ts.yaml       # TypeScript stubs
```

### Step 2: Implement the Service

Implement every RPC in the protobuf service. Errors are conveyed via gRPC status codes.

### Step 3: Include the Handshake RPC

Every plugin **must** implement `Handshake`. Your `HandshakeResponse` must include:

- `interface_version` -- currently `1` for all interfaces.
- `plugin_name` -- e.g. `"jira"`, `"slack"`.
- `plugin_version` -- semver of your binary, e.g. `"1.2.0"`.

### Step 4: Build as a Standalone Binary

Package as a single executable. The controller spawns it via `exec.Command`, so it must be directly runnable.

### Step 5: Configure in robodev-config.yaml

```yaml
plugins:
  - name: jira
    command: /opt/robodev/plugins/robodev-plugin-jira
    type: ticketing
    interface_version: 1
```

### Python Example

A Jira ticketing plugin is provided at `examples/plugins/example-jira-python/`. Key excerpt from `robodev_plugin_jira/__main__.py`:

```python
class JiraTicketingBackend:
    @property
    def name(self) -> str:
        return "jira"

    @property
    def interface_version(self) -> int:
        return 1

    def poll_ready_tickets(self):
        jql = f'project = "{self.project_key}" AND labels = "{self.label}" AND status = "To Do"'
        # ... call Jira REST API, return tickets ...

    def mark_in_progress(self, ticket_id: str):
        # ... transition issue via Jira REST API ...

    def add_comment(self, ticket_id: str, comment: str):
        # ... post comment via Jira REST API ...

def main():
    # from robodev_plugin_sdk import serve
    # serve(JiraTicketingBackend(), interface="ticketing")
    pass
```

Packaged with a `Dockerfile` and `pyproject.toml`. See the full source for details.

### TypeScript Example

A Teams notification plugin is provided at `examples/plugins/example-teams-ts/`. Key excerpt from `src/index.ts`:

```typescript
class TeamsNotificationChannel {
  get name(): string { return 'teams'; }
  get interfaceVersion(): number { return 1; }

  async notify(message: string, ticket: Ticket): Promise<void> {
    await this.sendCard({ title: `RoboDev: ${ticket.title}`, text: message });
  }

  async notifyComplete(ticket: Ticket, result: TaskResult): Promise<void> {
    const colour = result.success ? '00CC6A' : 'FF4444';
    await this.sendCard({ title: ticket.title, text: result.summary, themeColor: colour });
  }

  // ... POST Adaptive Card to Teams webhook ...
}

// import { serve } from '@robodev/plugin-sdk';
// serve(channel, { interface: 'notifications' });
```

Build with `tsc`, run with `node dist/index.js`.

## Protobuf Definitions

All definitions live in `proto/`:

| File | Contents |
|------|----------|
| `common.proto` | `HandshakeRequest/Response`, `Ticket`, `TaskResult`, `TokenUsage` |
| `ticketing.proto` | `TicketingBackend` service |
| `notifications.proto` | `NotificationChannel` service |
| `approval.proto` | `HumanApprovalBackend` service |
| `secrets.proto` | `SecretsBackend` service |
| `scm.proto` | `SCMBackend` service |
| `review.proto` | `ReviewBackend` service |

Key shared types in `common.proto`:

- **`Ticket`** -- id, title, description, ticket_type, labels, repo_url, external_url, and a `google.protobuf.Struct raw` field for backend-specific data.
- **`TaskResult`** -- success, merge_request_url, branch_name, summary, token_usage, cost_estimate_usd, exit_code (0=success, 1=agent failure, 2=guard rail blocked).

## Plugin Configuration

### Helm values.yaml

External plugins are configured under the `plugins` key in `charts/robodev/values.yaml`:

```yaml
plugins:
  - name: jira
    image: ghcr.io/myorg/robodev-plugin-jira:v1.2.0
    binaryPath: /plugin
  - name: pagerduty
    image: ghcr.io/myorg/robodev-plugin-pagerduty:v0.3.1
    binaryPath: /plugin
```

Each entry specifies an OCI image containing the plugin binary and the path to the executable within that image.

### Health Configuration

In `robodev-config.yaml`:

```yaml
plugin_health:
  max_plugin_restarts: 3
  restart_backoff: [1, 5, 30]  # seconds between restart attempts
```

## Deploying with Helm

RoboDev uses an **init container pattern** to load plugins. For each entry in `plugins`, the Helm chart (`charts/robodev/templates/deployment.yaml`) creates an init container that copies the plugin binary into a shared `emptyDir` volume:

```yaml
initContainers:
  - name: plugin-{{ .name }}
    image: {{ .image }}
    command: ["cp", "{{ .binaryPath }}", "/plugins/robodev-plugin-{{ .name }}"]
    volumeMounts:
      - name: plugins
        mountPath: /plugins
```

The controller container mounts the same volume read-only at `/opt/robodev/plugins` and spawns each binary from there. This keeps plugin binaries out of the controller image, enabling independent versioning.

## Testing

**Unit tests (built-in Go plugins):** Write table-driven tests with `testify` and mock HTTP servers via `httptest.Server`. Place tests alongside source files.

**Integration tests (gRPC plugins):** Start the plugin binary as a subprocess, connect a gRPC client, call `Handshake` to verify the version response, then exercise each RPC. Place these in `tests/integration/`.

**Mock implementations:** For testing controller logic in isolation, create mock backends:

```go
type MockTicketingBackend struct {
    PollFunc func(ctx context.Context) ([]ticketing.Ticket, error)
}

func (m *MockTicketingBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
    return m.PollFunc(ctx)
}
```

## Interface Versioning

Every interface carries an `interface_version`. The handshake protocol enforces compatibility:

1. The controller calls `Handshake` with its expected version.
2. The plugin responds with the version it implements.
3. On mismatch, the controller refuses to load the plugin, logs a structured error, and marks it unhealthy. It will **not** attempt restarts, since restarting cannot resolve an incompatibility.

### Compatibility Expectations

- **Patch releases** never change `interface_version`.
- **Minor releases** may add optional fields to messages but do not bump `interface_version`.
- **Major bumps** to `interface_version` indicate breaking changes (renamed RPCs, removed fields, changed semantics). Plugins must be updated.

The current `interface_version` for all six interfaces is **1**, defined as a constant in each Go package (e.g. `ticketing.InterfaceVersion = 1`).
