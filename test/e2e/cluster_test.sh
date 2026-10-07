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
# Vagrant state is persisted at /tmp/cca-vagrant-state-<env> between runs so that
# CI checkout doesn't lose track of existing VMs.
#
# Usage:
#   ./test/e2e/cluster_test.sh                       # 2-node e2e test, reuse VMs if available
#   ./test/e2e/cluster_test.sh --clean               # destroy VMs + wipe persisted state (fresh start)
#   ./test/e2e/cluster_test.sh --no-destroy          # keep VMs running after test (debugging)
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
CLEAN_START=false

for arg in "$@"; do
    case "$arg" in
        --no-destroy)   DESTROY_ON_EXIT=none ;;
        --clean)        CLEAN_START=true; DESTROY_ON_EXIT=destroy ;;
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

# Persisted Vagrant state directory — survives git checkout and workspace cleanup.
VAGRANT_STATE_DIR="/tmp/cca-vagrant-state-${CLUSTER_ENV}"

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
        log "Destroying VMs and wiping persisted state..."
        cd "$ANSIBLE_DIR"
        VAGRANT_VAGRANTFILE="$VAGRANTFILE" vagrant destroy -f 2>/dev/null || true
        rm -rf "$VAGRANT_STATE_DIR"
    elif [[ "$DESTROY_ON_EXIT" == "halt" ]]; then
        log "Stopping VMs (images and state preserved for next run)..."
        cd "$ANSIBLE_DIR"
        VAGRANT_VAGRANTFILE="$VAGRANTFILE" vagrant halt 2>/dev/null || true
        # Persist Vagrant state so next CI run can reuse the VMs.
        rm -rf "$VAGRANT_STATE_DIR"
        cp -a "$ANSIBLE_DIR/.vagrant" "$VAGRANT_STATE_DIR"
        log "Vagrant state saved to $VAGRANT_STATE_DIR"
    else
        log "Keeping VMs running (--no-destroy). Stop manually:"
        log "  cd $ANSIBLE_DIR && VAGRANT_VAGRANTFILE=$VAGRANTFILE vagrant halt"
        rm -rf "$VAGRANT_STATE_DIR"
        cp -a "$ANSIBLE_DIR/.vagrant" "$VAGRANT_STATE_DIR"
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

# ---- Clean start: wipe everything and start fresh ----
if [[ "$CLEAN_START" == "true" ]]; then
    log "Clean start requested — destroying VMs and wiping state..."
    rm -rf "$VAGRANT_STATE_DIR"
    rm -rf "$ANSIBLE_DIR/.vagrant"
    for vm in "${VM_NAMES[@]}"; do
        stale_domain="ansible_${vm}"
        if sudo virsh dominfo "$stale_domain" >/dev/null 2>&1; then
            sudo virsh destroy "$stale_domain" 2>/dev/null || true
            sudo virsh undefine "$stale_domain" --remove-all-storage 2>/dev/null || true
        fi
    done
fi

# ---- Restore persisted Vagrant state ----
# CI checkout wipes .vagrant/ — restore from the persisted directory if available.
if [[ ! -d "$ANSIBLE_DIR/.vagrant" ]] && [[ -d "$VAGRANT_STATE_DIR" ]]; then
    log "Restoring Vagrant state from $VAGRANT_STATE_DIR"
    cp -a "$VAGRANT_STATE_DIR" "$ANSIBLE_DIR/.vagrant"
fi

# ---- Handle orphaned libvirt domains ----
# If libvirt domains exist but Vagrant state is missing (persisted state was also
# lost), clean them up so vagrant up can recreate from scratch.
for vm in "${VM_NAMES[@]}"; do
    stale_domain="ansible_${vm}"
    vagrant_state="$ANSIBLE_DIR/.vagrant/machines/$vm/libvirt/id"
    if sudo virsh dominfo "$stale_domain" >/dev/null 2>&1 && [[ ! -f "$vagrant_state" ]]; then
        log "Cleaning orphaned libvirt domain: $stale_domain (no Vagrant state)"
        sudo virsh destroy "$stale_domain" 2>/dev/null || true
        sudo virsh undefine "$stale_domain" --remove-all-storage 2>/dev/null || true
    fi
