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
# Workload selection (each workload has a .cca file and a .workload manifest):
#   --workload airflow,zabbix   deploy specific workloads (comma-separated)
#   --workload all              deploy every workload that has a .workload manifest
#   (default: airflow,zabbix for backward compatibility)
#
# Available workloads are auto-discovered from test/e2e/workloads/*.workload files.
# Each manifest declares: images to pre-pull, expected container count, secrets.
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
#   ./test/e2e/cluster_test.sh                                 # default workloads (airflow,zabbix)
#   ./test/e2e/cluster_test.sh --workload all                  # deploy all available workloads
#   ./test/e2e/cluster_test.sh --workload whoami               # deploy only whoami
#   ./test/e2e/cluster_test.sh --workload airflow,whoami       # deploy airflow + whoami
#   ./test/e2e/cluster_test.sh --list-workloads                # list available workloads and exit
#   ./test/e2e/cluster_test.sh --clear-workloads               # remove all workloads (agents reconcile)
#   ./test/e2e/cluster_test.sh --clean                         # destroy VMs + wipe state (fresh start)
#   ./test/e2e/cluster_test.sh --no-destroy                    # keep VMs running after test
#   ./test/e2e/cluster_test.sh --from-release                  # download latest release binary
#   ./test/e2e/cluster_test.sh --env deploy --from-release     # 3-node deploy cluster
#
# Caddy reverse proxy: after deployment, Caddy on the testbed host
# (192.168.100.43) is configured to proxy traffic to deployed services.
# Each workload manifest declares WORKLOAD_EXPOSE entries mapping
# external_port → service VIP:internal_port.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"
WORKLOADS_DIR="$SCRIPT_DIR/workloads"
DESTROY_ON_EXIT=halt
BINARY_SOURCE=build
CLUSTER_ENV=test
CLEAN_START=false
SELECTED_WORKLOADS=""
LIST_WORKLOADS=false
CLEAR_WORKLOADS_ONLY=false

for arg in "$@"; do
    case "$arg" in
        --no-destroy)        DESTROY_ON_EXIT=none ;;
        --clean)             CLEAN_START=true; DESTROY_ON_EXIT=destroy ;;
        --clear-workloads)   CLEAR_WORKLOADS_ONLY=true ;;
        --from-release)      BINARY_SOURCE=release ;;
        --list-workloads)    LIST_WORKLOADS=true ;;
        --env=*)             CLUSTER_ENV="${arg#--env=}" ;;
        --env)               ;; # handled below with next arg
        --workload=*)        SELECTED_WORKLOADS="${arg#--workload=}" ;;
        --workload)          ;; # handled below with next arg
    esac
