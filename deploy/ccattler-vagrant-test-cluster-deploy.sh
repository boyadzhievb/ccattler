#!/usr/bin/env bash
set -euo pipefail

CCATTLER_VERSION="${CCATTLER_VERSION:-v0.11.0}"
GITHUB_REPO="boyadzhievb/ccattler"
RELEASE_URL="https://github.com/${GITHUB_REPO}/releases/download/${CCATTLER_VERSION}"
INSTALL_DIR="${CCATTLER_HOME:-$HOME/.ccattler-deploy}"

usage() {
    cat <<EOF
CCattler deployment manager ${CCATTLER_VERSION}

Usage: $(basename "$0") <command>

Test cluster (libvirt VMs):
  test up             Create VMs + deploy CCattler + Java test app
  test destroy        Tear down all VMs
  test status         Show VM status
  test ssh <vm>       SSH into VM (cca-test-ctrl or cca-test-worker)
  test provision      Re-deploy CCattler on existing VMs
  test halt           Stop VMs without destroying
  test rebuild        Destroy and recreate from scratch
  test app            Re-deploy only the Java test app

Production (bare metal):
  deploy              Full deploy (cleanup + provision)
  deploy --no-clean   Deploy without wiping old state

Environment variables:
  CCATTLER_VERSION    Release version (default: ${CCATTLER_VERSION})
  CCATTLER_HOME       Install directory (default: ~/.ccattler-deploy)
EOF
}

check_dependencies() {
    local missing=()
    command -v ansible-playbook >/dev/null || missing+=(ansible)
    command -v vagrant >/dev/null || missing+=(vagrant)
    command -v curl >/dev/null || missing+=(curl)

    if [ ${#missing[@]} -gt 0 ]; then
        echo "Missing dependencies: ${missing[*]}"
        echo "Install them and retry."
        exit 1
    fi
}

download_release() {
    if [ -d "$INSTALL_DIR/ansible" ] && [ -f "$INSTALL_DIR/ansible/cca-linux-amd64" ]; then
        return
    fi

    local arch
    arch="$(uname -m)"
    case "$arch" in
        x86_64)  arch="amd64" ;;
        aarch64) arch="arm64" ;;
    esac

    echo "Downloading CCattler ${CCATTLER_VERSION} (linux/${arch})..."
    mkdir -p "$INSTALL_DIR"

    curl -fsSL "${RELEASE_URL}/ccattler-deploy.tar.gz" -o /tmp/ccattler-deploy.tar.gz
    tar -xzf /tmp/ccattler-deploy.tar.gz -C "$INSTALL_DIR"
    rm /tmp/ccattler-deploy.tar.gz

    curl -fsSL "${RELEASE_URL}/cca-${CCATTLER_VERSION}-linux-${arch}.tar.gz" -o /tmp/cca.tar.gz
    tar -xzf /tmp/cca.tar.gz -C "$INSTALL_DIR/ansible"
    mv "$INSTALL_DIR/ansible/cca" "$INSTALL_DIR/ansible/cca-linux-amd64"
    chmod +x "$INSTALL_DIR/ansible/cca-linux-amd64"
    rm /tmp/cca.tar.gz

    echo "Installed to ${INSTALL_DIR}"
}

cmd_test() {
    local ansible_dir="$INSTALL_DIR/ansible"
    export VAGRANT_VAGRANTFILE="$ansible_dir/test-Vagrantfile"
    cd "$ansible_dir"

    case "${1:-}" in
        up)        ansible-playbook -i test-inventory.ini test-deploy.yml ;;
        destroy)   vagrant destroy -f ;;
        status)    vagrant status ;;
        ssh)       vagrant ssh "${2:?Usage: $0 test ssh <vm-name>}" ;;
        provision) ansible-playbook -i test-inventory.ini test-deploy.yml --skip-tags vagrant,cleanup ;;
        halt)      vagrant halt ;;
        rebuild)   vagrant destroy -f && ansible-playbook -i test-inventory.ini test-deploy.yml ;;
        app)       ansible-playbook -i test-inventory.ini test-deploy.yml --tags testapp ;;
        *)         usage; exit 1 ;;
    esac
}

cmd_deploy() {
    local ansible_dir="$INSTALL_DIR/ansible"
    cd "$ansible_dir"

    if [ "${1:-}" = "--no-clean" ]; then
        ansible-playbook -i inventory.ini site.yml --skip-tags cleanup
    else
        ansible-playbook -i inventory.ini site.yml
    fi
}

check_dependencies
download_release

case "${1:-}" in
    test)    shift; cmd_test "$@" ;;
    deploy)  shift; cmd_deploy "$@" ;;
    *)       usage; exit 1 ;;
esac
