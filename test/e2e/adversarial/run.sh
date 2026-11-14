#!/usr/bin/env bash
# shellcheck disable=SC2086,SC2129
# SC2086: cca args are intentionally word-split; SC2129: style preference.
# run.sh — Replay adversarial test cases against a live CCattler cluster.
#
# Deterministic: no AI, no randomness. Each case in cases.yml runs a cca
# command as a specific identity and asserts exit code + output patterns.
#
# Usage:
#   ./test/e2e/adversarial/run.sh --env deploy   # vmherd VMs on m4800
#   ./test/e2e/adversarial/run.sh --env lan       # vmherd VMs on .77
#
# Requires: yq (go-yq), jq, vmherd in PATH.
# Exit codes: 0 = all passed (known_failure cases are skipped), 1 = any failure.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CASES_FILE="${SCRIPT_DIR}/cases.yml"
TRANSCRIPT="${SCRIPT_DIR}/transcript.md"
ENV="deploy"
CTRL_VM=""
PASS=0
FAIL=0
SKIP=0

usage() {
  echo "Usage: $0 --env deploy|lan"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) ENV="$2"; shift 2 ;;
    *) usage ;;
  esac
done

REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

case "$ENV" in
  deploy) CTRL_VM="cca-deploy-ctrl"; VMHERD_DIR="$REPO_ROOT/test/e2e/vm-deploy" ;;
  lan)    CTRL_VM="cca-lan-ctrl";    VMHERD_DIR="$REPO_ROOT/test/e2e/vm-lan" ;;
  *)      echo "Unknown env: $ENV"; exit 1 ;;
esac

CCA="/usr/local/bin/cca"

# vmherd state is per-directory; cd to the right VM set.
cd "$VMHERD_DIR" || { echo "FATAL: cannot cd to $VMHERD_DIR"; exit 1; }

# Print a message and stop the run; the EXIT trap still cleans up.
fatal() {
  echo "FATAL: $*"
  echo "**FATAL**: $*" >> "$TRANSCRIPT"
  exit 1
}

# Run a cca command as a specific user (using their token via env).
# An empty token would make cca fall back to the admin local-token.
run_cca_as() {
  local identity="$1"
  local token="${TOKENS[$identity]:-}"
  shift
  [[ -n "$token" ]] || fatal "no token for $identity (would run as admin)"
  local exit_code=0
  local output
  output=$(vmherd ssh "$CTRL_VM" -- "sudo CCA_TOKEN=$token $CCA $*" 2>&1) || exit_code=$?
  echo "$output"
  return "$exit_code"
}

# Run an admin setup/teardown command; any failure stops the run, because
# a refusal case means nothing if its identity was not set up as intended.
run_cca_or_fatal() {
  local output
  output=$(run_cca "$@") || fatal "setup command failed: cca $* → $output"
}

# Mint a token for a user and store it; refuse anything that is not a JWT.
mint_token() {
  local name="$1"
  local token
  token=$(vmherd ssh "$CTRL_VM" -- "sudo $CCA user token $name" 2>/dev/null) || fatal "token mint failed for $name"
  [[ "$token" =~ ^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$ ]] || fatal "token for $name is not a JWT"
  TOKENS["$name"]="$token"
}

# Per-run tag substituted for @RUN@ in marker keys and patterns, so a marker
# planted by an earlier run can never satisfy this run's assertions.
RUN_ID="$(date +%s)"

# Read one store key through GET /api/state?key= as a test user. The API
# server audits every read with the key as target, so this plants an audit
# entry in the tenant named by a key segment. 200 and 404 both mean the read
# was authorized and audited; anything else stops the run.
read_state_key_as() {
  local identity="$1" key="$2"
  local token="${TOKENS[$identity]:-}"
  [[ -n "$token" ]] || fatal "no token for $identity"
  local remote_cmd="addr=\$(cat /etc/ccattler/server-address 2>/dev/null || echo 127.0.0.1:9770); curl -s -o /dev/null -w '%{http_code}' --cacert /etc/ccattler/ca.pem -H 'Authorization: Bearer $token' \"https://\$addr/api/state?key=$key\""
  local http_code
  http_code=$(vmherd ssh "$CTRL_VM" -- "$remote_cmd" 2>&1) || fatal "marker read failed for $identity: $http_code"
  [[ "$http_code" == "200" || "$http_code" == "404" ]] || fatal "marker read of $key as $identity returned HTTP $http_code"
  echo "- Marker read \`$key\` as $identity → HTTP $http_code" >> "$TRANSCRIPT"
}

