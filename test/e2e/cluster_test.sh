#!/usr/bin/env bash
# cluster_test.sh — End-to-end CCattler cluster test
#
# Provisions libvirt VMs, deploys a full CCattler cluster via Ansible,
# applies test workloads, verifies they converge, and tears down.
#
# Exit codes: 0 = pass, 1 = fail
#
# Two environments (different VM names/IPs so they can coexist):
#   --env test   (default): 2 VMs on 192.168.122.x, ephemeral (destroyed after)
#   --env deploy:           3 VMs on 192.168.124.x, long-lived (--no-destroy implied)
#
# Prerequisites:
#   - Linux host with libvirt/KVM, Vagrant (vagrant-libvirt), Ansible
#   - CCattler binary at deploy/ansible/cca-linux-amd64
#
# VM lifecycle: VMs are stopped (not destroyed) after each run, preserving cached
# container images. On the next run, stopped VMs boot in seconds and skip Ansible
# provisioning. The binary is updated and services restarted automatically.
#
# Usage:
#   ./test/e2e/cluster_test.sh                       # 2-node e2e test, build from source
#   ./test/e2e/cluster_test.sh --destroy             # destroy VMs after run (fresh next time)
#   ./test/e2e/cluster_test.sh --no-destroy          # keep VMs running for debugging
#   ./test/e2e/cluster_test.sh --from-release        # download latest release binary
#   ./test/e2e/cluster_test.sh --env deploy --from-release  # 3-node deploy cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"
WORKLOADS_DIR="$SCRIPT_DIR/workloads"
DESTROY_ON_EXIT=halt
BINARY_SOURCE=build
CLUSTER_ENV=test

for arg in "$@"; do
    case "$arg" in
        --no-destroy)   DESTROY_ON_EXIT=none ;;
        --destroy)      DESTROY_ON_EXIT=destroy ;;
        --from-release) BINARY_SOURCE=release ;;
        --env=*)        CLUSTER_ENV="${arg#--env=}" ;;
        --env)          ;; # handled below with next arg
    esac
done
# Handle --env <value> (two-arg form)
for ((i=1; i<=$#; i++)); do
    if [[ "${!i}" == "--env" ]]; then
        next=$((i+1))
        if [[ $next -le $# ]]; then
            CLUSTER_ENV="${!next}"
        fi
    fi
done

# Environment-specific configuration
case "$CLUSTER_ENV" in
    test)
        VAGRANTFILE="test-Vagrantfile"
        INVENTORY="test-inventory.ini"
        PLAYBOOK="test-deploy.yml"
        VM_PREFIX="cca-test"
        CTRL_IP="192.168.122.10"
        WORKER_IPS=("192.168.122.20")
        VM_NAMES=("${VM_PREFIX}-ctrl" "${VM_PREFIX}-worker-1")
        EXPECTED_NODES=2
        ;;
    deploy)
        VAGRANTFILE="deploy-Vagrantfile"
        INVENTORY="deploy-inventory.ini"
        PLAYBOOK="deploy-deploy.yml"
        VM_PREFIX="cca-deploy"
        CTRL_IP="192.168.124.10"
        WORKER_IPS=("192.168.124.20" "192.168.124.30")
        VM_NAMES=("${VM_PREFIX}-ctrl" "${VM_PREFIX}-worker-1" "${VM_PREFIX}-worker-2")
        EXPECTED_NODES=3
        DESTROY_ON_EXIT=false  # deploy cluster is long-lived
        ;;
    *)
        echo "Unknown --env: $CLUSTER_ENV (must be 'test' or 'deploy')" >&2
        exit 1
        ;;
esac

ALL_IPS=("$CTRL_IP" "${WORKER_IPS[@]}")
ETCD_ENDPOINTS="http://${CTRL_IP}:2379"

# Maximum seconds to wait for convergence checks.
CONVERGE_TIMEOUT=120
CONTAINER_TIMEOUT=180

log() { echo "==> [$(date +%H:%M:%S)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

collect_vm_logs() {
    log "Collecting VM logs before teardown..."
    for vm in "${VM_NAMES[@]}"; do
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
    if [[ "$DESTROY_ON_EXIT" == "destroy" ]]; then
        log "Destroying VMs..."
        cd "$ANSIBLE_DIR"
        VAGRANT_VAGRANTFILE="$VAGRANTFILE" vagrant destroy -f 2>/dev/null || true
    elif [[ "$DESTROY_ON_EXIT" == "halt" ]]; then
        log "Stopping VMs (images and state preserved for next run)..."
        cd "$ANSIBLE_DIR"
        VAGRANT_VAGRANTFILE="$VAGRANTFILE" vagrant halt 2>/dev/null || true
    else
        log "Keeping VMs running (--no-destroy). Stop manually:"
        log "  cd $ANSIBLE_DIR && VAGRANT_VAGRANTFILE=$VAGRANTFILE vagrant halt"
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
        *-ctrl)     ip="$CTRL_IP" ;;
        *-worker-1) ip="${WORKER_IPS[0]}" ;;
        *-worker-2) ip="${WORKER_IPS[1]:-}" ;;
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

