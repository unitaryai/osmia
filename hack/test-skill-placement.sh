#!/usr/bin/env bash
# test-skill-placement.sh — local smoke test for where
# docker/engine-claude-code/setup-claude.sh writes skills and sub-agents.
#
# Claude Code reads user-scoped skills from ${CLAUDE_CONFIG_DIR}/skills/ when
# that variable is set, and from ${HOME}/.claude/skills/ when it is not. It
# does not fall back from one to the other. Writing to the wrong one produces
# no error: the skill is simply never listed, and the agent fails the first
# /<name> invocation with "Unknown skill". That silence is why this got
# shipped backwards once already, so the placement is pinned here.
#
# Cases:
#   1. CLAUDE_CONFIG_DIR set    — skills and agents land under it, not HOME
#   2. CLAUDE_CONFIG_DIR unset  — they land under ${HOME}/.claude
#   3. Multi-file ConfigMap     — every .md in the mounted directory is copied
#   4. Stale skill              — a skill from a previous run is cleared
#
# setup-claude.sh hardcodes absolute paths (/etc/claude-code/*.json,
# /workspace/.mcp.json) to match the real agent image, so it can only run
# unmodified inside a container with that layout. This uses a throwaway
# alpine container, like hack/test-incident-mcp-setup.sh.
#
# Local developer convenience, not a CI gate. Skips cleanly (exit 0) when
# Docker is unavailable.
#
# Usage:
#   ./hack/test-skill-placement.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
FIXTURE_DIR="${ROOT_DIR}/docker/engine-claude-code"

if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker not found on PATH — this test runs setup-claude.sh inside a throwaway container so its hardcoded absolute paths behave as they do in production."
  exit 0
fi

if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker CLI found but the daemon is not reachable (e.g. Docker Desktop is not running)."
  exit 0
fi

FAILURES=0

# run_case runs setup-claude.sh in a container and prints the resulting tree
# of skill and agent files, one relative path per line, so the caller can
# assert on placement. Extra `docker run` arguments (env vars, mounts) are
# passed through as this function's arguments.
run_case() {
  local workspace
  workspace="$(mktemp -d)"

  docker run --rm \
    -v "${FIXTURE_DIR}/mcp.json:/etc/claude-code/mcp.json:ro" \
    -v "${FIXTURE_DIR}/settings.json:/etc/claude-code/settings.json:ro" \
    -v "${FIXTURE_DIR}/setup-claude.sh:/usr/local/bin/setup-claude.sh:ro" \
    -v "${workspace}:/workspace" \
    -w /workspace \
    -e HOME=/home/osmia \
    "$@" \
    --entrypoint /bin/sh \
    alpine:3.20 \
    -c '
      set -e
      mkdir -p /home/osmia
      printf "#!/bin/sh\n" > /usr/local/bin/claude
      chmod +x /usr/local/bin/claude
      cp /usr/local/bin/setup-claude.sh /tmp/setup-claude.sh
      chmod +x /tmp/setup-claude.sh
      /tmp/setup-claude.sh >/dev/null 2>&1
      # Print every skill/agent file found anywhere it could plausibly land.
      for root in /home/osmia/.claude /session; do
        [ -d "$root" ] || continue
        find "$root" -type f \( -name "*.md" \) 2>/dev/null | sort
      done
    ' 2>/dev/null

  rm -rf "${workspace}"
}

# assert_contains fails the run unless $2 appears in the newline-separated
# list $1.
assert_contains() {
  local haystack="$1" needle="$2" label="$3"
  if printf '%s\n' "${haystack}" | grep -qxF "${needle}"; then
    echo "PASS: ${label}"
  else
    echo "FAIL: ${label}"
    echo "      expected to find: ${needle}"
    echo "      got:"
    printf '%s\n' "${haystack}" | sed 's/^/        /'
    FAILURES=$((FAILURES + 1))
  fi
}

# assert_absent is the inverse: the path must not be present.
assert_absent() {
  local haystack="$1" needle="$2" label="$3"
  if printf '%s\n' "${haystack}" | grep -qxF "${needle}"; then
    echo "FAIL: ${label}"
    echo "      did not expect: ${needle}"
    FAILURES=$((FAILURES + 1))
  else
    echo "PASS: ${label}"
  fi
}

