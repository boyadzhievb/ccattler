#!/usr/bin/env bash
# Deploy DAG files from deploy/ansible/dags/ into all running Airflow
# containers across the deploy cluster VMs.
#
# Usage (from testbed-100.43):
#   cd /path/to/ccattler/deploy/ansible
#   bash deploy-dags.sh
#
# Or with a specific DAG directory:
#   bash deploy-dags.sh /path/to/dags/

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DAG_DIR="${1:-$SCRIPT_DIR/dags}"
VAGRANT_STATE_DIR="${HOME}/.local/share/cca-vagrant-state-deploy"

if [ ! -d "$DAG_DIR" ] || [ -z "$(ls "$DAG_DIR"/*.py 2>/dev/null)" ]; then
    echo "ERROR: No .py files found in $DAG_DIR"
    exit 1
fi

echo "==> DAG directory: $DAG_DIR"
echo "==> DAGs to deploy:"
ls -1 "$DAG_DIR"/*.py | while read -r dagfile; do
    echo "    $(basename "$dagfile")"
done

VMS=("cca-deploy-ctrl" "cca-deploy-worker-1" "cca-deploy-worker-2")
IPS=("192.168.124.10" "192.168.124.20" "192.168.124.30")

ssh_vm() {
    local vm_name="$1"
    shift
    local ip=""
    local idx=0
    for candidate in "${VMS[@]}"; do
        if [ "$candidate" = "$vm_name" ]; then
            ip="${IPS[$idx]}"
            break
        fi
        idx=$((idx + 1))
    done
    local key_path="$VAGRANT_STATE_DIR/machines/$vm_name/libvirt/private_key"
    if [ ! -f "$key_path" ]; then
        echo "WARNING: SSH key not found for $vm_name at $key_path"
        return 1
    fi
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$key_path" "vagrant@$ip" "$@"
}

scp_vm() {
    local vm_name="$1"
    local local_path="$2"
    local remote_path="$3"
    local ip=""
    local idx=0
    for candidate in "${VMS[@]}"; do
        if [ "$candidate" = "$vm_name" ]; then
            ip="${IPS[$idx]}"
            break
        fi
        idx=$((idx + 1))
    done
    local key_path="$VAGRANT_STATE_DIR/machines/$vm_name/libvirt/private_key"
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$key_path" "$local_path" "vagrant@${ip}:${remote_path}"
}

total_copied=0

for idx in "${!VMS[@]}"; do
    vm="${VMS[$idx]}"
    echo ""
    echo "==> $vm (${IPS[$idx]})"

    ssh_vm "$vm" "mkdir -p /tmp/airflow-dags" 2>/dev/null || continue

    for dagfile in "$DAG_DIR"/*.py; do
        dagname="$(basename "$dagfile")"
        scp_vm "$vm" "$dagfile" "/tmp/airflow-dags/$dagname" 2>/dev/null
    done

    containers=$(ssh_vm "$vm" "sudo nerdctl ps --format '{{.Names}}' | grep -E '^(scheduler|webserver|worker)-' || true" 2>/dev/null)

    if [ -z "$containers" ]; then
        echo "    No Airflow containers on this node"
        continue
    fi

    for container in $containers; do
        for dagfile in "$DAG_DIR"/*.py; do
            dagname="$(basename "$dagfile")"
            echo "    Deploying $dagname -> $container"
            ssh_vm "$vm" "cat /tmp/airflow-dags/$dagname | sudo nerdctl exec -i $container tee /opt/airflow/dags/$dagname > /dev/null" 2>/dev/null
            total_copied=$((total_copied + 1))
        done
    done
done

echo ""
echo "==> Done. Deployed $total_copied DAG file(s) into Airflow containers."
echo "    The scheduler will pick up new DAGs within ~30 seconds."
echo "    Check at: https://airflow.home.arpa"
