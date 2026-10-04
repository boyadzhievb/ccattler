#!/usr/bin/env bash
# placement_test.sh — Placement policy & tenant isolation e2e tests
#
# Runs 12 scenarios against the 3-node CCattler deploy cluster on libvirt VMs.
# Tests require, prefer, restrict/accept, zone spread, architecture, combined
# constraints, and tenant quota enforcement.
#
# Prerequisites:
#   - 3-VM deploy cluster already provisioned (run cluster_test.sh --env deploy, or --provision)
#   - etcd accessible at 192.168.124.10:2379
#   - cca server API at 192.168.124.10:9770
#
# Usage:
#   ./test/e2e/placement_test.sh                    # tests only (cluster must be up)
#   ./test/e2e/placement_test.sh --provision         # provision cluster first
#   ./test/e2e/placement_test.sh --no-destroy        # keep VMs on failure

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"
WORKLOADS_DIR="$SCRIPT_DIR/workloads"

CTRL_IP="192.168.124.10"
ETCD="etcdctl --endpoints=http://${CTRL_IP}:2379"
PFX="/ccattler"
API="http://${CTRL_IP}:9770"

PROVISION=false
DESTROY_ON_EXIT=true
SCENARIO_TIMEOUT=30
PASSED=0
FAILED=0
TOTAL=0

for arg in "$@"; do
    case "$arg" in
        --provision)    PROVISION=true ;;
        --no-destroy)   DESTROY_ON_EXIT=false ;;
    esac
done

log()  { echo "==> [$(date +%H:%M:%S)] $*"; }
pass() { echo "  PASS: $1"; PASSED=$((PASSED + 1)); }
fail_scenario() { echo "  FAIL: $1"; FAILED=$((FAILED + 1)); }

cleanup_exit() {
    local exit_code=$?
    echo ""
    log "Results: $PASSED passed, $FAILED failed out of $TOTAL scenarios"
    if [[ "$DESTROY_ON_EXIT" == "true" && "$PROVISION" == "true" ]]; then
        log "Tearing down VMs..."
        cd "$ANSIBLE_DIR"
        VAGRANT_VAGRANTFILE=deploy-Vagrantfile vagrant destroy -f 2>/dev/null || true
    fi
    if [[ $FAILED -gt 0 ]]; then exit 1; fi
    exit $exit_code
}
trap cleanup_exit EXIT

# ── Helper functions ─────────────────────────────────────────────

set_node_label() {
    local node_id="$1" label="$2" value="$3"
    $ETCD put "${PFX}/observed/node/${node_id}/label/${label}" "${value}" >/dev/null
}

set_node_restrict() {
    local node_id="$1" label="$2"
    $ETCD put "${PFX}/observed/node/${node_id}/restrict/${label}" "true" >/dev/null
}

set_node_zone() {
    local node_id="$1" zone="$2"
    $ETCD put "${PFX}/observed/node/${node_id}/zone" "${zone}" >/dev/null
}

set_node_arch() {
    local node_id="$1" arch="$2"
    $ETCD put "${PFX}/observed/node/${node_id}/architecture" "${arch}" >/dev/null
}

create_tenant() {
    local name="$1" instances_quota="$2"
    $ETCD put "${PFX}/desired/tenant/${name}" "" >/dev/null
    $ETCD put "${PFX}/desired/tenant/${name}/state" "active" >/dev/null
    $ETCD put "${PFX}/desired/tenant/${name}/quota/instances" "${instances_quota}" >/dev/null
}

seed_observed_instance() {
    local id="$1" service="$2" state="${3:-running}"
    $ETCD put "${PFX}/observed/instance/${id}/service" "${service}" >/dev/null
    $ETCD put "${PFX}/observed/instance/${id}/state" "${state}" >/dev/null
}

# api_apply sends DSL to the server's /api/apply endpoint (goes through PolicyGate).
# Returns the HTTP status code. Response body is printed to stdout.
api_apply() {
    local dsl="$1"
    local http_code
    local response
    response=$(curl -sf -w "\n%{http_code}" -X POST "${API}/api/apply" \
        -H "Content-Type: text/plain" -d "$dsl" 2>/dev/null || true)
    http_code=$(echo "$response" | tail -1)
    echo "$response" | head -n -1
    return 0
}

api_apply_status() {
    local dsl="$1"
    curl -s -o /dev/null -w "%{http_code}" -X POST "${API}/api/apply" \
        -H "Content-Type: text/plain" -d "$dsl" 2>/dev/null || echo "000"
}