INLINE_B64="$(printf -- '---\nname: probe\ndescription: probe\n---\n\nProbe.\n' | base64 | tr -d '\n')"

echo "=== Case 1: CLAUDE_CONFIG_DIR set ==="
OUT="$(run_case -e "CLAUDE_CONFIG_DIR=/session/claude" -e "CLAUDE_SKILL_INLINE_PROBE=${INLINE_B64}")"
assert_contains "${OUT}" "/session/claude/skills/probe/SKILL.md" \
  "inline skill lands under CLAUDE_CONFIG_DIR"
assert_absent "${OUT}" "/home/osmia/.claude/skills/probe/SKILL.md" \
  "inline skill is not also written under HOME"

echo ""
echo "=== Case 1b: single-segment CLAUDE_CONFIG_DIR (per-taskrun-pvc) ==="
# The per-taskrun-pvc store sets CLAUDE_CONFIG_DIR=/session. A path guard that
# demanded two segments once refused every job on that backend, and Case 1
# could not catch it because it uses the shared-pvc shape. Pin the real value.
OUT="$(run_case -e "CLAUDE_CONFIG_DIR=/session" -e "CLAUDE_SKILL_INLINE_PROBE=${INLINE_B64}")"
assert_contains "${OUT}" "/session/skills/probe/SKILL.md" \
  "inline skill lands under a single-segment CLAUDE_CONFIG_DIR"

echo ""
echo "=== Case 2: CLAUDE_CONFIG_DIR unset ==="
OUT="$(run_case -e "CLAUDE_SKILL_INLINE_PROBE=${INLINE_B64}")"
assert_contains "${OUT}" "/home/osmia/.claude/skills/probe/SKILL.md" \
  "inline skill lands under HOME when CLAUDE_CONFIG_DIR is unset"

echo ""
echo "=== Case 3: multi-file ConfigMap skill ==="
CM_DIR="$(mktemp -d)"
printf -- '---\nname: multi\ndescription: multi\n---\n\nSee reference.md.\n' > "${CM_DIR}/SKILL.md"
printf -- '# Reference\n\nDetail.\n' > "${CM_DIR}/reference.md"
printf -- 'not markdown\n' > "${CM_DIR}/ignored.txt"
OUT="$(run_case \
  -v "${CM_DIR}:/skills/multi:ro" \
  -e "CLAUDE_CONFIG_DIR=/session/claude" \
  -e "CLAUDE_SKILL_DIR_MULTI=/skills/multi")"
assert_contains "${OUT}" "/session/claude/skills/multi/SKILL.md" \
  "multi-file skill copies SKILL.md"
assert_contains "${OUT}" "/session/claude/skills/multi/reference.md" \
  "multi-file skill copies sibling reference files"
rm -rf "${CM_DIR}"

echo ""
echo "=== Case 4: a stale skill from a previous run is cleared ==="
# Seed a skill directly into the persisted config dir, then run with a
# different skill configured. The seeded one must not survive.
STALE_OUT="$(docker run --rm \
  -v "${FIXTURE_DIR}/mcp.json:/etc/claude-code/mcp.json:ro" \
  -v "${FIXTURE_DIR}/settings.json:/etc/claude-code/settings.json:ro" \
  -v "${FIXTURE_DIR}/setup-claude.sh:/usr/local/bin/setup-claude.sh:ro" \
  -w /workspace \
  -e HOME=/home/osmia \
  -e "CLAUDE_CONFIG_DIR=/session/claude" \
  -e "CLAUDE_SKILL_INLINE_PROBE=${INLINE_B64}" \
  --entrypoint /bin/sh \
  alpine:3.20 \
  -c '
    set -e
    mkdir -p /workspace /home/osmia /session/claude/skills/leftover
    printf -- "stale\n" > /session/claude/skills/leftover/SKILL.md
    printf "#!/bin/sh\n" > /usr/local/bin/claude
    chmod +x /usr/local/bin/claude
    cp /usr/local/bin/setup-claude.sh /tmp/setup-claude.sh
    chmod +x /tmp/setup-claude.sh
    /tmp/setup-claude.sh >/dev/null 2>&1
    find /session -type f -name "*.md" | sort
  ' 2>/dev/null)"
assert_absent "${STALE_OUT}" "/session/claude/skills/leftover/SKILL.md" \
  "a skill removed from config does not survive on the persisted volume"
