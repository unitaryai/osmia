#!/usr/bin/env bash
# install-hooks.sh: install the recommended git hooks for RoboDev development.
#
# Run once after cloning:
#   ./hack/install-hooks.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOOKS_DIR="${REPO_ROOT}/.git/hooks"

if [[ ! -d "${HOOKS_DIR}" ]]; then
  echo "Error: ${HOOKS_DIR} not found. Are you running this from inside the repo?" >&2
  exit 1
fi

cat > "${HOOKS_DIR}/pre-push" << 'EOF'
#!/usr/bin/env bash
# pre-push: run lint and tests before pushing to catch issues early.
set -euo pipefail

echo "Running golangci-lint..."
if ! golangci-lint run; then
  echo ""
  echo "golangci-lint failed. Push aborted."
  echo "Fix the issues above, then push again."
  exit 1
fi
echo "golangci-lint passed."

echo ""
echo "Running tests..."
if ! go test -race ./...; then
  echo ""
  echo "Tests failed. Push aborted."
  echo "Fix the failures above, then push again."
  exit 1
fi
echo "Tests passed."
EOF

chmod +x "${HOOKS_DIR}/pre-push"

echo "Installed pre-push hook → ${HOOKS_DIR}/pre-push"
echo "It will run golangci-lint and go test -race ./... before every push."
