#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ANSIBLE_DIR="$SCRIPT_DIR/ansible"

usage() {
    cat <<EOF
CCattler deployment manager — run on 192.168.100.43

Test cluster (libvirt VMs):
  test up             Create VMs + deploy CCattler + Java test app
  test destroy        Tear down all VMs
  test status         Show VM status
  test ssh <vm>       SSH into VM (cca-test-ctrl or cca-test-worker)
  test provision      Re-deploy CCattler on existing VMs
  test halt           Stop VMs without destroying
  test rebuild        Destroy and recreate from scratch
  test app            Re-deploy only the Java test app

Production (bare metal .43 + .215):
  deploy              Full deploy (cleanup + provision)
  deploy --no-clean   Deploy without wiping old state
EOF
}

cmd_test() {
    export VAGRANT_VAGRANTFILE="$ANSIBLE_DIR/test-Vagrantfile"
    cd "$ANSIBLE_DIR"

    case "${1:-}" in
        up)
            ansible-playbook -i test-inventory.ini test-deploy.yml
            ;;
        destroy)
            vagrant destroy -f
            ;;
        status)
            vagrant status
            ;;
        ssh)
            vagrant ssh "${2:?Usage: $0 test ssh <vm-name>}"
            ;;
        provision)
            ansible-playbook -i test-inventory.ini test-deploy.yml --skip-tags vagrant,cleanup
            ;;
        halt)
            vagrant halt
            ;;
        rebuild)
            vagrant destroy -f
            ansible-playbook -i test-inventory.ini test-deploy.yml
            ;;
        app)
            ansible-playbook -i test-inventory.ini test-deploy.yml --tags testapp
            ;;
        *)
            usage; exit 1
            ;;
    esac
}

cmd_deploy() {
    cd "$ANSIBLE_DIR"

    if [ "${1:-}" = "--no-clean" ]; then
        ansible-playbook -i inventory.ini site.yml --skip-tags cleanup
    else
        ansible-playbook -i inventory.ini site.yml
    fi
}

case "${1:-}" in
    test)    shift; cmd_test "$@" ;;
    deploy)  shift; cmd_deploy "$@" ;;
    *)       usage; exit 1 ;;
esac