done
# Handle two-arg forms: --env <value>, --workload <value>
for ((i=1; i<=$#; i++)); do
    if [[ "${!i}" == "--env" ]]; then
        next=$((i+1))
        if [[ $next -le $# ]]; then
            CLUSTER_ENV="${!next}"
        fi
    fi
    if [[ "${!i}" == "--workload" ]]; then
        next=$((i+1))
        if [[ $next -le $# ]]; then
            SELECTED_WORKLOADS="${!next}"
        fi
    fi
done

# ---- Workload discovery ----

# discover_workloads lists all available workload names by scanning for .workload
# manifest files. Each manifest must have a matching .cca file.
discover_workloads() {
    local manifests
    manifests=$(find "$WORKLOADS_DIR" -maxdepth 1 -name "*.workload" -type f | sort)
    for manifest in $manifests; do
        local name
        name="$(basename "$manifest" .workload)"
        if [[ -f "$WORKLOADS_DIR/${name}.cca" ]]; then
            echo "$name"
        fi
    done
}

if [[ "$LIST_WORKLOADS" == "true" ]]; then
    echo "Available workloads:"
    for workload_name in $(discover_workloads); do
        # shellcheck source=/dev/null
        source "$WORKLOADS_DIR/${workload_name}.workload"
        printf "  %-16s %s\n" "$workload_name" "$WORKLOAD_DESCRIPTION"
    done
    exit 0
fi

# Resolve selected workloads into an ordered array.
ALL_AVAILABLE_WORKLOADS=($(discover_workloads))

if [[ -z "$SELECTED_WORKLOADS" ]]; then
    SELECTED_WORKLOADS="airflow,zabbix"
fi

WORKLOAD_NAMES=()
if [[ "$SELECTED_WORKLOADS" == "all" ]]; then
    WORKLOAD_NAMES=("${ALL_AVAILABLE_WORKLOADS[@]}")
else
    IFS=',' read -ra WORKLOAD_NAMES <<< "$SELECTED_WORKLOADS"
fi

# Validate that every selected workload has a manifest and .cca file.
for workload_name in "${WORKLOAD_NAMES[@]}"; do
    if [[ ! -f "$WORKLOADS_DIR/${workload_name}.workload" ]]; then
        echo "ERROR: No manifest for workload '$workload_name' (expected $WORKLOADS_DIR/${workload_name}.workload)" >&2
        echo "Available: ${ALL_AVAILABLE_WORKLOADS[*]}" >&2
        exit 1
    fi
    if [[ ! -f "$WORKLOADS_DIR/${workload_name}.cca" ]]; then
        echo "ERROR: No .cca file for workload '$workload_name' (expected $WORKLOADS_DIR/${workload_name}.cca)" >&2
        exit 1
    fi
done

log() { echo "==> [$(date +%H:%M:%S)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

log "Selected workloads: ${WORKLOAD_NAMES[*]}"

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

# ---- Clear workloads mode ----
# Wipes desired state from etcd and lets agents reconcile (stop orphaned containers).
# Faster and more principled than brute-force container removal: the agents handle
# cleanup through their normal reconciliation loop. Falls back to nerdctl rm if
# containers don't drain within the timeout.
if [[ "$CLEAR_WORKLOADS_ONLY" == "true" ]]; then
    # Restore Vagrant state if needed.
    if [[ ! -d "$ANSIBLE_DIR/.vagrant" ]] && [[ -d "$VAGRANT_STATE_DIR" ]]; then
        cp -a "$VAGRANT_STATE_DIR" "$ANSIBLE_DIR/.vagrant"
    fi
    log "Clearing all workloads from cluster (${CLUSTER_ENV})..."
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/desired/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/derived/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/placement/ >/dev/null 2>&1 || true"
    log "Desired state cleared — agents will reconcile and stop containers..."
    deadline=$((SECONDS + 60))
    while [[ $SECONDS -lt $deadline ]]; do
        sleep 5
        total_containers=0
        for vm in "${VM_NAMES[@]}"; do
            c=$(count_containers "$vm")
            total_containers=$((total_containers + c))
        done
        log "  Draining: $total_containers containers remaining..."
        if [[ "$total_containers" -eq 0 ]]; then break; fi
    done
    if [[ "$total_containers" -gt 0 ]]; then
        log "WARNING: $total_containers containers still running after 60s, force-removing..."
        for vm in "${VM_NAMES[@]}"; do
            ssh_vm "$vm" "sudo nerdctl rm -f \$(sudo nerdctl ps -aq) 2>/dev/null || true" &
        done
        wait
    fi
    log "Cluster cleared"
    exit 0
fi

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

# Check if cluster is already provisioned by testing for the cca binary and certs.
ALREADY_PROVISIONED=false
if ssh_vm "${VM_PREFIX}-ctrl" "test -f /usr/local/bin/cca" 2>/dev/null && \
   ssh_vm "${VM_PREFIX}-ctrl" "test -f /etc/ccattler/pki/node.pem" 2>/dev/null; then
    ALREADY_PROVISIONED=true
    log "VMs already provisioned — skipping Ansible, cleaning previous workloads..."
    log "Stopping agents to prevent reconciliation races..."
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo systemctl stop cca-agent 2>/dev/null || true" &
    done
    wait
    ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl stop cca-server 2>/dev/null || true"
    log "Removing all containers and pruning name store..."
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo nerdctl rm -f \$(sudo nerdctl ps -aq) 2>/dev/null || true; sudo nerdctl container prune -f 2>/dev/null || true" &
    done
    wait
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS del --prefix /ccattler/ >/dev/null 2>&1 || true"
    log "Truncating old service logs..."
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo truncate -s 0 /var/log/ccattler/server.log /var/log/ccattler/agent.log 2>/dev/null || true" &
    done
    wait
    log "Previous workloads cleaned"
fi

# ---- Step 2: Deploy cluster via Ansible (skip if reusing) ----
if [[ "$ALREADY_PROVISIONED" == "false" ]]; then
    log "Running Ansible deployment (first-time provisioning)..."
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK"
else
    log "Ensuring DNS configuration is up to date..."
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK" --tags dns
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
            test)   case "$vm" in *-ctrl) node_id="cca-test-ctrl" ;; *-worker-1) node_id="cca-test-worker-1" ;; esac ;;
            deploy) case "$vm" in *-ctrl) node_id="cca-deploy-ctrl" ;; *-worker-1) node_id="cca-deploy-worker-1" ;; *-worker-2) node_id="cca-deploy-worker-2" ;; esac ;;
        esac
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
            -i "$ANSIBLE_DIR/.vagrant/machines/$vm/libvirt/private_key" \
            "$ANSIBLE_DIR/cca-linux-amd64" "vagrant@${local_ip}:/tmp/cca-linux-amd64"
        ssh_vm "$vm" "sudo mv /tmp/cca-linux-amd64 /usr/local/bin/cca && sudo chmod +x /usr/local/bin/cca"
        ssh_vm "$vm" "sudo mkdir -p /etc/ccattler && echo '${ETCD_ENDPOINTS}' | sudo tee /etc/ccattler/endpoints > /dev/null"
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
  --advertise-address ${local_ip} \\
  --proxy --proxy-listen ${local_ip}:80 \\
  --debug-listen ${local_ip}:9771 \\
  --secret-api-listen ${local_ip}:9772 \\
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
    # Regenerate server service file with --ca-key for enrollment support.
    ctrl_node_id=""
    case "$CLUSTER_ENV" in
        test)   ctrl_node_id="cca-test-ctrl" ;;
        deploy) ctrl_node_id="cca-deploy-ctrl" ;;
    esac
    cat > /tmp/cca-server.service <<SVCEOF
