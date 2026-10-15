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
#   - Go toolchain on testbed (for default rsync+build mode)
#   - Or: CCattler binary at deploy/ansible/cca-linux-amd64 (--skip-build)
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
#   ./test/e2e/cluster_test.sh --skip-build                    # use pre-built binary (no go build)
#   ./test/e2e/cluster_test.sh --cross-compile                 # cross-compile locally (old behavior)
#   ./test/e2e/cluster_test.sh --keep-data                     # preserve volumes + etcd between runs
#   ./test/e2e/cluster_test.sh --upgrade                       # deploy latest release, upgrade to local build
#   ./test/e2e/cluster_test.sh --env deploy --from-release     # 3-node deploy cluster
#
# Caddy reverse proxy: after deployment, Caddy on the testbed host
# (192.168.100.43) is configured to proxy traffic to deployed services.
# Each workload manifest declares WORKLOAD_EXPOSE entries mapping
# external_port → service VIP:internal_port.

set -euo pipefail

# Prefer /usr/local/go if available (system Go may be too old for go.mod).
if [[ -x /usr/local/go/bin/go ]]; then
    export PATH="/usr/local/go/bin:$PATH"
fi
# Allow Go to auto-download the toolchain version required by go.mod.
export GOTOOLCHAIN=auto

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"
WORKLOADS_DIR="$SCRIPT_DIR/workloads"
DESTROY_ON_EXIT=halt
BINARY_SOURCE=rsync
CLUSTER_ENV=test
CLEAN_START=false
SELECTED_WORKLOADS=""
LIST_WORKLOADS=false
CLEAR_WORKLOADS_ONLY=false
KEEP_DATA=false
UPGRADE_TEST=false

for arg in "$@"; do
    case "$arg" in
        --no-destroy)        DESTROY_ON_EXIT=none ;;
        --clean)             CLEAN_START=true; DESTROY_ON_EXIT=destroy ;;
        --clear-workloads)   CLEAR_WORKLOADS_ONLY=true ;;
        --from-release)      BINARY_SOURCE=release ;;
        --skip-build)        BINARY_SOURCE=skip ;;
        --cross-compile)     BINARY_SOURCE=cross ;;
        --keep-data)         KEEP_DATA=true ;;
        --upgrade)           UPGRADE_TEST=true ;;
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

# --upgrade implies --keep-data: upgrade tests must not wipe volumes or etcd.
if [[ "$UPGRADE_TEST" == "true" ]]; then
    KEEP_DATA=true
fi

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
ETCD_ENDPOINTS="https://${CTRL_IP}:2379"
ETCD_TLS_FLAGS="--cacert /etc/ccattler/pki/ca.pem --cert /etc/ccattler/pki/node.pem --key /etc/ccattler/pki/node-key.pem"

# Persisted Vagrant state directory — survives git checkout and workspace cleanup.
VAGRANT_STATE_DIR="/tmp/cca-vagrant-state-${CLUSTER_ENV}"

# Each environment gets its own .vagrant directory so test and deploy state don't
# collide. VAGRANT_DOTFILE_PATH separates the machine registrations (SSH keys,
# libvirt domain IDs) per environment.
VAGRANT_DOTFILE="$ANSIBLE_DIR/.vagrant-${CLUSTER_ENV}"
export VAGRANT_DOTFILE_PATH="$VAGRANT_DOTFILE"

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
        if [[ -d "$VAGRANT_DOTFILE" ]]; then
            rm -rf "$VAGRANT_STATE_DIR"
            cp -a "$VAGRANT_DOTFILE" "$VAGRANT_STATE_DIR"
            log "Vagrant state saved to $VAGRANT_STATE_DIR"
        fi
    else
        log "Keeping VMs running (--no-destroy). Stop manually:"
        log "  cd $ANSIBLE_DIR && VAGRANT_VAGRANTFILE=$VAGRANTFILE VAGRANT_DOTFILE_PATH=$VAGRANT_DOTFILE vagrant halt"
        if [[ -d "$VAGRANT_DOTFILE" ]]; then
            rm -rf "$VAGRANT_STATE_DIR"
            cp -a "$VAGRANT_DOTFILE" "$VAGRANT_STATE_DIR"
        fi
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
    local key_path="$VAGRANT_DOTFILE/machines/$vm_name/libvirt/private_key"
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

