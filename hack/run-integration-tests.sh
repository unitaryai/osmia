#!/usr/bin/env bash
# run-integration-tests.sh — orchestrates the full Osmia integration test suite.
#
# Phases:
#   1. Setup  — build, create kind cluster, deploy with test values
#   2. Tier 2/3 — in-process integration tests (no cluster needed)
#   3. Tier 1 — e2e tests against the kind cluster
#   4. Report — markdown summary of all results
#   5. Teardown — remove cluster
#
# Usage:
#   ./hack/run-integration-tests.sh                     # full run
#   ./hack/run-integration-tests.sh --skip-setup        # reuse existing cluster
#   ./hack/run-integration-tests.sh --skip-teardown     # keep cluster for debugging
#   ./hack/run-integration-tests.sh 2>/dev/null | claude -p "Analyse this test report."

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

# --- Flags ---
SKIP_SETUP=false
SKIP_TEARDOWN=false
for arg in "$@"; do
  case "${arg}" in
    --skip-setup)    SKIP_SETUP=true ;;
    --skip-teardown) SKIP_TEARDOWN=true ;;
    --help|-h)
      echo "Usage: $0 [--skip-setup] [--skip-teardown]"
      exit 0
      ;;
    *)
      echo "Unknown flag: ${arg}" >&2
      exit 1
      ;;
  esac
done

# --- Temp files for JSON output ---
INTEGRATION_JSON=$(mktemp /tmp/osmia-integration-XXXXXX.json)
E2E_JSON=$(mktemp /tmp/osmia-e2e-XXXXXX.json)
trap 'rm -f "${INTEGRATION_JSON}" "${E2E_JSON}"' EXIT

EXIT_CODE=0

# --- Phase 1: Setup ---
if [[ "${SKIP_SETUP}" == "false" ]]; then
  echo "=== Phase 1: Setup ===" >&2
  echo "Building controller..." >&2
  make build >&2 2>&1

  echo "Building dev container image..." >&2
  make docker-build-dev-controller >&2 2>&1

  echo "Creating kind cluster..." >&2
  make kind-create >&2 2>&1

  echo "Loading images into kind..." >&2
  make kind-load >&2 2>&1

  echo "Deploying with test values..." >&2
  make deploy-test >&2 2>&1

  echo "Waiting for deployment to be ready..." >&2
  kubectl rollout status deployment/osmia -n osmia --timeout=120s >&2 2>&1
  echo "Setup complete." >&2
else
  echo "=== Skipping setup ===" >&2
fi

# --- Phase 2: Tier 2/3 integration tests ---
echo "=== Phase 2: Integration tests (Tier 2/3) ===" >&2
if go test -tags=integration -count=1 -v -json ./tests/integration/... > "${INTEGRATION_JSON}" 2>&1; then
  echo "Integration tests passed." >&2
else
  echo "Integration tests had failures." >&2
  EXIT_CODE=1
fi

# --- Phase 3: Tier 1 e2e tests ---
echo "=== Phase 3: E2E tests (Tier 1) ===" >&2
if go test -tags=e2e -count=1 -v -json ./tests/e2e/... > "${E2E_JSON}" 2>&1; then
  echo "E2E tests passed." >&2
else
  echo "E2E tests had failures." >&2
  EXIT_CODE=1
fi

# --- Phase 4: Report ---
# Parse go test -json output into markdown tables.
parse_json_results() {
  local json_file="$1"
  local suite_name="$2"

  echo "### ${suite_name}"
  echo ""
  echo "| Test | Status | Duration | Error |"
  echo "|------|--------|----------|-------|"

  # Extract test results (Action=pass or Action=fail lines with a Test field).
  python3 -c "
import json, sys

results = {}
for line in open('${json_file}'):
    line = line.strip()
    if not line:
        continue
    try:
        obj = json.loads(line)
    except json.JSONDecodeError:
        continue
    action = obj.get('Action', '')
    test = obj.get('Test', '')
    if not test:
        continue
    if action in ('pass', 'fail', 'skip'):
        elapsed = obj.get('Elapsed', 0)
        results[test] = {'status': action, 'elapsed': elapsed}
    elif action == 'output' and test in results and results[test]['status'] == 'fail':
        output = obj.get('Output', '').strip()
        if output and 'FAIL' not in output and '---' not in output:
            results.setdefault(test, {}).setdefault('error', '')
            results[test]['error'] = output

for test, info in sorted(results.items()):
    status = info['status']
    elapsed = info.get('elapsed', 0)
    error = info.get('error', '').replace('|', '\\\\|')[:120]
    icon = '✅' if status == 'pass' else ('❌' if status == 'fail' else '⏭️')
    print(f'| {test} | {icon} {status} | {elapsed:.2f}s | {error} |')
" 2>/dev/null || echo "| (parse error) | ⚠️ | — | Could not parse JSON output |"

  echo ""
}

# Count pass/fail from JSON.
count_results() {
  local json_file="$1"
  python3 -c "
import json
passed = failed = skipped = 0
for line in open('${json_file}'):
    line = line.strip()
    if not line:
        continue
    try:
        obj = json.loads(line)
    except json.JSONDecodeError:
        continue
    if obj.get('Test') and obj.get('Action') == 'pass':
        passed += 1
    elif obj.get('Test') and obj.get('Action') == 'fail':
        failed += 1
    elif obj.get('Test') and obj.get('Action') == 'skip':
        skipped += 1
print(f'{passed} passed, {failed} failed, {skipped} skipped')
" 2>/dev/null || echo "unknown"
}

INTEGRATION_SUMMARY=$(count_results "${INTEGRATION_JSON}")
E2E_SUMMARY=$(count_results "${E2E_JSON}")

# Generate the markdown report to stdout.
cat <<REPORT_EOF
# Osmia Integration Test Report

**Date**: $(date -u '+%Y-%m-%d %H:%M:%S UTC')
**Branch**: $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
**Commit**: $(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

## Summary

| Suite | Result |
|-------|--------|
| Integration (Tier 2/3) | ${INTEGRATION_SUMMARY} |
| E2E (Tier 1) | ${E2E_SUMMARY} |

## Detailed Results

REPORT_EOF

parse_json_results "${INTEGRATION_JSON}" "Integration Tests (Tier 2/3)"
parse_json_results "${E2E_JSON}" "E2E Tests (Tier 1)"

# Append controller logs.
echo "## Controller Logs (last 50 lines)"
echo ""
echo '```'
kubectl logs -n osmia -l app.kubernetes.io/name=osmia --tail=50 2>/dev/null || echo "(no logs available — cluster may be down)"
echo '```'

# --- Phase 5: Teardown ---
if [[ "${SKIP_TEARDOWN}" == "false" ]]; then
  echo "=== Phase 5: Teardown ===" >&2
  make local-down >&2 2>&1 || true
  echo "Teardown complete." >&2
else
  echo "=== Skipping teardown ===" >&2
fi

exit ${EXIT_CODE}
