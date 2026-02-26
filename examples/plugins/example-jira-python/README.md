# Example Jira Ticketing Plugin (Python)

This is an example third-party ticketing plugin for RoboDev, written in Python using the RoboDev Plugin SDK.

## Overview

This plugin implements the `TicketingBackend` gRPC interface to integrate RoboDev with Jira Cloud. It demonstrates how to build a plugin in Python that communicates with the RoboDev controller over gRPC.

## Prerequisites

- Python 3.11+
- `robodev-plugin-sdk` (pip install robodev-plugin-sdk)
- Jira Cloud API token

## Getting Started

```bash
# Install dependencies
pip install -e .

# Run the plugin (port allocated by hashicorp/go-plugin)
robodev-plugin serve --port 0

# Test locally without a controller
robodev-plugin test --interface ticketing --binary "python -m robodev_plugin_jira"
```

## Configuration

Add to your `robodev-config.yaml`:

```yaml
plugins:
  ticketing:
    jira:
      command: "python -m robodev_plugin_jira"
      interface_version: 1
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `JIRA_BASE_URL` | Jira Cloud instance URL (e.g. https://yourorg.atlassian.net) |
| `JIRA_EMAIL` | Jira account email |
| `JIRA_API_TOKEN` | Jira API token |
| `JIRA_PROJECT_KEY` | Jira project key to poll |
| `JIRA_LABEL` | Label to filter issues (default: "robodev") |
