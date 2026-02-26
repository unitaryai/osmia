# Example Microsoft Teams Notification Plugin (TypeScript)

This is an example third-party notification plugin for RoboDev, written in TypeScript using the RoboDev Plugin SDK.

## Overview

This plugin implements the `NotificationChannel` gRPC interface to send notifications to Microsoft Teams channels via incoming webhooks.

## Prerequisites

- Node.js 20+
- `@robodev/plugin-sdk` (npm install @robodev/plugin-sdk)
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
robodev-plugin test --interface notifications --binary "node dist/index.js"
```

## Configuration

Add to your `robodev-config.yaml`:

```yaml
plugins:
  notifications:
    teams:
      command: "node /opt/robodev/plugins/robodev-plugin-teams/dist/index.js"
      interface_version: 1
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TEAMS_WEBHOOK_URL` | Microsoft Teams incoming webhook URL |
