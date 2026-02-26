# Writing a Plugin

> **Note:** The plugin SDK and gRPC interfaces are under active development. This guide will be expanded as the SDK stabilises.

## Overview

RoboDev supports two types of plugins:

1. **Built-in plugins** — compiled into the controller binary (Go only)
2. **Third-party plugins** — separate processes communicating over gRPC (any language)

## Plugin Interfaces

All plugin interfaces are defined as protobuf services in `proto/`. The available interfaces are:

- `TicketingBackend` — ticket polling and lifecycle management
- `NotificationChannel` — sending notifications
- `HumanApprovalBackend` — requesting and receiving human approval
- `SecretsBackend` — secret retrieval
- `SCMBackend` — source control management operations
- `ReviewBackend` — automated code review integration

## Getting Started

### Python

```bash
pip install robodev-plugin-sdk
robodev-plugin scaffold --interface ticketing --name my-plugin
```

### Go

```bash
go get github.com/robodev/plugin-sdk-go
```

### TypeScript

```bash
npm install @robodev/plugin-sdk
```

## Interface Versioning

Every plugin interface includes an `interface_version` field. The controller and plugin perform a version handshake at startup. See `proto/` for the current interface versions.

## Testing

```bash
robodev-plugin test --interface ticketing --binary ./my-plugin
```
