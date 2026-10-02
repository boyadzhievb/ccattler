#!/usr/bin/env bash
# cluster_test.sh — End-to-end CCattler cluster test
#
# Provisions 3 libvirt VMs, deploys a full CCattler cluster via Ansible,
# applies test workloads, verifies they converge, and tears down.
#
# Exit codes: 0 = pass, 1 = fail
#
# Prerequisites:
#   - Linux host with libvirt/KVM, Vagrant (vagrant-libvirt), Ansible
#   - CCattler binary at deploy/ansible/cca-linux-amd64
#
# Usage:
#   ./test/e2e/cluster_test.sh              # full run
#   ./test/e2e/cluster_test.sh --no-destroy # keep VMs on failure for debugging

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"
WORKLOADS_DIR="$SCRIPT_DIR/workloads"
DESTROY_ON_EXIT=true

CTRL_IP="192.168.122.10"
WORKER1_IP="192.168.122.20"
WORKER2_IP="192.168.122.30"
ETCD_ENDPOINTS="http://${CTRL_IP}:2379"

# Maximum seconds to wait for convergence checks.
CONVERGE_TIMEOUT=120
CONTAINER_TIMEOUT=90

if [[ "${1:-}" == "--no-destroy" ]]; then
    DESTROY_ON_EXIT=false
fi

log() { echo "==> [$(date +%H:%M:%S)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

cleanup() {
    local exit_code=$?
    if [[ "$DESTROY_ON_EXIT" == "true" ]]; then
        log "Tearing down VMs..."
        cd "$ANSIBLE_DIR"
        VAGRANT_VAGRANTFILE=test-Vagrantfile vagrant destroy -f 2>/dev/null || true
    else
        log "Keeping VMs alive (--no-destroy). Destroy manually:"
        log "  cd $ANSIBLE_DIR && VAGRANT_VAGRANTFILE=test-Vagrantfile vagrant destroy -f"
    fi
    rm -f /tmp/cca-e2e-ca.pem
    if [[ $exit_code -eq 0 ]]; then
        log "TEST PASSED"
    else
        log "TEST FAILED (exit code $exit_code)"
    fi
}
trap cleanup EXIT

wait_for_port() {
    local host="$1" port="$2" timeout="${3:-60}" label="${4:-$host:$port}"
    local deadline=$((SECONDS + timeout))
    while [[ $SECONDS -lt $deadline ]]; do
        if nc -z "$host" "$port" 2>/dev/null; then
            return 0
        fi
        sleep 2
    done
    fail "Timed out waiting for $label (${timeout}s)"
}

ssh_vm() {
    local vm_name="$1"; shift
    local key_path="$ANSIBLE_DIR/.vagrant/machines/$vm_name/libvirt/private_key"
    local ip
    case "$vm_name" in
        cca-test-ctrl)     ip="$CTRL_IP" ;;
        cca-test-worker-1) ip="$WORKER1_IP" ;;
        cca-test-worker-2) ip="$WORKER2_IP" ;;
        *) fail "Unknown VM: $vm_name" ;;
    esac
    ssh -o StrictHostKeyChecking=no -o LogLevel=ERROR -i "$key_path" "vagrant@$ip" "$@"
}

count_containers() {
    local vm_name="$1" filter="${2:-cca-}"
    ssh_vm "$vm_name" "sudo nerdctl ps --filter name=$filter -q 2>/dev/null | wc -l" | tr -d ' '
}

