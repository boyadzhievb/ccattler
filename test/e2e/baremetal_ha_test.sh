#!/usr/bin/env bash
# baremetal_ha_test.sh — HA cluster validation on bare-metal nodes
#
# Tests etcd 3-node quorum, leader election, workload placement,
# VIP routing, control plane failover, and etcd member failure recovery.
#
# Topology: 3 ctrl (etcd + cca-server) + 1 worker (cca-agent)
#   node-1 (192.168.100.215): etcd + control plane
#   node-2 (192.168.100.30):  etcd + control plane
#   node-3 (192.168.100.31):  etcd + control plane
#   node-4 (192.168.100.51):  worker
#
# Usage:
#   ./test/e2e/baremetal_ha_test.sh
#   ./test/e2e/baremetal_ha_test.sh --skip-failover   # skip destructive tests

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

CTRL1_IP="192.168.100.215"
CTRL2_IP="192.168.100.30"
CTRL3_IP="192.168.100.31"
WORKER1_IP="192.168.100.51"

CTRL_IPS=("$CTRL1_IP" "$CTRL2_IP" "$CTRL3_IP")
CTRL_NAMES=("node-1" "node-2" "node-3")
ALL_IPS=("$CTRL1_IP" "$CTRL2_IP" "$CTRL3_IP" "$WORKER1_IP")
ALL_NAMES=("node-1" "node-2" "node-3" "node-4")

ETCD_ENDPOINTS="https://${CTRL1_IP}:2379,https://${CTRL2_IP}:2379,https://${CTRL3_IP}:2379"

SSH_USER="${SSH_USER:-bozhan}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_rsa_dell_bozhan}"
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10"

SKIP_FAILOVER=false
for argument in "$@"; do
    case "$argument" in
        --skip-failover) SKIP_FAILOVER=true ;;
    esac
done

PASSED=0
FAILED=0
TOTAL=0

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
pass() { log "  PASS: $*"; PASSED=$((PASSED + 1)); TOTAL=$((TOTAL + 1)); }
fail() { log "  FAIL: $*"; FAILED=$((FAILED + 1)); TOTAL=$((TOTAL + 1)); }

node_ip_for_name() {
    local node_name="$1"
    case "$node_name" in
        node-1) echo "$CTRL1_IP" ;;
        node-2) echo "$CTRL2_IP" ;;
        node-3) echo "$CTRL3_IP" ;;
        node-4) echo "$WORKER1_IP" ;;
        *) echo "" ;;
    esac
}

ssh_node() {
    local node_name="$1"; shift
    local node_ip
    node_ip=$(node_ip_for_name "$node_name")
    ssh $SSH_OPTS -i "$SSH_KEY" "${SSH_USER}@${node_ip}" "$@"
}

scp_to_node() {
    local node_name="$1" local_path="$2" remote_path="$3"
    local node_ip
    node_ip=$(node_ip_for_name "$node_name")
    scp $SSH_OPTS -i "$SSH_KEY" "$local_path" "${SSH_USER}@${node_ip}:${remote_path}"
}

etcd_tls_flags() {
    echo "--cacert /etc/ccattler/pki/ca.pem --cert /etc/ccattler/pki/node.pem --key /etc/ccattler/pki/node-key.pem"
}

api_curl() {
    local ctrl_ip="$1" path="$2"
    local ctrl_name
    for index in "${!CTRL_IPS[@]}"; do
        if [[ "${CTRL_IPS[$index]}" == "$ctrl_ip" ]]; then
            ctrl_name="${CTRL_NAMES[$index]}"
            break
        fi
    done
    ssh_node "$ctrl_name" "curl -sf --cacert /etc/ccattler/pki/ca.pem --cert /etc/ccattler/pki/node.pem --key /etc/ccattler/pki/node-key.pem https://${ctrl_ip}:9770${path}" 2>/dev/null
}

wait_for_condition() {
    local description="$1" timeout="$2"
    shift 2
    local deadline=$((SECONDS + timeout))
    while [[ $SECONDS -lt $deadline ]]; do
        if "$@" 2>/dev/null; then
            return 0
        fi
        sleep 3
    done
    return 1
}

