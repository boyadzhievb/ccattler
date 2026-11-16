#!/usr/bin/env bash
# Self-test for check_groups.sh (M231, spec deploy-test-runs "Gate TLS unticked").
# Runs without a cluster: ./test/e2e/check_groups_selftest.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=test/e2e/check_groups.sh
source "$SCRIPT_DIR/check_groups.sh"

FAILURES=0

# expect_equal compares an actual value with the expected one and counts failures.
expect_equal() {
  local description="$1" actual="$2" expected="$3"
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL  $description: got '$actual', want '$expected'"
    FAILURES=$((FAILURES + 1))
  else
    echo "PASS  $description"
  fi
}

set_skipped_check_groups ""
expect_equal "no flag reports none" "$(skipped_check_groups_report)" "skipped check groups: none"
if check_skipped gate-tls; then result=skipped; else result=run; fi
expect_equal "nothing skipped by default" "$result" "run"

set_skipped_check_groups "gate-tls"
expect_equal "one group reported" "$(skipped_check_groups_report)" "skipped check groups: gate-tls"
check_skipped gate-tls && result=skipped || result=run
expect_equal "gate-tls skipped" "$result" "skipped"
check_skipped node-failure && result=skipped || result=run
expect_equal "node-failure still runs" "$result" "run"

set_skipped_check_groups "gate-tls,node-failure,multiport,warmzero"
expect_equal "all slow groups reported" "$(skipped_check_groups_report)" "skipped check groups: gate-tls,node-failure,multiport,warmzero"

if set_skipped_check_groups "gate-tls,vip" 2>/dev/null; then result=accepted; else result=refused; fi
expect_equal "quick group vip cannot be skipped" "$result" "refused"
if set_skipped_check_groups "gate_tls" 2>/dev/null; then result=accepted; else result=refused; fi
expect_equal "misspelled group refused" "$result" "refused"

if [[ $FAILURES -gt 0 ]]; then
  echo "check_groups self-test: $FAILURES failure(s)"
  exit 1
fi
echo "check_groups self-test: all passed"
