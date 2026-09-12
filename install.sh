#!/usr/bin/env bash
# CCattler installer — download from a release and deploy a cluster.
#
#   curl -sSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash -s demo
#
# Commands:
#   demo              Create VMs with Vagrant, deploy CCattler, deploy Java app
#   demo destroy      Tear down all VMs
#   demo status       Show VM status
#   demo ssh <vm>     SSH into a VM (ctrl or worker)
#   demo provision    Re-deploy CCattler on existing VMs
#   demo halt         Stop VMs without destroying
#   demo rebuild      Destroy and recreate from scratch
#   demo app          Re-deploy only the Java test app
#   deploy            Full deploy to existing hosts (edit inventory.ini first)
#   deploy --no-clean Deploy without wiping old state
#
# Environment variables:
#   CCATTLER_VERSION  Release version (default: latest)
#   CCATTLER_HOME     Install directory (default: ~/.ccattler)
#   VAGRANT_PROVIDER  Vagrant provider: libvirt (Linux) or qemu (macOS); auto-detected

set -euo pipefail

GITHUB_REPO="boyadzhievb/ccattler"
INSTALL_DIR="${CCATTLER_HOME:-$HOME/.ccattler}"
ANSIBLE_DIR="$INSTALL_DIR/ansible"

# --- helpers ----------------------------------------------------------------

