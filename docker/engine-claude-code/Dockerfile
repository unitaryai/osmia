# syntax=docker/dockerfile:1

# Claude Code execution environment for RoboDev agent jobs.
FROM node:22-slim

# Install system dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        python3 \
        python3-httpx \
        curl \
        jq \
        ca-certificates \
        gnupg \
    && mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | gpg --dearmor -o /etc/apt/keyrings/github-cli.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/github-cli.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI.
RUN npm install -g @anthropic-ai/claude-code

# Create non-root user (GID/UID 10000 to avoid conflicts with node base image).
RUN groupadd --gid 10000 robodev \
    && useradd --uid 10000 --gid robodev --create-home robodev

# Copy entrypoint, hooks, MCP server, and setup wrapper.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY hooks/ /opt/robodev/hooks/
COPY slack_mcp.py /usr/local/bin/slack_mcp.py
COPY setup-claude.sh /usr/local/bin/setup-claude.sh

# Install MCP configuration and settings at a fixed path outside the home
# emptyDir.  setup-claude.sh copies these into ~/.claude/ and /workspace/
# at container startup so they are visible to Claude Code at runtime.
RUN mkdir -p /etc/claude-code
COPY mcp.json /etc/claude-code/mcp.json
COPY settings.json /etc/claude-code/settings.json

RUN chmod +x /usr/local/bin/entrypoint.sh \
    && chmod +x /opt/robodev/hooks/*.sh \
    && chmod +x /usr/local/bin/slack_mcp.py \
    && chmod +x /usr/local/bin/setup-claude.sh

WORKDIR /workspace
RUN chown robodev:robodev /workspace

USER robodev

ENTRYPOINT ["entrypoint.sh"]