# ---- Cleanup stale VMs (only with --destroy, forces fresh provisioning) ----
if [[ "$DESTROY_ON_EXIT" == "destroy" ]]; then
    for vm in "${VM_NAMES[@]}"; do
        stale_domain="ansible_${vm}"
        if sudo virsh dominfo "$stale_domain" >/dev/null 2>&1; then
            log "Removing stale libvirt domain: $stale_domain"
            sudo virsh destroy "$stale_domain" 2>/dev/null || true
            sudo virsh undefine "$stale_domain" --remove-all-storage 2>/dev/null || true
        fi
    done
fi

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

# ---- Step 1: Start VMs (reuse existing or provision new) ----
cd "$ANSIBLE_DIR"
VAGRANT_VAGRANTFILE="$VAGRANTFILE" vagrant up --no-provision

log "Waiting for SSH on all VMs..."
for ip in "${ALL_IPS[@]}"; do
    wait_for_port "$ip" 22 180 "SSH on $ip"
done

# Check if cluster is already provisioned by testing for the cca binary.
ALREADY_PROVISIONED=false
if ssh_vm "${VM_PREFIX}-ctrl" "test -f /usr/local/bin/cca" 2>/dev/null; then
    ALREADY_PROVISIONED=true
    log "VMs already provisioned — skipping Ansible, cleaning previous workloads..."
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo nerdctl rm -f \$(sudo nerdctl ps -aq) 2>/dev/null || true" &
    done
    wait
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/desired/service/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/effective/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/observed/instance/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/placement/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/derived/ >/dev/null 2>&1 || true"
    log "Previous workloads cleaned"
fi

# ---- Step 2: Deploy cluster via Ansible (skip if reusing) ----
if [[ "$ALREADY_PROVISIONED" == "false" ]]; then
    log "Running Ansible deployment (first-time provisioning)..."
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK"
else
    log "Updating binary on existing VMs..."
    for vm in "${VM_NAMES[@]}"; do
        local_ip=""
        case "$vm" in
            *-ctrl)     local_ip="$CTRL_IP" ;;
            *-worker-1) local_ip="${WORKER_IPS[0]}" ;;
            *-worker-2) local_ip="${WORKER_IPS[1]:-}" ;;
        esac
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
            -i "$ANSIBLE_DIR/.vagrant/machines/$vm/libvirt/private_key" \
            "$ANSIBLE_DIR/cca-linux-amd64" "vagrant@${local_ip}:/tmp/cca-linux-amd64"
        ssh_vm "$vm" "sudo mv /tmp/cca-linux-amd64 /usr/local/bin/cca && sudo chmod +x /usr/local/bin/cca"
    done
    # Restart services with new binary.
    ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl restart cca-server" || true
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo systemctl restart cca-agent" 2>/dev/null || true
    done
    sleep 3
    log "Binary updated and services restarted"
fi

# ---- Step 3: Verify cluster basics ----
log "Verifying etcd health..."
ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS endpoint health" || fail "etcd unhealthy"

log "Fetching cluster TLS certificates for API verification..."
CA_CERT_LOCAL="/tmp/cca-e2e-ca.pem"
CLIENT_CERT_LOCAL="/tmp/cca-e2e-client.pem"
CLIENT_KEY_LOCAL="/tmp/cca-e2e-client-key.pem"
ssh_vm "${VM_PREFIX}-ctrl" "sudo cat /etc/ccattler/pki/ca.pem" > "$CA_CERT_LOCAL"
ssh_vm "${VM_PREFIX}-ctrl" "sudo cat /etc/ccattler/pki/node.pem" > "$CLIENT_CERT_LOCAL"
ssh_vm "${VM_PREFIX}-ctrl" "sudo cat /etc/ccattler/pki/node-key.pem" > "$CLIENT_KEY_LOCAL"
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