[Unit]
Description=CCattler Server — controllers + API
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cca server \\
  --listen ${CTRL_IP}:9770 \\
  --dns --dns-listen ${CTRL_IP}:15353 \\
  --cert /etc/ccattler/pki/node.pem \\
  --key /etc/ccattler/pki/node-key.pem \\
  --ca /etc/ccattler/pki/ca.pem \\
  --ca-key /etc/ccattler/pki/ca-key.pem
Restart=on-failure
RestartSec=5
StandardOutput=append:/var/log/ccattler/server.log
StandardError=append:/var/log/ccattler/server.log

[Install]
WantedBy=multi-user.target
SVCEOF
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$ANSIBLE_DIR/.vagrant/machines/${VM_PREFIX}-ctrl/libvirt/private_key" \
        "/tmp/cca-server.service" "vagrant@${CTRL_IP}:/tmp/cca-server.service"
    ssh_vm "${VM_PREFIX}-ctrl" "sudo mv /tmp/cca-server.service /etc/systemd/system/cca-server.service && sudo systemctl daemon-reload"
    rm -f /tmp/cca-server.service
    # Restart services with new binary and service files.
    ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl restart cca-server" || true
    sleep 2
    # Re-join workers to distribute fresh secret master key and endpoints.
    log "Re-joining worker nodes to cluster..."
    for worker_ip in "${WORKER_IPS[@]}"; do
        worker_vm=""
        worker_node_id=""
        case "$worker_ip" in
            "${WORKER_IPS[0]}") worker_vm="${VM_PREFIX}-worker-1" ;;
            "${WORKER_IPS[1]:-}") worker_vm="${VM_PREFIX}-worker-2" ;;
        esac
        case "$CLUSTER_ENV" in
            test)   case "$worker_vm" in *-worker-1) worker_node_id="cca-test-worker-1" ;; esac ;;
            deploy) case "$worker_vm" in *-worker-1) worker_node_id="cca-deploy-worker-1" ;; *-worker-2) worker_node_id="cca-deploy-worker-2" ;; esac ;;
        esac
        JOIN_TOKEN=$(ssh_vm "${VM_PREFIX}-ctrl" "sudo /usr/local/bin/cca token create --node-id $worker_node_id --ttl 5m" 2>/dev/null | grep '^Token:' | awk '{print $2}')
        ssh_vm "$worker_vm" "sudo /usr/local/bin/cca join https://${CTRL_IP}:9770 ${JOIN_TOKEN} --node-id $worker_node_id"
    done
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