done
# With --destroy, also clean domains that Vagrant knows about.
if [[ "$DESTROY_ON_EXIT" == "destroy" ]]; then
    for vm in "${VM_NAMES[@]}"; do
        stale_domain="ansible_${vm}"
        if sudo virsh dominfo "$stale_domain" >/dev/null 2>&1; then
            log "Removing libvirt domain: $stale_domain (--destroy)"
            sudo virsh destroy "$stale_domain" 2>/dev/null || true
            sudo virsh undefine "$stale_domain" --remove-all-storage 2>/dev/null || true
        fi
    done
    rm -rf "$ANSIBLE_DIR/.vagrant"
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
VAGRANT_VAGRANTFILE="$VAGRANTFILE" vagrant up --no-provision --no-parallel

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
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/observed/node/ >/dev/null 2>&1 || true"
    log "Previous workloads cleaned"
fi

# ---- Step 2: Deploy cluster via Ansible (skip if reusing) ----
if [[ "$ALREADY_PROVISIONED" == "false" ]]; then
    log "Running Ansible deployment (first-time provisioning)..."
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK"
else
    log "Updating binary and service files on existing VMs..."
    for vm in "${VM_NAMES[@]}"; do
        local_ip=""
        node_id=""
        case "$vm" in
            *-ctrl)     local_ip="$CTRL_IP" ;;
            *-worker-1) local_ip="${WORKER_IPS[0]}" ;;
            *-worker-2) local_ip="${WORKER_IPS[1]:-}" ;;
        esac
        case "$CLUSTER_ENV" in
            test)   case "$vm" in *-ctrl) node_id="test-node-1" ;; *-worker-1) node_id="test-node-2" ;; esac ;;
            deploy) case "$vm" in *-ctrl) node_id="deploy-node-1" ;; *-worker-1) node_id="deploy-node-2" ;; *-worker-2) node_id="deploy-node-3" ;; esac ;;
        esac
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
            -i "$ANSIBLE_DIR/.vagrant/machines/$vm/libvirt/private_key" \
            "$ANSIBLE_DIR/cca-linux-amd64" "vagrant@${local_ip}:/tmp/cca-linux-amd64"
        ssh_vm "$vm" "sudo mv /tmp/cca-linux-amd64 /usr/local/bin/cca && sudo chmod +x /usr/local/bin/cca"
        # Regenerate agent service file to pick up new flags (e.g. --debug-listen).
        cat > /tmp/cca-agent-${vm}.service <<SVCEOF
[Unit]
Description=CCattler Agent — node ${node_id}
After=network.target containerd.service
Requires=containerd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/cca agent \\
  --node-id ${node_id} \\
  --store etcd \\
  --endpoints ${ETCD_ENDPOINTS} \\
  --advertise-address ${local_ip} \\
  --proxy --proxy-listen ${local_ip}:80 \\
  --debug-listen ${local_ip}:9771 \\
  --cert /etc/ccattler/pki/node.pem \\
  --key /etc/ccattler/pki/node-key.pem \\
  --ca /etc/ccattler/pki/ca.pem
Restart=on-failure
RestartSec=5
StandardOutput=append:/var/log/ccattler/agent.log
StandardError=append:/var/log/ccattler/agent.log

[Install]
WantedBy=multi-user.target
SVCEOF
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
            -i "$ANSIBLE_DIR/.vagrant/machines/$vm/libvirt/private_key" \
            "/tmp/cca-agent-${vm}.service" "vagrant@${local_ip}:/tmp/cca-agent.service"
        ssh_vm "$vm" "sudo mv /tmp/cca-agent.service /etc/systemd/system/cca-agent.service && sudo systemctl daemon-reload"
        rm -f "/tmp/cca-agent-${vm}.service"
    done
    # Restart services with new binary and service files.
    ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl restart cca-server" || true
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo systemctl restart cca-agent" 2>/dev/null || true
    done
    sleep 3
    log "Binary and service files updated, services restarted"
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

# ---- Step 5.5: Phase 69 — Node Runtime Inspection tests ----
# The CLI commands (node-inspect, exec, images) connect to the control plane
# HTTP API. In the E2E cluster, the API uses mTLS on a non-default address,
# so we test the debug API endpoints directly via curl and test 'agent debug'
# via SSH on the worker node.

log "Testing 'cca agent debug' on worker node (local runtime inspection)..."
AGENT_DEBUG_OUTPUT=$(ssh_vm "${VM_PREFIX}-worker-1" "sudo /usr/local/bin/cca agent debug 2>&1") || fail "cca agent debug failed"
echo "$AGENT_DEBUG_OUTPUT" | grep -q "CONTAINERS" || fail "agent debug missing CONTAINERS section"
echo "$AGENT_DEBUG_OUTPUT" | grep -q "RESOURCE USAGE" || fail "agent debug missing RESOURCE USAGE section"
echo "$AGENT_DEBUG_OUTPUT" | grep -q "IMAGES" || fail "agent debug missing IMAGES section"
log "'cca agent debug' OK — shows containers, resource usage, and images"

