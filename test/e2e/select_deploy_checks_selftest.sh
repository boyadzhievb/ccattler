#!/usr/bin/env bash
# Self-test for select_deploy_checks.sh (M231, spec deploy-test-runs): what a
# manual and a push run execute. Needs git and yq, no cluster.
#   ./test/e2e/select_deploy_checks_selftest.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SELECT_SCRIPT="$SCRIPT_DIR/select_deploy_checks.sh"
FAILURES=0

# selection runs the script with the given environment and prints one key's value.
selection() {
  local wanted_key="$1"; shift
  env -i PATH="$PATH" HOME="$HOME" "$@" "$SELECT_SCRIPT" | sed -n "s/^${wanted_key}=//p"
}

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

# Manual run, form unchanged: full run.
expect_equal "unchanged form is a full run" "$(selection run_kind EVENT_NAME=workflow_dispatch)" "full"
expect_equal "unchanged form skips nothing" "$(selection skip_checks EVENT_NAME=workflow_dispatch)" ""
expect_equal "unchanged form runs the replay" "$(selection run_adversarial EVENT_NAME=workflow_dispatch)" "true"
expect_equal "unchanged form keeps the default workloads" "$(selection workload EVENT_NAME=workflow_dispatch)" \
  "airflow,whoami,whoami-multiport,gate-tls"

# Manual run, boxes unticked.
expect_equal "gate TLS unticked" "$(selection skip_checks EVENT_NAME=workflow_dispatch CHECK_GATE_TLS=false)" "gate-tls"
expect_equal "gate TLS unticked is partial" "$(selection run_kind EVENT_NAME=workflow_dispatch CHECK_GATE_TLS=false)" "partial"
expect_equal "everything unticked" "$(selection skip_checks EVENT_NAME=workflow_dispatch CHECK_GATE_TLS=false \
  CHECK_NODE_FAILURE=false CHECK_MULTIPORT=false CHECK_WARMZERO=false RUN_ADVERSARIAL=false)" \
  "gate-tls,node-failure,multiport,warmzero"
expect_equal "replay unticked" "$(selection run_adversarial EVENT_NAME=workflow_dispatch RUN_ADVERSARIAL=false)" "false"
expect_equal "case filter passed through" "$(selection adversarial_cases EVENT_NAME=workflow_dispatch ADVERSARIAL_CASES='scale-*')" "scale-*"
expect_equal "case filter is partial" "$(selection run_kind EVENT_NAME=workflow_dispatch ADVERSARIAL_CASES='scale-*')" "partial"
expect_equal "ticked boxes stay full" "$(selection run_kind EVENT_NAME=workflow_dispatch CHECK_GATE_TLS=true RUN_ADVERSARIAL=true)" "full"
expect_equal "fewer workloads than the default is partial" \
  "$(selection run_kind EVENT_NAME=workflow_dispatch WORKLOAD_INPUT=whoami)" "partial"
expect_equal "default workloads plus more stay full" \
  "$(selection run_kind EVENT_NAME=workflow_dispatch WORKLOAD_INPUT=airflow,whoami,whoami-multiport,gate-tls,wordpress)" "full"
expect_equal "all workloads stay full" "$(selection run_kind EVENT_NAME=workflow_dispatch WORKLOAD_INPUT=all)" "full"

# Form values that could inject output lines are refused (security audit).
env -i PATH="$PATH" HOME="$HOME" EVENT_NAME=workflow_dispatch ADVERSARIAL_CASES=$'x\nworkload=evil' \
  "$SELECT_SCRIPT" >/dev/null 2>&1
expect_equal "newline in the case filter is refused" "$?" "1"
env -i PATH="$PATH" HOME="$HOME" EVENT_NAME=workflow_dispatch WORKLOAD_INPUT='whoami;id' "$SELECT_SCRIPT" >/dev/null 2>&1
expect_equal "shell characters in the workload are refused" "$?" "1"

# Push run: fast profile, only changed replay cases.
head_sha=$(git -C "$SCRIPT_DIR" rev-parse HEAD)
expect_equal "push uses whoami and wordpress" "$(selection workload EVENT_NAME=push BEFORE_SHA="$head_sha")" "whoami,wordpress"
expect_equal "push skips the slow groups" "$(selection skip_checks EVENT_NAME=push BEFORE_SHA="$head_sha")" \
  "gate-tls,node-failure,multiport,warmzero"
expect_equal "push without case changes skips the replay" "$(selection run_adversarial EVENT_NAME=push BEFORE_SHA="$head_sha")" "false"
expect_equal "push is a partial run" "$(selection run_kind EVENT_NAME=push BEFORE_SHA="$head_sha")" "partial"
expect_equal "new branch replays all cases" "$(selection run_adversarial EVENT_NAME=push BEFORE_SHA=0000000000000000000000000000000000000000)" "true"
expect_equal "new branch has no filter" "$(selection adversarial_cases EVENT_NAME=push BEFORE_SHA=0000000000000000000000000000000000000000)" ""

# Push that changes one case: compare against a copy with that case edited.
edited_cases=$(mktemp)
yq '(.[] | select(.name == "scale-upper-bound") | .description) = "edited by the self-test"' \
  "$SCRIPT_DIR/adversarial/cases.yml" > "$edited_cases"
expect_equal "push replays exactly the changed case" \
  "$(selection adversarial_cases EVENT_NAME=push BEFORE_SHA="$head_sha" CASES_FILE="$edited_cases")" "scale-upper-bound"
rm -f "$edited_cases"

if [[ $FAILURES -gt 0 ]]; then
  echo "select_deploy_checks self-test: $FAILURES failure(s)"
  exit 1
fi
echo "select_deploy_checks self-test: all passed"
