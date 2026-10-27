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
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
SSH_KEY="${REPO_DIR}/test/e2e/vm-deploy/.vmherd/id_ed25519"

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
    if [ ! -f "$SSH_KEY" ]; then
        echo "WARNING: SSH key not found at $SSH_KEY"
        return 1
    fi
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$SSH_KEY" "vmherd@$ip" "$@"
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
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -i "$SSH_KEY" "$local_path" "vmherd@${ip}:${remote_path}"
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

    instances=$(ssh_vm "$vm" "sudo cca get instances -o json 2>/dev/null | jq -r '.[] | select(.node == \"$vm\") | select(.service | test(\"scheduler|webserver|worker\")) | .id' || true" 2>/dev/null)

    if [ -z "$instances" ]; then
        echo "    No Airflow instances on this node"
        continue
    fi

    for instance in $instances; do
        for dagfile in "$DAG_DIR"/*.py; do
            dagname="$(basename "$dagfile")"
            echo "    Deploying $dagname -> $instance"
            ssh_vm "$vm" "cat /tmp/airflow-dags/$dagname | sudo cca exec $instance -- tee /opt/airflow/dags/$dagname > /dev/null" 2>/dev/null
            total_copied=$((total_copied + 1))
        done
    done
done

echo ""
echo "==> Done. Deployed $total_copied DAG file(s) into Airflow containers."
echo "    The scheduler will pick up new DAGs within ~30 seconds."
echo "    Check at: https://airflow.home.arpa"