log "Verifying all $EXPECTED_NODES nodes registered..."
node_count=$(count_alive_nodes)
log "Initial node count: $node_count"
if [[ "$node_count" -lt "$EXPECTED_NODES" ]]; then
    log "WARNING: Only $node_count/$EXPECTED_NODES nodes registered, waiting..."
    deadline=$((SECONDS + CONVERGE_TIMEOUT))
    while [[ $SECONDS -lt $deadline ]]; do
        sleep 5
        node_count=$(count_alive_nodes)
        log "  Retry: $node_count/$EXPECTED_NODES nodes registered (${SECONDS}s elapsed)"
        if [[ "$node_count" -ge "$EXPECTED_NODES" ]]; then break; fi
    done
    if [[ "$node_count" -lt "$EXPECTED_NODES" ]]; then
        log "Final node count: $node_count"
        log "Final /status response:"
        curl -s $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>&1 || true
        echo ""
        log "etcd keys under /ccattler/observed/node/:"
        ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS get --prefix /ccattler/observed/node/ --keys-only" 2>/dev/null || true
        fail "Only $node_count/$EXPECTED_NODES nodes registered after ${CONVERGE_TIMEOUT}s"
    fi
fi
log "All $EXPECTED_NODES nodes registered and alive"

# ---- Step 3.5: Pre-pull container images on all nodes ----
log "Pre-pulling container images on all nodes..."
IMAGES=("tomcat:11-jre21" "postgres:16" "zabbix/zabbix-server-pgsql:alpine-7.4-latest" "zabbix/zabbix-web-nginx-pgsql:alpine-7.4-latest")
for vm in "${VM_NAMES[@]}"; do
    for image in "${IMAGES[@]}"; do
        ssh_vm "$vm" "sudo nerdctl pull $image" &
    done
done
log "Waiting for all image pulls to complete..."
wait
log "Image pre-pull complete"

# ---- Step 4: Deploy Java test app ----
log "Applying Java test workload..."
scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -i "$ANSIBLE_DIR/.vagrant/machines/${VM_PREFIX}-ctrl/libvirt/private_key" \
    "$WORKLOADS_DIR/java-app.cca" "$WORKLOADS_DIR/index.jsp" "vagrant@${CTRL_IP}:/tmp/"

ssh_vm "${VM_PREFIX}-ctrl" "cd /tmp && /usr/local/bin/cca apply /tmp/java-app.cca --store etcd --endpoints $ETCD_ENDPOINTS"

log "Waiting for Java app containers to start..."
deadline=$((SECONDS + CONTAINER_TIMEOUT))
total_containers=0
while [[ $SECONDS -lt $deadline ]]; do
    sleep 5
    total_containers=0
    for vm in "${VM_NAMES[@]}"; do
        c=$(count_containers "$vm")
        total_containers=$((total_containers + c))
    done
    log "  Retry: $total_containers/2 containers (${SECONDS}s elapsed)"
    if [[ "$total_containers" -ge 2 ]]; then break; fi
done
[[ "$total_containers" -ge 2 ]] || fail "Expected 2+ containers, found $total_containers after ${CONTAINER_TIMEOUT}s"
log "Java app running: $total_containers containers across ${#VM_NAMES[@]} nodes"

# ---- Step 5: Deploy Zabbix stack ----
log "Applying Zabbix test workload..."
scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -i "$ANSIBLE_DIR/.vagrant/machines/${VM_PREFIX}-ctrl/libvirt/private_key" \
    "$WORKLOADS_DIR/zabbix.cca" "vagrant@${CTRL_IP}:/tmp/zabbix.cca"

ssh_vm "${VM_PREFIX}-ctrl" "/usr/local/bin/cca apply /tmp/zabbix.cca --store etcd --endpoints $ETCD_ENDPOINTS"

log "Waiting for Zabbix containers (3 services)..."
deadline=$((SECONDS + CONTAINER_TIMEOUT))
while [[ $SECONDS -lt $deadline ]]; do
    sleep 5
    total_containers=0
    for vm in "${VM_NAMES[@]}"; do
        c=$(count_containers "$vm")
        total_containers=$((total_containers + c))
    done
    log "  Retry: $total_containers/5 containers (${SECONDS}s elapsed)"
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
for vm in "${VM_NAMES[@]}"; do
    echo "--- $vm ---"
    ssh_vm "$vm" 'sudo nerdctl ps --format "table {{.Names}}\t{{.Image}}\t{{.Status}}"' 2>/dev/null || true
done

log "All checks passed"
exit 0
