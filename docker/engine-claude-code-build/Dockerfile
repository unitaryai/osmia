# syntax=docker/dockerfile:1

# Claude Code execution environment for build tasks: the default
# engine-claude-code image plus the tools that application build work needs.
#
# Kept separate from the default image so ordinary ticket fixes do not pull a
# larger image with a larger attack surface. Select it per engine or task with
# engines.claude-code.image.
#
# Every download is pinned and checksum-verified against the checksum file
# published with its release, for the same reason as glab in the default
# image: an unverified binary would run with the agent's tokens.
#
# Docker inside the agent pod is not supported. Builds test against a
# development cluster instead.
ARG BASE_IMAGE=ghcr.io/unitaryai/osmia/engine-claude-code:main
FROM ${BASE_IMAGE}

USER root

ARG TARGETARCH

# ffmpeg for cutting review frames from recordings; Xvfb, xauth (for
# xvfb-run) and xdpyinfo so a headed browser can run without a display.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg \
        unzip \
        x11-utils \
        xauth \
        xvfb \
    && rm -rf /var/lib/apt/lists/*

# Map Docker's architecture names to the ones each release uses, failing on
# anything unmapped rather than installing the wrong binary.
RUN case "${TARGETARCH:-amd64}" in \
        amd64|arm64) ;; \
        *) echo "unsupported TARGETARCH '${TARGETARCH}' for the build image" >&2; exit 1 ;; \
    esac

# uv: Python tooling. Installed system-wide so the non-root agent can use it.
# renovate: datasource=github-releases depName=astral-sh/uv
ARG UV_VERSION="0.13.5"
RUN case "${TARGETARCH:-amd64}" in amd64) UV_ARCH="x86_64" ;; arm64) UV_ARCH="aarch64" ;; esac \
    && UV_TARBALL="uv-${UV_ARCH}-unknown-linux-gnu.tar.gz" \
    && UV_BASE="https://github.com/astral-sh/uv/releases/download/${UV_VERSION}" \
    && curl -fsSL "${UV_BASE}/${UV_TARBALL}" -o "/tmp/${UV_TARBALL}" \
    && curl -fsSL "${UV_BASE}/${UV_TARBALL}.sha256" -o /tmp/uv.sha256 \
    && cd /tmp && sha256sum -c /tmp/uv.sha256 \
    && tar -xzf "/tmp/${UV_TARBALL}" -C /tmp \
    && mv "/tmp/uv-${UV_ARCH}-unknown-linux-gnu/uv" "/tmp/uv-${UV_ARCH}-unknown-linux-gnu/uvx" /usr/local/bin/ \
    && uv --version \
    && rm -rf /tmp/uv*

# Python 3.13 and uv tools live outside the home directory, which is an
# emptyDir at runtime, so they survive into the agent pod. Tools are linked
# into /usr/local/bin.
ENV UV_PYTHON_INSTALL_DIR=/opt/uv/python \
    UV_TOOL_DIR=/opt/uv/tools \
    UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_PYTHON_PREFERENCE=only-managed
RUN uv python install 3.13

# copier: project templates.
# renovate: datasource=pypi depName=copier
ARG COPIER_VERSION="9.19.1"
RUN uv tool install --python 3.13 "copier==${COPIER_VERSION}" && copier --version

# Temporal CLI: reads workflow state from a development Temporal server.
# renovate: datasource=github-releases depName=temporalio/cli
ARG TEMPORAL_VERSION="1.9.1"
RUN TEMPORAL_TARBALL="temporal_cli_${TEMPORAL_VERSION}_linux_${TARGETARCH:-amd64}.tar.gz" \
    && TEMPORAL_BASE="https://github.com/temporalio/cli/releases/download/v${TEMPORAL_VERSION}" \
    && curl -fsSL "${TEMPORAL_BASE}/${TEMPORAL_TARBALL}" -o "/tmp/${TEMPORAL_TARBALL}" \
    && curl -fsSL "${TEMPORAL_BASE}/checksums.txt" -o /tmp/temporal-checksums.txt \
    && cd /tmp \
    && awk -v want="${TEMPORAL_TARBALL}" '$2 == want { print; found = 1 } END { exit !found }' \
        /tmp/temporal-checksums.txt > /tmp/temporal-expected.txt \
    && sha256sum -c /tmp/temporal-expected.txt \
    && tar -xzf "/tmp/${TEMPORAL_TARBALL}" -C /tmp temporal \
    && mv /tmp/temporal /usr/local/bin/temporal \
    && temporal --version \
    && rm -rf /tmp/temporal*

# kubectl: inspects workloads on the development cluster.
# renovate: datasource=github-releases depName=kubernetes/kubernetes
ARG KUBECTL_VERSION="1.37.1"
RUN KUBECTL_BASE="https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH:-amd64}" \
    && curl -fsSL "${KUBECTL_BASE}/kubectl" -o /tmp/kubectl \
    && echo "$(curl -fsSL "${KUBECTL_BASE}/kubectl.sha256")  /tmp/kubectl" | sha256sum -c - \
    && install -m 0755 /tmp/kubectl /usr/local/bin/kubectl \
    && kubectl version --client \
    && rm -f /tmp/kubectl

# Playwright and Chromium: drive customer web applications. The browser is
# installed at build time under a fixed path, since the root filesystem is
# read-only at runtime. Playwright browsers are tied to the Playwright
# version: a derived image that uses another version must install its own
# matching browser (playwright install chromium) into the same path.
# renovate: datasource=npm depName=playwright
ARG PLAYWRIGHT_VERSION="1.64.1"
# renovate: datasource=npm depName=@playwright/mcp
ARG PLAYWRIGHT_MCP_VERSION="0.0.83"
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN npm install -g "playwright@${PLAYWRIGHT_VERSION}" "@playwright/mcp@${PLAYWRIGHT_MCP_VERSION}" \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* \
    && chmod -R a+rX "${PLAYWRIGHT_BROWSERS_PATH}"

USER osmia