# Copy a local file to a VM via scp.
scp_to_vm() {
    local vm_name="$1" local_path="$2" remote_path="$3"
    local key_path="$VAGRANT_DOTFILE/machines/$vm_name/libvirt/private_key"
    local ip
    case "$vm_name" in
        *-ctrl)     ip="$CTRL_IP" ;;
        *-worker-1) ip="${WORKER_IPS[0]}" ;;
        *-worker-2) ip="${WORKER_IPS[1]:-}" ;;
        *) fail "Unknown VM: $vm_name" ;;
    esac
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$key_path" "$local_path" "vagrant@${ip}:${remote_path}"
}

# ---- Clear workloads mode ----
# Wipes desired state from etcd and lets agents reconcile (stop orphaned containers).
# Faster and more principled than brute-force container removal: the agents handle
# cleanup through their normal reconciliation loop. Falls back to nerdctl rm if
# containers don't drain within the timeout.
if [[ "$CLEAR_WORKLOADS_ONLY" == "true" ]]; then
    # Restore Vagrant state if needed.
    if [[ ! -d "$VAGRANT_DOTFILE" ]] && [[ -d "$VAGRANT_STATE_DIR" ]]; then
        cp -a "$VAGRANT_STATE_DIR" "$VAGRANT_DOTFILE"
    fi
    log "Clearing all workloads from cluster (${CLUSTER_ENV})..."
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS $ETCD_TLS_FLAGS del --prefix /ccattler/desired/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS $ETCD_TLS_FLAGS del --prefix /ccattler/derived/ >/dev/null 2>&1 || true"
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS $ETCD_TLS_FLAGS del --prefix /ccattler/placement/ >/dev/null 2>&1 || true"
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
    if [[ "$KEEP_DATA" == "false" ]]; then
        log "Pruning container volumes for blank-slate redeploy..."
        for vm in "${VM_NAMES[@]}"; do
            ssh_vm "$vm" "sudo nerdctl volume prune -f 2>/dev/null || true" &
        done
        wait
    else
        log "Keeping container volumes (--keep-data)"
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
    rm -rf "$VAGRANT_DOTFILE"
    for vm in "${VM_NAMES[@]}"; do
        stale_domain="ansible_${vm}"
        if sudo virsh dominfo "$stale_domain" >/dev/null 2>&1; then
            sudo virsh destroy "$stale_domain" 2>/dev/null || true
            sudo virsh undefine "$stale_domain" --remove-all-storage 2>/dev/null || true
        fi
    done
fi

# ---- Restore persisted Vagrant state ----
# CI checkout wipes .vagrant-<env>/ — restore from the persisted directory if available.
if [[ ! -d "$VAGRANT_DOTFILE" ]] && [[ -d "$VAGRANT_STATE_DIR" ]]; then
    log "Restoring Vagrant state from $VAGRANT_STATE_DIR"
    cp -a "$VAGRANT_STATE_DIR" "$VAGRANT_DOTFILE"
fi

# ---- Handle orphaned libvirt domains ----
# If libvirt domains exist but Vagrant state is missing (persisted state was also
# lost), clean them up so vagrant up can recreate from scratch.
for vm in "${VM_NAMES[@]}"; do
    stale_domain="ansible_${vm}"
    vagrant_state="$VAGRANT_DOTFILE/machines/$vm/libvirt/id"
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
    rm -rf "$VAGRANT_DOTFILE"
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
elif [[ "$BINARY_SOURCE" == "skip" ]]; then
    if [[ ! -f "$ANSIBLE_DIR/cca-linux-amd64" ]]; then
        fail "No binary at $ANSIBLE_DIR/cca-linux-amd64 — build locally first"
    fi
    log "Using pre-built binary: $(ls -lh "$ANSIBLE_DIR/cca-linux-amd64" | awk '{print $5}')"
