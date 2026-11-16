#!/usr/bin/env bash
# Self-test for run.sh case selection (M231, spec deploy-test-runs "Replay
# cases can be selected by name"). No cluster needed: uses --list-cases.
#   ./test/e2e/adversarial/run_selftest.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_SCRIPT="$SCRIPT_DIR/run.sh"
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

all_case_count=$(yq '. | length' "$SCRIPT_DIR/cases.yml")
expect_equal "no filter selects every case" "$("$RUN_SCRIPT" --list-cases | wc -l | tr -d ' ')" "$all_case_count"
expect_equal "scale-* selects the two scale cases" "$("$RUN_SCRIPT" --case 'scale-*' --list-cases | tr '\n' ' ')" \
  "scale-undeclared-service scale-upper-bound "
expect_equal "two patterns" "$("$RUN_SCRIPT" --case 'scale-upper-bound,gc-requires-admin' --list-cases | sort | tr '\n' ' ')" \
  "gc-requires-admin scale-upper-bound "

"$RUN_SCRIPT" --case 'no-such-case-*' --list-cases >/dev/null 2>&1
expect_equal "pattern matching nothing fails" "$?" "1"
"$RUN_SCRIPT" --case >/dev/null 2>&1
expect_equal "--case without a value fails" "$?" "1"
"$RUN_SCRIPT" --case '' --list-cases >/dev/null 2>&1
expect_equal "--case with an empty value fails" "$?" "1"

# A filter that matches only known_failure cases runs nothing, so it must fail.
known_failure_cases=$(mktemp)
yq '.[0].known_failure = "self-test"' "$SCRIPT_DIR/cases.yml" > "$known_failure_cases"
first_case_name=$(yq '.[0].name' "$SCRIPT_DIR/cases.yml")
CASES_FILE_OVERRIDE="$known_failure_cases" "$RUN_SCRIPT" --case "$first_case_name" --list-cases >/dev/null 2>&1
expect_equal "filter matching only a known_failure case fails" "$?" "1"
rm -f "$known_failure_cases"

if [[ $FAILURES -gt 0 ]]; then
  echo "run.sh case selection self-test: $FAILURES failure(s)"
  exit 1
fi
echo "run.sh case selection self-test: all passed"