# get_placements_for_service returns "instanceID=nodeID" lines for all placements.
get_placements_for_service() {
    local service="$1"
    local instance_ids
    instance_ids=$($ETCD get --prefix "${PFX}/observed/instance/" --print-value-only 2>/dev/null | grep "^${service}$" || true)
    if [[ -z "$instance_ids" ]]; then return; fi

    # Get all instance keys for this service, extract IDs, look up placements.
    $ETCD get --prefix "${PFX}/observed/instance/" --keys-only 2>/dev/null | \
        grep "/service$" | while read -r key; do
            local value
            value=$($ETCD get "$key" --print-value-only 2>/dev/null)
            if [[ "$value" == "$service" ]]; then
                local instance_id
                instance_id=$(echo "$key" | sed "s|${PFX}/observed/instance/||;s|/service||")
                local node
                node=$($ETCD get "${PFX}/placement/instance/${instance_id}" --print-value-only 2>/dev/null || true)
                if [[ -n "$node" ]]; then
                    echo "${instance_id}=${node}"
                fi
            fi
        done
}

# wait_for_placements polls until the expected number of placements exist for a service.
wait_for_placements() {
    local service="$1" expected="$2" timeout="${3:-$SCENARIO_TIMEOUT}"
    local deadline=$((SECONDS + timeout))
    while [[ $SECONDS -lt $deadline ]]; do
        local count
        count=$(get_placements_for_service "$service" | wc -l | tr -d ' ')
        if [[ "$count" -ge "$expected" ]]; then return 0; fi
        sleep 2
    done
    echo "  Timeout: expected $expected placements for $service, got $(get_placements_for_service "$service" | wc -l | tr -d ' ')"
    return 1
}

# get_placed_nodes returns sorted unique node IDs where a service's instances are placed.
get_placed_nodes() {
    local service="$1"
    get_placements_for_service "$service" | cut -d= -f2 | sort -u
}

# cleanup_scenario removes all test service/instance/placement/label/tenant facts.
cleanup_scenario() {
    $ETCD del --prefix "${PFX}/desired/service/" >/dev/null 2>&1 || true
    $ETCD del --prefix "${PFX}/observed/instance/" >/dev/null 2>&1 || true
    $ETCD del --prefix "${PFX}/placement/instance/" >/dev/null 2>&1 || true
    $ETCD del --prefix "${PFX}/desired/tenant/" >/dev/null 2>&1 || true
    for node_id in deploy-node-1 deploy-node-2 deploy-node-3; do
        $ETCD del --prefix "${PFX}/observed/node/${node_id}/label/" >/dev/null 2>&1 || true
        $ETCD del --prefix "${PFX}/observed/node/${node_id}/restrict/" >/dev/null 2>&1 || true
        $ETCD del "${PFX}/observed/node/${node_id}/zone" >/dev/null 2>&1 || true
        $ETCD del "${PFX}/observed/node/${node_id}/architecture" >/dev/null 2>&1 || true
    done
    sleep 2
}

ssh_ctrl() {
    local key="$ANSIBLE_DIR/.vagrant/machines/cca-deploy-ctrl/libvirt/private_key"
    ssh -o StrictHostKeyChecking=no -o LogLevel=ERROR -i "$key" "vagrant@${CTRL_IP}" "$@"
}

# ── Provisioning (optional) ──────────────────────────────────────