elif [[ "$BINARY_SOURCE" == "cross" ]]; then
    log "Cross-compiling cca binary for linux/amd64..."
    (cd "$REPO_DIR" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$ANSIBLE_DIR/cca-linux-amd64" ./cmd/cca/)
    log "Binary built: $(ls -lh "$ANSIBLE_DIR/cca-linux-amd64" | awk '{print $5}')"
else
    # Default: build from local source tree on the testbed.
    # Assumes the repo has been rsynced to $REPO_DIR on this host before running.
    # From a remote workstation: rsync -az --exclude='.git' repo/ testbed:~/ccattler/
    # Then: ssh testbed 'cd ~/ccattler && test/e2e/cluster_test.sh'
    log "Building cca binary from local source tree..."
    TESTBED_GO="${TESTBED_GO:-/usr/local/go/bin/go}"
    (cd "$REPO_DIR" && CGO_ENABLED=0 "${TESTBED_GO}" build -o "$ANSIBLE_DIR/cca-linux-amd64" ./cmd/cca/)
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

    # Ensure etcd endpoints file uses https:// and etcd runs with mTLS.
    # On clusters provisioned before M110, etcd used plain HTTP — upgrade in-place.
    log "Ensuring etcd mTLS configuration..."
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "echo 'https://${CTRL_IP}:2379' | sudo tee /etc/ccattler/endpoints > /dev/null" &
    done
    wait
    ETCD_NEEDS_RESTART=false
    if ssh_vm "${VM_PREFIX}-ctrl" "grep -q 'client-cert-auth' /etc/systemd/system/etcd.service 2>/dev/null"; then
        log "etcd already configured for mTLS"
    else
        log "Upgrading etcd to mTLS..."
        ETCD_CERT_DIR="/etc/ccattler/pki"
        ssh_vm "${VM_PREFIX}-ctrl" "sudo tee /etc/systemd/system/etcd.service > /dev/null" <<ETCDSVC
[Unit]
Description=etcd — CCattler fact store
After=network.target

[Service]
Type=notify
User=etcd
Group=etcd
ExecStart=/usr/local/bin/etcd \\
  --name ${VM_PREFIX}-ctrl \\
  --data-dir /var/lib/etcd \\
  --listen-client-urls https://${CTRL_IP}:2379,https://127.0.0.1:2379 \\
  --advertise-client-urls https://${CTRL_IP}:2379 \\
  --listen-peer-urls https://${CTRL_IP}:2380 \\
  --client-cert-auth=true \\
  --cert-file=${ETCD_CERT_DIR}/node.pem \\
  --key-file=${ETCD_CERT_DIR}/node-key.pem \\
  --trusted-ca-file=${ETCD_CERT_DIR}/ca.pem \\
  --peer-cert-file=${ETCD_CERT_DIR}/node.pem \\
  --peer-key-file=${ETCD_CERT_DIR}/node-key.pem \\
  --peer-trusted-ca-file=${ETCD_CERT_DIR}/ca.pem \\
  --peer-client-cert-auth=true
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
ETCDSVC
        ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl daemon-reload"
        ETCD_NEEDS_RESTART=true
    fi
    if [[ "$ETCD_NEEDS_RESTART" == "true" ]]; then
        log "WARNING: Wiping etcd data directory — existing cluster state will be lost"
        log "  Reason: etcd listen URLs changed from http:// to https:// (mTLS upgrade)"
        log "  All workload state will be redeployed from .cca files"
        ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl stop etcd; sudo rm -rf /var/lib/etcd"
        # Create etcd user, data dir, and grant cert access.
        ssh_vm "${VM_PREFIX}-ctrl" "sudo useradd --system --shell /usr/sbin/nologin --no-create-home etcd 2>/dev/null || true"
        ssh_vm "${VM_PREFIX}-ctrl" "sudo mkdir -p /var/lib/etcd && sudo chown etcd:etcd /var/lib/etcd && sudo chmod 0700 /var/lib/etcd"
        ssh_vm "${VM_PREFIX}-ctrl" "sudo chgrp etcd /etc/ccattler/pki/node.pem /etc/ccattler/pki/node-key.pem /etc/ccattler/pki/ca.pem && sudo chmod 0640 /etc/ccattler/pki/node.pem /etc/ccattler/pki/node-key.pem /etc/ccattler/pki/ca.pem"
        # Remove any systemd drop-in overrides from prior installs.
        ssh_vm "${VM_PREFIX}-ctrl" "sudo rm -rf /etc/systemd/system/etcd.service.d 2>/dev/null || true; sudo systemctl daemon-reload"
        ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl start etcd"
        sleep 3
        ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS $ETCD_TLS_FLAGS endpoint health" || fail "etcd unhealthy after mTLS upgrade"
        log "etcd restarted with mTLS — data directory recreated"
    fi

    if [[ "$KEEP_DATA" == "false" ]]; then
        log "Removing all containers, volumes, and pruning name store..."
        for vm in "${VM_NAMES[@]}"; do
            ssh_vm "$vm" "sudo nerdctl rm -f \$(sudo nerdctl ps -aq) 2>/dev/null || true; sudo nerdctl container prune -f 2>/dev/null || true; sudo nerdctl volume prune -f 2>/dev/null || true" &
        done
        wait
        if ! ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS $ETCD_TLS_FLAGS del --prefix /ccattler/ >/dev/null 2>&1"; then
            log "WARNING: etcdctl del failed (expected if data dir was just wiped)"
        fi
    else
        log "Keeping existing data (--keep-data) — stopping containers only..."
        for vm in "${VM_NAMES[@]}"; do
            ssh_vm "$vm" "sudo nerdctl stop \$(sudo nerdctl ps -aq) 2>/dev/null || true" &
        done
        wait
    fi
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
            -i "$VAGRANT_DOTFILE/machines/$vm/libvirt/private_key" \
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
  --ca /etc/ccattler/pki/ca.pem \\
  --etcd-cert /etc/ccattler/pki/node.pem \\
  --etcd-key /etc/ccattler/pki/node-key.pem \\
  --etcd-ca /etc/ccattler/pki/ca.pem
Restart=on-failure
RestartSec=5
StandardOutput=append:/var/log/ccattler/agent.log
StandardError=append:/var/log/ccattler/agent.log

[Install]
WantedBy=multi-user.target
SVCEOF
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
            -i "$VAGRANT_DOTFILE/machines/$vm/libvirt/private_key" \
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
  --ca-key /etc/ccattler/pki/ca-key.pem \\
  --etcd-cert /etc/ccattler/pki/node.pem \\
  --etcd-key /etc/ccattler/pki/node-key.pem \\
  --etcd-ca /etc/ccattler/pki/ca.pem
Restart=on-failure
RestartSec=5
StandardOutput=append:/var/log/ccattler/server.log
StandardError=append:/var/log/ccattler/server.log

[Install]
WantedBy=multi-user.target
SVCEOF
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$VAGRANT_DOTFILE/machines/${VM_PREFIX}-ctrl/libvirt/private_key" \
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
ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS $ETCD_TLS_FLAGS endpoint health" || fail "etcd unhealthy"

log "Verifying etcd rejects unauthenticated access (T1 mTLS enforcement)..."
if ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS endpoint health 2>&1" | grep -q "is healthy"; then
    fail "etcd accepted unauthenticated connection — mTLS not enforced (T1 CRITICAL)"
fi
log "etcd correctly rejects connections without client certificate"

log "Verifying etcd rejects plain HTTP access..."
ETCD_HTTP_ENDPOINT="http://${CTRL_IP}:2379"
if ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_HTTP_ENDPOINT endpoint health 2>&1" | grep -q "is healthy"; then
    fail "etcd accepted plain HTTP connection — TLS not enforced (T1 CRITICAL)"
fi
log "etcd correctly rejects plain HTTP — all access requires mTLS"

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
        log "Registered nodes (from API):"
        curl -s $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null | jq -r '.nodes[] | "  \(.id): \(.state)"' || true
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

PULL_MAX_ATTEMPTS=5
PULL_RETRY_DELAY=15
IMAGE_CACHE_DIR="/tmp/cca-image-cache"
mkdir -p "$IMAGE_CACHE_DIR"

log "Pre-pulling ${#UNIQUE_IMAGES[@]} images for ${#WORKLOAD_NAMES[@]} workloads..."

# Strategy: pull images on the testbed host (fast internet), save as tarballs,
# load into VMs over the local network. Avoids Docker Hub rate limits and slow
# pulls from inside libvirt VMs.
ensure_image_cached() {
    local image="$1"
    local safe_name="${image//\//_}"
    safe_name="${safe_name//:/_}"
    local tar_path="${IMAGE_CACHE_DIR}/${safe_name}.tar"
    if [[ -f "$tar_path" ]]; then
        log "  $image — cached tarball exists"
        return 0
    fi
    if ! sudo nerdctl image inspect "$image" >/dev/null 2>&1; then
        log "  $image — pulling on host..."
        local pull_output
        for attempt in $(seq 1 "$PULL_MAX_ATTEMPTS"); do
            if pull_output=$(sudo nerdctl pull "$image" 2>&1); then
                break
            fi
            log "  Pull retry $attempt/$PULL_MAX_ATTEMPTS: $image — ${pull_output##*$'\n'}"
            sleep "$PULL_RETRY_DELAY"
            if [[ "$attempt" -eq "$PULL_MAX_ATTEMPTS" ]]; then
                log "WARNING: failed to pull $image on host after $PULL_MAX_ATTEMPTS attempts"
                return 1
            fi
        done
    fi
    log "  $image — saving tarball..."
    sudo nerdctl save -o "$tar_path" "$image" 2>&1 || { log "WARNING: failed to save $image"; return 1; }
}

load_images_on_vm() {
    local vm="$1"
    for image in "${!UNIQUE_IMAGES[@]}"; do
        if ssh_vm "$vm" "sudo nerdctl image inspect $image >/dev/null 2>&1"; then
            log "  $image already cached on $vm"
            continue
        fi
        local safe_name="${image//\//_}"
        safe_name="${safe_name//:/_}"
        local tar_path="${IMAGE_CACHE_DIR}/${safe_name}.tar"
        if [[ ! -f "$tar_path" ]]; then
            log "WARNING: no tarball for $image, pulling directly on $vm..."
            ssh_vm "$vm" "sudo nerdctl pull $image" 2>/dev/null || true
            continue
        fi
        local vm_ip
        case "$vm" in
            *-ctrl)     vm_ip="$CTRL_IP" ;;
            *-worker-1) vm_ip="${WORKER_IPS[0]}" ;;
            *-worker-2) vm_ip="${WORKER_IPS[1]:-}" ;;
        esac
        local key_path="$VAGRANT_DOTFILE/machines/$vm/libvirt/private_key"
        log "  Loading $image on $vm..."
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
            -i "$key_path" "$tar_path" "vagrant@${vm_ip}:/tmp/${safe_name}.tar"
        ssh_vm "$vm" "sudo nerdctl load -i /tmp/${safe_name}.tar && rm -f /tmp/${safe_name}.tar" 2>/dev/null
    done
}

