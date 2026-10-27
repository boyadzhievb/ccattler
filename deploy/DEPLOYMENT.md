# CCattler Deployment Guide

## Environments

CCattler supports three deployment environments, all managed by `test/e2e/cluster_test.sh`:

| Environment | Nodes | Network | Lifecycle |
|---|---|---|---|
| `--env test` (default) | 3 bare-metal on 192.168.100.{215,30,31} | Physical LAN | Always-on |
| `--env vagrant` | 3 libvirt VMs on 192.168.122.x | NAT bridge | Ephemeral (destroyed after test) |
| `--env deploy` | 3 libvirt VMs on 192.168.124.x | Isolated bridge | Long-lived (stopped, not destroyed) |

The `deploy` environment is the standard development cluster. VMs are managed by [vmherd](https://github.com/boyadzhievb/vmherd) on the testbed host (192.168.100.43).

## Prerequisites

- SSH access to the testbed host (192.168.100.43)
- Go toolchain (cross-compiles the `cca` binary for linux/amd64)
- Ansible (on the machine running the playbook)
- For VM environments: libvirt/KVM and vmherd on the testbed host

## Quick Start (Deploy Environment)

From the testbed host (192.168.100.43):

```bash
cd /path/to/ccattler

# Full deploy: create VMs, provision, deploy workloads
./test/e2e/cluster_test.sh --env deploy --workload all

# Deploy specific workloads only
./test/e2e/cluster_test.sh --env deploy --workload airflow,debug

# Binary update only (skip VM provisioning)
./test/e2e/cluster_test.sh --env deploy --skip-build --from-release
```

## Step-by-Step Manual Deployment

### 1. Create VMs (deploy environment only)

```bash
cd test/e2e/vm-deploy
vmherd up
```

This creates 3 Ubuntu Noble VMs using the `Herdfile`:
- `cca-deploy-ctrl` (192.168.124.10) — control plane + etcd + worker
- `cca-deploy-worker-1` (192.168.124.20) — worker
- `cca-deploy-worker-2` (192.168.124.30) — worker

Each VM: 2 CPUs, 6 GiB RAM, 20 GiB disk.

### 2. Build the Binary

```bash
cd deploy/ansible
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o cca-linux-amd64 ../../cmd/cca/
```

Or use a release binary:
```bash
curl -sL https://github.com/<org>/ccattler/releases/latest/download/cca-linux-amd64 -o cca-linux-amd64
chmod +x cca-linux-amd64
```

### 3. Run Ansible

```bash
cd deploy/ansible

# Full deployment (clean + provision + demo workload)
ansible-playbook -i deploy-inventory.ini baremetal-deploy.yml \
  -e vmherd_ssh_key=$(vmherd info ssh-key)

# Update binary only (skip cleanup)
ansible-playbook -i deploy-inventory.ini baremetal-deploy.yml \
  -e vmherd_ssh_key=$(vmherd info ssh-key) \
  --skip-tags cleanup,testapp
```

### 4. Push Images to Local Registry

When using an insecure registry (e.g., `192.168.100.43:5000`), push workload images first:

```bash
# On a machine with Docker and internet access
docker pull nicolaka/netshoot:latest
docker tag nicolaka/netshoot:latest 192.168.100.43:5000/netshoot:latest
docker push 192.168.100.43:5000/netshoot:latest
```

Pass the registry to Ansible:
```bash
ansible-playbook -i deploy-inventory.ini baremetal-deploy.yml \
  -e vmherd_ssh_key=$(vmherd info ssh-key) \
  -e insecure_registry=192.168.100.43:5000
```

This configures containerd on each node with `/etc/containerd/certs.d/<registry>/hosts.toml` to allow HTTP pulls.

### 5. Deploy Workloads

```bash
# SSH to the control plane node
vmherd ssh cca-deploy-ctrl

# Apply a workload
cca apply /var/lib/ccattler/examples/whoami.cca

# Or from the testbed host, using cluster_test.sh
./test/e2e/cluster_test.sh --env deploy --workload debug
```

## What Ansible Does

The playbook runs these roles in order:

### cleanup (skippable via `--skip-tags cleanup`)
Stops all CCattler services, removes PKI, etcd data, containers, VXLAN interfaces, and network config.

### common (all nodes)
1. Validates OS version (Ubuntu 22.04+ or Debian 12+)
2. Cross-compiles `cca` binary from Go source
3. Starts local Docker registry (if `insecure_registry` is set)
4. Disables `systemd-resolved`, sets up `dnsmasq` with upstream DNS and `cca.local` forwarding
5. Enables IP forwarding, `br_netfilter`, VXLAN firewall rules
6. Installs containerd and CNI plugins (bridge, VXLAN, etc.)
7. Configures containerd `hosts.toml` for insecure registry
8. Copies `cca` binary to `/usr/local/bin/cca`
9. Creates CCattler directories (`/etc/ccattler`, `/var/lib/ccattler`, `/var/log/ccattler`)

### controlplane
1. Runs `cca init` to generate PKI, etcd TLS config, and systemd units
2. Starts `cca-server`
3. Copies example `.cca` files to the node

### worker
1. Configures containerd
2. Creates a join token via `cca token create`
3. Joins the cluster via `cca join`
4. Removes stale `cca0` bridge interface from previous deployments
5. Installs and starts `cca-agent` systemd unit
6. Cleans stale BPF pins (if eBPF data plane)
7. Waits for agent to register via the server API
8. Verifies eBPF programs loaded (if eBPF data plane)

## Checking Logs

```bash
# Agent logs (human-readable format)
journalctl -u cca-agent --no-pager -n 50

# Server logs
journalctl -u cca-server --no-pager -n 50

# Follow logs in real time
journalctl -u cca-agent -f

# Filter by log level
journalctl -u cca-agent --no-pager | grep WARN
journalctl -u cca-agent --no-pager | grep ERROR
```

Log format is human-readable by default (matches standard Linux service output). Use `--log-format json` for structured JSON output.

## Checking Cluster Status

```bash
# Cluster overview (nodes, services, instances)
cca status

# List instances with placement and probe status
cca get instances

# Watch events in real time
cca events

# Watch a specific fact prefix
cca watch /observed/

# Node resource usage
cca top nodes

# Workload resource usage
cca top workloads

# Inspect a specific node
cca node-inspect <node-id>

# Execute a command in a running container
cca exec <instance-id> -- sh
```

## Resetting the Cluster

```bash
cd deploy/ansible

# Full reset (stops services, removes all state)
ansible-playbook -i deploy-inventory.ini baremetal-reset.yml \
  -e vmherd_ssh_key=$(vmherd info ssh-key)
```

This removes all containers, VXLAN/bridge interfaces, nftables/iptables rules, PKI, etcd data, and systemd units.

## Destroying VMs

```bash
cd test/e2e/vm-deploy
vmherd destroy
```

## Troubleshooting

### Image Pull Failures
- Verify the local registry is running: `curl http://192.168.100.43:5000/v2/_catalog`
- Check containerd registry config: `cat /etc/containerd/certs.d/192.168.100.43:5000/hosts.toml`
- The `cca` binary reads `hosts.toml` via `ConfigureHosts` — containerd's CRI `config_path` alone is not enough

### CNI Bridge Conflicts
- A stale `cca0` interface (not a bridge type) from a previous deployment blocks CNI setup
- Ansible automatically removes non-bridge `cca0` interfaces before starting the agent
- Manual fix: `sudo ip link del cca0` then `sudo systemctl restart cca-agent`

### DNS Resolution
- CCattler runs its own DNS forwarder on the control-plane advertise address, port 15353
- `dnsmasq` on each node forwards `*.cca.local` queries to the CCattler DNS server
- Check: `dig @127.0.0.1 <service>.cca.local`

### OOM Kills
- `cca get instances` shows OOM kills in the REASON column (e.g., `startup:7/60 OOM killed: memory limit 2048Mi`)
- Agent logs emit WARN when OOM kills are detected via cgroup `memory.events`
- Increase memory limits in the `.cca` workload file or increase VM memory in the Herdfile

### etcd Timeouts During Image Pull
- Large image pulls (multi-minute) can block the agent's heartbeat loop, causing the node to be marked stale
- The node recovers automatically once the pull completes and heartbeats resume

### Nodes Not Registering
- Check agent logs: `journalctl -u cca-agent --no-pager -n 20`
- Verify join succeeded: `ls /etc/ccattler/secret-master.key`
- Check server is reachable: `curl -k https://<ctrl-ip>:9770/healthz`

## Network Architecture

- **VXLAN overlay** (`cca-vxlan0`, VNI 42, MTU 1450): cross-node container traffic
- **CNI bridge** (`cca0`): per-node container bridge, each node gets a /24 from `--cluster-cidr`
- **VIP pool** (`--vip-cidr`): one VIP per exposed service, nftables DNAT to container endpoints
- **CIDRs are randomized** on each fresh deployment (seeded by deploy timestamp) to prove nothing is hardcoded