# ---- Step 3.5: Verify DNS resolution before image pulls ----
log "Checking DNS configuration and resolution on all VMs..."
for vm in "${VM_NAMES[@]}"; do
    ssh_vm "$vm" "
        echo \"  resolv.conf: \$(cat /etc/resolv.conf | tr '\n' ' ')\"
        echo \"  dnsmasq upstream: \$(cat /etc/dnsmasq.d/upstream.conf 2>/dev/null | tr '\n' ' ')\"
        echo \"  dnsmasq status: \$(systemctl is-active dnsmasq 2>/dev/null)\"
    " 2>/dev/null || true
done

DNS_MAX_ATTEMPTS=5
for vm in "${VM_NAMES[@]}"; do
    dns_ok=false
    for attempt in $(seq 1 "$DNS_MAX_ATTEMPTS"); do
        if ssh_vm "$vm" "dig +short registry-1.docker.io 2>/dev/null | grep -q ." 2>/dev/null; then
            dns_ok=true
            break
        fi
        if [[ "$attempt" -eq 1 ]]; then
            log "DNS not resolving on $vm — restarting dnsmasq..."
            ssh_vm "$vm" "sudo systemctl restart dnsmasq"
        fi
        log "  DNS retry $attempt/$DNS_MAX_ATTEMPTS on $vm..."
        sleep 3
    done
    if [[ "$dns_ok" != "true" ]]; then
        log "DNS debug for $vm:"
        ssh_vm "$vm" "dig registry-1.docker.io 2>&1 | tail -5" 2>/dev/null || true
        fail "DNS resolution failed on $vm after $DNS_MAX_ATTEMPTS attempts"
    fi
done
log "DNS resolution OK"

# ---- Step 3.6: Pre-pull container images for selected workloads ----
# Collect unique images from all selected workload manifests.
declare -A UNIQUE_IMAGES
for workload_name in "${WORKLOAD_NAMES[@]}"; do
    # shellcheck source=/dev/null
    source "$WORKLOADS_DIR/${workload_name}.workload"
    for image in "${WORKLOAD_IMAGES[@]}"; do
        UNIQUE_IMAGES["$image"]=1
    done
done

PULL_MAX_ATTEMPTS=3
log "Pre-pulling ${#UNIQUE_IMAGES[@]} images for ${#WORKLOAD_NAMES[@]} workloads..."
pull_image_with_retry() {
    local vm="$1" image="$2"
    local pull_output
    for attempt in $(seq 1 "$PULL_MAX_ATTEMPTS"); do
        if pull_output=$(ssh_vm "$vm" "sudo nerdctl pull $image" 2>&1); then
            return 0
        fi
        log "  Pull retry $attempt/$PULL_MAX_ATTEMPTS: $image on $vm — ${pull_output##*$'\n'}"
        sleep 5
    done
    log "WARNING: failed to pull $image on $vm after $PULL_MAX_ATTEMPTS attempts"
    log "  Last error: $pull_output"
    return 1
}
for vm in "${VM_NAMES[@]}"; do
    for image in "${!UNIQUE_IMAGES[@]}"; do
        pull_image_with_retry "$vm" "$image" &
    done
done
log "Waiting for all image pulls to complete..."
wait
log "Image pre-pull complete"

# ---- Step 4: Deploy selected workloads ----

CUMULATIVE_EXPECTED_CONTAINERS=0

