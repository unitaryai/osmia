# syntax=docker/dockerfile:1

# OpenAI Codex execution environment for RoboDev agent jobs.
FROM node:22-slim

# Install system dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        python3 \
        curl \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Codex CLI.
RUN npm install -g @openai/codex

# Create non-root user.
RUN groupadd --gid 1000 robodev \
    && useradd --uid 1000 --gid robodev --create-home robodev

# Copy entrypoint.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

WORKDIR /workspace
RUN chown robodev:robodev /workspace

USER robodev

ENTRYPOINT ["entrypoint.sh"]
