# syntax=docker/dockerfile:1

# Claude Code execution environment for Osmia agent jobs.
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

# Install glab (GitLab CLI) for merge request creation.
ARG TARGETARCH
RUN GLAB_VERSION="1.79.0" \
    && if [ "${TARGETARCH:-amd64}" = "arm64" ]; then GLAB_ARCH="arm64"; else GLAB_ARCH="amd64"; fi \
    && curl -fsSL "https://gitlab.com/gitlab-org/cli/-/releases/v${GLAB_VERSION}/downloads/glab_${GLAB_VERSION}_linux_${GLAB_ARCH}.tar.gz" -o /tmp/glab.tar.gz \
    && tar -xzf /tmp/glab.tar.gz -C /tmp \
    && mv /tmp/bin/glab /usr/local/bin/glab \
    && chmod +x /usr/local/bin/glab \
    && rm -rf /tmp/glab* /tmp/bin

# Install Claude Code CLI.
RUN npm install -g @anthropic-ai/claude-code

# Create non-root user (GID/UID 10000 to avoid conflicts with node base image).
RUN groupadd --gid 10000 osmia \
    && useradd --uid 10000 --gid osmia --create-home osmia

# Copy entrypoint, hooks, MCP server, and setup wrapper.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY hooks/ /opt/osmia/hooks/
COPY slack_mcp.py /usr/local/bin/slack_mcp.py
COPY setup-claude.sh /usr/local/bin/setup-claude.sh

# Install MCP configuration and settings at a fixed path outside the home
# emptyDir.  setup-claude.sh copies these into ~/.claude/ and /workspace/
# at container startup so they are visible to Claude Code at runtime.
RUN mkdir -p /etc/claude-code
COPY mcp.json /etc/claude-code/mcp.json
COPY settings.json /etc/claude-code/settings.json

RUN chmod +x /usr/local/bin/entrypoint.sh \
    && chmod +x /opt/osmia/hooks/*.sh \
    && chmod +x /usr/local/bin/slack_mcp.py \
    && chmod +x /usr/local/bin/setup-claude.sh

WORKDIR /workspace
RUN chown osmia:osmia /workspace

USER osmia

ENTRYPOINT ["entrypoint.sh"]
