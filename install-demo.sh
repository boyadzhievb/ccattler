#!/usr/bin/env bash
# Create a CCattler demo cluster on Vagrant VMs (libvirt).
#
#   curl -sSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install-demo.sh | bash
#
# Creates 2 VMs, deploys etcd + CCattler cluster, deploys a Java test app.
# Prerequisites: curl, tar, ansible, vagrant, libvirt

set -euo pipefail

GITHUB_REPO="boyadzhievb/ccattler"
INSTALL_DIR="${CCATTLER_HOME:-$HOME/.ccattler}"
ANSIBLE_DIR="$INSTALL_DIR/ansible"

log_info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
log_ok()    { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
log_error() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; }

for dependency in curl tar ansible-playbook vagrant; do
    if ! command -v "$dependency" >/dev/null; then
        log_error "Missing: $dependency"
        exit 1
    fi
done

if [ -n "${CCATTLER_VERSION:-}" ]; then
    version="$CCATTLER_VERSION"
else
    log_info "Detecting latest release..."
    version=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    if [ -z "$version" ]; then
        log_error "Could not detect latest release. Set CCATTLER_VERSION manually."
        exit 1
    fi
fi

release_url="https://github.com/${GITHUB_REPO}/releases/download/${version}"
log_info "CCattler $version"

mkdir -p "$INSTALL_DIR"
curl -fsSL "${release_url}/ccattler-deploy.tar.gz" -o /tmp/ccattler-deploy.tar.gz
tar -xzf /tmp/ccattler-deploy.tar.gz -C "$INSTALL_DIR"
rm -f /tmp/ccattler-deploy.tar.gz

curl -fsSL "${release_url}/cca-${version}-linux-amd64.tar.gz" -o /tmp/cca.tar.gz
tar -xzf /tmp/cca.tar.gz -C "$ANSIBLE_DIR"
mv "$ANSIBLE_DIR/cca" "$ANSIBLE_DIR/cca-linux-amd64"
chmod +x "$ANSIBLE_DIR/cca-linux-amd64"
rm -f /tmp/cca.tar.gz

log_ok "Downloaded to $INSTALL_DIR"

cd "$ANSIBLE_DIR"

sed "s/PROVIDER_PLACEHOLDER/libvirt/g" demo-inventory.ini.template > demo-inventory.ini

export VAGRANT_VAGRANTFILE="$ANSIBLE_DIR/demo-Vagrantfile"
export CCA_VAGRANT_PROVIDER="libvirt"

log_info "Creating demo cluster (2 VMs + CCattler + Java app)..."
ansible-playbook -i demo-inventory.ini demo-deploy.yml

log_ok "Demo cluster is running!"
echo ""
echo "  To clean up:"
echo "    cd $ANSIBLE_DIR"
echo "    VAGRANT_VAGRANTFILE=$ANSIBLE_DIR/demo-Vagrantfile vagrant destroy -f"