assert_contains "${STALE_OUT}" "/session/claude/skills/probe/SKILL.md" \
  "the configured skill is still written after the clear"

echo ""
echo "=== Case 5: the last skill removed still clears the directory ==="
# No CLAUDE_SKILL_* variables at all. The clear must still run, otherwise a
# skill removed from the controller's config stays readable forever.
EMPTY_OUT="$(docker run --rm \
  -v "${FIXTURE_DIR}/mcp.json:/etc/claude-code/mcp.json:ro" \
  -v "${FIXTURE_DIR}/settings.json:/etc/claude-code/settings.json:ro" \
  -v "${FIXTURE_DIR}/setup-claude.sh:/usr/local/bin/setup-claude.sh:ro" \
  -w /workspace \
  -e HOME=/home/osmia \
  -e "CLAUDE_CONFIG_DIR=/session/claude" \
  --entrypoint /bin/sh \
  alpine:3.20 \
  -c '
    set -e
    mkdir -p /workspace /home/osmia /session/claude/skills/leftover /session/claude/agents
    printf -- "stale\n" > /session/claude/skills/leftover/SKILL.md
    printf -- "stale\n" > /session/claude/agents/leftover.md
    printf "#!/bin/sh\n" > /usr/local/bin/claude
    chmod +x /usr/local/bin/claude
    cp /usr/local/bin/setup-claude.sh /tmp/setup-claude.sh
    chmod +x /tmp/setup-claude.sh
    /tmp/setup-claude.sh >/dev/null 2>&1
    find /session -type f -name "*.md" | sort
  ' 2>/dev/null)"
assert_absent "${EMPTY_OUT}" "/session/claude/skills/leftover/SKILL.md" \
  "removing the last skill clears the skills directory"
assert_absent "${EMPTY_OUT}" "/session/claude/agents/leftover.md" \
  "removing the last sub-agent clears the agents directory"

echo ""
echo "=== Case 6: a config dir that normalises outside itself is refused ==="
# "/tmp/.." passes a naive "nested absolute path" check but resolves to "/",
# which would put the skills path at /skills — the ConfigMap mount point.
for bad in "/tmp/.." "//" "/" "/skills"; do
  if docker run --rm \
      -v "${FIXTURE_DIR}/mcp.json:/etc/claude-code/mcp.json:ro" \
      -v "${FIXTURE_DIR}/settings.json:/etc/claude-code/settings.json:ro" \
      -v "${FIXTURE_DIR}/setup-claude.sh:/usr/local/bin/setup-claude.sh:ro" \
      -w /workspace \
      -e HOME=/home/osmia \
      -e "CLAUDE_CONFIG_DIR=${bad}" \
      -e "CLAUDE_SKILL_INLINE_PROBE=${INLINE_B64}" \
      --entrypoint /bin/sh \
      alpine:3.20 \
      -c '
        set -e
        mkdir -p /workspace /home/osmia /skills/canary
        printf -- "canary\n" > /skills/canary/SKILL.md
        printf "#!/bin/sh\n" > /usr/local/bin/claude
        chmod +x /usr/local/bin/claude
        cp /usr/local/bin/setup-claude.sh /tmp/setup-claude.sh
        chmod +x /tmp/setup-claude.sh

        # Both halves matter and are asserted separately. Letting set -e
        # abort on the expected failure would make the container exit
        # nonzero without ever reaching the canary, so the test would pass
        # even if the script had deleted the ConfigMap mount on its way out.
        set +e
        /tmp/setup-claude.sh >/dev/null 2>&1
        setup_status=$?
        set -e

        # The script must have refused...
        test "${setup_status}" -ne 0
        # ...and must not have touched the mount point on its way to refusing.
        test -f /skills/canary/SKILL.md
      ' >/dev/null 2>&1; then
    echo "PASS: CLAUDE_CONFIG_DIR=${bad} is refused with /skills intact"
  else
    echo "FAIL: CLAUDE_CONFIG_DIR=${bad} was accepted, or /skills was altered"
    FAILURES=$((FAILURES + 1))
  fi
done

echo ""
if [ "${FAILURES}" -eq 0 ]; then
  echo "All skill placement checks passed."
else
  echo "${FAILURES} check(s) failed."
  exit 1
fi
