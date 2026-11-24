# Installation

CCattler can be installed as a single binary, built from source, or deployed across a cluster with Ansible.

## Prerequisites

| Requirement | Details |
|---|---|
| **OS** | Linux (Ubuntu 22.04+, Debian 12+, RHEL/AlmaLinux/Rocky 9) or macOS (Apple Silicon and Intel) |
| **Architecture** | amd64 or arm64 |
| **Go** | 1.22+ (only if building from source) |
| **Container runtime** | Docker, nerdctl, or Lima — required for `cca run-container`. Optional for simulation and process modes. |
| **etcd** | Required for multi-node clusters (installed by Ansible) |

### Prepare a fresh host

A minimal install ships without several tools these instructions use. Run the steps that apply, in order. Steps 2
and 3 have one block for **Debian and Ubuntu** and one for **RHEL, AlmaLinux and Rocky Linux 9**; run the one for your
distribution.

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
works the same on every distribution. Without sudo yet, replace `apt install -y sudo` with your distribution's command
from the table above (`su` asks for the root password; `su -` with the dash gives root's PATH, which has `visudo`):

```bash
su - -c "apt install -y sudo \
  && echo 'USERNAME ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/USERNAME-nopasswd \
  && chmod 0440 /etc/sudoers.d/USERNAME-nopasswd \
  && visudo -cf /etc/sudoers.d/USERNAME-nopasswd"
```

If you already have sudo (for example the first user on Ubuntu or Rocky), write the same file with it:

```bash
echo "$USER ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/$USER-nopasswd
sudo chmod 0440 /etc/sudoers.d/$USER-nopasswd
sudo visudo -cf /etc/sudoers.d/$USER-nopasswd
```

`visudo -cf` checks the syntax, because a broken sudoers file can lock you out of sudo. The file name must not contain
a `.`, because sudo ignores such files. To remove it: `sudo rm /etc/sudoers.d/USERNAME-nopasswd`. Interactive installs
do not need this.

**`sudo cca` on RHEL, AlmaLinux and Rocky.** sudo there runs commands with its own `secure_path`
(`/sbin:/bin:/usr/sbin:/usr/bin`), which leaves out `/usr/local/bin`, where the quick install puts `cca`. Every
`sudo cca …` below then fails with `command not found`. Add the directory once (Debian and Ubuntu already include it;
the quick install warns when it is missing):

```bash
echo 'Defaults secure_path="/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/bin"' \
    | sudo tee /etc/sudoers.d/ccattler-secure-path
sudo chmod 0440 /etc/sudoers.d/ccattler-secure-path
sudo visudo -cf /etc/sudoers.d/ccattler-secure-path
```

Or call `cca` by its full path: `sudo /usr/local/bin/cca …`.

**2. Install the download tools** (needed by the quick install below).

Debian and Ubuntu:

```bash
sudo apt update
sudo apt install -y curl tar ca-certificates
```

RHEL, AlmaLinux and Rocky (`curl` is preinstalled there, as `curl-minimal`):

```bash
sudo dnf install -y tar ca-certificates
```

**3. For a host that runs real containers or joins a cluster**, also install containerd, nftables, the CNI plugins and,
on the control-plane host only, etcd. The commands below need the CPU architecture in Go's naming:

```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')   # amd64 or arm64
```

Debian and Ubuntu:

```bash
# Container runtime and the nft tool used by the agent's data plane
sudo apt install -y containerd nftables

# Control-plane host only: etcd
sudo apt install -y etcd-server etcd-client

# CNI plugins (container networking)
CNI_VERSION=1.9.1
sudo mkdir -p /opt/cni/bin
curl -fsSL "https://github.com/containernetworking/plugins/releases/download/v${CNI_VERSION}/cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz" \
    | sudo tar -xz -C /opt/cni/bin
```

RHEL, AlmaLinux and Rocky:

```bash
# Container runtime from Docker's repository (containerd.io; it also installs container-selinux)
# and the nft tool used by the agent's data plane
sudo dnf install -y dnf-plugins-core
sudo dnf config-manager --add-repo https://download.docker.com/linux/rhel/docker-ce.repo
sudo dnf install -y containerd.io nftables
sudo systemctl enable --now containerd

# CNI plugins (container networking) from AppStream; they install to /usr/libexec/cni
sudo dnf install -y containernetworking-plugins

# Control-plane host only: etcd. No EL9 repository ships it, so install the release binary,
# checked against the release's SHA256SUMS
ETCD_VERSION=3.5.36
ETCD_RELEASE="etcd-v${ETCD_VERSION}-linux-${ARCH}"
cd "$(mktemp -d)"
curl -fsSLO "https://github.com/etcd-io/etcd/releases/download/v${ETCD_VERSION}/${ETCD_RELEASE}.tar.gz"
curl -fsSLO "https://github.com/etcd-io/etcd/releases/download/v${ETCD_VERSION}/SHA256SUMS"
grep " ${ETCD_RELEASE}.tar.gz\$" SHA256SUMS | sha256sum -c -
tar -xzf "${ETCD_RELEASE}.tar.gz"
sudo install -m 755 "${ETCD_RELEASE}/etcd" "${ETCD_RELEASE}/etcdctl" /usr/local/bin/
sudo restorecon /usr/local/bin/etcd /usr/local/bin/etcdctl   # SELinux label for systemd
cd -
```

`sha256sum -c` must print `OK`; stop if it does not. If the CNI package is not available, the Debian tarball commands
above work too (into `/opt/cni/bin`). If you cannot use Docker's repository, EPEL also ships `containerd`; EPEL needs
the CRB repository enabled (`sudo dnf config-manager --set-enabled crb && sudo dnf install -y epel-release`).

Verify:

```bash
systemctl is-active containerd     # active
ls /usr/sbin/nft                   # /usr/sbin/nft (not on a normal user's PATH on Debian)
ls /opt/cni/bin/bridge             # Debian/Ubuntu; /usr/libexec/cni/bridge on RHEL, AlmaLinux, Rocky
command -v etcd                    # control plane only
```

On Debian and Ubuntu, `etcd-server` starts etcd right away without TLS, listening only on `127.0.0.1`. That is
expected: `setup.sh` (below) reconfigures it with TLS. On RHEL, AlmaLinux and Rocky, etcd has no service yet:
`setup.sh` creates the `etcd` user and writes `etcd.service`, with its data in `/var/lib/etcd/default`.

**SELinux and firewalld (RHEL, AlmaLinux, Rocky).** Leave both on. `container-selinux`, installed with `containerd.io`,
is the policy for containers. The quick install restores the SELinux label of `cca`, so systemd can run it. When
firewalld is running, `setup.sh` opens 9770/tcp (API), 2379/tcp (etcd clients, used by agents on worker hosts) and
4789/udp (VXLAN), and enables masquerading; with ufw active on Debian or Ubuntu it opens the same ports.

## Quick install

One command — detects your OS and architecture, downloads the right binary, installs to `/usr/local/bin/`:

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

Works on Linux (amd64/arm64) and macOS (Intel/Apple Silicon). On SELinux hosts it restores the binary's SELinux label.
When `/usr/local/bin` is not on sudo's `secure_path`, it warns and prints the fix from
[step 1 of Prepare a fresh host](#prepare-a-fresh-host).

Pin a specific version (the variable must be set for `bash`, which runs the script, not for `curl`):

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh \
    | CCATTLER_VERSION=v1.95.0-beta bash      # any tag from GitHub Releases
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

## Set up a cluster by hand

These are the steps the Ansible playbook runs, done by hand. In the commands, replace `CONTROL_PLANE_IP` with the
control-plane host's address, `WORKER_IP` with the worker's address, and `worker-1` with the worker's name.

### 1. Control-plane host

This host also runs an agent, so it runs containers too: **a single-host cluster is just this step** (then step 3).

Install the prerequisites, including etcd ([Prepare a fresh host](#prepare-a-fresh-host)),
then the `cca` binary ([Quick install](#quick-install)). Then:

```bash
# Generate the cluster CA, this host's certificates, the secret root key and a setup script
cca init --advertise-address CONTROL_PLANE_IP --node-id ctrl -o ~/cca-init

# Review the script, then run it: installs the certificates under /etc/ccattler,
# configures etcd with TLS and writes the cca-server and cca-agent systemd units
less ~/cca-init/setup.sh
sudo ~/cca-init/setup.sh

# Start the server (API, controllers, DNS) and this host's agent
sudo systemctl enable --now cca-server cca-agent

# Check: the node shows as alive
sudo cca status
```

`~/cca-init` holds the CA private key and the secret root key. Once `setup.sh` has copied them to `/etc/ccattler`,
keep a backup of that directory somewhere safe and delete `~/cca-init`.

If something fails, read the logs: `sudo journalctl -u cca-server -u cca-agent -u etcd -e`.

### 2. Worker hosts

On each worker, install the prerequisites without etcd ([step 3 of Prepare a fresh host](#prepare-a-fresh-host))
and the `cca` binary.

Create a join token **on the control-plane host**. It works once, for this worker name, for 15 minutes:

```bash
sudo cca token create --node-id worker-1 --ttl 15m
```

**On the worker**, join and point the agent at the control plane:

```bash
# Enroll: writes this node's certificate and the cluster CA to /etc/ccattler/pki
sudo cca join https://CONTROL_PLANE_IP:9770 <token> --node-id worker-1

# Tell the agent where etcd is
echo "https://CONTROL_PLANE_IP:2379" | sudo tee /etc/ccattler/endpoints
```

Create `/etc/systemd/system/cca-agent.service` (for example with `sudo nano`):

```ini
[Unit]
Description=CCattler Agent
After=network.target containerd.service
Requires=containerd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/cca agent \
  --node-id worker-1 \
  --advertise-address WORKER_IP \
  --api-server CONTROL_PLANE_IP:9770 \
  --cert /etc/ccattler/pki/node.pem \
  --key /etc/ccattler/pki/node-key.pem \
  --ca /etc/ccattler/pki/ca.pem \
  --etcd-cert /etc/ccattler/pki/node.pem \
  --etcd-key /etc/ccattler/pki/node-key.pem \
  --etcd-ca /etc/ccattler/pki/ca.pem \
  --dns-upstream CONTROL_PLANE_IP:15353
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Start it, then check from the control-plane host that the worker shows as alive:

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now cca-agent
sudo cca status        # on the control-plane host
```

### 3. Use the CLI without sudo

`sudo cca …` works on the control-plane host because root can read the node's own certificate in `/etc/ccattler/pki`.
That certificate is the machine's identity, with full rights, so don't use it for daily work. Give yourself a user
account instead (replace `USERNAME`):

```bash
sudo cca user create USERNAME
sudo cca role grant user:USERNAME --capability cluster.admin --scope cluster   # or e.g. workload.read for read-only
export CCA_TOKEN=$(sudo cca user token USERNAME --ttl 24h)
cca status
```

The token is valid for the `--ttl` you chose. Create a new one when it expires. `export` sets it only for the
current shell: in a new terminal or after logging in again, `cca status` fails with `client certificate required`
until you run the `export` line again. To keep it across logins, put the `export` line in `~/.bashrc`.

### 4. Upgrade

Install the new binary on every host, then restart the services, the control plane first:

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
sudo systemctl restart cca-server cca-agent   # control-plane host
sudo systemctl restart cca-agent              # each worker, one at a time
sudo cca status                               # every node alive again
```

The running services keep the old binary until they restart. Read the [changelog](/changelog) for **BREAKING**
entries before upgrading.

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
