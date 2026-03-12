#!/usr/bin/env bash
# install-deps.sh — Install development dependencies for Osmia
# Run with: sudo ./scripts/install-deps.sh (needs sudo for apt/system installs)
set -euo pipefail

GO_VERSION="1.23.6"
GOLANGCI_LINT_VERSION="v1.63.4"
BUF_VERSION="1.50.0"
HELM_VERSION="v3.17.1"
KIND_VERSION="v0.27.0"
KUBECTL_VERSION="v1.32.2"
GH_VERSION="2.67.0"
COSIGN_VERSION="v2.4.3"
SYFT_VERSION="v1.20.0"

ARCH=$(dpkg --print-architecture 2>/dev/null || echo "amd64")
OS="linux"

echo "=== Osmia dependency installer ==="
echo "Architecture: ${ARCH}"
echo ""

# ─── Tier 1: Core build tools ───

echo "--- [1/11] Go ${GO_VERSION} ---"
if command -v go &>/dev/null && go version | grep -q "go${GO_VERSION}"; then
    echo "Already installed: $(go version)"
else
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.${OS}-${ARCH}.tar.gz" -o /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    # Ensure /usr/local/go/bin is on PATH for this script
    export PATH="/usr/local/go/bin:${PATH}"
    echo "Installed: $(go version)"
fi

# Set up GOPATH/GOBIN for go install commands
export GOPATH="${GOPATH:-/home/${SUDO_USER:-$USER}/go}"
export GOBIN="${GOPATH}/bin"
mkdir -p "${GOBIN}"

echo ""
echo "--- [2/11] gofumpt ---"
if command -v gofumpt &>/dev/null || [ -f "${GOBIN}/gofumpt" ]; then
    echo "Already installed"
else
    GOPATH="${GOPATH}" GOBIN="${GOBIN}" go install mvdan.cc/gofumpt@latest
    echo "Installed to ${GOBIN}/gofumpt"
fi

echo ""
echo "--- [3/11] golangci-lint ${GOLANGCI_LINT_VERSION} ---"
if command -v golangci-lint &>/dev/null || [ -f "${GOBIN}/golangci-lint" ]; then
    echo "Already installed"
else
    curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "${GOBIN}" "${GOLANGCI_LINT_VERSION}"
    echo "Installed to ${GOBIN}/golangci-lint"
fi

echo ""
echo "--- [4/11] protoc (protobuf compiler) ---"
if command -v protoc &>/dev/null; then
    echo "Already installed: $(protoc --version)"
else
    apt-get update -qq && apt-get install -y -qq protobuf-compiler
    echo "Installed: $(protoc --version)"
fi

echo ""
echo "--- [5/11] protoc-gen-go + protoc-gen-go-grpc ---"
if [ -f "${GOBIN}/protoc-gen-go" ] && [ -f "${GOBIN}/protoc-gen-go-grpc" ]; then
    echo "Already installed"
else
    GOPATH="${GOPATH}" GOBIN="${GOBIN}" go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    GOPATH="${GOPATH}" GOBIN="${GOBIN}" go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
    echo "Installed to ${GOBIN}/"
fi

echo ""
echo "--- [6/11] buf ${BUF_VERSION} ---"
if command -v buf &>/dev/null || [ -f "${GOBIN}/buf" ]; then
    echo "Already installed"
else
    curl -fsSL "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-${OS}-$(uname -m)" -o "${GOBIN}/buf"
    chmod +x "${GOBIN}/buf"
    echo "Installed to ${GOBIN}/buf"
fi

# ─── Tier 2: Helm + K8s testing ───

echo ""
echo "--- [7/11] helm ${HELM_VERSION} ---"
if command -v helm &>/dev/null; then
    echo "Already installed: $(helm version --short)"
else
    curl -fsSL https://get.helm.sh/helm-${HELM_VERSION}-${OS}-${ARCH}.tar.gz -o /tmp/helm.tar.gz
    tar -xzf /tmp/helm.tar.gz -C /tmp
    mv /tmp/${OS}-${ARCH}/helm /usr/local/bin/helm
    rm -rf /tmp/${OS}-${ARCH} /tmp/helm.tar.gz
    echo "Installed: $(helm version --short)"
fi

echo ""
echo "--- [8/11] kubectl ${KUBECTL_VERSION} ---"
if command -v kubectl &>/dev/null; then
    echo "Already installed: $(kubectl version --client 2>/dev/null | head -1)"
else
    curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${OS}/${ARCH}/kubectl" -o /usr/local/bin/kubectl
    chmod +x /usr/local/bin/kubectl
    echo "Installed: $(kubectl version --client 2>/dev/null | head -1)"
fi

echo ""
echo "--- [9/11] kind ${KIND_VERSION} ---"
if command -v kind &>/dev/null || [ -f "${GOBIN}/kind" ]; then
    echo "Already installed"
else
    curl -fsSL "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-${OS}-${ARCH}" -o "${GOBIN}/kind"
    chmod +x "${GOBIN}/kind"
    echo "Installed to ${GOBIN}/kind"
fi

# ─── Tier 3: CI/release + GitHub ───

echo ""
echo "--- [10/11] gh (GitHub CLI) ${GH_VERSION} ---"
if command -v gh &>/dev/null; then
    echo "Already installed: $(gh version | head -1)"
else
    curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_${OS}_${ARCH}.deb" -o /tmp/gh.deb
    dpkg -i /tmp/gh.deb
    rm /tmp/gh.deb
    echo "Installed: $(gh version | head -1)"
fi

echo ""
echo "--- [11/11] cosign ${COSIGN_VERSION} + syft ${SYFT_VERSION} ---"
if command -v cosign &>/dev/null; then
    echo "cosign already installed"
else
    curl -fsSL "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/cosign-${OS}-${ARCH}" -o /usr/local/bin/cosign
    chmod +x /usr/local/bin/cosign
    echo "cosign installed"
fi
if command -v syft &>/dev/null; then
    echo "syft already installed"
else
    curl -fsSL "https://github.com/anchore/syft/releases/download/${SYFT_VERSION}/syft_${SYFT_VERSION#v}_${OS}_${ARCH}.tar.gz" -o /tmp/syft.tar.gz
    tar -xzf /tmp/syft.tar.gz -C /usr/local/bin syft
    rm /tmp/syft.tar.gz
    echo "syft installed"
fi

# ─── PATH setup ───

echo ""
echo "=== Ensuring PATH includes Go and GOBIN ==="

PROFILE_FILE="/home/${SUDO_USER:-$USER}/.bashrc"
PATH_LINES='export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"'

if ! grep -qF '/usr/local/go/bin' "${PROFILE_FILE}" 2>/dev/null; then
    echo "" >> "${PROFILE_FILE}"
    echo "# Go and Go-installed tools" >> "${PROFILE_FILE}"
    echo "${PATH_LINES}" >> "${PROFILE_FILE}"
    echo "Added PATH entries to ${PROFILE_FILE}"
else
    echo "PATH entries already present in ${PROFILE_FILE}"
fi

# ─── Verify ───

echo ""
echo "=== Verification ==="
export PATH="/usr/local/go/bin:${GOBIN}:${PATH}"

for cmd in go gofumpt golangci-lint protoc protoc-gen-go protoc-gen-go-grpc buf helm kubectl kind gh cosign syft; do
    if command -v "${cmd}" &>/dev/null; then
        printf "  %-25s OK\n" "${cmd}"
    else
        printf "  %-25s MISSING\n" "${cmd}"
    fi
done

echo ""
echo "=== Done ==="
echo "Run 'source ~/.bashrc' or open a new terminal to pick up PATH changes."
