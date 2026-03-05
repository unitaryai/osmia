"""Example: custom ticketing backend plugin for RoboDev.

This skeleton shows how to implement a TicketingBackend plugin in Python.
The plugin runs as a standalone gRPC server; the RoboDev controller
connects to it over the hashicorp/go-plugin transport.

Prerequisites:
    1. Run ``make sdk-gen`` from the repo root to generate
       ``sdk/python/src/robodev/proto/`` stubs.
    2. Install the SDK: ``pip install -e sdk/python/``

Run the plugin:
    python sdk/python/examples/ticketing/main.py
"""

from __future__ import annotations

# Generated stubs — run ``make sdk-gen`` first.
# from robodev.proto import ticketing_pb2, ticketing_pb2_grpc, common_pb2

from robodev.plugin import PluginBase

# Bump this when the protobuf service definition changes.
INTERFACE_VERSION = 1


# ---------------------------------------------------------------------------
# Plugin implementation
# ---------------------------------------------------------------------------

# Uncomment once stubs are generated:
# class MyTicketingPlugin(ticketing_pb2_grpc.TicketingBackendServicer):
class MyTicketingPlugin:
    """A minimal custom ticketing backend.

    Replace the stub method bodies with your actual integration logic
    (e.g. REST calls to Jira, Linear, Shortcut, etc.).
    """

    def Handshake(self, request, context):
        """Perform version negotiation with the controller."""
        # return common_pb2.HandshakeResponse(
        #     interface_version=INTERFACE_VERSION,
        #     plugin_name="my-ticketing",
        #     plugin_version="0.1.0",
        # )
        raise NotImplementedError

    def PollReadyTickets(self, request, context):
        """Return tickets that are ready to be processed by an agent."""
        # Fetch from your issue tracker here and return a list of Ticket
        # messages. Filter by label, state, or assignee as required.
        raise NotImplementedError

    def MarkInProgress(self, request, context):
        """Transition a ticket to 'in progress'."""
        raise NotImplementedError

    def MarkComplete(self, request, context):
        """Transition a ticket to 'done' and attach the task result."""
        raise NotImplementedError

    def MarkFailed(self, request, context):
        """Transition a ticket to a failure state and record the reason."""
        raise NotImplementedError

    def AddComment(self, request, context):
        """Post a comment on the ticket (progress updates, status, etc.)."""
        raise NotImplementedError


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    PluginBase.serve(MyTicketingPlugin())