# Pull and cache images on host.
for image in "${!UNIQUE_IMAGES[@]}"; do
    ensure_image_cached "$image"
done
log "Host image cache ready"

# Load images into VMs in parallel (LAN transfer, fast).
for vm in "${VM_NAMES[@]}"; do
    load_images_on_vm "$vm" &
done
log "Loading images into VMs..."
wait
log "Image loading complete"

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
        -i "$VAGRANT_DOTFILE/machines/${VM_PREFIX}-ctrl/libvirt/private_key" \
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
    tls internal {
        on_demand
    }
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
        # Global options: disable auto HTTP→HTTPS redirect (avoids port 80
        # conflict) and skip installing the internal CA into the system trust
        # store (caddy user has no sudo).
        {
            echo "{"
            echo "    auto_https disable_redirects"
            echo "    skip_install_trust"
            echo "}"
            echo "$CADDY_CONFIG"
        } | sudo tee "$CADDY_FILE" > /dev/null
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
WORKER1_DEBUG_ADDR=$(curl -sf $CURL_TLS "${CCA_API}/api/state?prefix=observed/node/${WORKER1_NODE_ID}/debug_address" 2>/dev/null | jq -r '.[0].value // empty' | tr -d '\r\n')
if [[ -z "$WORKER1_DEBUG_ADDR" ]]; then
    deadline=$((SECONDS + 30))
    while [[ $SECONDS -lt $deadline ]]; do
        sleep 2
        WORKER1_DEBUG_ADDR=$(curl -sf $CURL_TLS "${CCA_API}/api/state?prefix=observed/node/${WORKER1_NODE_ID}/debug_address" 2>/dev/null | jq -r '.[0].value // empty' | tr -d '\r\n')
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
STATUS_JSON=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null)
INSTANCE_ID=$(echo "$STATUS_JSON" | jq -r '.instances[0].id // empty')
if [[ -n "$INSTANCE_ID" ]]; then
    WORKER1_INSTANCE=$(echo "$STATUS_JSON" | jq -r --arg node "$WORKER1_NODE_ID" '[.instances[] | select(.node == $node)][0].id // empty')
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

