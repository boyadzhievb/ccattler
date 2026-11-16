#!/usr/bin/env bash
# Decides what a Deploy Testbed run executes (M231, design D2, D4, D5, D7).
# Reads the trigger and the form inputs from the environment and prints
# key=value lines for $GITHUB_OUTPUT:
#   workload, skip_checks, run_adversarial, adversarial_cases, run_kind
#
# Environment (all optional):
#   EVENT_NAME            workflow_dispatch (default) or push
#   WORKLOAD_INPUT        the form's workload choice (manual runs)
#   CHECK_GATE_TLS, CHECK_NODE_FAILURE, CHECK_MULTIPORT, CHECK_WARMZERO,
#   RUN_ADVERSARIAL       "true"/"false" checkboxes (manual runs; empty = true)
#   ADVERSARIAL_CASES     replay filter (manual runs; empty = all)
#   BEFORE_SHA            commit before the push (push runs)
#   CASES_FILE            replay cases (default test/e2e/adversarial/cases.yml)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CASES_FILE="${CASES_FILE:-$REPO_ROOT/test/e2e/adversarial/cases.yml}"

# DEFAULT_WORKLOADS is the manual run's workload set when the form gives none.
DEFAULT_WORKLOADS="airflow,whoami,whoami-multiport,gate-tls"
# PUSH_WORKLOADS is the push profile's workload set.
PUSH_WORKLOADS="whoami,wordpress"
# PUSH_SKIPPED_GROUPS are the slow groups a push run never runs.
PUSH_SKIPPED_GROUPS="gate-tls,node-failure,multiport,warmzero"
# ZERO_SHA is what GitHub sends as "before" for a newly created branch.
ZERO_SHA="0000000000000000000000000000000000000000"

# checkbox_unticked reports whether a checkbox value means "do not run";
# empty (no form, e.g. an old dispatch) counts as ticked.
checkbox_unticked() {
  [[ "${1:-true}" == "false" ]]
}

# workloads_include_defaults reports whether a workload list ("all" or
# comma-separated) contains every default workload, which a full run needs.
workloads_include_defaults() {
  local workload_list="$1" default_workload
  [[ "$workload_list" == "all" ]] && return 0
  IFS=',' read -r -a default_workloads <<< "$DEFAULT_WORKLOADS"
  for default_workload in "${default_workloads[@]}"; do
    [[ ",$workload_list," == *",$default_workload,"* ]] || return 1
  done
}

# changed_replay_cases prints the comma-separated names of cases that are new
# or changed in CASES_FILE since BEFORE_SHA; "*" when that commit is unknown.
changed_replay_cases() {
  local before_sha="$1" previous_cases case_name changed_names=()
  if [[ -z "$before_sha" || "$before_sha" == "$ZERO_SHA" ]] ||
     ! previous_cases=$(git -C "$REPO_ROOT" show "${before_sha}:test/e2e/adversarial/cases.yml" 2>/dev/null); then
    echo "*"
    return
  fi
  while IFS= read -r case_name; do
    [[ -z "$case_name" ]] && continue
    if [[ "$(yq -o=json ".[] | select(.name == \"$case_name\")" "$CASES_FILE")" != \
          "$(echo "$previous_cases" | yq -o=json ".[] | select(.name == \"$case_name\")")" ]]; then
      changed_names+=("$case_name")
    fi
  done < <(yq '.[].name' "$CASES_FILE")
  (IFS=','; echo "${changed_names[*]}")
}

workload="" skip_groups=() run_adversarial=true adversarial_cases="" run_kind=full

if [[ "${EVENT_NAME:-workflow_dispatch}" == "push" ]]; then
  workload="$PUSH_WORKLOADS"
  IFS=',' read -r -a skip_groups <<< "$PUSH_SKIPPED_GROUPS"
  adversarial_cases=$(changed_replay_cases "${BEFORE_SHA:-}")
  if [[ "$adversarial_cases" == "*" ]]; then
    adversarial_cases=""
  elif [[ -z "$adversarial_cases" ]]; then
    run_adversarial=false
  fi
  run_kind=partial
else
  workload="${WORKLOAD_INPUT:-$DEFAULT_WORKLOADS}"
  checkbox_unticked "${CHECK_GATE_TLS:-}" && skip_groups+=(gate-tls)
  checkbox_unticked "${CHECK_NODE_FAILURE:-}" && skip_groups+=(node-failure)
  checkbox_unticked "${CHECK_MULTIPORT:-}" && skip_groups+=(multiport)
  checkbox_unticked "${CHECK_WARMZERO:-}" && skip_groups+=(warmzero)
  checkbox_unticked "${RUN_ADVERSARIAL:-}" && run_adversarial=false
  adversarial_cases="${ADVERSARIAL_CASES:-}"
  if [[ ${#skip_groups[@]} -gt 0 || "$run_adversarial" == "false" || -n "$adversarial_cases" ]] ||
     ! workloads_include_defaults "$workload"; then
    run_kind=partial
  fi
fi

echo "workload=$workload"
echo "skip_checks=$(IFS=','; echo "${skip_groups[*]}")"
echo "run_adversarial=$run_adversarial"
echo "adversarial_cases=$adversarial_cases"
echo "run_kind=$run_kind"
