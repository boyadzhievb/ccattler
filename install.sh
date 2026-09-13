#!/usr/bin/env bash
# Deploy CCattler on existing hosts.
#
#   curl -sSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
#
# Prerequisites: curl, tar, ansible
# First run creates inventory.ini for you to edit. Second run deploys.

set -euo pipefail

GITHUB_REPO="boyadzhievb/ccattler"
INSTALL_DIR="${CCATTLER_HOME:-$HOME/.ccattler}"
ANSIBLE_DIR="$INSTALL_DIR/ansible"

log_info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
log_ok()    { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
log_error() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; }

for dependency in curl tar ansible-playbook; do
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

if [ ! -f inventory.ini ]; then
    cp inventory.ini.template inventory.ini
    log_info "Created $ANSIBLE_DIR/inventory.ini"
    log_info "Edit it with your host details, then run this script again."
    exit 0
fi

log_info "Deploying CCattler..."
python3 -c "
import subprocess, sys, os
proc = subprocess.Popen(
    ['ansible-playbook', '-i', 'inventory.ini', 'site.yml'],
    cwd='$ANSIBLE_DIR',
    stdin=subprocess.DEVNULL,
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT
)
for line in proc.stdout:
    sys.stdout.buffer.write(line)
    sys.stdout.buffer.flush()
sys.exit(proc.wait())
"
log_ok "CCattler deployed!"
