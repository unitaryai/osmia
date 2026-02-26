# Security

## Overview

RoboDev is designed as a security-first platform. Running autonomous AI agents against production codebases requires defence in depth at every layer.

## Threat Model

See section 10 of `oss-plan.md` for the complete threat model.

### Key Principles

- **Least privilege** — every component runs with the minimum permissions required
- **Isolation** — each agent job runs in its own Kubernetes pod with restricted security contexts
- **No secrets in logs** — structured logging with redaction of sensitive fields
- **Input validation** — all external input (tickets, plugin responses, webhooks) is validated
- **Guard rails** — configurable safety boundaries at controller, agent, and quality gate levels

## Container Security

All RoboDev containers enforce:

- `runAsNonRoot: true`
- `readOnlyRootFilesystem: true`
- `allowPrivilegeEscalation: false`
- All capabilities dropped

## Vulnerability Disclosure

If you discover a security vulnerability in RoboDev, please report it responsibly. See [SECURITY.md](../SECURITY.md) for our disclosure process.
