# Writing a Plugin

!!! tip "New to Osmia plugins?"
    For a high-level overview of what plugins do and which types exist, see the Plugins section of [What is Osmia?](../concepts/what-is-osmia.md#what-are-plugins).

## Overview

Osmia is extended through plugins: modular backends that integrate with external services. There are two types:

1. Built-in plugins are compiled into the controller binary (Go only). The GitHub Issues ticketing backend at `pkg/plugin/ticketing/github/` is an example.
2. Third-party plugins are standalone processes that talk gRPC through [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin). They can be written in any language and suit organisation-specific integrations (Jira, PagerDuty, Slack).

Both types implement the same logical interface; they differ only in how they are compiled, deployed and loaded.

!!! warning "External plugins are not loaded yet"
    The controller does not start third-party plugin subprocesses today. The plugin host (`pkg/plugin/host.go`) is written but not called from the controller, and config such as `ticketing.backend: jira` is rejected as unsupported. The guidance on third-party plugins below describes the intended contract; use the examples in `examples/plugins/` as templates until loading is wired in.

## Plugin interfaces

Every plugin implements one of six interfaces, defined as protobuf services in `proto/` with corresponding Go interfaces in `pkg/plugin/`:

| Interface | Protobuf service | Description |
|-----------|-----------------|-------------|
| Ticketing | `TicketingBackend` | Polls issue trackers for ready tickets and manages lifecycle. See [ticketing.md](ticketing.md). |
| Notifications | `NotificationChannel` | Sends fire-and-forget notifications (Slack, Teams, email). See [notifications.md](notifications.md). |
| Approval | `HumanApprovalBackend` | Requests and receives human approval before critical operations. |
| Secrets | `SecretsBackend` | Retrieves secrets from external vaults at runtime. See [secrets.md](secrets.md). |
| SCM | `SCMBackend` | Source control operations: cloning, branching, creating merge requests. |
| Review | `ReviewBackend` | Automated code review integration. |

All six include a `Handshake` RPC for version negotiation. Shared message types (`HandshakeRequest`, `HandshakeResponse`, `Ticket`, `TaskResult`) are in `proto/common.proto`.

## Architecture

Third-party plugins use the hashicorp/go-plugin subprocess model. The controller spawns each plugin as a child process and talks to it over gRPC on a local socket. This gives you:

- Language independence: any language with gRPC support.
- Process isolation: a crashing plugin does not bring down the controller.
- Automatic restart: the `Host` (`pkg/plugin/host.go`) restarts failed plugins with exponential backoff.

### Lifecycle

1. Version check. Before spawning the subprocess, the `Host` validates the plugin's declared `interface_version` (from config) against the controller's expected version for that plugin type. If they do not match, loading is refused immediately with a structured error. This avoids spawning a subprocess that cannot satisfy the controller's contract.
2. Startup. The `Host` spawns each plugin binary via `exec.Command` and establishes a gRPC connection with a transport-level handshake (magic cookie verification via hashicorp/go-plugin).
3. Health monitoring. The `Host` tracks health status. Unresponsive plugins are marked unhealthy.
4. Restart with backoff. Failed plugins are restarted up to `max_plugin_restarts` times (default: 3) with configurable backoff (default: 1s, 5s, 30s). After the maximum, the plugin is permanently unhealthy. Version mismatches are not retried, since restarting cannot resolve an incompatibility.
5. Shutdown. `Host.Shutdown()` kills all plugin subprocesses.

The controller verifies plugin identity using a magic cookie:

```go
goplugin.HandshakeConfig{
    ProtocolVersion:  1,
    MagicCookieKey:   "OSMIA_PLUGIN",
    MagicCookieValue: "osmia",
}
```

## Writing a Go plugin (built-in)

### Step 1: Implement the interface

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

### Step 2: Register with the controller

Register your backend in the controller's initialisation logic so it is selected when `ticketing.backend: yourbackend` is configured. See `pkg/plugin/ticketing/github/github.go` for the pattern.

### Step 3: Write tests

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

## Writing a gRPC plugin (third-party)

### Step 1: Generate stubs from protobuf

The protobuf definitions live in `proto/`. Use `buf` to generate stubs:

```bash
buf generate proto/                            # Go stubs
buf generate proto/ --template buf.gen.python.yaml   # Python stubs
buf generate proto/ --template buf.gen.ts.yaml       # TypeScript stubs
```

### Step 2: Implement the service

Implement every RPC in the protobuf service. Errors are conveyed via gRPC status codes.

### Step 3: Include the Handshake RPC

Every plugin must implement `Handshake`. Your `HandshakeResponse` must include:

- `interface_version`: the version your plugin implements. It must match the controller's expected version for that interface type (see [Interface versioning](#interface-versioning) below).
- `plugin_name`: for example `"jira"`, `"slack"`.
- `plugin_version`: the semver of your binary, for example `"1.2.0"`.

### Step 4: Build as a standalone binary

Package it as a single executable. The controller spawns it via `exec.Command`, so it must be directly runnable.

### Step 5: Configure in osmia-config.yaml

```yaml
plugins:
  - name: jira
    command: /opt/osmia/plugins/osmia-plugin-jira
    type: ticketing
    interface_version: 1
```

### Python example

A Jira ticketing plugin is provided at `examples/plugins/example-jira-python/`. Key excerpt from `osmia_plugin_jira/__main__.py`:

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
    # from osmia_plugin_sdk import serve
    # serve(JiraTicketingBackend(), interface="ticketing")
    pass
```

Packaged with a `Dockerfile` and `pyproject.toml`. See the full source for details.

### TypeScript example

A Teams notification plugin is provided at `examples/plugins/example-teams-ts/`. Key excerpt from `src/index.ts`:

```typescript
class TeamsNotificationChannel {
  get name(): string { return 'teams'; }
  get interfaceVersion(): number { return 1; }

  async notify(message: string, ticket: Ticket): Promise<void> {
    await this.sendCard({ title: `Osmia: ${ticket.title}`, text: message });
  }

  async notifyComplete(ticket: Ticket, result: TaskResult): Promise<void> {
    const colour = result.success ? '00CC6A' : 'FF4444';
    await this.sendCard({ title: ticket.title, text: result.summary, themeColor: colour });
  }

  // ... POST Adaptive Card to Teams webhook ...
}

// import { serve } from '@osmia/plugin-sdk';
// serve(channel, { interface: 'notifications' });
```

Build with `tsc`, run with `node dist/index.js`.

## Protobuf definitions

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

- `Ticket`: id, title, description, ticket_type, labels, repo_url, external_url, and a `google.protobuf.Struct raw` field for backend-specific data.
- `TaskResult`: success, merge_request_url, branch_name, summary, token_usage, cost_estimate_usd, exit_code (0=success, 1=agent failure, 2=guard rail blocked).

## Plugin configuration

### Helm values.yaml

External plugins are configured under the `plugins` key in `charts/osmia/values.yaml`:

```yaml
plugins:
  - name: jira
    image: ghcr.io/myorg/osmia-plugin-jira:v1.2.0
    binaryPath: /plugin
  - name: pagerduty
    image: ghcr.io/myorg/osmia-plugin-pagerduty:v0.3.1
    binaryPath: /plugin
```

Each entry specifies an OCI image containing the plugin binary and the path to the executable within that image.

### Health configuration

In `osmia-config.yaml`:

```yaml
plugin_health:
  max_plugin_restarts: 3
  restart_backoff: [1, 5, 30]  # seconds between restart attempts
```

## Deploying with Helm

Osmia loads plugins with init containers. For each entry in `plugins`, the Helm chart (`charts/osmia/templates/deployment.yaml`) creates an init container that copies the plugin binary into a shared `emptyDir` volume:

```yaml
initContainers:
  - name: plugin-{{ .name }}
    image: {{ .image }}
    command: ["cp", "{{ .binaryPath }}", "/plugins/osmia-plugin-{{ .name }}"]
    volumeMounts:
      - name: plugins
        mountPath: /plugins
```

The controller container mounts the same volume read-only at `/opt/osmia/plugins` and spawns each binary from there. This keeps plugin binaries out of the controller image, so they can be versioned independently.

## Testing

Unit tests (built-in Go plugins): Write table-driven tests with `testify` and mock HTTP servers via `httptest.Server`. Place tests alongside source files.

Integration tests (gRPC plugins): Start the plugin binary as a subprocess, connect a gRPC client, call `Handshake` to verify the version response, then exercise each RPC. Place these in `tests/integration/`.

Mock implementations: For testing controller logic in isolation, create mock backends:

```go
type MockTicketingBackend struct {
    PollFunc func(ctx context.Context) ([]ticketing.Ticket, error)
}

func (m *MockTicketingBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) {
    return m.PollFunc(ctx)
}
```

## Interface versioning

Every interface carries an `interface_version`. Compatibility is checked at two levels:

1. Pre-spawn config check. Before starting the subprocess, the controller compares the `interface_version` declared in `osmia-config.yaml` against its expected version for the plugin type. A mismatch is rejected immediately with a structured error, without spawning the binary.
2. Transport handshake. hashicorp/go-plugin verifies the magic cookie when the subprocess connects, confirming it is a valid Osmia plugin binary.

On a version mismatch, the controller refuses to load the plugin. `LoadPlugin` returns an error and no plugin instance is stored. The plugin is never registered, so there is no health state to track. Restarts are not attempted, since restarting cannot resolve a version incompatibility.

!!! note "Handshake RPC"
    The `Handshake` RPC defined in each protobuf service is required: external plugins must implement it. The controller will call it at startup once generated proto stubs are available. Until then, enforcement is config-level: you must set `interface_version` in your plugin config to match the version your binary implements.

### Compatibility expectations

- Patch releases never change `interface_version`.
- Minor releases may add optional fields to messages but do not bump `interface_version`.
- Major bumps to `interface_version` indicate breaking changes (renamed RPCs, removed fields, changed semantics), and plugins must be updated.

The current `interface_version` for each interface is defined as a constant in the corresponding Go package:

| Interface | Version | Constant |
|-----------|---------|----------|
| Ticketing | 1 | `ticketing.InterfaceVersion` |
| Notifications | 3 | `notifications.InterfaceVersion` |
| Approval | 1 | `approval.InterfaceVersion` |
| Secrets | 1 | `secrets.InterfaceVersion` |
| Review | 1 | `review.InterfaceVersion` |
| SCM | 2 | `scm.InterfaceVersion` |

SCM was bumped to 2 when `ListReviewComments`, `ReplyToComment`, `ResolveThread`, and `GetDiff` were added.
