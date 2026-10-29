# Example Microsoft Teams Notification Plugin (TypeScript)

This is an example third-party notification plugin for Osmia, written in TypeScript using the Osmia Plugin SDK.

## Overview

This plugin implements the `NotificationChannel` gRPC interface to send notifications to Microsoft Teams channels via incoming webhooks.

## Prerequisites

- Node.js 20+
- `@unitaryai/osmia-plugin-sdk`, built from `sdk/typescript` (it is not on npm yet; see the [SDK README](../../../sdk/typescript/README.md))
- Microsoft Teams incoming webhook URL

## Getting Started

```bash
# Install dependencies
npm install

# Build
npm run build

# Run the plugin
node dist/index.js

# Test locally
osmia-plugin test --interface notifications --binary "node dist/index.js"
```

## Configuration

Add to your `osmia-config.yaml`:

```yaml
plugins:
  notifications:
    teams:
      command: "node /opt/osmia/plugins/osmia-plugin-teams/dist/index.js"
      interface_version: 3
```

The controller expects notifications interface version 3. This example still
reports version 1 from `interfaceVersion` and has not been updated to the
version 3 contract, so update it before relying on it.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TEAMS_WEBHOOK_URL` | Microsoft Teams incoming webhook URL |
