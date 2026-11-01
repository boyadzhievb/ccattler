#!/usr/bin/env bash
# warmzero_test.sh — End-to-end test for scale-to-zero lifecycle
#
# Tests the full warm-zero chain:
#   1. Deploy nginx with idle_timeout=30s, activation_timeout=15s
#   2. Verify initial scale-up (min=0, but first apply triggers activation)
#   3. Send HTTP request, verify nginx response
#   4. Wait for idle_timeout, verify scale to zero (0 running instances)
#   5. Send HTTP request during zero-state, verify proxy buffers and activates
#   6. Measure cold-start latency
#   7. (Extended) Concurrent requests during zero-state
#
# Usage:
#   ./test/e2e/warmzero_test.sh                    # run on default cluster
#   ./test/e2e/warmzero_test.sh --ctrl 192.168.124.10
#   ./test/e2e/warmzero_test.sh --extended         # include concurrency test
#
# Prerequisites: workload deployed via cluster_test.sh --workload warmzero

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

CTRL_NODE="192.168.124.10"
SSH_OPTS="-o ConnectTimeout=5 -o StrictHostKeyChecking=no"
EXTENDED=false
IDLE_TIMEOUT=30
ACTIVATION_TIMEOUT=15
SERVICE_NAME="nginx-warmzero"
GATE_HOST="warmzero.home.arpa"

for arg in "$@"; do
    case "$arg" in
        --ctrl) shift; CTRL_NODE="$1"; shift ;;
        --ctrl=*) CTRL_NODE="${arg#*=}" ;;
        --extended) EXTENDED=true; shift ;;
        *) ;;
    esac
done

log() { echo "[$(date '+%H:%M:%S')] $*"; }
fail() { log "FAIL: $*"; exit 1; }
pass() { log "PASS: $*"; }

ssh_ctrl() {
    ssh $SSH_OPTS "$CTRL_NODE" "$@"
}

get_running_count() {
    ssh_ctrl "sudo /usr/local/bin/cca get instances --no-headers 2>/dev/null" \
        | awk -v svc="$SERVICE_NAME" '$1 == svc && $3 == "running" { count++ } END { print count+0 }'
}

get_service_vip() {
    ssh_ctrl "sudo /usr/local/bin/cca get networking --no-headers 2>/dev/null" \
        | awk -v svc="$SERVICE_NAME" '$1 == svc && $2 == "vip" { print $3 }'
}

curl_service() {
    local vip="$1"
    ssh_ctrl "curl -s -o /dev/null -w '%{http_code}' \
        --max-time $ACTIVATION_TIMEOUT \
        -H 'Host: $GATE_HOST' \
        http://${vip}:80/" 2>/dev/null
}

curl_service_timed() {
    local vip="$1"
    ssh_ctrl "curl -s -o /dev/null -w '%{time_total}' \
        --max-time $((ACTIVATION_TIMEOUT + 5)) \
        -H 'Host: $GATE_HOST' \
        http://${vip}:80/" 2>/dev/null
}

# ---- Step 1: Verify service is deployed and has a VIP ----

log "=== Warm-Zero E2E Test ==="

vip=$(get_service_vip)
[[ -n "$vip" ]] || fail "No VIP found for $SERVICE_NAME"
log "Service VIP: $vip"

# ---- Step 2: Verify warm state (should have 1+ running instance) ----

running=$(get_running_count)
log "Current running instances: $running"
if [[ "$running" -eq 0 ]]; then
    log "Service at zero, sending activation request..."
    status=$(curl_service "$vip")
    [[ "$status" == "200" ]] || fail "Activation request returned HTTP $status, expected 200"
    sleep 5
    running=$(get_running_count)
fi
[[ "$running" -ge 1 ]] || fail "Expected at least 1 running instance, got $running"
pass "Service is warm with $running instance(s)"

# ---- Step 3: Verify HTTP response while warm ----

status=$(curl_service "$vip")
[[ "$status" == "200" ]] || fail "Warm request returned HTTP $status, expected 200"
pass "Warm HTTP request returned 200"

# ---- Step 4: Wait for idle_timeout + buffer, verify scale to zero ----

wait_seconds=$((IDLE_TIMEOUT + 15))
log "Waiting ${wait_seconds}s for idle_timeout (${IDLE_TIMEOUT}s) + reconciliation..."
sleep "$wait_seconds"

running=$(get_running_count)
deadline=$((SECONDS + 30))
while [[ "$running" -gt 0 && "$SECONDS" -lt "$deadline" ]]; do
    sleep 5
    running=$(get_running_count)
    log "  Waiting for scale-down... running=$running"
done

[[ "$running" -eq 0 ]] || fail "Expected 0 running instances after idle, got $running"
pass "Service scaled to zero after idle_timeout"

# ---- Step 5: Cold-start activation — send request at zero state ----

log "Sending cold-start request (service at zero)..."
cold_start_time=$(curl_service_timed "$vip")
cold_start_status=$(curl_service "$vip")

[[ "$cold_start_status" == "200" ]] || fail "Cold-start request returned HTTP $cold_start_status, expected 200"
pass "Cold-start request returned 200 in ${cold_start_time}s"

# ---- Step 6: Verify instance came back ----

sleep 3
running=$(get_running_count)
[[ "$running" -ge 1 ]] || fail "Expected at least 1 instance after cold-start, got $running"
pass "Service activated: $running instance(s) running"

# ---- Step 7: (Extended) Concurrent requests during zero-state ----

if [[ "$EXTENDED" == "true" ]]; then
    log "=== Extended: Concurrent cold-start test ==="

    wait_seconds=$((IDLE_TIMEOUT + 15))
    log "Waiting ${wait_seconds}s for scale-down..."
    sleep "$wait_seconds"

    running=$(get_running_count)
    deadline=$((SECONDS + 30))
    while [[ "$running" -gt 0 && "$SECONDS" -lt "$deadline" ]]; do
        sleep 5
        running=$(get_running_count)
    done
    [[ "$running" -eq 0 ]] || fail "Expected 0 instances for concurrency test, got $running"

    log "Sending 20 concurrent requests..."
    concurrent_results=$(ssh_ctrl "
        for i in \$(seq 1 20); do
            curl -s -o /dev/null -w '%{http_code}\n' \
                --max-time $((ACTIVATION_TIMEOUT + 5)) \
                -H 'Host: $GATE_HOST' \
                http://${vip}:80/ &
        done
        wait
    " 2>/dev/null)

    success_count=$(echo "$concurrent_results" | grep -c "200" || true)
    fail_count=$(echo "$concurrent_results" | grep -cv "200" || true)
    log "Concurrent results: $success_count/20 succeeded, $fail_count failed"
    [[ "$success_count" -ge 18 ]] || fail "Expected at least 18/20 concurrent requests to succeed, got $success_count"
    pass "Concurrent cold-start: $success_count/20 requests succeeded"
fi

# ---- Summary ----

log "=== All warm-zero tests passed ==="
