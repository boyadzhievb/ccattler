#!/usr/bin/env bash
# Check groups that cluster_test.sh can skip (M231, design D1). Sourced by
# cluster_test.sh and by check_groups_selftest.sh; defines functions only.
#
#   --skip-checks gate-tls,node-failure     skip those groups (default: none)
#
# The quick checks (Phase 69 inspection, API authorization, VIP and DNS, HTTP,
# cross-node path, backend churn) and the final status/stability check always run.

# SKIPPABLE_CHECK_GROUPS lists the slow groups, in the order the script runs them.
SKIPPABLE_CHECK_GROUPS=(node-failure multiport warmzero gate-tls)

# SKIPPED_CHECK_GROUPS holds the groups to skip, filled by set_skipped_check_groups.
SKIPPED_CHECK_GROUPS=()

# set_skipped_check_groups parses a comma-separated list; an unknown group name
# prints an error naming the valid groups and returns 1.
set_skipped_check_groups() {
  local group_list="$1"
  local requested_group known_group is_known
  SKIPPED_CHECK_GROUPS=()
  [[ -z "$group_list" ]] && return 0
  IFS=',' read -r -a requested_groups <<< "$group_list"
  for requested_group in "${requested_groups[@]}"; do
    [[ -z "$requested_group" ]] && continue
    is_known=false
    for known_group in "${SKIPPABLE_CHECK_GROUPS[@]}"; do
      [[ "$requested_group" == "$known_group" ]] && is_known=true
    done
    if [[ "$is_known" != "true" ]]; then
      echo "error: --skip-checks: unknown check group '$requested_group' (valid: ${SKIPPABLE_CHECK_GROUPS[*]})" >&2
      return 1
    fi
    SKIPPED_CHECK_GROUPS+=("$requested_group")
  done
}

# check_skipped returns 0 when the named group is to be skipped.
check_skipped() {
  local wanted_group="$1" skipped_group
  for skipped_group in "${SKIPPED_CHECK_GROUPS[@]}"; do
    [[ "$skipped_group" == "$wanted_group" ]] && return 0
  done
  return 1
}

# skipped_check_groups_report prints the line every run logs, e.g.
# "skipped check groups: gate-tls,node-failure" or "skipped check groups: none".
skipped_check_groups_report() {
  if [[ ${#SKIPPED_CHECK_GROUPS[@]} -eq 0 ]]; then
    echo "skipped check groups: none"
    return
  fi
  local joined_groups
  joined_groups=$(IFS=','; echo "${SKIPPED_CHECK_GROUPS[*]}")
  echo "skipped check groups: ${joined_groups}"
}