# Run a cca command as admin on the control VM and capture output + exit code.
# vmherd ssh requires the entire remote command as a single string argument.
run_cca() {
  local exit_code=0
  local output
  output=$(vmherd ssh "$CTRL_VM" -- "sudo $CCA $*" 2>&1) || exit_code=$?
  echo "$output"
  return "$exit_code"
}

# Check if a command is available.
require_cmd() {
  if ! command -v "$1" &>/dev/null; then
    echo "FATAL: $1 not found in PATH"
    exit 1
  fi
}

require_cmd yq
require_cmd jq
require_cmd vmherd

echo "# Adversarial Test Replay — $(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$TRANSCRIPT"
echo "" >> "$TRANSCRIPT"
echo "Environment: $ENV ($CTRL_VM)" >> "$TRANSCRIPT"
echo "" >> "$TRANSCRIPT"

TOTAL_CASES=$(yq '. | length' "$CASES_FILE")
echo "Running $TOTAL_CASES adversarial cases against $CTRL_VM"

# --- Setup: create the test identities ---

declare -A TOKENS

setup_identities() {
  echo "## Setup" >> "$TRANSCRIPT"
  echo "" >> "$TRANSCRIPT"

  # Admin uses the local-token on the control VM.
  # adv-reader: api-reader, team frontend
  # adv-writer: api-writer, team payments
  # adv-nongrant: authenticated but no grants

  local identities=(
    "adv-reader:frontend:api-reader"
    "adv-writer:payments:api-writer"
    "adv-nongrant:none:"
  )

  # Users left by an interrupted run would otherwise make create fail.
  cleanup_identities >/dev/null

  for entry in "${identities[@]}"; do
    IFS=':' read -r name team role <<< "$entry"
    local team_args=()
    if [[ "$team" != "none" ]]; then
      team_args=(--team "$team")
    fi
    run_cca_or_fatal user create "$name" "${team_args[@]}"
    if [[ -n "$role" ]]; then
      run_cca_or_fatal role grant "user:$name" --role "$role"
    fi
    mint_token "$name"
    echo "- Created user \`$name\` (team=$team, role=$role)" >> "$TRANSCRIPT"
  done
  echo "" >> "$TRANSCRIPT"
  verify_identities
}

# Positive controls: granted identities can read, the ungranted one is
# refused. Proves every token is used and no identity runs as admin.
verify_identities() {
  local identity output
  for identity in adv-reader adv-writer; do
    output=$(run_cca_as "$identity" status) || fatal "$identity cannot run status (grant missing?): $output"
  done
  if output=$(run_cca_as adv-nongrant status); then
    fatal "adv-nongrant ran status without a grant (token ignored, running as admin?)"
  fi
  echo "$output" | grep -qi "forbidden" || fatal "adv-nongrant refused for the wrong reason: $output"
  echo "- Positive controls passed (reader/writer read, nongrant refused)" >> "$TRANSCRIPT"
  echo "" >> "$TRANSCRIPT"
}

cleanup_identities() {
  echo "" >> "$TRANSCRIPT"
  echo "## Cleanup" >> "$TRANSCRIPT"
  echo "" >> "$TRANSCRIPT"
  for name in adv-reader adv-writer adv-nongrant adv-ephemeral adv-reuse; do
    run_cca user delete "$name" >/dev/null 2>&1 || true
    echo "- Deleted user \`$name\`" >> "$TRANSCRIPT"
  done
}

# --- Run cases ---