print_banner() {
    cat <<'BANNER'

   ___  ___      _   _   _
  / __\/ __\__ _| |_| |_| | ___ _ __
 / /  / /  / _` | __| __| |/ _ \ '__|
/ /__/ /__| (_| | |_| |_| |  __/ |
\____\____/\__,_|\__|\__|_|\___|_|

  Container Cattler — fact-based orchestrator

BANNER
}

log_info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
log_ok()    { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
log_warn()  { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
log_error() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; }

usage() {
    cat <<EOF
Usage: install.sh <command> [options]

Commands:
  demo              Create VMs + deploy CCattler + deploy Java test app
  demo destroy      Tear down demo VMs
  demo status       Show VM status
  demo ssh <vm>     SSH into VM (ctrl or worker)
  demo provision    Re-deploy CCattler (skip VM creation)
  demo halt         Stop VMs without destroying
  demo rebuild      Destroy + recreate from scratch
  demo app          Re-deploy only the Java test app
  deploy            Full deploy to bare-metal/cloud hosts (inventory.ini)
  deploy --no-clean Deploy without wiping old state

Environment variables:
  CCATTLER_VERSION    Release version (default: latest)
  CCATTLER_HOME       Install directory (default: ~/.ccattler)
  VAGRANT_PROVIDER    libvirt (Linux) | qemu (macOS) (default: auto-detect)
EOF
}

# --- dependency checks ------------------------------------------------------

check_common_dependencies() {
    local missing=()
    command -v curl    >/dev/null || missing+=(curl)
    command -v tar     >/dev/null || missing+=(tar)
    command -v ansible-playbook >/dev/null || missing+=(ansible)

    if [ ${#missing[@]} -gt 0 ]; then
        log_error "Missing dependencies: ${missing[*]}"
        log_error "Install them and retry."
        exit 1
    fi
}

check_demo_dependencies() {
    if ! command -v vagrant >/dev/null; then
        log_error "Vagrant is required for the demo. Install from https://www.vagrantup.com"
        exit 1
    fi
}

detect_vagrant_provider() {
    if [ -n "${VAGRANT_PROVIDER:-}" ]; then
        echo "$VAGRANT_PROVIDER"
        return
    fi

    case "$(uname -s)" in
        Darwin) echo "qemu" ;;
        *)      echo "libvirt" ;;
    esac
}

# --- version resolution -----------------------------------------------------

resolve_version() {
    if [ -n "${CCATTLER_VERSION:-}" ]; then
        echo "$CCATTLER_VERSION"
        return
    fi

    log_info "Detecting latest CCattler release..."
    local latest_tag
    latest_tag=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')

    if [ -z "$latest_tag" ]; then
        log_error "Could not determine latest release. Set CCATTLER_VERSION manually."
        exit 1
    fi

    echo "$latest_tag"
}

# --- download ---------------------------------------------------------------

download_release() {
    local version="$1"
    local release_url="https://github.com/${GITHUB_REPO}/releases/download/${version}"

    if [ -d "$ANSIBLE_DIR" ] && [ -f "$ANSIBLE_DIR/cca-linux-amd64" ]; then
        local existing_version=""
        if [ -f "$INSTALL_DIR/.version" ]; then
            existing_version=$(cat "$INSTALL_DIR/.version")
        fi
        if [ "$existing_version" = "$version" ]; then
            log_info "CCattler $version already installed in $INSTALL_DIR"
            return
        fi
        log_info "Upgrading from $existing_version to $version..."
    fi

    local arch
    arch="$(uname -m)"
    case "$arch" in
        x86_64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
    esac

    log_info "Downloading CCattler $version (linux/$arch)..."
    mkdir -p "$INSTALL_DIR"

    curl -fsSL "${release_url}/ccattler-deploy.tar.gz" -o /tmp/ccattler-deploy.tar.gz
    tar -xzf /tmp/ccattler-deploy.tar.gz -C "$INSTALL_DIR"
    rm -f /tmp/ccattler-deploy.tar.gz

    curl -fsSL "${release_url}/cca-${version}-linux-${arch}.tar.gz" -o /tmp/cca.tar.gz
    tar -xzf /tmp/cca.tar.gz -C "$ANSIBLE_DIR"
    mv "$ANSIBLE_DIR/cca" "$ANSIBLE_DIR/cca-linux-amd64"
    chmod +x "$ANSIBLE_DIR/cca-linux-amd64"
    rm -f /tmp/cca.tar.gz

    echo "$version" > "$INSTALL_DIR/.version"
    log_ok "Installed CCattler $version to $INSTALL_DIR"
}

# --- demo (Vagrant) ---------------------------------------------------------

command_demo() {
    check_demo_dependencies

    local provider
    provider=$(detect_vagrant_provider)
    log_info "Vagrant provider: $provider"

    export VAGRANT_VAGRANTFILE="$ANSIBLE_DIR/demo-Vagrantfile"
    export CCA_VAGRANT_PROVIDER="$provider"
    cd "$ANSIBLE_DIR"

    sed "s/PROVIDER_PLACEHOLDER/$provider/g" demo-inventory.ini.template > demo-inventory.ini

    case "${1:-up}" in
        up)
            log_info "Creating demo cluster (2 VMs, CCattler + Java app)..."
            ansible-playbook -i demo-inventory.ini demo-deploy.yml
            log_ok "Demo cluster is running!"
            log_info "Try: install.sh demo ssh ctrl"
            log_info "Try: install.sh demo status"
            ;;
        destroy)
            vagrant destroy -f
            log_ok "Demo VMs destroyed"
            ;;
        status)
            vagrant status
            ;;
        ssh)
            local target="${2:-}"
            if [ -z "$target" ]; then
                log_error "Usage: install.sh demo ssh <ctrl|worker>"
                exit 1
            fi
            vagrant ssh "cca-demo-${target}"
            ;;
        provision)
            ansible-playbook -i demo-inventory.ini demo-deploy.yml \
                --skip-tags vagrant,cleanup
            ;;
        halt)
            vagrant halt
            log_ok "Demo VMs halted"
            ;;
        rebuild)
            vagrant destroy -f
            ansible-playbook -i demo-inventory.ini demo-deploy.yml
            log_ok "Demo cluster rebuilt!"
            ;;
        app)
            ansible-playbook -i demo-inventory.ini demo-deploy.yml \
                --tags testapp
            log_ok "Java app re-deployed"
            ;;
        *)
            usage
            exit 1
            ;;
    esac
}

# --- deploy (bare metal / cloud) --------------------------------------------

command_deploy() {
    cd "$ANSIBLE_DIR"

    if [ ! -f inventory.ini ]; then
        log_warn "No inventory.ini found — copying template"
        cp inventory.ini.template inventory.ini
        log_info "Edit $ANSIBLE_DIR/inventory.ini with your host details, then re-run."
        exit 0
    fi

    if [ "${1:-}" = "--no-clean" ]; then
        ansible-playbook -i inventory.ini site.yml --skip-tags cleanup
    else
        ansible-playbook -i inventory.ini site.yml
    fi

    log_ok "CCattler deployed!"
}

# --- main -------------------------------------------------------------------

print_banner

if [ $# -eq 0 ]; then
    usage
    exit 0
fi

check_common_dependencies

CCATTLER_VERSION=$(resolve_version)
download_release "$CCATTLER_VERSION"

case "$1" in
    demo)    shift; command_demo "$@" ;;
    deploy)  shift; command_deploy "$@" ;;
    -h|--help|help) usage ;;
    *)       log_error "Unknown command: $1"; usage; exit 1 ;;
esac
