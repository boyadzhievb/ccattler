# Installation

CCattler can be installed as a single binary, built from source, or deployed across a cluster with Ansible.

## Prerequisites

| Requirement | Details |
|---|---|
| **OS** | Linux (Ubuntu 22.04+, Debian 12+) or macOS (for local dev) |
| **Architecture** | amd64 or arm64 |
| **Go** | 1.22+ (only if building from source) |
| **containerd + nerdctl** | Required for container runtime; optional for simulation mode. Docker Desktop includes both. |
| **etcd** | Required for multi-node clusters (installed by Ansible) |

## Binary download

Download the latest release:

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

Or download manually from [GitHub Releases](https://github.com/boyadzhievb/ccattler/releases).

Verify:

```bash
cca version
# cca v0.20.1

cca demo
# Runs simulated deployment
```

## Build from source

```bash
git clone https://github.com/boyadzhievb/ccattler
cd ccattler
go build -o cca ./cmd/cca/
sudo mv cca /usr/local/bin/
```

## Demo cluster (Vagrant + libvirt)

Spin up a full two-node cluster on local VMs with one command:

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install-demo.sh | bash
```

**Requirements:** `ansible`, `vagrant`, and `libvirt` on the host machine.

This creates 2 VMs, deploys CCattler (etcd + server + agents), and runs a Java test application to validate the cluster.

### Lima (macOS)

For macOS users without libvirt:

```bash
cd deploy/lima
./install-lima-demo.sh
```

Creates a single Lima VM with an all-in-one CCattler deployment.

## Production deployment (Ansible)

The recommended way to deploy CCattler on existing hosts.

### Step 1: Download and create inventory

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

First run downloads the Ansible playbook and `cca` binary, then creates an inventory template at `~/.ccattler/ansible/inventory.ini`.

### Step 2: Edit the inventory

```ini
[controlplane]
ctrl ansible_host=192.168.1.10 ansible_user=ubuntu

[workers]
worker-1 ansible_host=192.168.1.11 ansible_user=ubuntu
worker-2 ansible_host=192.168.1.12 ansible_user=ubuntu
```

**Requirements:**
- Passwordless SSH to all nodes (key-based authentication)
- `curl`, `tar`, and `ansible` on the machine running the script

### Step 3: Deploy

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

Second run detects the existing inventory and deploys CCattler across your hosts. The Ansible playbook handles:

- etcd installation and cluster formation
- Certificate generation (internal CA)
- Control plane startup (`cca server --listen 0.0.0.0:9770 --tls --dns`)
- Agent enrollment on worker nodes
- DNS server and HTTP proxy setup

### What gets deployed

| Component | Location | Service |
|---|---|---|
| etcd | Control plane node | `etcd.service` |
| cca server | Control plane node | `cca-server.service` |
| cca agent | Every worker node | `cca-agent.service` |
| Certificates | `/etc/ccattler/` | Auto-generated CA + node certs |

## Manual multi-host setup

If you prefer to set things up manually:

### Start the server

```bash
cca server --listen 0.0.0.0:9770 --tls
```

Auto-generates an ephemeral CA and server certificate. Writes `ca.pem` to `.ccattler/` for client trust.

### Create a join token

```bash
cca token create --node-id worker-1 --ttl 15m
# Token: cca_abc123...
# Join command: cca join 192.168.1.10:9770 cca_abc123... --node-id worker-1
```

### Join a node

On each worker:

```bash
cca join 192.168.1.10:9770 <token> --node-id worker-1 --ca-cert /path/to/ca.pem
```

The node generates a private key, sends a CSR to the server, receives a signed certificate, and stores credentials in `.ccattler/`.

### Start the agent

```bash
cca agent --node-id worker-1
```

The agent auto-discovers certificates from `.ccattler/` (created by `cca join`).

### Using external certificates

For environments with an existing PKI:

```bash
# Server
cca server --listen 0.0.0.0:9770 --cert server.pem --key server-key.pem --ca ca.pem

# Agent
cca agent --node-id worker-1 --cert node.pem --key node-key.pem --ca ca.pem
```

## Token management

```bash
# List active tokens
cca token list

# Revoke a token
cca token revoke <token>
```

Tokens are single-use and short-lived (default 15 minutes). After a node joins, its bootstrap token is destroyed.