for workload_name in "${WORKLOAD_NAMES[@]}"; do
    log "--- Deploying workload: $workload_name ---"

    # Source the manifest to get WORKLOAD_* variables.
    # shellcheck source=/dev/null
    source "$WORKLOADS_DIR/${workload_name}.workload"

    # Create secrets if the workload declares any.
    if [[ ${#WORKLOAD_SECRETS[@]} -gt 0 ]]; then
        log "Creating secrets for $workload_name..."
        for secret_entry in "${WORKLOAD_SECRETS[@]}"; do
            secret_name="${secret_entry%%=*}"
            secret_value="${secret_entry#*=}"
            ssh_vm "${VM_PREFIX}-ctrl" "sudo /usr/local/bin/cca secret set $secret_name $secret_value"
        done
    fi

    # Copy and apply the .cca file.
    log "Applying $workload_name workload..."
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$ANSIBLE_DIR/.vagrant/machines/${VM_PREFIX}-ctrl/libvirt/private_key" \
        "$WORKLOADS_DIR/${workload_name}.cca" "vagrant@${CTRL_IP}:/tmp/${workload_name}.cca"

    ssh_vm "${VM_PREFIX}-ctrl" "sudo /usr/local/bin/cca apply /tmp/${workload_name}.cca"

    # Wait for containers to converge.
    CUMULATIVE_EXPECTED_CONTAINERS=$((CUMULATIVE_EXPECTED_CONTAINERS + WORKLOAD_EXPECTED_CONTAINERS))
    log "Waiting for $workload_name containers ($WORKLOAD_EXPECTED_CONTAINERS new, $CUMULATIVE_EXPECTED_CONTAINERS total)..."
    deadline=$((SECONDS + CONTAINER_TIMEOUT))
    total_containers=0
    while [[ $SECONDS -lt $deadline ]]; do
        sleep 5
        total_containers=0
        for vm in "${VM_NAMES[@]}"; do
            c=$(count_containers "$vm")
            total_containers=$((total_containers + c))
        done
        log "  $workload_name: $total_containers/$CUMULATIVE_EXPECTED_CONTAINERS containers (${SECONDS}s elapsed)"
        if [[ "$total_containers" -ge "$CUMULATIVE_EXPECTED_CONTAINERS" ]]; then break; fi
    done
    [[ "$total_containers" -ge "$CUMULATIVE_EXPECTED_CONTAINERS" ]] || fail "Expected $CUMULATIVE_EXPECTED_CONTAINERS+ containers, found $total_containers after ${CONTAINER_TIMEOUT}s (failed at $workload_name)"
    log "$workload_name deployed: $total_containers containers across ${#VM_NAMES[@]} nodes"
done

log "All ${#WORKLOAD_NAMES[@]} workloads deployed: $CUMULATIVE_EXPECTED_CONTAINERS containers"

# ---- Step 4.5: Configure Caddy reverse proxy on testbed host ----
# Only for the deploy environment (long-lived cluster). The test environment is
# ephemeral and halted after tests — no point exposing it.
#
# Caddy runs on the testbed host and proxies external traffic to CCattler
# services inside the libvirt VMs. Each workload manifest declares
# WORKLOAD_EXPOSE entries as "domain:service_name".
#
# VIPs (10.100.x.x) are only routable inside the VMs where agent iptables DNAT
# rules translate them. From the testbed host we proxy through the agent's HTTP
# proxy (port 80 on each VM) which routes by Host header to the correct service.

if [[ "$CLUSTER_ENV" == "deploy" ]]; then
    CADDY_CONFIG=""
    EXPOSE_SUMMARY=""
    TESTBED_HOST_IP="192.168.100.43"

    # Build upstream list: all VM IPs on port 80 (agent HTTP proxy).
    PROXY_UPSTREAMS=""
    for ip in "${ALL_IPS[@]}"; do
        PROXY_UPSTREAMS+=" ${ip}:80"
    done

    for workload_name in "${WORKLOAD_NAMES[@]}"; do
        # shellcheck source=/dev/null
        source "$WORKLOADS_DIR/${workload_name}.workload"
        if [[ -z "${WORKLOAD_EXPOSE+x}" ]] || [[ ${#WORKLOAD_EXPOSE[@]} -eq 0 ]]; then
            continue
        fi
        for expose_entry in "${WORKLOAD_EXPOSE[@]}"; do
            domain="${expose_entry%%:*}"
            service_name="${expose_entry#*:}"
            CADDY_CONFIG+="
${domain} {
    tls internal
    reverse_proxy${PROXY_UPSTREAMS} {
        header_up Host ${service_name}
    }
}
"
            EXPOSE_SUMMARY+="  https://${domain} → ${service_name} (via agent proxy)\n"
        done
    done

    if [[ -n "$CADDY_CONFIG" ]]; then
        log "Configuring Caddy reverse proxy on testbed host..."
        CADDY_FILE="/etc/caddy/Caddyfile"
        echo "$CADDY_CONFIG" | sudo tee "$CADDY_FILE" > /dev/null
        sudo systemctl restart caddy 2>/dev/null || log "WARNING: Caddy not available on host"
        log "Caddy configured — services accessible from:"
        echo -e "$EXPOSE_SUMMARY"
    fi
fi

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
    test)   WORKER1_NODE_ID="cca-test-worker-1" ;;
    deploy) WORKER1_NODE_ID="cca-deploy-worker-1" ;;
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
