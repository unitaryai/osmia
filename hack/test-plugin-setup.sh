#!/usr/bin/env bash
# test-plugin-setup.sh — local smoke test for the plugin loading performed by
# docker/engine-claude-code/setup-claude.sh.
#
# Builds a throwaway plugin marketplace repository with two commits, then
# runs setup-claude.sh in an alpine container with OSMIA_PLUGINS pointing at
# it, and checks that:
#   - each chosen plugin is passed to claude as --plugin-dir, read from the
#     pinned commit rather than the latest one;
#   - plugin token variables are unset before claude starts;
#   - an unknown plugin, a source outside the repository and a path that
#     climbs out of it each stop the job.
#
# Like hack/test-incident-mcp-setup.sh, setup-claude.sh uses the container's
# absolute paths, so it runs inside a container. Requires docker and git, and
# skips cleanly (exit 0) when either is unavailable.
#
# Usage:
#   ./hack/test-plugin-setup.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
FIXTURE_DIR="${ROOT_DIR}/docker/engine-claude-code"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker is not available — this test runs setup-claude.sh inside a throwaway container."
  exit 0
fi
if ! command -v git >/dev/null 2>&1; then
  echo "SKIP: git not found on PATH — needed to build the fixture repository."
  exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
FAILURES=0

# build_marketplace creates the fixture repository, tags its first commit v1
# and prints that commit's SHA. The second commit, on branch main, changes a
# file, so a checkout of the wrong commit is detectable.
build_marketplace() {
  local repo="${WORK}/marketplace"
  mkdir -p "${repo}/.claude-plugin" "${repo}/plugins/alpha/skills/hello" "${repo}/plugins/beta"
  cat >"${repo}/.claude-plugin/marketplace.json" <<'JSON'
{
  "name": "fixture",
  "plugins": [
    {"name": "alpha", "source": "./plugins/alpha"},
    {"name": "beta", "source": "plugins/beta"},
    {"name": "remote", "source": {"source": "github", "repo": "example/remote"}},
    {"name": "escape", "source": "./plugins/../../outside"},
    {"name": "linked", "source": "./plugins/linked"}
  ]
}
JSON
  echo "first" >"${repo}/plugins/alpha/skills/hello/SKILL.md"
  echo "beta" >"${repo}/plugins/beta/README.md"
  # A plugin directory that is a symlink out of the repository.
  ln -s /etc "${repo}/plugins/linked"
  git -C "${repo}" init -q -b main
  git -C "${repo}" -c user.name=t -c user.email=t@t add -A
  git -C "${repo}" -c user.name=t -c user.email=t@t commit -q -m first
  git -C "${repo}" tag v1
  git -C "${repo}" rev-parse HEAD
  echo "second" >"${repo}/plugins/alpha/skills/hello/SKILL.md"
  git -C "${repo}" -c user.name=t -c user.email=t@t commit -q -am second
}

# run_setup runs setup-claude.sh with OSMIA_PLUGINS=$1 and prints its output.
# The claude stub prints each argument on its own line, then any plugin token
# variable still set, then the alpha skill file it would load.
run_setup() {
  local plugins="$1"
  docker run --rm \
    -v "${FIXTURE_DIR}/mcp.json:/etc/claude-code/mcp.json:ro" \
    -v "${FIXTURE_DIR}/settings.json:/etc/claude-code/settings.json:ro" \
    -v "${FIXTURE_DIR}/setup-claude.sh:/usr/local/bin/setup-claude.sh:ro" \
    -v "${WORK}/marketplace:/fixture/marketplace:ro" \
    -e HOME=/home/osmia \
    -e "OSMIA_PLUGINS=${plugins}" \
    -e OSMIA_PLUGIN_TOKEN_FIXTURE=not-a-real-token \
    --entrypoint /bin/sh \
    alpine:3.20 \
    -c '
      apk add --no-cache -q git jq >/dev/null
      mkdir -p /home/osmia /workspace
      git config --global --add safe.directory "*"
      cat >/usr/local/bin/claude <<"STUB"
#!/bin/sh
for a in "$@"; do echo "ARG $a"; done
env | grep "^OSMIA_PLUGIN_TOKEN_" | sed "s/^/LEAK /"
cat /home/osmia/.osmia-plugins/fixture/plugins/alpha/skills/hello/SKILL.md | sed "s/^/SKILL /"
STUB
      chmod +x /usr/local/bin/claude
      cd /workspace
      sh /usr/local/bin/setup-claude.sh -p prompt
    ' 2>&1 || true
}

spec() {
  local ref="$1" plugins="$2"
  printf '[{"source":"fixture","repo_url":"file:///fixture/marketplace","ref":"%s","token_env":"OSMIA_PLUGIN_TOKEN_FIXTURE","token_username":"x-access-token","plugins":%s}]' \
    "${ref}" "${plugins}"
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

FIRST_SHA="$(build_marketplace)"

out="$(run_setup "$(spec "${FIRST_SHA}" '["alpha","beta"]')")"
expect "alpha is loaded" "${out}" "ARG /home/osmia/.osmia-plugins/fixture/plugins/alpha"
expect "beta is loaded" "${out}" "ARG /home/osmia/.osmia-plugins/fixture/plugins/beta"
expect "the pinned commit is checked out" "${out}" "SKILL first"
expect_not "the token is unset before claude starts" "${out}" "LEAK "

out="$(run_setup "$(spec "${FIRST_SHA}" '["missing"]')")"
expect "an unknown plugin stops the job" "${out}" "plugin 'missing' is listed 0 times"
expect_not "claude does not start after an unknown plugin" "${out}" "ARG -p"

out="$(run_setup "$(spec "${FIRST_SHA}" '["remote"]')")"
expect "a source outside the repository stops the job" "${out}" "must have a relative path source"

out="$(run_setup "$(spec "${FIRST_SHA}" '["escape"]')")"
expect "a path climbing out of the repository stops the job" "${out}" "must have a relative path source"

out="$(run_setup "$(spec "${FIRST_SHA}" '["linked"]')")"
expect "a plugin directory symlinked out of the repository stops the job" "${out}" "resolves outside the repository"
expect_not "claude does not start after a symlink escape" "${out}" "ARG -p"

out="$(run_setup "$(spec "v1" '["alpha"]')")"
expect "a tag loads the tagged commit" "${out}" "SKILL first"

out="$(run_setup "$(spec "main" '["alpha"]')")"
expect "a branch name is not fetched as a branch" "${out}" "cannot fetch"
expect_not "the branch tip is never loaded" "${out}" "SKILL second"

out="$(run_setup "$(spec "0000000000000000000000000000000000000000" '["alpha"]')")"
expect "a ref that does not exist stops the job" "${out}" "cannot fetch"

if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} check(s) failed"
  exit 1
fi
echo "all checks passed"
