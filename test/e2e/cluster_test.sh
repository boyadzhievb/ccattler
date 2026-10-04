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
#   ./test/e2e/cluster_test.sh                       # full run, build from source
#   ./test/e2e/cluster_test.sh --no-destroy          # keep VMs for debugging
#   ./test/e2e/cluster_test.sh --from-release        # download latest release binary
#   ./test/e2e/cluster_test.sh --from-release --no-destroy

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"
WORKLOADS_DIR="$SCRIPT_DIR/workloads"
DESTROY_ON_EXIT=true
BINARY_SOURCE=build

CTRL_IP="192.168.122.10"
WORKER1_IP="192.168.122.20"
WORKER2_IP="192.168.122.30"
ETCD_ENDPOINTS="http://${CTRL_IP}:2379"

# Maximum seconds to wait for convergence checks.
CONVERGE_TIMEOUT=120
CONTAINER_TIMEOUT=90

for arg in "$@"; do
    case "$arg" in
        --no-destroy)  DESTROY_ON_EXIT=false ;;
        --from-release) BINARY_SOURCE=release ;;
    esac
done

log() { echo "==> [$(date +%H:%M:%S)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

collect_vm_logs() {
    log "Collecting VM logs before teardown..."
    for vm in cca-test-ctrl cca-test-worker-1 cca-test-worker-2; do
        echo "=== $vm server.log ==="
        ssh_vm "$vm" "sudo cat /var/log/ccattler/server.log 2>/dev/null || echo '(no server.log)'" 2>/dev/null || true
        echo "=== $vm agent.log ==="
        ssh_vm "$vm" "sudo cat /var/log/ccattler/agent.log 2>/dev/null || echo '(no agent.log)'" 2>/dev/null || true
        echo "=== $vm systemctl status ==="
        ssh_vm "$vm" "sudo systemctl status cca-server cca-agent 2>/dev/null || true" 2>/dev/null || true
    done
}

cleanup() {
    local exit_code=$?
    if [[ $exit_code -ne 0 ]]; then
        collect_vm_logs || true
    fi
    if [[ "$DESTROY_ON_EXIT" == "true" ]]; then
        log "Tearing down VMs..."
        cd "$ANSIBLE_DIR"
        VAGRANT_VAGRANTFILE=test-Vagrantfile vagrant destroy -f 2>/dev/null || true
    else
        log "Keeping VMs alive (--no-destroy). Destroy manually:"
        log "  cd $ANSIBLE_DIR && VAGRANT_VAGRANTFILE=test-Vagrantfile vagrant destroy -f"
    fi
    rm -f /tmp/cca-e2e-ca.pem /tmp/cca-e2e-client.pem /tmp/cca-e2e-client-key.pem
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
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -i "$key_path" "vagrant@$ip" "$@"
}

count_containers() {
    local vm_name="$1"
    ssh_vm "$vm_name" "sudo nerdctl ps -q 2>/dev/null | wc -l" | tr -d ' '
}

# ---- Prerequisites ----
command -v vagrant >/dev/null 2>&1 || fail "vagrant not found in PATH"
command -v ansible-playbook >/dev/null 2>&1 || fail "ansible-playbook not found in PATH"

if ! vagrant plugin list 2>/dev/null | grep -q vagrant-libvirt; then
    log "vagrant-libvirt plugin not found, installing..."
    vagrant plugin install vagrant-libvirt || fail "failed to install vagrant-libvirt plugin"
    log "vagrant-libvirt installed"
fi

# ---- Cleanup stale VMs from previous runs ----
for stale_domain in ansible_cca-test-ctrl ansible_cca-test-worker-1 ansible_cca-test-worker-2; do
    if sudo virsh dominfo "$stale_domain" >/dev/null 2>&1; then
        log "Removing stale libvirt domain: $stale_domain"
        sudo virsh destroy "$stale_domain" 2>/dev/null || true
        sudo virsh undefine "$stale_domain" --remove-all-storage 2>/dev/null || true
    fi
done

# ---- Step 0: Obtain binary ----
if [[ "$BINARY_SOURCE" == "release" ]]; then
    log "Downloading latest release binary..."
    GITHUB_REPO="boyadzhievb/ccattler"
    RELEASE_TAG=$(curl -sS "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" | jq -r '.tag_name')
    TARBALL_NAME="cca-${RELEASE_TAG}-linux-amd64.tar.gz"
    DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_TAG}/${TARBALL_NAME}"
    log "Release: ${RELEASE_TAG} — downloading ${TARBALL_NAME}..."
    curl -sSL "$DOWNLOAD_URL" -o "/tmp/${TARBALL_NAME}"
    tar xzf "/tmp/${TARBALL_NAME}" -C "$ANSIBLE_DIR" --strip-components=0
    mv "$ANSIBLE_DIR/cca" "$ANSIBLE_DIR/cca-linux-amd64" 2>/dev/null || true
    rm -f "/tmp/${TARBALL_NAME}"
    log "Release binary: $(ls -lh "$ANSIBLE_DIR/cca-linux-amd64" | awk '{print $5}')"