# ---- Step 5.6: VIP data plane validation ----
# Verify that the agent programs iptables DNAT rules for service VIPs. The chain
# CCA_SERVICES should contain jump rules to per-service chains, and each per-service
# chain should contain DNAT rules pointing to host-accessible backends.

log "Validating VIP data plane (iptables DNAT rules)..."

# Wait for networking (endpoints + VIPs) via the status API.
ENDPOINT_DEADLINE=$((SECONDS + 60))
ENDPOINT_COUNT=0
while [[ $SECONDS -lt $ENDPOINT_DEADLINE ]]; do
    NET_JSON=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null)
    ENDPOINT_COUNT=$(echo "$NET_JSON" | jq '[.instances[] | select(.state == "running" and .ip != null and .ip != "")] | length' 2>/dev/null || echo "0")
    if [[ "$ENDPOINT_COUNT" -gt 0 ]]; then
        break
    fi
    sleep 3
done
log "  Running instances with endpoints: $ENDPOINT_COUNT"

VIP_COUNT=$(echo "$NET_JSON" | jq '.networking | length' 2>/dev/null || echo "0")
log "  VIP/networking entries: $VIP_COUNT"

# Wait for iptables rules to appear on the ctrl node (gives data plane reconciler time).
IPTABLES_DEADLINE=$((SECONDS + 30))
DNAT_RULE_COUNT=0
while [[ $SECONDS -lt $IPTABLES_DEADLINE ]]; do
    DNAT_RULE_COUNT=$(ssh_vm "${VM_PREFIX}-ctrl" "sudo iptables -t nat -L CCA_SERVICES -n 2>/dev/null | grep -c 'CCA_SVC_'" || echo "0")
    if [[ "$DNAT_RULE_COUNT" -gt 0 ]]; then
        break
    fi
    sleep 3
