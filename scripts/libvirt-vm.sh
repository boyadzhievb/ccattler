#!/usr/bin/env bash
# libvirt-vm.sh — Create and manage libvirt/KVM VMs using official Ubuntu cloud images
#
# Replaces Vagrant for CCattler's test and deploy clusters. Uses official
# Ubuntu cloud images from Canonical (GPG-signed, SHA256-verified — same
# images used by AWS, GCP, Azure) with cloud-init for provisioning.
#
# Usage:
#   libvirt-vm.sh create  --env <deploy|vagrant>  [--clean]
#   libvirt-vm.sh destroy --env <deploy|vagrant>
#   libvirt-vm.sh stop    --env <deploy|vagrant>
#   libvirt-vm.sh start   --env <deploy|vagrant>
#   libvirt-vm.sh status  --env <deploy|vagrant>
#
# State directory: ~/.local/share/cca-vms-<env>/
#   - id_ed25519 / id_ed25519.pub — SSH key pair for VM access
#   - <vm-name>/seed.iso           — cloud-init seed ISO
#
# Cloud images cached at: ~/.local/share/cca-vms-images/
#
# SSH user: cca (passwordless sudo)
#
# Prerequisites: libvirt + qemu-kvm, cloud-image-utils (cloud-localds), wget

set -euo pipefail

CLOUD_IMAGE_URL="https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
CLOUD_IMAGE_SHA256_URL="https://cloud-images.ubuntu.com/noble/current/SHA256SUMS"
CLOUD_IMAGE_FILENAME="noble-server-cloudimg-amd64.img"
IMAGE_CACHE_DIR="${HOME}/.local/share/cca-vms-images"

log() { echo "==> [$(date +%H:%M:%S)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

ACTION=""
CLUSTER_ENV=""
CLEAN_START=false

for arg in "$@"; do
    case "$arg" in
        create|destroy|stop|start|status) ACTION="$arg" ;;
        --env=*)   CLUSTER_ENV="${arg#--env=}" ;;
        --clean)   CLEAN_START=true ;;
    esac
