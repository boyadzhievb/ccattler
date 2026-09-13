#!/usr/bin/env bash
set -euo pipefail

# CCattler Lima demo — single-VM all-in-one on macOS
# Requires: lima, ansible

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CCA_VERSION="${CCA_VERSION:-v0.12.10}"
VM_NAME="cca-ctrl"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

check_dependencies() {
    info "Checking dependencies..."
    command -v limactl >/dev/null 2>&1 || error "lima not found — install with: brew install lima"
    command -v ansible-playbook >/dev/null 2>&1 || error "ansible not found — install with: brew install ansible"
}

detect_architecture() {
    local arch
    arch="$(uname -m)"
    case "$arch" in
        arm64|aarch64) LINUX_ARCH="arm64" ;;
        x86_64|amd64)  LINUX_ARCH="amd64" ;;
        *) error "Unsupported architecture: $arch" ;;
    esac
    info "Architecture: $LINUX_ARCH"
}

download_binary() {
    local binary_path="${SCRIPT_DIR}/cca-linux-${LINUX_ARCH}"
    if [[ -f "$binary_path" ]]; then
        info "CCattler binary already exists: $binary_path"
        return
    fi

    local tarball="cca-${CCA_VERSION}-linux-${LINUX_ARCH}.tar.gz"
    local download_url="https://github.com/boyadzhievb/ccattler/releases/download/${CCA_VERSION}/${tarball}"

    info "Downloading CCattler ${CCA_VERSION} for linux-${LINUX_ARCH}..."
    curl -fSL -o "/tmp/${tarball}" "$download_url" || error "Download failed — check version ${CCA_VERSION} exists"
    tar -xzf "/tmp/${tarball}" -C "$SCRIPT_DIR"
    mv "${SCRIPT_DIR}/cca" "$binary_path"
    chmod +x "$binary_path"
    rm -f "/tmp/${tarball}"
    info "Binary ready: $binary_path"
}

create_vm() {
    if limactl list --json 2>/dev/null | grep -q "\"name\":\"${VM_NAME}\""; then
        local vm_status
        vm_status="$(limactl list --json | python3 -c "import sys,json; [print(vm['status']) for vm in json.loads('['+','.join(sys.stdin.readlines())+']') if vm['name']=='${VM_NAME}']" 2>/dev/null || echo "unknown")"
        if [[ "$vm_status" == "Running" ]]; then
            info "VM '${VM_NAME}' is already running"
            return
        fi
        info "Starting existing VM '${VM_NAME}'..."
        limactl start "$VM_NAME"
        return
    fi

    info "Creating Lima VM '${VM_NAME}'..."
    limactl start --name="$VM_NAME" "${SCRIPT_DIR}/cca-ctrl.yaml" --tty=false
    info "VM '${VM_NAME}' is running"
}

generate_inventory() {
    local inventory_file="${SCRIPT_DIR}/lima-inventory.ini"
    local ssh_config
    ssh_config="$(limactl show-ssh --format config "$VM_NAME")"

    local ssh_host ssh_port ssh_key ssh_user
    ssh_host="$(echo "$ssh_config" | grep '^ *HostName' | awk '{print $2}')"
    ssh_port="$(echo "$ssh_config" | grep '^ *Port' | awk '{print $2}')"
    ssh_key="$(echo "$ssh_config" | grep '^ *IdentityFile' | head -1 | awk '{print $2}')"
    ssh_user="$(echo "$ssh_config" | grep '^ *User' | awk '{print $2}')"

    cat > "$inventory_file" <<EOF
[all]
${VM_NAME} ansible_host=${ssh_host} ansible_port=${ssh_port} ansible_user=${ssh_user} ansible_ssh_private_key_file=${ssh_key} ansible_ssh_common_args='-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null'
EOF

    info "Inventory written: $inventory_file"
}

run_playbook() {
    info "Running Ansible playbook..."
    ansible-playbook \
        -i "${SCRIPT_DIR}/lima-inventory.ini" \
        "${SCRIPT_DIR}/lima-deploy.yml" \
        --timeout=60
}

cleanup_command() {
    echo ""
    info "To tear down the demo:"
    echo "  limactl stop ${VM_NAME}"
    echo "  limactl delete ${VM_NAME}"
    echo "  rm -f ${SCRIPT_DIR}/lima-inventory.ini ${SCRIPT_DIR}/cca-linux-*"
}

main() {
    echo "============================================"
    echo " CCattler Lima Demo (macOS)"
    echo " Single VM — etcd + server + agent"
    echo "============================================"
    echo ""

    check_dependencies
    detect_architecture
    download_binary
    create_vm
    generate_inventory
    run_playbook
    cleanup_command
}

main "$@"
