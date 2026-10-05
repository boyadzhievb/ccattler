#!/usr/bin/env bash
# health_check.sh — Check health of a running CCattler test cluster
#
# Verifies: etcd health, API reachability, node count, container count,
# service convergence. Designed for periodic cron execution against a
# long-running test cluster.
#
# Exit codes: 0 = healthy, 1 = unhealthy
#
# Prerequisites:
#   - Cluster deployed via cluster_test.sh --env deploy or deploy workflow
#   - Vagrant SSH keys present at deploy/ansible/.vagrant/

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"

CTRL_IP="192.168.124.10"
WORKER1_IP="192.168.124.20"
WORKER2_IP="192.168.124.30"
ETCD_ENDPOINTS="http://${CTRL_IP}:2379"

# Restore persisted Vagrant state — CI checkout wipes .vagrant/ but the deploy
# workflow saves keys and machine IDs to this directory.
VAGRANT_STATE_DIR="/tmp/cca-vagrant-state-deploy"
if [[ ! -d "$ANSIBLE_DIR/.vagrant" ]] && [[ -d "$VAGRANT_STATE_DIR" ]]; then
    cp -a "$VAGRANT_STATE_DIR" "$ANSIBLE_DIR/.vagrant"
fi

HEALTHY=true

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

ssh_vm() {
    local vm_name="$1"; shift
    local key_path="$ANSIBLE_DIR/.vagrant/machines/$vm_name/libvirt/private_key"
    local ip
    case "$vm_name" in
        cca-deploy-ctrl)     ip="$CTRL_IP" ;;
        cca-deploy-worker-1) ip="$WORKER1_IP" ;;
        cca-deploy-worker-2) ip="$WORKER2_IP" ;;
        *) echo "Unknown VM: $vm_name" >&2; return 1 ;;
    esac
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR -o ConnectTimeout=10 \
        -i "$key_path" "vagrant@$ip" "$@"
}

count_containers() {
    local vm_name="$1"
    ssh_vm "$vm_name" "sudo nerdctl ps -q 2>/dev/null | wc -l" 2>/dev/null | tr -d ' ' || echo "0"
}

# ---- Check 1: VM reachability ----
log "Checking VM reachability..."
for vm in cca-deploy-ctrl cca-deploy-worker-1 cca-deploy-worker-2; do
    if ssh_vm "$vm" "true" 2>/dev/null; then
        log "  $vm: reachable"
    else
        log "  $vm: UNREACHABLE"
        HEALTHY=false
    fi
done

# ---- Check 2: etcd health ----
log "Checking etcd health..."
if ssh_vm cca-deploy-ctrl "etcdctl --endpoints=$ETCD_ENDPOINTS endpoint health" 2>/dev/null; then
    log "  etcd: healthy"
else
    log "  etcd: UNHEALTHY"
    HEALTHY=false
fi

# ---- Check 3: cca server API ----
log "Checking cca server API..."
CA_CERT="/tmp/cca-healthcheck-ca.pem"
CLIENT_CERT="/tmp/cca-healthcheck-client.pem"
CLIENT_KEY="/tmp/cca-healthcheck-client-key.pem"

CERTS_FETCHED=true
if ! ssh_vm cca-deploy-ctrl "sudo cat /etc/ccattler/pki/ca.pem" > "$CA_CERT" 2>/dev/null; then
    CERTS_FETCHED=false
fi
if ! ssh_vm cca-deploy-ctrl "sudo cat /etc/ccattler/pki/node.pem" > "$CLIENT_CERT" 2>/dev/null; then
    CERTS_FETCHED=false
fi
if ! ssh_vm cca-deploy-ctrl "sudo cat /etc/ccattler/pki/node-key.pem" > "$CLIENT_KEY" 2>/dev/null; then
    CERTS_FETCHED=false
fi

CCA_API="https://${CTRL_IP}:9770"
CURL_TLS="--cacert $CA_CERT --cert $CLIENT_CERT --key $CLIENT_KEY"

STATUS_JSON=""
if [[ "$CERTS_FETCHED" != "true" ]]; then
    log "  API /status: UNREACHABLE (could not fetch TLS certs from ctrl node)"
    HEALTHY=false
elif STATUS_JSON=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null); then
    log "  API /status: reachable"
    echo "$STATUS_JSON" | jq . 2>/dev/null || echo "$STATUS_JSON"
else
    log "  API /status: UNREACHABLE"
    HEALTHY=false
fi

# ---- Check 4: Node count ----
log "Checking node count..."
if [[ -n "$STATUS_JSON" ]]; then
    alive_count=$(echo "$STATUS_JSON" | grep -o '"state":"alive"' | wc -l | tr -d ' ')
    log "  Alive nodes: $alive_count/3"
    if [[ "$alive_count" -lt 3 ]]; then
        log "  WARNING: expected 3 alive nodes"
        HEALTHY=false
    fi
fi

# ---- Check 5: Container count ----
log "Checking containers on each node..."
total_containers=0
for vm in cca-deploy-ctrl cca-deploy-worker-1 cca-deploy-worker-2; do
    count=$(count_containers "$vm")
    log "  $vm: $count containers"
    total_containers=$((total_containers + count))
done
log "  Total containers: $total_containers"
if [[ "$total_containers" -lt 5 ]]; then
    log "  WARNING: expected at least 5 containers (2 Java + 3 Zabbix)"
    HEALTHY=false
fi

# ---- Check 6: systemd services ----
log "Checking systemd services..."
for vm in cca-deploy-ctrl cca-deploy-worker-1 cca-deploy-worker-2; do
    server_active=$(ssh_vm "$vm" "systemctl is-active cca-server 2>/dev/null || echo inactive" 2>/dev/null)
    agent_active=$(ssh_vm "$vm" "systemctl is-active cca-agent 2>/dev/null || echo inactive" 2>/dev/null)
    log "  $vm: server=$server_active agent=$agent_active"
    if [[ "$agent_active" != "active" ]]; then
        log "  WARNING: $vm agent not running"
        HEALTHY=false
    fi
done

# ---- Cleanup temp files ----
rm -f "$CA_CERT" "$CLIENT_CERT" "$CLIENT_KEY"

# ---- Result ----
echo ""
if [[ "$HEALTHY" == "true" ]]; then
    log "HEALTH CHECK: PASS"
    exit 0
else
    log "HEALTH CHECK: FAIL"
    exit 1
fi
