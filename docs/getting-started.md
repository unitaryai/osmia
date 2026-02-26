# Getting Started

> **Note:** RoboDev is under active development. This guide will be expanded as features land.

## Prerequisites

- Kubernetes cluster (1.28+)
- Helm 3.x
- A supported ticketing backend (GitHub Issues for the quickest start)
- API credentials for your chosen AI coding agent (Claude Code or Codex)

## Installation

```bash
helm repo add robodev https://robodev-inc.github.io/robodev
helm install robodev robodev/robodev -f values.yaml
```

## Configuration

RoboDev is configured via `robodev-config.yaml`, mounted as a ConfigMap. See `charts/robodev/values.yaml` for all available options.

## Next Steps

- [Architecture overview](architecture.md)
- [Writing a plugin](plugins/writing-a-plugin.md)
- [Security model](security.md)