log "Discovering agent debug addresses from etcd..."
# The agent publishes its debug address under its node_id, not the VM name.
case "$CLUSTER_ENV" in
    test)   WORKER1_NODE_ID="test-node-2" ;;
    deploy) WORKER1_NODE_ID="deploy-node-2" ;;
esac
WORKER1_DEBUG_ADDR=$(ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS get /ccattler/observed/node/${WORKER1_NODE_ID}/debug_address --print-value-only 2>/dev/null" | tr -d '\r\n')
if [[ -z "$WORKER1_DEBUG_ADDR" ]]; then
    # Wait for the debug address to be published.
    deadline=$((SECONDS + 30))
    while [[ $SECONDS -lt $deadline ]]; do
        sleep 2
        WORKER1_DEBUG_ADDR=$(ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS get /ccattler/observed/node/${WORKER1_NODE_ID}/debug_address --print-value-only 2>/dev/null" | tr -d '\r\n')
        [[ -n "$WORKER1_DEBUG_ADDR" ]] && break
    done
fi
[[ -n "$WORKER1_DEBUG_ADDR" ]] || fail "Worker-1 debug address not found in etcd"
log "Worker-1 debug API at $WORKER1_DEBUG_ADDR"

log "Testing GET /debug/containers on worker-1..."
CONTAINERS_JSON=$(curl -sf "http://${WORKER1_DEBUG_ADDR}/debug/containers" 2>&1) || fail "debug/containers request failed"
echo "$CONTAINERS_JSON" | grep -q '"node_id"' || fail "debug/containers missing node_id"
CONTAINER_COUNT=$(echo "$CONTAINERS_JSON" | grep -o '"count":[0-9]*' | head -1 | cut -d: -f2)
log "GET /debug/containers OK — $CONTAINER_COUNT containers on worker-1"

log "Testing GET /debug/stats on worker-1..."
STATS_JSON=$(curl -sf "http://${WORKER1_DEBUG_ADDR}/debug/stats" 2>&1) || fail "debug/stats request failed"
echo "$STATS_JSON" | grep -q '"workload_count"' || fail "debug/stats missing workload_count"
log "GET /debug/stats OK"

log "Testing GET /debug/images on worker-1..."
IMAGES_JSON=$(curl -sf "http://${WORKER1_DEBUG_ADDR}/debug/images" 2>&1) || fail "debug/images request failed"
echo "$IMAGES_JSON" | grep -q '"images"' || fail "debug/images missing images field"
log "GET /debug/images OK"

log "Testing POST /debug/exec on worker-1..."
INSTANCE_ID=$(ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS get --prefix /ccattler/placement/instance/ 2>/dev/null | grep -v '^/ccattler/' | head -1" | tr -d '\r\n')
if [[ -n "$INSTANCE_ID" ]]; then
    # Find an instance placed on worker-1 (placement values are node IDs, not VM names).
    WORKER1_INSTANCE=$(ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS get --prefix /ccattler/placement/instance/ 2>/dev/null" | \
        awk -v node="$WORKER1_NODE_ID" 'prev && $0 == node {print prev} {prev=$0}' | head -1 | sed 's|.*/||')
    if [[ -n "$WORKER1_INSTANCE" ]]; then
        EXEC_JSON=$(curl -sf -X POST "http://${WORKER1_DEBUG_ADDR}/debug/exec" \
            -H "Content-Type: application/json" \
            -d "{\"instance_id\":\"$WORKER1_INSTANCE\",\"command\":\"hostname\"}" 2>&1) || log "WARNING: exec request returned non-zero"
        echo "$EXEC_JSON" | grep -q '"instance_id"' || fail "debug/exec missing instance_id"
        log "POST /debug/exec OK for instance $WORKER1_INSTANCE"
    else
        log "WARNING: No instance placed on worker-1, skipping exec test"
    fi
else
    log "WARNING: No placements found, skipping exec test"
fi

log "Testing POST /debug/images/pull on worker-1..."
PULL_JSON=$(curl -sf -X POST "http://${WORKER1_DEBUG_ADDR}/debug/images/pull" \
    -H "Content-Type: application/json" \
    -d '{"image":"alpine:3.20"}' 2>&1) || fail "debug/images/pull request failed"
echo "$PULL_JSON" | grep -q '"success":true' || fail "image pull did not succeed"
log "POST /debug/images/pull OK — pulled alpine:3.20 on worker-1"

log "Phase 69 E2E tests passed"

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
