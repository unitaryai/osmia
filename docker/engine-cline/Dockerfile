# syntax=docker/dockerfile:1

# Cline CLI execution environment for Osmia agent jobs.
FROM node:22-slim

# Install system dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        python3 \
        curl \
        jq \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Cline CLI (headless mode).
RUN npm install -g @anthropic-ai/cline

# Create non-root user.
RUN groupadd --gid 1000 osmia \
    && useradd --uid 1000 --gid osmia --create-home osmia

# Copy entrypoint.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

WORKDIR /workspace
RUN chown osmia:osmia /workspace

USER osmia

ENTRYPOINT ["entrypoint.sh"]
