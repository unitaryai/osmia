# Example Microsoft Teams Notification Plugin (TypeScript)

This is an example third-party notification plugin for Osmia, written in TypeScript using the Osmia Plugin SDK.

## Overview

This plugin implements the `NotificationChannel` gRPC interface to send notifications to Microsoft Teams channels via incoming webhooks.

## Prerequisites

- Node.js 20+
- `@osmia/plugin-sdk` (npm install @osmia/plugin-sdk)
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
      interface_version: 1
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TEAMS_WEBHOOK_URL` | Microsoft Teams incoming webhook URL |