done
log "  CCA_SERVICES jump rules on ctrl: $DNAT_RULE_COUNT"

if [[ "$DNAT_RULE_COUNT" -gt 0 ]]; then
    # Show the per-service DNAT rules for debugging.
    ssh_vm "${VM_PREFIX}-ctrl" "sudo iptables -t nat -L CCA_SERVICES -n 2>/dev/null" | head -20
    log "VIP data plane OK — DNAT rules programmed"
else
    log "WARNING: CCA_SERVICES chain has no jump rules — cross-node VIP routing may not work"
    log "  Dumping diagnostics..."
    ssh_vm "${VM_PREFIX}-ctrl" "sudo iptables -t nat -L -n 2>/dev/null | head -40" || true
    log "  Networking from API:"
    curl -s $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null | jq '.networking' || true
    log "  Nodes from API:"
    curl -s $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null | jq '.nodes' || true
fi

# ---- Step 6: Final status ----
log "Final cluster status:"
curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" || true
echo ""

log "Instances per node (from API):"
FINAL_STATUS=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null)
echo "$FINAL_STATUS" | jq -r '.nodes[] | "--- \(.id) (\(.state), \(.instances) instances) ---"' || true
echo "$FINAL_STATUS" | jq -r '.instances[] | "  \(.id)\t\(.service)\t\(.state)\t\(.node)"' || true

