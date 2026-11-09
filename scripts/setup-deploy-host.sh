#!/usr/bin/env bash
# Copyright (C) 2026 CCattler Contributors
# SPDX-License-Identifier: GPL-3.0-only
#
# setup-deploy-host.sh — prepare a Debian/Ubuntu machine to host the deploy
# cluster (3 vmherd VMs on 192.168.124.0/24) and run the Deploy Testbed
# workflow. Idempotent: safe to re-run. See design/deploy-host.md.
#
# Usage (as the user that runs the GitHub runner, with passwordless sudo):
#   scripts/setup-deploy-host.sh            # packages, groups, registry, NFS
#   scripts/setup-deploy-host.sh --images   # only (re)seed the local registry

set -euo pipefail

# DEPLOY_SUBNET is the libvirt network of the deploy VMs (test/e2e/vm-deploy/Herdfile).
DEPLOY_SUBNET="192.168.124.0/24"
# NFS_EXPORT_PATH must match nfs_export_path in deploy/ansible/deploy-inventory.ini.
NFS_EXPORT_PATH="/srv/exports/cca-volumes"
# REGISTRY_PORT must match the registry in test/e2e/deploy.ccv (192.168.124.1:5000).
REGISTRY_PORT=5000

# HOST_PACKAGES: vmherd (libvirt/KVM + cloud-init ISO), Ansible deployment,
# source build in the workflow, local registry, NFS volumes and Caddy.
HOST_PACKAGES=(
    qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients virt-install
    dnsmasq-base genisoimage xxd
    ansible golang git jq curl rsync openssl
    docker.io nfs-kernel-server caddy
)

# REGISTRY_IMAGES maps upstream image = name in the local registry. The .cca
# workloads reference "{{ .registry }}/<name>" (test/e2e/workloads/*.cca).
REGISTRY_IMAGES=(
    "apache/airflow:2.10.5=airflow:2.10.5"
    "postgres:16=postgres:16"
    "redis:7=redis:7"
    "traefik/whoami:latest=whoami:latest"
    "nginx:alpine=library/nginx:alpine"
    "nginx:1.27=nginx:1.27"
    "nicolaka/netshoot:latest=netshoot:latest"
    "zabbix/zabbix-server-pgsql:alpine-7.4-latest=zabbix-server-pgsql:alpine-7.4-latest"
    "zabbix/zabbix-web-nginx-pgsql:alpine-7.4-latest=zabbix-web-nginx-pgsql:alpine-7.4-latest"
)

# log prints a progress line.
log() { echo "==> $*"; }

# install_host_packages installs everything the deploy host needs.
install_host_packages() {
    log "Installing host packages"
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -q "${HOST_PACKAGES[@]}" >/dev/null
    sudo systemctl enable --now libvirtd docker nfs-server >/dev/null 2>&1
}

# add_user_groups lets the current user drive libvirt and docker without sudo
# (vmherd and the Ansible registry task run docker/virsh as this user).
add_user_groups() {
    log "Adding $USER to libvirt and docker groups"
    sudo usermod -aG libvirt,docker "$USER"
    echo "    NOTE: log in again (and restart the GitHub runner service) for new groups to apply"
}

# start_local_registry runs registry:2 on all interfaces, port REGISTRY_PORT.
start_local_registry() {
    log "Starting local registry on :$REGISTRY_PORT"
    if sudo docker ps --format '{{.Names}}' | grep -q '^registry$'; then
        return
    fi
    sudo docker run -d --name registry --restart always -p "$REGISTRY_PORT:5000" registry:2 >/dev/null \
        || sudo docker start registry >/dev/null
}

# seed_registry_images copies every workload image into the local registry.
seed_registry_images() {
    log "Seeding local registry"
    for image_pair in "${REGISTRY_IMAGES[@]}"; do
        upstream_image="${image_pair%%=*}"
        local_name="${image_pair#*=}"
        sudo docker pull -q "$upstream_image" >/dev/null
        sudo docker tag "$upstream_image" "127.0.0.1:$REGISTRY_PORT/$local_name"
        sudo docker push -q "127.0.0.1:$REGISTRY_PORT/$local_name" >/dev/null
        echo "    $local_name"
    done
}

# export_nfs_volumes exports NFS_EXPORT_PATH to the deploy VM subnet.
export_nfs_volumes() {
    log "Exporting $NFS_EXPORT_PATH to $DEPLOY_SUBNET"
    sudo mkdir -p "$NFS_EXPORT_PATH"
    sudo chmod 0777 "$NFS_EXPORT_PATH"
    if ! grep -qF "$NFS_EXPORT_PATH " /etc/exports; then
        echo "$NFS_EXPORT_PATH $DEPLOY_SUBNET(rw,sync,no_subtree_check,no_root_squash)" | sudo tee -a /etc/exports >/dev/null
    fi
    sudo exportfs -ra
}

# install_vmherd installs the latest vmherd release if it is missing.
install_vmherd() {
    if command -v vmherd >/dev/null 2>&1; then
        return
    fi
    log "Installing vmherd"
    vmherd_version=$(curl -sS https://api.github.com/repos/boyadzhievb/vmherd/releases/latest | jq -r .tag_name)
    curl -sSL "https://github.com/boyadzhievb/vmherd/releases/download/${vmherd_version}/vmherd-${vmherd_version}-linux-amd64.tar.gz" -o /tmp/vmherd.tar.gz
    sudo tar xzf /tmp/vmherd.tar.gz -C /usr/local/bin/ vmherd
}

if [[ "${1:-}" == "--images" ]]; then
    seed_registry_images
    exit 0
fi

sudo -n true 2>/dev/null || { echo "error: passwordless sudo is required" >&2; exit 1; }
install_host_packages
add_user_groups
install_vmherd
start_local_registry
seed_registry_images
export_nfs_volumes
log "Done. Next: give this host's runner the 'deploy' label (design/deploy-host.md)"
