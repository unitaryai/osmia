# syntax=docker/dockerfile:1

# Claude Code execution environment for RoboDev agent jobs.
FROM node:22-slim

# Install system dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        python3 \
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

# Create non-root user.
RUN groupadd --gid 1000 robodev \
    && useradd --uid 1000 --gid robodev --create-home robodev

# Copy entrypoint and hooks.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY hooks/ /opt/robodev/hooks/

RUN chmod +x /usr/local/bin/entrypoint.sh \
    && chmod +x /opt/robodev/hooks/*.sh

WORKDIR /workspace
RUN chown robodev:robodev /workspace

USER robodev

ENTRYPOINT ["entrypoint.sh"]