else
    log "Building cca binary for linux/amd64..."
    (cd "$REPO_DIR" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$ANSIBLE_DIR/cca-linux-amd64" ./cmd/cca/)
    log "Binary built: $(ls -lh "$ANSIBLE_DIR/cca-linux-amd64" | awk '{print $5}')"
fi

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

log "Fetching cluster TLS certificates for API verification..."
CA_CERT_LOCAL="/tmp/cca-e2e-ca.pem"
CLIENT_CERT_LOCAL="/tmp/cca-e2e-client.pem"
CLIENT_KEY_LOCAL="/tmp/cca-e2e-client-key.pem"
ssh_vm cca-test-ctrl "sudo cat /etc/ccattler/pki/ca.pem" > "$CA_CERT_LOCAL"
ssh_vm cca-test-ctrl "sudo cat /etc/ccattler/pki/node.pem" > "$CLIENT_CERT_LOCAL"
ssh_vm cca-test-ctrl "sudo cat /etc/ccattler/pki/node-key.pem" > "$CLIENT_KEY_LOCAL"
CCA_API="https://${CTRL_IP}:9770"
CURL_TLS="--cacert $CA_CERT_LOCAL --cert $CLIENT_CERT_LOCAL --key $CLIENT_KEY_LOCAL"

log "Verifying cca server API..."
deadline=$((SECONDS + 60))
while [[ $SECONDS -lt $deadline ]]; do
    if curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" > /dev/null 2>&1; then
        break
    fi
    sleep 2
done
curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" > /dev/null || fail "cca server /status unreachable after 60s"

count_alive_nodes() {
    local status_json
    status_json=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null) || { echo 0; return; }
    local count
    count=$(echo "$status_json" | grep -o '"state":"alive"' | wc -l | tr -d ' \n') || true
    echo "${count:-0}"
}

log "Verifying all 3 nodes registered..."
node_count=$(count_alive_nodes)
log "Initial node count: $node_count"
if [[ "$node_count" -lt 3 ]]; then
    log "WARNING: Only $node_count/3 nodes registered, waiting..."
    deadline=$((SECONDS + CONVERGE_TIMEOUT))
    while [[ $SECONDS -lt $deadline ]]; do
        sleep 5
        node_count=$(count_alive_nodes)
        log "  Retry: $node_count/3 nodes registered (${SECONDS}s elapsed)"
        if [[ "$node_count" -ge 3 ]]; then break; fi
    done
    if [[ "$node_count" -lt 3 ]]; then
        log "Final node count: $node_count"
        log "Final /status response:"
        curl -s $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>&1 || true
        echo ""
        log "etcd keys under /ccattler/observed/node/:"
        ssh_vm cca-test-ctrl "etcdctl --endpoints=$ETCD_ENDPOINTS get --prefix /ccattler/observed/node/ --keys-only" 2>/dev/null || true
        fail "Only $node_count/3 nodes registered after ${CONVERGE_TIMEOUT}s"
    fi
fi
log "All 3 nodes registered and alive"

# ---- Step 4: Deploy Java test app ----
log "Applying Java test workload..."
scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -i "$ANSIBLE_DIR/.vagrant/machines/cca-test-ctrl/libvirt/private_key" \
    "$WORKLOADS_DIR/java-app.cca" "vagrant@${CTRL_IP}:/tmp/java-app.cca"

ssh_vm cca-test-ctrl "/usr/local/bin/cca apply /tmp/java-app.cca --store etcd --endpoints $ETCD_ENDPOINTS"

log "Waiting for Java app containers to start..."
deadline=$((SECONDS + CONTAINER_TIMEOUT))
total_containers=0
while [[ $SECONDS -lt $deadline ]]; do
    sleep 5
    c1=$(count_containers cca-test-ctrl)
    c2=$(count_containers cca-test-worker-1)
    c3=$(count_containers cca-test-worker-2)
    total_containers=$((c1 + c2 + c3))
    log "  Retry: $total_containers/2 containers (ctrl=$c1 w1=$c2 w2=$c3, ${SECONDS}s elapsed)"
    if [[ "$total_containers" -ge 2 ]]; then break; fi
done
[[ "$total_containers" -ge 2 ]] || fail "Expected 2+ containers, found $total_containers after ${CONTAINER_TIMEOUT}s"
log "Java app running: $total_containers containers across 3 nodes"

# ---- Step 5: Deploy Zabbix stack ----
log "Applying Zabbix test workload..."
scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -i "$ANSIBLE_DIR/.vagrant/machines/cca-test-ctrl/libvirt/private_key" \
    "$WORKLOADS_DIR/zabbix.cca" "vagrant@${CTRL_IP}:/tmp/zabbix.cca"

ssh_vm cca-test-ctrl "/usr/local/bin/cca apply /tmp/zabbix.cca --store etcd --endpoints $ETCD_ENDPOINTS"

log "Waiting for Zabbix containers (3 services)..."
deadline=$((SECONDS + CONTAINER_TIMEOUT))
while [[ $SECONDS -lt $deadline ]]; do
    sleep 5
    c1=$(count_containers cca-test-ctrl)
    c2=$(count_containers cca-test-worker-1)
    c3=$(count_containers cca-test-worker-2)
    total_containers=$((c1 + c2 + c3))
    log "  Retry: $total_containers/5 containers (ctrl=$c1 w1=$c2 w2=$c3, ${SECONDS}s elapsed)"
    # Java (2) + Zabbix (3) = 5 total
    if [[ "$total_containers" -ge 5 ]]; then break; fi
done
[[ "$total_containers" -ge 5 ]] || fail "Expected 5+ containers, found $total_containers after ${CONTAINER_TIMEOUT}s"
log "Zabbix stack running: $total_containers total containers"

# ---- Step 6: Final status ----
log "Final cluster status:"
curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" || true
echo ""

log "Containers on each node:"
for vm in cca-test-ctrl cca-test-worker-1 cca-test-worker-2; do
    echo "--- $vm ---"
    ssh_vm "$vm" 'sudo nerdctl ps --format "table {{.Names}}\t{{.Image}}\t{{.Status}}"' 2>/dev/null || true
done

log "All checks passed"
exit 0
