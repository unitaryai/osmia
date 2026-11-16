#!/usr/bin/env bash
# test-workdir-setup.sh — local smoke test for the init script and working
# directory that a plugin profile can set, as handled by
# docker/engine-claude-code/setup-claude.sh.
#
# Runs setup-claude.sh in an alpine container with OSMIA_INIT_SCRIPT and
# OSMIA_WORKDIR set, and checks that the init script runs before Claude
# starts, that Claude starts in the working directory, and that a missing
# script or directory stops the job. Requires docker; skips cleanly (exit 0)
# without it.
#
# Usage:
#   ./hack/test-workdir-setup.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
FIXTURE_DIR="${ROOT_DIR}/docker/engine-claude-code"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker is not available — this test runs setup-claude.sh inside a throwaway container."
  exit 0
fi

FAILURES=0

# run_setup runs setup-claude.sh with the given init script and working
# directory (either may be empty) and prints its output. The image has a
# /opt/tool directory and an init script that writes /workspace/init-ran.
# The claude stub prints where it started and whether the init script ran.
run_setup() {
  local init="$1" workdir="$2"
  docker run --rm \
    -v "${FIXTURE_DIR}/mcp.json:/etc/claude-code/mcp.json:ro" \
    -v "${FIXTURE_DIR}/settings.json:/etc/claude-code/settings.json:ro" \
    -v "${FIXTURE_DIR}/setup-claude.sh:/usr/local/bin/setup-claude.sh:ro" \
    -e HOME=/home/osmia \
    -e "OSMIA_INIT_SCRIPT=${init}" \
    -e "OSMIA_WORKDIR=${workdir}" \
    --entrypoint /bin/sh \
    alpine:3.20 \
    -c '
      apk add --no-cache -q jq >/dev/null
      mkdir -p /home/osmia /workspace /opt/tool
      printf "#!/bin/sh\ntouch /workspace/init-ran\n" > /opt/tool/init.sh
      chmod +x /opt/tool/init.sh
      printf "not executable\n" > /opt/tool/plain.txt
      cat >/usr/local/bin/claude <<"STUB"
#!/bin/sh
echo "PWD $(pwd)"
[ -f /workspace/init-ran ] && echo "INIT RAN"
STUB
      chmod +x /usr/local/bin/claude
      cd /workspace
      sh /usr/local/bin/setup-claude.sh -p prompt
    ' 2>&1 || true
}

expect() {
  local name="$1" haystack="$2" needle="$3"
  if grep -qF -- "${needle}" <<<"${haystack}"; then
    echo "PASS: ${name}"
  else
    echo "FAIL: ${name}: expected to find '${needle}' in:"
    while IFS= read -r line; do echo "    ${line}"; done <<<"${haystack}"
    FAILURES=$((FAILURES + 1))
  fi
}

expect_not() {
  local name="$1" haystack="$2" needle="$3"
  if grep -qF -- "${needle}" <<<"${haystack}"; then
    echo "FAIL: ${name}: did not expect '${needle}' in:"
    while IFS= read -r line; do echo "    ${line}"; done <<<"${haystack}"
    FAILURES=$((FAILURES + 1))
  else
    echo "PASS: ${name}"
  fi
}

out="$(run_setup "" "")"
expect "without a profile Claude starts in /workspace" "${out}" "PWD /workspace"
expect_not "without a profile no init script runs" "${out}" "INIT RAN"

out="$(run_setup /opt/tool/init.sh /opt/tool)"
expect "the init script runs before Claude starts" "${out}" "INIT RAN"
expect "Claude starts in the working directory" "${out}" "PWD /opt/tool"

out="$(run_setup /opt/tool/missing.sh "")"
expect "a missing init script stops the job" "${out}" "is not an executable file"
expect_not "Claude does not start after a missing init script" "${out}" "PWD "

out="$(run_setup /opt/tool/plain.txt "")"
expect "a non-executable init script stops the job" "${out}" "is not an executable file"

out="$(run_setup "" /opt/missing)"
expect "a missing working directory stops the job" "${out}" "does not exist in this image"
expect_not "Claude does not start after a missing working directory" "${out}" "PWD "

if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} check(s) failed"
  exit 1
fi
echo "all checks passed"
