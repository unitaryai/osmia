# Osmia Go Plugin SDK

The Go SDK is the controller's own `pkg/plugin/` packages, importable directly
from the main module. There is no separate package to install — third-party
consumers import the interfaces directly from:

```
github.com/unitaryai/osmia
```

## Built-in plugins (compiled into the controller)

Import the relevant interface package and implement it. Register your
implementation in `cmd/osmia/main.go` when constructing the controller.

```go
import (
    "context"

    "github.com/unitaryai/osmia/pkg/engine"
    "github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// MyTicketingBackend implements ticketing.Backend.
type MyTicketingBackend struct{}

func (b *MyTicketingBackend) PollReadyTickets(ctx context.Context) ([]ticketing.Ticket, error) { ... }
func (b *MyTicketingBackend) MarkInProgress(ctx context.Context, id string) error              { ... }
func (b *MyTicketingBackend) MarkComplete(ctx context.Context, id string, r engine.TaskResult) error { ... }
func (b *MyTicketingBackend) MarkFailed(ctx context.Context, id string, reason string) error   { ... }
func (b *MyTicketingBackend) AddComment(ctx context.Context, id, comment string) error         { ... }
func (b *MyTicketingBackend) Name() string                                                     { return "my-backend" }
func (b *MyTicketingBackend) InterfaceVersion() int                                            { return ticketing.InterfaceVersion }
```

Available interface packages:

| Package                                         | Interface             |
|-------------------------------------------------|-----------------------|
| `pkg/plugin/ticketing`                          | `Backend`             |
| `pkg/plugin/notifications`                      | `Channel`             |
| `pkg/plugin/approval`                           | `Backend`             |
| `pkg/plugin/secrets`                            | `Backend`             |
| `pkg/plugin/scm`                                | `Backend`             |
| `pkg/plugin/review`                             | `Backend`             |

## External plugins (out-of-process, via hashicorp/go-plugin)

External plugins run as subprocesses and communicate with the controller over
gRPC using the hashicorp/go-plugin transport. The protobuf service definitions
in `proto/` are the authoritative contract.

Steps:

1. Copy or generate the protobuf stubs (`make sdk-gen` outputs to
   `sdk/go/` via `buf generate`).
2. Implement the generated gRPC server interface.
3. Call `plugin.Serve` from `github.com/hashicorp/go-plugin` with the
   appropriate `GRPCPlugin` wrapper.
4. Configure the controller to load your binary via `osmia-config.yaml`.

Every plugin service exposes a `Handshake` RPC (defined in `proto/common.proto`)
that the controller calls on start-up to verify interface compatibility.

## Further reading

Full plugin authoring guide:
<https://unitaryai.github.io/osmia/plugins/writing-a-plugin/>
