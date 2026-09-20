#!/usr/bin/env bash
# Deploy a CCattler demo cluster using Ansible.
#
#   curl -sSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install-demo.sh | bash
#
# This script:
#   1. Runs install.sh to install the cca binary on this machine
#   2. Downloads the deploy tarball (Ansible playbooks + example configs)
#   3. Creates a demo inventory and runs the Ansible deployment
#
# For Vagrant/libvirt demo: creates 2 VMs, deploys etcd + CCattler + Java test app.
# For bare-metal: edit the inventory after first run, then run again.
#
# Prerequisites: curl, tar, ansible-playbook
# Vagrant demo also requires: vagrant, libvirt
#
# Environment variables:
#   CCATTLER_VERSION   Pin a specific release (e.g. v0.38.0). Default: latest.
#   CCATTLER_HOME      Override working directory. Default: ~/.ccattler
#   DEMO_MODE          "vagrant" (default) or "inventory" (bare-metal with manual hosts)

set -euo pipefail

GITHUB_REPO="boyadzhievb/ccattler"
INSTALL_DIR="${CCATTLER_HOME:-$HOME/.ccattler}"
ANSIBLE_DIR="$INSTALL_DIR/ansible"
DEMO_MODE="${DEMO_MODE:-vagrant}"

log_info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
log_ok()    { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
log_error() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; }

# --- Step 1: Install the cca binary via install.sh ---
log_info "Step 1: Installing cca binary..."

install_script_url="https://github.com/${GITHUB_REPO}/releases/latest/download/install.sh"
if [ -n "${CCATTLER_VERSION:-}" ]; then
    install_script_url="https://github.com/${GITHUB_REPO}/releases/download/${CCATTLER_VERSION}/install.sh"
fi

curl -fsSL "$install_script_url" | CCATTLER_VERSION="${CCATTLER_VERSION:-}" bash

# --- Check Ansible dependency ---
if ! command -v ansible-playbook >/dev/null; then
    log_error "Missing: ansible-playbook (install Ansible first: pip install ansible)"
    exit 1
fi

# --- Resolve version (same logic as install.sh) ---
if [ -n "${CCATTLER_VERSION:-}" ]; then
    version="$CCATTLER_VERSION"
else
    version=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    if [ -z "$version" ]; then
        log_error "Could not detect latest release."
        exit 1
    fi
fi

release_url="https://github.com/${GITHUB_REPO}/releases/download/${version}"

# --- Step 2: Download deploy tarball (Ansible playbooks + examples) ---
log_info "Step 2: Downloading deployment playbooks and examples..."

mkdir -p "$INSTALL_DIR"
curl -fsSL "${release_url}/ccattler-deploy.tar.gz" -o /tmp/ccattler-deploy.tar.gz
tar -xzf /tmp/ccattler-deploy.tar.gz -C "$INSTALL_DIR"
rm -f /tmp/ccattler-deploy.tar.gz

# Also download the linux-amd64 binary for node deployment by Ansible.
curl -fsSL "${release_url}/cca-${version}-linux-amd64.tar.gz" -o /tmp/cca-linux.tar.gz
tar -xzf /tmp/cca-linux.tar.gz -C "$ANSIBLE_DIR"
mv "$ANSIBLE_DIR/cca" "$ANSIBLE_DIR/cca-linux-amd64"
chmod +x "$ANSIBLE_DIR/cca-linux-amd64"
rm -f /tmp/cca-linux.tar.gz

log_ok "Downloaded to $INSTALL_DIR"

# --- Step 3: Run Ansible deployment ---
cd "$ANSIBLE_DIR"

if [ "$DEMO_MODE" = "vagrant" ]; then
    # Vagrant demo: spin up VMs and deploy.
    if ! command -v vagrant >/dev/null; then
        log_error "Missing: vagrant (required for Vagrant demo mode)"
        exit 1
    fi

    sed "s/PROVIDER_PLACEHOLDER/libvirt/g" demo-inventory.ini.template > demo-inventory.ini

    export VAGRANT_VAGRANTFILE="$ANSIBLE_DIR/demo-Vagrantfile"
    export CCA_VAGRANT_PROVIDER="libvirt"

    log_info "Step 3: Creating demo cluster (2 VMs + CCattler + Java app)..."
    ansible-playbook -i demo-inventory.ini demo-deploy.yml

    log_ok "Demo cluster is running!"
    echo ""
    echo "  To clean up:"
    echo "    cd $ANSIBLE_DIR"
    echo "    VAGRANT_VAGRANTFILE=$ANSIBLE_DIR/demo-Vagrantfile vagrant destroy -f"
else
    # Bare-metal mode: create inventory on first run, deploy on second.
    if [ ! -f inventory.ini ]; then
        cp inventory.ini.template inventory.ini
        log_info "Created $ANSIBLE_DIR/inventory.ini"
        log_info "Edit it with your host details, then run this script again with DEMO_MODE=inventory."
        exit 0
    fi

    log_info "Step 3: Deploying CCattler to hosts in inventory..."
    ansible-playbook -i inventory.ini site.yml

    log_ok "CCattler deployed!"
fi
