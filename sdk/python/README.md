# RoboDev Python Plugin SDK

Use this SDK to build external RoboDev plugins in Python. Plugins run as
out-of-process gRPC servers; the RoboDev controller loads them at start-up
via the hashicorp/go-plugin subprocess transport.

## Prerequisites

- Python 3.10 or later
- `grpcio >= 1.60`
- `grpcio-tools >= 1.60`
- `protobuf >= 4.25`

## Installation

> **Note:** The package is not yet published to PyPI — this is a scaffold.
> Install from source for now:

```bash
pip install -e sdk/python/
```

Once published, installation will be:

```bash
pip install robodev-plugin-sdk
```

## Generating protobuf stubs

Run the following from the repository root. This regenerates all Python
(and TypeScript) stubs from the canonical `proto/` definitions:

```bash
make sdk-gen
```

Generated files are written to `sdk/python/src/robodev/proto/`. Do not
edit them by hand — they are overwritten on every `make sdk-gen` run.

## Quick-start — ticketing plugin

```python
import grpc
from concurrent import futures

# Generated stubs — run `make sdk-gen` first.
from robodev.proto import ticketing_pb2, ticketing_pb2_grpc, common_pb2

INTERFACE_VERSION = 1


class MyTicketingPlugin(ticketing_pb2_grpc.TicketingBackendServicer):
    """A minimal custom ticketing backend."""

    def Handshake(self, request, context):
        return common_pb2.HandshakeResponse(
            interface_version=INTERFACE_VERSION,
            plugin_name="my-ticketing",
            plugin_version="0.1.0",
        )

    def PollReadyTickets(self, request, context):
        # Return tickets ready for the agent to process.
        return ticketing_pb2.PollReadyTicketsResponse(tickets=[])

    def MarkComplete(self, request, context):
        return ticketing_pb2.MarkCompleteResponse()


if __name__ == "__main__":
    from robodev.plugin import PluginBase
    PluginBase.serve(MyTicketingPlugin())
```

## Further reading

Full plugin authoring guide:
<https://unitaryai.github.io/robodev/plugins/writing-a-plugin/>
