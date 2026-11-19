# Installation

CCattler can be installed as a single binary, built from source, or deployed across a cluster with Ansible.

## Prerequisites

| Requirement | Details |
|---|---|
| **OS** | Linux (Ubuntu 22.04+, Debian 12+) or macOS (Apple Silicon and Intel) |
| **Architecture** | amd64 or arm64 |
| **Go** | 1.22+ (only if building from source) |
| **Container runtime** | Docker, nerdctl, or Lima — required for `cca run-container`. Optional for simulation and process modes. |
| **etcd** | Required for multi-node clusters (installed by Ansible) |

### Prepare a fresh Debian or Ubuntu host

A minimal install ships without several tools these instructions use. Run the steps that apply, in order. Step 1
works on any distribution; steps 2 and 3 use Debian/Ubuntu package names, the distributions CCattler is tested on.

**1. Get `sudo`.** This step is the same on every Linux distribution except for the package manager and the name
of the admin group. Check:

```bash
sudo -v
```

If that prints `sudo: command not found` or `user is not in the sudoers file`, become root, install sudo if it is
missing, and add your user to the admin group (replace `USERNAME` with your login name):

| Distribution | Install sudo | Admin group |
|---|---|---|
| Debian | `apt install -y sudo` (missing when a root password was set during installation) | `sudo` |
| Ubuntu | preinstalled; the first user already has it | `sudo` |
| RHEL, AlmaLinux, Rocky, Fedora | `dnf install -y sudo` (usually preinstalled) | `wheel` |
| Arch, openSUSE | `pacman -S sudo` / `zypper install sudo` | `wheel` (uncomment the `%wheel` line with `visudo`) |

Debian example:

```bash
su -
apt update && apt install -y sudo
usermod -aG sudo USERNAME      # wheel instead of sudo on RHEL, Fedora, Arch, openSUSE
exit
```

Log out and back in (group membership is read at login), then run `sudo -v` again.

**Passwordless sudo (only for automation).** Ansible and other non-interactive tools that connect over SSH cannot
answer a password prompt. On those hosts, allow `USERNAME` to use sudo without a password instead. The drop-in file
works the same on every distribution; replace `apt install -y sudo` with your distribution's command from the table
above (`su` asks for the root password):

```bash
su -c "apt install -y sudo \
  && echo 'USERNAME ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/USERNAME-nopasswd \
  && chmod 0440 /etc/sudoers.d/USERNAME-nopasswd \
  && visudo -cf /etc/sudoers.d/USERNAME-nopasswd"
```

`visudo -cf` checks the syntax, because a broken sudoers file can lock you out of sudo. The file name must not contain
a `.`, because sudo ignores such files. To remove it: `sudo rm /etc/sudoers.d/USERNAME-nopasswd`. Interactive installs
do not need this.

**2. Install the download tools** (needed by the quick install below):

```bash
sudo apt update
sudo apt install -y curl tar ca-certificates
```

**3. For a host that runs real containers or joins a cluster**, also install:

```bash
# Container runtime and the nft tool used by the agent's data plane
sudo apt install -y containerd nftables

# Control-plane host only: etcd
sudo apt install -y etcd-server etcd-client

# CNI plugins (container networking)
CNI_VERSION=1.9.1
ARCH=$(dpkg --print-architecture)   # amd64 or arm64
sudo mkdir -p /opt/cni/bin
curl -fsSL "https://github.com/containernetworking/plugins/releases/download/v${CNI_VERSION}/cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz" \
    | sudo tar -xz -C /opt/cni/bin
```

Verify:

```bash
systemctl is-active containerd     # active
command -v nft                     # /usr/sbin/nft
ls /opt/cni/bin/bridge             # /opt/cni/bin/bridge
command -v etcd                    # control plane only
```

## Quick install

One command — detects your OS and architecture, downloads the right binary, installs to `/usr/local/bin/`:

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

Works on Linux (amd64/arm64) and macOS (Intel/Apple Silicon).

Pin a specific version (the variable must be set for `bash`, which runs the script, not for `curl`):

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh \
    | CCATTLER_VERSION=v1.92.0-beta bash
```

Release tags are listed on [GitHub Releases](https://github.com/boyadzhievb/ccattler/releases).

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