# ============================================================
# Check 1: Node reachability
# ============================================================
log "Check 1: Node reachability"
for node_name in "${ALL_NAMES[@]}"; do
    if ssh_node "$node_name" "true" 2>/dev/null; then
        pass "$node_name ($(node_ip_for_name "$node_name")) reachable"
    else
        fail "$node_name ($(node_ip_for_name "$node_name")) UNREACHABLE"
    fi
done

# ============================================================
# Check 2: etcd cluster health — all 3 members healthy
# ============================================================
log "Check 2: etcd cluster health"
ETCD_HEALTHY_COUNT=0
for index in "${!CTRL_NAMES[@]}"; do
    ctrl_name="${CTRL_NAMES[$index]}"
    ctrl_ip="${CTRL_IPS[$index]}"
    if ssh_node "$ctrl_name" "ETCDCTL_API=3 etcdctl --endpoints https://${ctrl_ip}:2379 $(etcd_tls_flags) endpoint health" 2>/dev/null | grep -q "is healthy"; then
        pass "etcd on $ctrl_name ($ctrl_ip) healthy"
        ETCD_HEALTHY_COUNT=$((ETCD_HEALTHY_COUNT + 1))
    else
        fail "etcd on $ctrl_name ($ctrl_ip) NOT healthy"
    fi
done

if [[ "$ETCD_HEALTHY_COUNT" -ge 2 ]]; then
    pass "etcd quorum: $ETCD_HEALTHY_COUNT/3 healthy (quorum=2)"
else
    fail "etcd quorum LOST: only $ETCD_HEALTHY_COUNT/3 healthy"
fi

MEMBER_COUNT=$(ssh_node "${CTRL_NAMES[0]}" "ETCDCTL_API=3 etcdctl --endpoints https://${CTRL1_IP}:2379 $(etcd_tls_flags) member list" 2>/dev/null | wc -l | tr -d ' ')
if [[ "$MEMBER_COUNT" -eq 3 ]]; then
    pass "etcd cluster has 3 members"
else
    fail "etcd cluster has $MEMBER_COUNT members (expected 3)"
fi

# ============================================================
# Check 3: cca-server API responds on all 3 ctrl nodes
# ============================================================
log "Check 3: cca-server API health"
for index in "${!CTRL_NAMES[@]}"; do
    ctrl_ip="${CTRL_IPS[$index]}"
    ctrl_name="${CTRL_NAMES[$index]}"
    if api_curl "$ctrl_ip" "/healthz" | grep -qi "ok\|healthy"; then
        pass "cca-server on $ctrl_name serves API"
    else
        fail "cca-server on $ctrl_name NOT responding"
    fi
done

# ============================================================
# Check 4: leader election — exactly one controller leader
# ============================================================
log "Check 4: leader election"
LEADER_HOLDER=$(ssh_node "${CTRL_NAMES[0]}" "ETCDCTL_API=3 etcdctl --endpoints https://${CTRL1_IP}:2379 $(etcd_tls_flags) get /ccattler/leader/controlplane/holder --print-value-only" 2>/dev/null | tr -d '\n')
if [[ -n "$LEADER_HOLDER" ]]; then
    pass "leader elected: $LEADER_HOLDER"
else
    fail "no leader holder found in etcd"
fi