done
for ((i=1; i<=$#; i++)); do
    if [[ "${!i}" == "--env" ]]; then
        next=$((i+1))
        if [[ $next -le $# ]]; then
            CLUSTER_ENV="${!next}"
        fi
    fi
done

[[ -n "$ACTION" ]]      || fail "Usage: $0 <create|destroy|stop|start|status> --env <deploy|vagrant>"
[[ -n "$CLUSTER_ENV" ]] || fail "Usage: $0 $ACTION --env <deploy|vagrant>"

# Environment-specific VM definitions.
declare -a VM_NAMES VM_IPS VM_MEMORY VM_CPUS
NETWORK_NAME=""
NETWORK_SUBNET=""

case "$CLUSTER_ENV" in
    vagrant)
        VM_NAMES=("cca-test-ctrl" "cca-test-worker-1" "cca-test-worker-2")
        VM_IPS=("192.168.122.10" "192.168.122.20" "192.168.122.30")
        VM_MEMORY=(4096 3072 3072)
        VM_CPUS=(2 2 2)
        NETWORK_NAME="cca-test"
        NETWORK_SUBNET="192.168.122"
        ;;
    deploy)
        VM_NAMES=("cca-deploy-ctrl" "cca-deploy-worker-1" "cca-deploy-worker-2")
        VM_IPS=("192.168.124.10" "192.168.124.20" "192.168.124.30")
        VM_MEMORY=(4096 4096 4096)
        VM_CPUS=(2 2 2)
        NETWORK_NAME="cca-deploy"
        NETWORK_SUBNET="192.168.124"
        ;;
    *)
        fail "Unknown --env: $CLUSTER_ENV (must be 'vagrant' or 'deploy')"
        ;;
esac

STATE_DIR="${HOME}/.local/share/cca-vms-${CLUSTER_ENV}"
SSH_KEY_PATH="${STATE_DIR}/id_ed25519"

# ensure_cloud_image downloads and verifies the Ubuntu cloud image.
ensure_cloud_image() {
    mkdir -p "$IMAGE_CACHE_DIR"
    local image_path="${IMAGE_CACHE_DIR}/${CLOUD_IMAGE_FILENAME}"

    if [[ -f "$image_path" ]]; then
        log "Cloud image cached: $image_path"
        return
    fi

    log "Downloading Ubuntu Noble (24.04) cloud image..."
    wget -q --show-progress -O "${image_path}.tmp" "$CLOUD_IMAGE_URL"

    log "Verifying SHA256 checksum..."
    local expected_sha256
    expected_sha256=$(wget -qO- "$CLOUD_IMAGE_SHA256_URL" \
        | grep "$CLOUD_IMAGE_FILENAME" | grep -v '.manifest' | head -1 | awk '{print $1}')
    local actual_sha256
    actual_sha256=$(sha256sum "${image_path}.tmp" | awk '{print $1}')
    if [[ "$expected_sha256" != "$actual_sha256" ]]; then
        rm -f "${image_path}.tmp"
        fail "SHA256 mismatch: expected $expected_sha256, got $actual_sha256"
    fi
    log "Checksum verified"

    mv "${image_path}.tmp" "$image_path"
    log "Cloud image cached: $image_path"
}

# generate_ssh_key creates an ed25519 key pair for VM access.
generate_ssh_key() {
    if [[ -f "$SSH_KEY_PATH" ]]; then
        return
    fi
    mkdir -p "$STATE_DIR"
    ssh-keygen -t ed25519 -f "$SSH_KEY_PATH" -N "" -C "cca-${CLUSTER_ENV}" -q
    log "SSH key generated: $SSH_KEY_PATH"
}

# ensure_network creates the libvirt network with autostart.
ensure_network() {
    if sudo virsh net-info "$NETWORK_NAME" >/dev/null 2>&1; then
        if ! sudo virsh net-info "$NETWORK_NAME" 2>/dev/null | grep -q "Active:.*yes"; then
            sudo virsh net-start "$NETWORK_NAME"
        fi
        sudo virsh net-autostart "$NETWORK_NAME" 2>/dev/null || true
        return
    fi

    log "Creating libvirt network: $NETWORK_NAME (${NETWORK_SUBNET}.0/24)"
    local net_xml="${STATE_DIR}/${NETWORK_NAME}-net.xml"
    mkdir -p "$STATE_DIR"
    cat > "$net_xml" <<NETEOF
<network>
  <name>${NETWORK_NAME}</name>
  <forward mode='nat'/>
  <bridge name='${NETWORK_NAME}' stp='on' delay='0'/>
  <ip address='${NETWORK_SUBNET}.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='${NETWORK_SUBNET}.100' end='${NETWORK_SUBNET}.200'/>
    </dhcp>
  </ip>
</network>
NETEOF
    sudo virsh net-define "$net_xml"
    sudo virsh net-start "$NETWORK_NAME"
    sudo virsh net-autostart "$NETWORK_NAME"
    log "Network $NETWORK_NAME created with autostart"
}

# create_cloud_init_iso builds the cloud-init seed ISO for a VM.
create_cloud_init_iso() {
    local vm_name="$1" vm_ip="$2"
    local seed_dir="${STATE_DIR}/${vm_name}"
    mkdir -p "$seed_dir"

    local pub_key
    pub_key=$(cat "${SSH_KEY_PATH}.pub")

    # Resolve DNS from host resolv.conf — never hardcode external DNS.
    local host_dns
    host_dns=$(grep '^nameserver' /etc/resolv.conf | head -1 | awk '{print $2}')
    host_dns="${host_dns:-${NETWORK_SUBNET}.1}"

    cat > "${seed_dir}/user-data" <<UDEOF
#cloud-config
hostname: ${vm_name}
manage_etc_hosts: true
users:
  - name: cca
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - ${pub_key}
ssh_pwauth: false
package_update: false
package_upgrade: false
UDEOF

    # Cloud images use predictable network interface names.
    # libvirt VMs with virtio get enp1s0.
    cat > "${seed_dir}/network-config" <<NCEOF
version: 2
ethernets:
  enp1s0:
    dhcp4: false
    addresses:
      - ${vm_ip}/24
    routes:
      - to: default
        via: ${NETWORK_SUBNET}.1
    nameservers:
      addresses:
        - ${host_dns}
NCEOF

    cat > "${seed_dir}/meta-data" <<MDEOF
instance-id: ${vm_name}
local-hostname: ${vm_name}
MDEOF

    cloud-localds -v --network-config "${seed_dir}/network-config" \
        "${seed_dir}/seed.iso" "${seed_dir}/user-data" "${seed_dir}/meta-data" 2>/dev/null
}

# create_vm_disk creates a qcow2 disk backed by the cached cloud image.
create_vm_disk() {
    local vm_name="$1" disk_size="${2:-20G}"
    local disk_path="/var/lib/libvirt/images/${vm_name}.qcow2"
    local base_image="${IMAGE_CACHE_DIR}/${CLOUD_IMAGE_FILENAME}"

    sudo qemu-img create -f qcow2 -b "$base_image" -F qcow2 "$disk_path" "$disk_size"
}

# create_single_vm creates one VM via virt-install.
create_single_vm() {
    local idx="$1"
    local vm_name="${VM_NAMES[$idx]}"
    local vm_ip="${VM_IPS[$idx]}"
    local memory="${VM_MEMORY[$idx]}"
    local cpus="${VM_CPUS[$idx]}"
    local seed_iso="${STATE_DIR}/${vm_name}/seed.iso"
    local disk_path="/var/lib/libvirt/images/${vm_name}.qcow2"

    if sudo virsh dominfo "$vm_name" >/dev/null 2>&1; then
        local state
        state=$(sudo virsh domstate "$vm_name" 2>/dev/null)
        if [[ "$state" == "running" ]]; then
            log "$vm_name already running"
            return
        fi
        log "Starting existing VM: $vm_name"
        sudo virsh start "$vm_name"
        return
    fi

    log "Creating VM: $vm_name (${vm_ip}, ${memory}MB, ${cpus} CPUs)"
    create_cloud_init_iso "$vm_name" "$vm_ip"
    create_vm_disk "$vm_name"

    sudo virt-install \
        --name "$vm_name" \
        --memory "$memory" \
        --vcpus "$cpus" \
        --disk "path=$disk_path,format=qcow2" \
        --disk "path=$seed_iso,device=cdrom" \
        --network "network=$NETWORK_NAME" \
        --os-variant ubuntu24.04 \
        --graphics none \
        --noautoconsole \
        --import \
        --autostart

    log "$vm_name created with autostart"
}

# wait_for_ssh polls until SSH is reachable on a VM.
wait_for_ssh() {
    local vm_ip="$1" vm_name="$2" timeout="${3:-180}"
    local deadline=$((SECONDS + timeout))
    while [[ $SECONDS -lt $deadline ]]; do
        if ssh -o ConnectTimeout=2 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            -o LogLevel=ERROR -i "$SSH_KEY_PATH" "cca@${vm_ip}" "true" 2>/dev/null; then
            return 0
        fi
        sleep 2
    done
    fail "SSH timeout for $vm_name ($vm_ip) after ${timeout}s"
}

# ---- Actions ----

do_create() {
    if [[ "$CLEAN_START" == "true" ]]; then
        log "Clean start — destroying existing VMs..."
        do_destroy
    fi

    ensure_cloud_image
    generate_ssh_key
    ensure_network

    for idx in "${!VM_NAMES[@]}"; do
        create_single_vm "$idx"
    done

    log "Waiting for SSH on all VMs..."
    for idx in "${!VM_NAMES[@]}"; do
        wait_for_ssh "${VM_IPS[$idx]}" "${VM_NAMES[$idx]}"
    done

    log "All VMs ready"
    for idx in "${!VM_NAMES[@]}"; do
        echo "  ${VM_NAMES[$idx]}  ${VM_IPS[$idx]}  ssh -i $SSH_KEY_PATH cca@${VM_IPS[$idx]}"
    done
}

do_destroy() {
    for vm_name in "${VM_NAMES[@]}"; do
        if sudo virsh dominfo "$vm_name" >/dev/null 2>&1; then
            log "Destroying $vm_name..."
            sudo virsh destroy "$vm_name" 2>/dev/null || true
            sudo virsh undefine "$vm_name" --remove-all-storage 2>/dev/null || true
        fi
    done
    # Remove the network if no other env uses it.
    if sudo virsh net-info "$NETWORK_NAME" >/dev/null 2>&1; then
        sudo virsh net-destroy "$NETWORK_NAME" 2>/dev/null || true
        sudo virsh net-undefine "$NETWORK_NAME" 2>/dev/null || true
        log "Network $NETWORK_NAME removed"
    fi
    rm -rf "$STATE_DIR"
    log "All VMs and state destroyed"
}

do_stop() {
    for vm_name in "${VM_NAMES[@]}"; do
        if sudo virsh domstate "$vm_name" 2>/dev/null | grep -q running; then
            log "Stopping $vm_name..."
            sudo virsh shutdown "$vm_name" 2>/dev/null || true
        fi
    done
    sleep 5
    for vm_name in "${VM_NAMES[@]}"; do
        if sudo virsh domstate "$vm_name" 2>/dev/null | grep -q running; then
            log "Force-stopping $vm_name..."
            sudo virsh destroy "$vm_name" 2>/dev/null || true
        fi
    done
    log "All VMs stopped"
}

do_start() {
    ensure_network
    for vm_name in "${VM_NAMES[@]}"; do
        local state
        state=$(sudo virsh domstate "$vm_name" 2>/dev/null || echo "missing")
        if [[ "$state" == "running" ]]; then
            log "$vm_name already running"
        elif [[ "$state" == "shut off" ]]; then
            log "Starting $vm_name..."
            sudo virsh start "$vm_name"
        else
            fail "$vm_name not found — run '$0 create --env $CLUSTER_ENV' first"
        fi
    done
    log "Waiting for SSH on all VMs..."
    for idx in "${!VM_NAMES[@]}"; do
        wait_for_ssh "${VM_IPS[$idx]}" "${VM_NAMES[$idx]}"
    done
    log "All VMs ready"
}

do_status() {
    echo "Environment: $CLUSTER_ENV"
    echo "State dir:   $STATE_DIR"
    echo "SSH key:     $SSH_KEY_PATH"
    echo ""
    for idx in "${!VM_NAMES[@]}"; do
        local vm_name="${VM_NAMES[$idx]}"
        local vm_ip="${VM_IPS[$idx]}"
        local state
        state=$(sudo virsh domstate "$vm_name" 2>/dev/null || echo "not created")
        printf "  %-25s  %-16s  %s\n" "$vm_name" "$vm_ip" "$state"
    done
}

case "$ACTION" in
    create)  do_create ;;
    destroy) do_destroy ;;
    stop)    do_stop ;;
    start)   do_start ;;
    status)  do_status ;;
esac