run_case() {
  local index="$1"
  local case_name case_desc identity known_failure
  case_name=$(yq ".[$index].name" "$CASES_FILE")
  case_desc=$(yq ".[$index].description" "$CASES_FILE")
  identity=$(yq ".[$index].identity" "$CASES_FILE")
  known_failure=$(yq ".[$index].known_failure // \"\"" "$CASES_FILE")

  echo "### Case: $case_name" >> "$TRANSCRIPT"
  echo "" >> "$TRANSCRIPT"
  echo "$case_desc" >> "$TRANSCRIPT"
  echo "" >> "$TRANSCRIPT"

  if [[ -n "$known_failure" && "$known_failure" != "null" ]]; then
    echo "  SKIP  $case_name ($known_failure)"
    echo "**SKIPPED**: $known_failure" >> "$TRANSCRIPT"
    echo "" >> "$TRANSCRIPT"
    ((SKIP++))
    return 0
  fi

  # Run setup commands (as admin).
  local setup_count
  setup_count=$(yq ".[$index].setup // [] | length" "$CASES_FILE")
  for ((s=0; s<setup_count; s++)); do
    local setup_cmd
    setup_cmd=$(yq ".[$index].setup[$s]" "$CASES_FILE")
    run_cca_or_fatal $setup_cmd
  done

  # Capture token if needed.
  local capture_token
  capture_token=$(yq ".[$index].capture_token // \"\"" "$CASES_FILE")
  if [[ -n "$capture_token" && "$capture_token" != "null" ]]; then
    mint_token "$capture_token"
  fi

  # Plant audit markers: reads of store keys as a given test user.
  local marker_count
  marker_count=$(yq ".[$index].marker_reads // [] | length" "$CASES_FILE")
  for ((mr=0; mr<marker_count; mr++)); do
    local marker_as marker_key
    marker_as=$(yq ".[$index].marker_reads[$mr].as" "$CASES_FILE")
    marker_key=$(yq ".[$index].marker_reads[$mr].key" "$CASES_FILE")
    read_state_key_as "$marker_as" "${marker_key//@RUN@/$RUN_ID}"
  done

  # Run steps.
  local step_count
  step_count=$(yq ".[$index].steps // [] | length" "$CASES_FILE")
  for ((st=0; st<step_count; st++)); do
    local step_cmd step_as step_identity
    step_cmd=$(yq ".[$index].steps[$st].command" "$CASES_FILE")
    step_as=$(yq ".[$index].steps[$st].as // \"\"" "$CASES_FILE")
    step_identity="${step_as:-$identity}"
    if [[ "$step_identity" == "null" || -z "$step_identity" ]]; then
      step_identity="$identity"
    fi

    local actual_exit=0
    local output=""
    if [[ "$step_identity" == "adv-admin" ]]; then
      output=$(run_cca $step_cmd 2>&1) || actual_exit=$?
    else
      output=$(run_cca_as "$step_identity" $step_cmd 2>&1) || actual_exit=$?
    fi

    echo "\`cca $step_cmd\` (as $step_identity) → exit $actual_exit" >> "$TRANSCRIPT"
    echo '```' >> "$TRANSCRIPT"
    echo "$output" | head -20 >> "$TRANSCRIPT"
    echo '```' >> "$TRANSCRIPT"
    echo "" >> "$TRANSCRIPT"

    assert_step "$index" "$st" "$actual_exit" "$output" "steps"
  done

  # Run teardown_before_assert (as admin).
  local teardown_count
  teardown_count=$(yq ".[$index].teardown_before_assert // [] | length" "$CASES_FILE")
  for ((td=0; td<teardown_count; td++)); do
    local td_cmd
    td_cmd=$(yq ".[$index].teardown_before_assert[$td]" "$CASES_FILE")
    run_cca_or_fatal $td_cmd
  done

  # Run assert_after_teardown.
  local aat_count
  aat_count=$(yq ".[$index].assert_after_teardown // [] | length" "$CASES_FILE")
  for ((a=0; a<aat_count; a++)); do
    local aat_cmd aat_as aat_identity
    aat_cmd=$(yq ".[$index].assert_after_teardown[$a].command" "$CASES_FILE")
    aat_as=$(yq ".[$index].assert_after_teardown[$a].as // \"\"" "$CASES_FILE")
    aat_identity="${aat_as:-$identity}"
    if [[ "$aat_identity" == "null" || -z "$aat_identity" ]]; then
      aat_identity="$identity"
    fi

    local actual_exit=0
    local output=""
    if [[ "$aat_identity" == "adv-admin" ]]; then
      output=$(run_cca $aat_cmd 2>&1) || actual_exit=$?
    else
      output=$(run_cca_as "$aat_identity" $aat_cmd 2>&1) || actual_exit=$?
    fi

    echo "\`cca $aat_cmd\` (as $aat_identity, after teardown) → exit $actual_exit" >> "$TRANSCRIPT"
    echo '```' >> "$TRANSCRIPT"
    echo "$output" | head -20 >> "$TRANSCRIPT"
    echo '```' >> "$TRANSCRIPT"
    echo "" >> "$TRANSCRIPT"

    assert_step "$index" "$a" "$actual_exit" "$output" "assert_after_teardown"
  done

  # Run assert.
  local assert_count
  assert_count=$(yq ".[$index].assert // [] | length" "$CASES_FILE")
  for ((a=0; a<assert_count; a++)); do
    local a_cmd a_as a_identity
    a_cmd=$(yq ".[$index].assert[$a].command" "$CASES_FILE")
    a_as=$(yq ".[$index].assert[$a].as // \"\"" "$CASES_FILE")
    a_identity="${a_as:-$identity}"
    if [[ "$a_identity" == "null" || -z "$a_identity" ]]; then
      a_identity="$identity"
    fi

    local actual_exit=0
    local output=""
    if [[ "$a_identity" == "adv-admin" ]]; then
      output=$(run_cca $a_cmd 2>&1) || actual_exit=$?
    else
      output=$(run_cca_as "$a_identity" $a_cmd 2>&1) || actual_exit=$?
    fi

    echo "\`cca $a_cmd\` (as $a_identity, assert) → exit $actual_exit" >> "$TRANSCRIPT"
    echo '```' >> "$TRANSCRIPT"
    echo "$output" | head -20 >> "$TRANSCRIPT"
    echo '```' >> "$TRANSCRIPT"
    echo "" >> "$TRANSCRIPT"

    assert_step "$index" "$a" "$actual_exit" "$output" "assert"
  done

  # Run cleanup (as admin).
  local cleanup_count
  cleanup_count=$(yq ".[$index].cleanup // [] | length" "$CASES_FILE")
  for ((c=0; c<cleanup_count; c++)); do
    local c_cmd
    c_cmd=$(yq ".[$index].cleanup[$c]" "$CASES_FILE")
    run_cca $c_cmd >/dev/null 2>&1 || true
  done
}