# ---- Step 0: Build binary ----
log "Building cca binary for linux/amd64..."
(cd "$REPO_DIR" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$ANSIBLE_DIR/cca-linux-amd64" ./cmd/cca/)
log "Binary built: $(ls -lh "$ANSIBLE_DIR/cca-linux-amd64" | awk '{print $5}')"

# ---- Step 1: Provision VMs ----
log "Provisioning 3 VMs via Vagrant..."
cd "$ANSIBLE_DIR"
VAGRANT_VAGRANTFILE=test-Vagrantfile vagrant up --no-provision

log "Waiting for SSH on all VMs..."
wait_for_port "$CTRL_IP" 22 180 "cca-test-ctrl SSH"
wait_for_port "$WORKER1_IP" 22 180 "cca-test-worker-1 SSH"
wait_for_port "$WORKER2_IP" 22 180 "cca-test-worker-2 SSH"

# ---- Step 2: Deploy cluster via Ansible ----
log "Running Ansible deployment..."
ansible-playbook -i test-inventory.ini test-deploy.yml

# ---- Step 3: Verify cluster basics ----
log "Verifying etcd health..."
ssh_vm cca-test-ctrl "etcdctl --endpoints=$ETCD_ENDPOINTS endpoint health" || fail "etcd unhealthy"

log "Fetching cluster CA certificate for API verification..."
CA_CERT_LOCAL="/tmp/cca-e2e-ca.pem"
ssh_vm cca-test-ctrl "sudo cat /etc/ccattler/pki/ca.pem" > "$CA_CERT_LOCAL"
CCA_API="https://${CTRL_IP}:9770"

log "Verifying cca server API..."
wait_for_port "$CTRL_IP" 9770 45 "cca-server API"
curl -sf --cacert "$CA_CERT_LOCAL" "${CCA_API}/status" > /dev/null || fail "cca server /status unreachable"

log "Verifying all 3 nodes registered..."
node_count=$(curl -sf --cacert "$CA_CERT_LOCAL" "${CCA_API}/status" | grep -c '"state":"alive"' || true)
if [[ "$node_count" -lt 3 ]]; then
    log "WARNING: Only $node_count/3 nodes registered, waiting..."
    deadline=$((SECONDS + CONVERGE_TIMEOUT))
    while [[ $SECONDS -lt $deadline ]]; do
        node_count=$(curl -sf --cacert "$CA_CERT_LOCAL" "${CCA_API}/status" | grep -c '"state":"alive"' || true)
        if [[ "$node_count" -ge 3 ]]; then break; fi
        sleep 5
    done
    [[ "$node_count" -ge 3 ]] || fail "Only $node_count/3 nodes registered after ${CONVERGE_TIMEOUT}s"
fi
log "All 3 nodes registered and alive"

# ---- Step 4: Deploy Java test app ----
log "Applying Java test workload..."
scp -o StrictHostKeyChecking=no -o LogLevel=ERROR \
    -i "$ANSIBLE_DIR/.vagrant/machines/cca-test-ctrl/libvirt/private_key" \
    "$WORKLOADS_DIR/java-app.cca" "vagrant@${CTRL_IP}:/tmp/java-app.cca"

ssh_vm cca-test-ctrl "/usr/local/bin/cca apply /tmp/java-app.cca --store etcd --endpoints $ETCD_ENDPOINTS"

log "Waiting for Java app containers to start..."
deadline=$((SECONDS + CONTAINER_TIMEOUT))
total_containers=0
while [[ $SECONDS -lt $deadline ]]; do
    c1=$(count_containers cca-test-ctrl)
    c2=$(count_containers cca-test-worker-1)
    c3=$(count_containers cca-test-worker-2)
    total_containers=$((c1 + c2 + c3))
    if [[ "$total_containers" -ge 2 ]]; then break; fi
    sleep 5
done
[[ "$total_containers" -ge 2 ]] || fail "Expected 2+ containers, found $total_containers after ${CONTAINER_TIMEOUT}s"
log "Java app running: $total_containers containers across 3 nodes"

# ---- Step 5: Deploy Zabbix stack ----
log "Applying Zabbix test workload..."
scp -o StrictHostKeyChecking=no -o LogLevel=ERROR \
    -i "$ANSIBLE_DIR/.vagrant/machines/cca-test-ctrl/libvirt/private_key" \
    "$WORKLOADS_DIR/zabbix.cca" "vagrant@${CTRL_IP}:/tmp/zabbix.cca"

ssh_vm cca-test-ctrl "/usr/local/bin/cca apply /tmp/zabbix.cca --store etcd --endpoints $ETCD_ENDPOINTS"

log "Waiting for Zabbix containers (3 services)..."
deadline=$((SECONDS + CONTAINER_TIMEOUT))
while [[ $SECONDS -lt $deadline ]]; do
    c1=$(count_containers cca-test-ctrl)
    c2=$(count_containers cca-test-worker-1)
    c3=$(count_containers cca-test-worker-2)
    total_containers=$((c1 + c2 + c3))
    # Java (2) + Zabbix (3) = 5 total
    if [[ "$total_containers" -ge 5 ]]; then break; fi
    sleep 5
done
[[ "$total_containers" -ge 5 ]] || fail "Expected 5+ containers, found $total_containers after ${CONTAINER_TIMEOUT}s"
log "Zabbix stack running: $total_containers total containers"

# ---- Step 6: Final status ----
log "Final cluster status:"
curl -sf --cacert "$CA_CERT_LOCAL" "${CCA_API}/status" || true
echo ""

log "Containers on each node:"
for vm in cca-test-ctrl cca-test-worker-1 cca-test-worker-2; do
    echo "--- $vm ---"
    ssh_vm "$vm" 'sudo nerdctl ps --format "table {{.Names}}\t{{.Image}}\t{{.Status}}"' 2>/dev/null || true
done

log "All checks passed"
exit 0