# ---- Step 7: Upgrade test (--upgrade only) ----
if [[ "$UPGRADE_TEST" == "true" ]]; then
    log "=== UPGRADE TEST: switching from release binary to local build ==="

    # Build current binary from source.
    log "Building local binary..."
    TESTBED_GO="${TESTBED_GO:-/usr/local/go/bin/go}"
    (cd "$REPO_DIR" && CGO_ENABLED=0 "${TESTBED_GO}" build -o "$ANSIBLE_DIR/cca-linux-amd64" ./cmd/cca/)
    log "Local binary built: $(ls -lh "$ANSIBLE_DIR/cca-linux-amd64" | awk '{print $5}')"

    # Record pre-upgrade state for comparison.
    PRE_UPGRADE_INSTANCES=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null | jq -r '[.instances[] | .id] | sort | join(",")' || echo "")
    PRE_UPGRADE_COUNT=$(echo "$PRE_UPGRADE_INSTANCES" | tr ',' '\n' | wc -l | tr -d ' ')
    log "Pre-upgrade: $PRE_UPGRADE_COUNT instances"

    # Stop agents first, then server — reverse startup order.
    log "Stopping agents..."
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo systemctl stop cca-agent 2>/dev/null || true" &
    done
    wait
    ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl stop cca-server 2>/dev/null || true"

    # Swap binary on all nodes.
    log "Deploying new binary to all nodes..."
    for vm in "${VM_NAMES[@]}"; do
        scp_to_vm "$vm" "$ANSIBLE_DIR/cca-linux-amd64" "/tmp/cca"
        ssh_vm "$vm" "sudo mv /tmp/cca /usr/local/bin/cca && sudo chmod 0755 /usr/local/bin/cca"
    done

    # Restart server then agents — same startup order as initial deploy.
    log "Starting server with new binary..."
    ssh_vm "${VM_PREFIX}-ctrl" "sudo systemctl start cca-server"
    sleep 3
    log "Starting agents with new binary..."
    for vm in "${VM_NAMES[@]}"; do
        ssh_vm "$vm" "sudo systemctl start cca-agent 2>/dev/null || true"
    done

    # Wait for all instances to return to running state.
    log "Waiting for workloads to recover after upgrade..."
    deadline=$((SECONDS + 120))
    while [[ $SECONDS -lt $deadline ]]; do
        sleep 5
        total_containers=0
        for vm in "${VM_NAMES[@]}"; do
            c=$(count_containers "$vm")
            total_containers=$((total_containers + c))
        done
        log "  Upgrade recovery: $total_containers/$CUMULATIVE_EXPECTED_CONTAINERS containers"
        if [[ "$total_containers" -ge "$CUMULATIVE_EXPECTED_CONTAINERS" ]]; then break; fi
    done
    [[ "$total_containers" -ge "$CUMULATIVE_EXPECTED_CONTAINERS" ]] || fail "Upgrade: expected $CUMULATIVE_EXPECTED_CONTAINERS+ containers, found $total_containers"

    # Verify API is healthy with new binary.
    log "Verifying API health after upgrade..."
    curl -sf $CURL_TLS "${CCA_API}/status" > /dev/null || fail "Upgrade: API unreachable after binary swap"

    # Verify instance IDs survived the upgrade (data preserved).
    POST_UPGRADE_INSTANCES=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null | jq -r '[.instances[] | .id] | sort | join(",")' || echo "")
    POST_UPGRADE_COUNT=$(echo "$POST_UPGRADE_INSTANCES" | tr ',' '\n' | wc -l | tr -d ' ')
    log "Post-upgrade: $POST_UPGRADE_COUNT instances"
    if [[ "$PRE_UPGRADE_INSTANCES" != "$POST_UPGRADE_INSTANCES" ]]; then
        log "WARNING: Instance IDs changed across upgrade"
        log "  Before: $PRE_UPGRADE_INSTANCES"
        log "  After:  $POST_UPGRADE_INSTANCES"
    else
        log "Instance IDs preserved across upgrade"
    fi

    # Verify etcd still healthy.
    ssh_vm "${VM_PREFIX}-ctrl" "etcdctl --endpoints=$ETCD_ENDPOINTS $ETCD_TLS_FLAGS endpoint health" || fail "Upgrade: etcd unhealthy after binary swap"

    log "=== UPGRADE TEST PASSED ==="
fi

log "All checks passed"
exit 0