assert_step() {
  local case_index="$1" step_index="$2" actual_exit="$3" output="$4" section="$5"
  local case_name
  case_name=$(yq ".[$case_index].name" "$CASES_FILE")

  local expected_exit
  expected_exit=$(yq ".[$case_index].${section}[$step_index].expect_exit // 0" "$CASES_FILE")

  if [[ "$actual_exit" -ne "$expected_exit" ]]; then
    echo "  FAIL  $case_name: expected exit $expected_exit, got $actual_exit"
    echo "**FAIL**: expected exit $expected_exit, got $actual_exit" >> "$TRANSCRIPT"
    ((FAIL++))
    return 0
  fi

  # Check must_contain patterns.
  local mc_count
  mc_count=$(yq ".[$case_index].${section}[$step_index].must_contain // [] | length" "$CASES_FILE")
  for ((m=0; m<mc_count; m++)); do
    local pattern
    pattern=$(yq ".[$case_index].${section}[$step_index].must_contain[$m]" "$CASES_FILE")
    pattern="${pattern//@RUN@/$RUN_ID}"
    if ! echo "$output" | grep -qi "$pattern"; then
      echo "  FAIL  $case_name: output missing '$pattern'"
      echo "**FAIL**: output missing \`$pattern\`" >> "$TRANSCRIPT"
      ((FAIL++))
      return 0
    fi
  done

  # Check must_not_contain patterns.
  local mnc_count
  mnc_count=$(yq ".[$case_index].${section}[$step_index].must_not_contain // [] | length" "$CASES_FILE")
  for ((m=0; m<mnc_count; m++)); do
    local pattern
    pattern=$(yq ".[$case_index].${section}[$step_index].must_not_contain[$m]" "$CASES_FILE")
    pattern="${pattern//@RUN@/$RUN_ID}"
    if echo "$output" | grep -qi "$pattern"; then
      echo "  FAIL  $case_name: output contains '$pattern' (must not)"
      echo "**FAIL**: output contains \`$pattern\` (must not)" >> "$TRANSCRIPT"
      ((FAIL++))
      return 0
    fi
  done

  echo "  PASS  $case_name"
  echo "**PASS**" >> "$TRANSCRIPT"
  echo "" >> "$TRANSCRIPT"
  ((PASS++))
}

# --- Main ---

trap cleanup_identities EXIT
setup_identities

for ((i=0; i<TOTAL_CASES; i++)); do
  run_case "$i"
done

echo ""
echo "========================================="
echo "  Results: $PASS passed, $FAIL failed, $SKIP skipped"
echo "  Transcript: $TRANSCRIPT"
echo "========================================="
echo "" >> "$TRANSCRIPT"
echo "## Results" >> "$TRANSCRIPT"
echo "" >> "$TRANSCRIPT"
echo "$PASS passed, $FAIL failed, $SKIP skipped" >> "$TRANSCRIPT"

if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