# ============================================================
# Check 5: node registration — all 4 nodes alive
# ============================================================
log "Check 5: node registration"
STATUS_JSON=$(api_curl "${CTRL1_IP}" "/status" 2>/dev/null || echo "{}")
ALIVE_NODES=$(echo "$STATUS_JSON" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    nodes = data.get('nodes', [])
    alive = [n for n in nodes if n.get('state') == 'alive']
    print(len(alive))
except:
    print(0)
" 2>/dev/null || echo "0")
if [[ "$ALIVE_NODES" -eq 4 ]]; then
    pass "all 4 nodes alive"
elif [[ "$ALIVE_NODES" -ge 1 ]]; then
    fail "only $ALIVE_NODES/4 nodes alive"
else
    fail "cannot read node status from API"
fi

# ============================================================
# Check 6: deploy test workload and verify placement
# ============================================================
log "Check 6: workload deployment"
scp_to_node "${CTRL_NAMES[0]}" "$SCRIPT_DIR/deploy.ccv" "/tmp/e2e-values.ccv"
scp_to_node "${CTRL_NAMES[0]}" "$SCRIPT_DIR/vm-ha/ha-test.cca" "/tmp/ha-test.cca"
ssh_node "${CTRL_NAMES[0]}" "sudo /usr/local/bin/cca apply --values /tmp/e2e-values.ccv /tmp/ha-test.cca" 2>/dev/null
log "  Waiting for workload convergence (90s max)..."

workload_converged() {
    local status_json running_count
    for check_ip in "${CTRL_IPS[@]}"; do
        status_json=$(api_curl "$check_ip" "/status" 2>/dev/null) && break
    done
    running_count=$(echo "$status_json" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    instances = data.get('instances', [])
    running = [i for i in instances if i.get('state') == 'running']
    print(len(running))
except:
    print(0)
" 2>/dev/null || echo "0")
    [[ "$running_count" -ge 5 ]]
}

if wait_for_condition "workload convergence" 90 workload_converged; then
    pass "5 instances running (3 whoami + 2 whoami-multiport)"
else
    fail "workload did not converge within 90s"
fi

# ============================================================
# Check 7: VIP routing — curl whoami VIP from each node
# ============================================================
log "Check 7: VIP routing"
WHOAMI_VIP=$(api_curl "${CTRL1_IP}" "/status" 2>/dev/null | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for svc in data.get('services', []):
        if svc.get('name') == 'whoami' and svc.get('vip'):
            print(svc['vip'])
            break
except:
    pass
" 2>/dev/null || echo "")

if [[ -n "$WHOAMI_VIP" ]]; then
    pass "whoami VIP assigned: $WHOAMI_VIP"
    for node_name in "${ALL_NAMES[@]}"; do
        if ssh_node "$node_name" "curl -sf --max-time 5 http://${WHOAMI_VIP}:80/" 2>/dev/null | grep -qi "hostname"; then
            pass "VIP reachable from $node_name"
        else
            fail "VIP NOT reachable from $node_name"
        fi
    done
else
    fail "whoami VIP not assigned"
fi

# ============================================================
# Check 8: control plane failover (destructive)
# ============================================================
if [[ "$SKIP_FAILOVER" == "true" ]]; then
    log "Check 8: SKIPPED (--skip-failover)"
else
    log "Check 8: control plane failover"

    LEADER_CTRL_NAME=""
    LEADER_CTRL_INDEX=""
    for index in "${!CTRL_NAMES[@]}"; do
        if [[ "${CTRL_NAMES[$index]}" == "$LEADER_HOLDER" || "$LEADER_HOLDER" == *"${CTRL_NAMES[$index]}"* ]]; then
            LEADER_CTRL_NAME="${CTRL_NAMES[$index]}"
            LEADER_CTRL_INDEX="$index"
            break
        fi
    done

    if [[ -z "$LEADER_CTRL_NAME" ]]; then
        for index in "${!CTRL_IPS[@]}"; do
            if [[ "$LEADER_HOLDER" == *"${CTRL_IPS[$index]}"* ]]; then
                LEADER_CTRL_NAME="${CTRL_NAMES[$index]}"
                LEADER_CTRL_INDEX="$index"
                break
            fi
        done
    fi

    if [[ -z "$LEADER_CTRL_NAME" ]]; then
        log "  WARNING: Could not match leader '$LEADER_HOLDER' to any ctrl, defaulting to node-1"
        LEADER_CTRL_NAME="${CTRL_NAMES[0]}"
        LEADER_CTRL_INDEX=0
    fi

    log "  Stopping cca-server on leader: $LEADER_CTRL_NAME ($(node_ip_for_name "$LEADER_CTRL_NAME"))"
    ssh_node "$LEADER_CTRL_NAME" "sudo systemctl stop cca-server" 2>/dev/null || true

    SURVIVOR_INDEX=$(( (LEADER_CTRL_INDEX + 1) % 3 ))
    SURVIVOR_NAME="${CTRL_NAMES[$SURVIVOR_INDEX]}"
    SURVIVOR_IP="${CTRL_IPS[$SURVIVOR_INDEX]}"

    log "  Waiting for new leader election on surviving nodes (30s)..."
    new_leader_elected() {
        local new_holder
        new_holder=$(ssh_node "$SURVIVOR_NAME" "ETCDCTL_API=3 etcdctl --endpoints https://${SURVIVOR_IP}:2379 $(etcd_tls_flags) get /ccattler/leader/controlplane/holder --print-value-only" 2>/dev/null | tr -d '\n')
        [[ -n "$new_holder" && "$new_holder" != "$LEADER_HOLDER" ]]
    }

    if wait_for_condition "new leader" 30 new_leader_elected; then
        NEW_LEADER=$(ssh_node "$SURVIVOR_NAME" "ETCDCTL_API=3 etcdctl --endpoints https://${SURVIVOR_IP}:2379 $(etcd_tls_flags) get /ccattler/leader/controlplane/holder --print-value-only" 2>/dev/null | tr -d '\n')
        pass "new leader elected: $NEW_LEADER (was: $LEADER_HOLDER)"
    else
        fail "no new leader elected within 30s"
    fi

    if api_curl "$SURVIVOR_IP" "/healthz" 2>/dev/null | grep -qi "ok\|healthy"; then
        pass "API still healthy on $SURVIVOR_NAME after leader failover"
    else
        fail "API unhealthy on $SURVIVOR_NAME after leader failover"
    fi

    if workload_converged; then
        pass "workloads still running after leader failover"
    else
        fail "workloads disrupted after leader failover"
    fi

    log "  Restarting cca-server on $LEADER_CTRL_NAME"
    ssh_node "$LEADER_CTRL_NAME" "sudo systemctl start cca-server" 2>/dev/null
    sleep 5
    if api_curl "${CTRL_IPS[$LEADER_CTRL_INDEX]}" "/healthz" 2>/dev/null | grep -qi "ok\|healthy"; then
        pass "$LEADER_CTRL_NAME rejoined cluster"
    else
        fail "$LEADER_CTRL_NAME did not rejoin after restart"
    fi
fi

# ============================================================
# Check 9: etcd member failure (destructive)
# ============================================================
if [[ "$SKIP_FAILOVER" == "true" ]]; then
    log "Check 9: SKIPPED (--skip-failover)"
else
    log "Check 9: etcd member failure"

    ETCD_VICTIM="node-3"
    ETCD_VICTIM_IP="$CTRL3_IP"
    log "  Stopping etcd on $ETCD_VICTIM ($ETCD_VICTIM_IP)"
    ssh_node "$ETCD_VICTIM" "sudo systemctl stop etcd" 2>/dev/null || true
    sleep 3

    QUORUM_OK=false
    for quorum_index in 0 1; do
        quorum_name="${CTRL_NAMES[$quorum_index]}"
        quorum_ip="${CTRL_IPS[$quorum_index]}"
        if ssh_node "$quorum_name" "ETCDCTL_API=3 etcdctl --endpoints https://${quorum_ip}:2379 $(etcd_tls_flags) endpoint health" 2>/dev/null | grep -q "is healthy"; then
            QUORUM_OK=true
            break
        fi
    done
    if [[ "$QUORUM_OK" == "true" ]]; then
        pass "etcd quorum holds with 2/3 members"
    else
        fail "etcd quorum LOST after stopping one member"
    fi

    API_OK=false
    for api_index in 0 1; do
        if api_curl "${CTRL_IPS[$api_index]}" "/healthz" 2>/dev/null | grep -qi "ok\|healthy"; then
            API_OK=true
            break
        fi
    done
    if [[ "$API_OK" == "true" ]]; then
        pass "API still works with 2/3 etcd members"
    else
        fail "API broken with 2/3 etcd members"
    fi

    log "  Restarting etcd on $ETCD_VICTIM"
    ssh_node "$ETCD_VICTIM" "sudo systemctl start etcd" 2>/dev/null
    sleep 5

    if ssh_node "$ETCD_VICTIM" "ETCDCTL_API=3 etcdctl --endpoints https://${ETCD_VICTIM_IP}:2379 $(etcd_tls_flags) endpoint health" 2>/dev/null | grep -q "is healthy"; then
        pass "etcd member $ETCD_VICTIM rejoined cluster"
    else
        fail "etcd member $ETCD_VICTIM did not rejoin"
    fi
fi

# ============================================================
# Summary
# ============================================================
echo ""
log "========================================="
log "HA Test Results: $PASSED passed, $FAILED failed ($TOTAL total)"
log "========================================="

if [[ "$FAILED" -gt 0 ]]; then
    exit 1
fi