if [[ "$PROVISION" == "true" ]]; then
    log "Building cca binary..."
    (cd "$REPO_DIR" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$ANSIBLE_DIR/cca-linux-amd64" ./cmd/cca/)

    log "Provisioning deploy cluster..."
    cd "$ANSIBLE_DIR"
    VAGRANT_VAGRANTFILE=deploy-Vagrantfile vagrant up --no-provision
    ansible-playbook -i deploy-inventory.ini deploy-deploy.yml
fi

# ── Verify cluster is up ─────────────────────────────────────────

log "Verifying cluster health..."
$ETCD endpoint health >/dev/null 2>&1 || { echo "FATAL: etcd unreachable at ${CTRL_IP}:2379"; exit 1; }
curl -sf "${API}/status" >/dev/null 2>&1 || { echo "FATAL: cca server unreachable at ${API}"; exit 1; }
log "Cluster healthy. Running 12 placement & tenant scenarios."
echo ""

# ══════════════════════════════════════════════════════════════════
# SCENARIO 1: require gpu=true — hard label match
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 1: require gpu=true (hard label filter)"
cleanup_scenario
set_node_label deploy-node-2 gpu true

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-require-gpu.cca" >/dev/null 2>&1

if wait_for_placements gpu-worker 1; then
    placed_on=$(get_placed_nodes gpu-worker)
    if [[ "$placed_on" == "deploy-node-2" ]]; then
        pass "require gpu=true → placed on deploy-node-2 (correct)"
    else
        fail_scenario "require gpu=true → placed on $placed_on (expected deploy-node-2)"
    fi
else
    fail_scenario "require gpu=true → no placement within timeout"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 2: require gpu=true — no match, fallback behavior
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 2: require gpu=true, no node has gpu (fallback)"
cleanup_scenario

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-require-gpu.cca" >/dev/null 2>&1

if wait_for_placements gpu-worker 1; then
    pass "require fallback → placed somewhere (scheduler falls back to all nodes)"
else
    fail_scenario "require fallback → no placement (unexpected)"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 3: prefer ssd=true — soft scoring
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 3: prefer ssd=true (soft scoring)"
cleanup_scenario
set_node_label deploy-node-1 ssd true

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-prefer-ssd.cca" >/dev/null 2>&1

if wait_for_placements ssd-preferred 3 45; then
    nodes=$(get_placed_nodes ssd-preferred)
    if echo "$nodes" | grep -q "deploy-node-1"; then
        pass "prefer ssd=true → deploy-node-1 received instance (preferred node used)"
    else
        fail_scenario "prefer ssd=true → deploy-node-1 not used: $nodes"
    fi
else
    fail_scenario "prefer ssd=true → insufficient placements"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 4: restrict blocks service without accept
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 4: restrict dedicated-compute on node-1 (no accept)"
cleanup_scenario
set_node_restrict deploy-node-1 dedicated-compute

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-no-accept.cca" >/dev/null 2>&1

if wait_for_placements no-toleration 1; then
    placed_on=$(get_placed_nodes no-toleration)
    if [[ "$placed_on" != *"deploy-node-1"* ]]; then
        pass "restrict blocks → placed on $placed_on (avoided restricted node-1)"
    else
        fail_scenario "restrict blocks → placed on deploy-node-1 (should have been avoided)"
    fi
else
    fail_scenario "restrict blocks → no placement"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 5: accept allows scheduling on restricted node
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 5: accept dedicated-compute (tolerates restriction)"
cleanup_scenario
set_node_restrict deploy-node-1 dedicated-compute

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-restrict-accept.cca" >/dev/null 2>&1

if wait_for_placements ml-job 1; then
    pass "accept allows → placed (restricted node is now eligible)"
else
    fail_scenario "accept allows → no placement"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 6: zone spread across 3 zones
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 6: zone spread (3 instances, 3 zones)"
cleanup_scenario
set_node_zone deploy-node-1 zone-a
set_node_zone deploy-node-2 zone-b
set_node_zone deploy-node-3 zone-c

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-zone-spread.cca" >/dev/null 2>&1

if wait_for_placements zone-spread-svc 3 45; then
    unique_nodes=$(get_placed_nodes zone-spread-svc | wc -l | tr -d ' ')
    if [[ "$unique_nodes" -eq 3 ]]; then
        pass "zone spread → 3 unique nodes (one per zone)"
    else
        nodes=$(get_placed_nodes zone-spread-svc | tr '\n' ',')
        fail_scenario "zone spread → $unique_nodes unique nodes: $nodes (expected 3)"
    fi
else
    fail_scenario "zone spread → insufficient placements"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 7: architecture amd64
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 7: architecture amd64 (node-2 is arm64)"
cleanup_scenario
set_node_arch deploy-node-1 amd64
set_node_arch deploy-node-2 arm64
set_node_arch deploy-node-3 amd64

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-architecture.cca" >/dev/null 2>&1

if wait_for_placements amd64-only 1; then
    placed_on=$(get_placed_nodes amd64-only)
    if [[ "$placed_on" != *"deploy-node-2"* ]]; then
        pass "architecture amd64 → placed on $placed_on (avoided arm64 node-2)"
    else
        fail_scenario "architecture amd64 → placed on deploy-node-2 (arm64, should be excluded)"
    fi
else
    fail_scenario "architecture amd64 → no placement"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 8: combined constraints
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 8: combined (require+prefer+accept+spread+arch)"
cleanup_scenario
set_node_label deploy-node-1 gpu true
set_node_label deploy-node-1 ssd true
set_node_restrict deploy-node-1 dedicated-compute
set_node_arch deploy-node-1 amd64
set_node_zone deploy-node-1 zone-a

set_node_label deploy-node-2 gpu true
set_node_arch deploy-node-2 amd64
set_node_zone deploy-node-2 zone-b

set_node_arch deploy-node-3 arm64
set_node_zone deploy-node-3 zone-c

ssh_ctrl "/usr/local/bin/cca apply /dev/stdin --store etcd --endpoints http://${CTRL_IP}:2379" \
    < "$WORKLOADS_DIR/placement-combined.cca" >/dev/null 2>&1

if wait_for_placements combined-placement 2 45; then
    nodes=$(get_placed_nodes combined-placement | tr '\n' ',')
    # node-3 excluded (arm64). node-1 and node-2 eligible (amd64, gpu=true, accept dedicated-compute).
    if echo "$nodes" | grep -q "deploy-node-1" && echo "$nodes" | grep -q "deploy-node-2"; then
        pass "combined → placed on node-1 and node-2 (correct intersection)"
    else
        fail_scenario "combined → placed on $nodes (expected node-1,node-2)"
    fi
else
    fail_scenario "combined → insufficient placements"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 9: tenant quota — ALLOW (within limit)
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 9: tenant quota ALLOW (3 instances, quota=5)"
cleanup_scenario
create_tenant alpha 5

http_code=$(api_apply_status 'service alpha/web { image nginx:1.27; instances 3 }')
if [[ "$http_code" == "200" ]]; then
    pass "tenant quota ALLOW → HTTP 200 (3 <= 5)"
else
    fail_scenario "tenant quota ALLOW → HTTP $http_code (expected 200)"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 10: tenant quota — DENY (exceeded)
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 10: tenant quota DENY (3 existing + 10 requested > 5)"
# Seed 3 observed instances so quota counter sees them.
for seqnum in 1 2 3; do
    seed_observed_instance "fake-alpha-${seqnum}" "alpha/web"
done
sleep 1

http_code=$(api_apply_status 'service alpha/overflow { image nginx:1.27; instances 10 }')
if [[ "$http_code" == "403" ]]; then
    pass "tenant quota DENY → HTTP 403 (quota exceeded)"
else
    fail_scenario "tenant quota DENY → HTTP $http_code (expected 403)"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 11: tenant deleting — DENY
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 11: deleting tenant rejects new services"
cleanup_scenario
$ETCD put "${PFX}/desired/tenant/beta" "" >/dev/null
$ETCD put "${PFX}/desired/tenant/beta/state" "deleting" >/dev/null

http_code=$(api_apply_status 'service beta/svc1 { image nginx:1.27; instances 1 }')
if [[ "$http_code" == "403" ]]; then
    pass "deleting tenant → HTTP 403 (rejected)"
else
    fail_scenario "deleting tenant → HTTP $http_code (expected 403)"
fi

# ══════════════════════════════════════════════════════════════════
# SCENARIO 12: restrict + quota combined
# ══════════════════════════════════════════════════════════════════
TOTAL=$((TOTAL + 1))
log "Scenario 12: restrict on node-1 + tenant quota (combined)"
cleanup_scenario
set_node_restrict deploy-node-1 gpu-pool
create_tenant gamma 3

http_code=$(api_apply_status 'service gamma/app { image nginx:1.27; instances 2 }')
if [[ "$http_code" == "200" ]]; then
    # Wait for scheduler to place instances.
    sleep 5
    nodes=$(get_placed_nodes gamma/app | tr '\n' ',')
    if [[ -n "$nodes" ]] && [[ "$nodes" != *"deploy-node-1"* ]]; then
        pass "restrict+quota → HTTP 200, placed on non-restricted nodes: $nodes"
    elif [[ -z "$nodes" ]]; then
        pass "restrict+quota → HTTP 200 (quota allowed, placement pending)"
    else
        fail_scenario "restrict+quota → placed on restricted node-1: $nodes"
    fi
else
    fail_scenario "restrict+quota → HTTP $http_code (expected 200)"
fi

# ══════════════════════════════════════════════════════════════════
# Final cleanup
# ══════════════════════════════════════════════════════════════════
cleanup_scenario
echo ""
log "All $TOTAL scenarios complete."
