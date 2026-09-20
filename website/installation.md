# Installation

CCattler can be installed as a single binary, built from source, or deployed across a cluster with Ansible.

## Prerequisites

| Requirement | Details |
|---|---|
| **OS** | Linux (Ubuntu 22.04+, Debian 12+) or macOS (Apple Silicon and Intel) |
| **Architecture** | amd64 or arm64 |
| **Go** | 1.22+ (only if building from source) |
| **containerd + nerdctl** | Required for container runtime; optional for simulation mode. Docker Desktop includes both. |
| **etcd** | Required for multi-node clusters (installed by Ansible) |

## Quick install

One command — detects your OS and architecture, downloads the right binary, installs to `/usr/local/bin/`:

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

Works on Linux (amd64/arm64) and macOS (Intel/Apple Silicon).

Pin a specific version:

```bash
CCATTLER_VERSION=v0.38.0 curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

Verify:

```bash
cca version
```

Or download manually from [GitHub Releases](https://github.com/boyadzhievb/ccattler/releases).

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

**What it does:**
1. Installs the `cca` binary on your machine (via `install.sh`)
2. Downloads Ansible playbooks and example configurations
3. Creates 2 Vagrant VMs with libvirt
4. Deploys CCattler (etcd + server + agents) and a Java test application

**Requirements:** `curl`, `tar`, `ansible`, `vagrant`, and `libvirt` on the host machine.

### Lima (macOS)

For macOS users without libvirt:

```bash
cd deploy/lima
./install-lima-demo.sh
```

Creates a single Lima VM with an all-in-one CCattler deployment.

## Production deployment (Ansible)

The recommended way to deploy CCattler on existing hosts. Uses `install-demo.sh` in inventory mode.

### Step 1: Download playbooks and create inventory

```bash
DEMO_MODE=inventory curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install-demo.sh | bash
```

First run installs the `cca` binary, downloads the Ansible playbook, and creates an inventory template at `~/.ccattler/ansible/inventory.ini`.

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
DEMO_MODE=inventory curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install-demo.sh | bash
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
