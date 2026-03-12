# syntax=docker/dockerfile:1

# OpenAI Codex execution environment for Osmia agent jobs.
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

# Create non-root user (GID/UID 10000 to avoid conflicts with node base image).
RUN groupadd --gid 10000 osmia \
    && useradd --uid 10000 --gid osmia --create-home osmia

# Copy entrypoint.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

WORKDIR /workspace
RUN chown osmia:osmia /workspace

USER osmia

ENTRYPOINT ["entrypoint.sh"]
