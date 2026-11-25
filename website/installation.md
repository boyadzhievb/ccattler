# Installation

CCattler can be installed as a single binary, built from source, or deployed across a cluster with Ansible.

## Prerequisites

| Requirement | Details |
|---|---|
| **OS** | Linux (Ubuntu 22.04+, Debian 12+, RHEL/AlmaLinux/Rocky 9 and 10) or macOS (Apple Silicon and Intel) |
| **Architecture** | amd64 or arm64 |
| **Go** | 1.22+ (only if building from source) |
| **Container runtime** | Docker, nerdctl, or Lima — required for `cca run-container`. Optional for simulation and process modes. |
| **etcd** | Required for multi-node clusters (installed by Ansible) |

### Prepare a fresh host

A minimal install ships without several tools these instructions use. Run the steps that apply, in order. Steps 2
and 3 have one block for **Debian and Ubuntu** and one for **RHEL, AlmaLinux and Rocky Linux 9 and 10**; run the one for your
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
  && echo 'USERNAME ALL=(ALL) NOPASSWD: ALL' > /root/USERNAME-nopasswd \
  && visudo -cf /root/USERNAME-nopasswd \
  && install -o root -g root -m 0440 /root/USERNAME-nopasswd /etc/sudoers.d/USERNAME-nopasswd \
  && rm /root/USERNAME-nopasswd"
```

If you already have sudo (for example the first user on Ubuntu or Rocky), write the same file with it:

```bash
sudoers_file=$(mktemp)
echo "$USER ALL=(ALL) NOPASSWD: ALL" > "$sudoers_file"
sudo visudo -cf "$sudoers_file" \
  && sudo install -o root -g root -m 0440 "$sudoers_file" "/etc/sudoers.d/$USER-nopasswd"
rm "$sudoers_file"
```

Both write the rule to a file outside `/etc/sudoers.d` first and install it only after `visudo -cf` has checked the
syntax, because a broken sudoers file can lock you out of sudo. The file name in `/etc/sudoers.d` must not contain a
`.` (sudo silently ignores such files), so if your user name has one, use another file name, such as
`first_last-nopasswd`. To remove it: `sudo rm /etc/sudoers.d/USERNAME-nopasswd`. Interactive installs
do not need this.

**`sudo cca` on RHEL, AlmaLinux and Rocky.** sudo there runs commands with its own `secure_path`
(`/sbin:/bin:/usr/sbin:/usr/bin`), which leaves out `/usr/local/bin`, where the quick install puts `cca`. Every
`sudo cca …` below then fails with `command not found`. Add the directory once (Debian and Ubuntu already include it):

```bash
sudoers_file=$(mktemp)
echo 'Defaults secure_path="/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/bin"' > "$sudoers_file"
sudo visudo -cf "$sudoers_file" \
  && sudo install -o root -g root -m 0440 "$sudoers_file" /etc/sudoers.d/ccattler-secure-path
rm "$sudoers_file"
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
```

CNI plugins (container networking) on **EL9** (RHEL, AlmaLinux, Rocky 9), from AppStream into `/usr/libexec/cni`:

```bash
sudo dnf install -y containernetworking-plugins
```

CNI plugins on **EL10** (RHEL, AlmaLinux, Rocky 10): no EL10 repository ships them, so install the release tarball into
`/opt/cni/bin`, with the same commands as on Debian:

```bash
CNI_VERSION=1.9.1
sudo mkdir -p /opt/cni/bin
curl -fsSL "https://github.com/containernetworking/plugins/releases/download/v${CNI_VERSION}/cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz" \
    | sudo tar -xz -C /opt/cni/bin
```

etcd, on the control-plane host only (EL9 and EL10):

```bash
# Control-plane host only: etcd. No EL9 or EL10 repository ships it, so install the release binary,
# checked against the SHA-256 pinned here (see below). Each step runs only if the one before succeeded.
ETCD_VERSION=3.5.36
case "$ARCH" in
    amd64) ETCD_SHA256=2733753e388c044b178290dd35fb1263f919254b882f602425848b8c13b54e6d ;;
    arm64) ETCD_SHA256=f1b93e5089b3f4b3e6d40b8377475e8ee675bbcc971de553daa9a79143441993 ;;
esac
ETCD_RELEASE="etcd-v${ETCD_VERSION}-linux-${ARCH}"
cd "$(mktemp -d)" \
  && curl -fsSLO "https://github.com/etcd-io/etcd/releases/download/v${ETCD_VERSION}/${ETCD_RELEASE}.tar.gz" \
  && echo "${ETCD_SHA256}  ${ETCD_RELEASE}.tar.gz" | sha256sum -c - \
  && tar -xzf "${ETCD_RELEASE}.tar.gz" \
  && sudo install -o root -g root -m 0755 "${ETCD_RELEASE}/etcd" "${ETCD_RELEASE}/etcdctl" /usr/local/bin/ \
  && sudo restorecon /usr/local/bin/etcd /usr/local/bin/etcdctl \
  && cd -
```

`sha256sum -c` must print `OK`; if it prints `FAILED`, nothing is installed. The two SHA-256 values are copied from the
`SHA256SUMS` file of the [etcd v3.5.36 release](https://github.com/etcd-io/etcd/releases/tag/v3.5.36)
(`https://github.com/etcd-io/etcd/releases/download/v3.5.36/SHA256SUMS`, read 2026-11-24); a different
`ETCD_VERSION` needs the values from that version's file. `restorecon` gives the binaries the SELinux label systemd
needs. If you cannot use Docker's repository, EPEL also ships `containerd`; EPEL needs
the CRB repository enabled (`sudo dnf config-manager --set-enabled crb && sudo dnf install -y epel-release`).

Verify:

```bash
systemctl is-active containerd     # active
ls /usr/sbin/nft                   # /usr/sbin/nft (not on a normal user's PATH on Debian)
ls /opt/cni/bin/bridge             # Debian/Ubuntu and EL10; on EL9: ls /usr/libexec/cni/bridge
command -v etcd                    # control plane only
```

On Debian and Ubuntu, `etcd-server` starts etcd right away without TLS, listening only on `127.0.0.1`. That is
expected: `setup.sh` (below) reconfigures it with TLS. On RHEL, AlmaLinux and Rocky, etcd has no service yet:
`setup.sh` creates the `etcd` user and writes `etcd.service`, with its data in `/var/lib/etcd/default`.

**SELinux and firewalld (RHEL, AlmaLinux, Rocky).** Leave both on. `container-selinux`, installed with `containerd.io`,
is the policy for containers. The quick install restores the SELinux label of `cca`, so systemd can run it. When
firewalld is running, `setup.sh` opens 9770/tcp (API) and 4789/udp (VXLAN) and enables masquerading; with ufw active
on Debian or Ubuntu it opens the same ports. It opens 2379/tcp (etcd clients, used by agents on worker hosts) only
from the networks given to `cca init --etcd-client-cidr` (see [Set up a cluster by hand](#set-up-a-cluster-by-hand)).

## Quick install

One command: it downloads the binary for your OS and CPU, checks its SHA-256 against the release's `checksums.txt`,
and installs `cca` owned by root (mode 0755) into `/usr/local/bin/`. It installs nothing else; the prerequisites for a
cluster host are in [Prepare a fresh host](#prepare-a-fresh-host).

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

Works on Linux (amd64/arm64) and macOS (Intel/Apple Silicon). On RHEL, AlmaLinux and Rocky with SELinux enabled it
also runs `restorecon` on the binary, so systemd can run it.

Or install it by hand. Each archive, `cca-<version>-linux-<arch>.tar.gz`, holds `cca` and `k8s-to-cca`:

```bash
VERSION=v1.97.0-beta   # any tag from GitHub Releases
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')   # amd64 or arm64
cd "$(mktemp -d)" \
  && curl -fsSL "https://github.com/boyadzhievb/ccattler/releases/download/${VERSION}/cca-${VERSION}-linux-${ARCH}.tar.gz" | tar -xz cca \
  && sudo install -o root -g root -m 0755 cca /usr/local/bin/cca \
  && cd -
sudo restorecon /usr/local/bin/cca   # RHEL family only
```

The download runs in a new private directory, and each step runs only if the one before it succeeded, so a failed
download never installs another file named `cca`. Install into a directory only root can write, such as
`/usr/local/bin`: whoever can write the directory can replace the binary systemd and `sudo` run as root.

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
sudo install -o root -g root -m 0755 cca /usr/local/bin/
# SELinux hosts (RHEL, AlmaLinux, Rocky): give it the label systemd needs
if command -v selinuxenabled >/dev/null && selinuxenabled; then sudo restorecon /usr/local/bin/cca; fi
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
| Certificates | `/etc/ccattler/pki` | Three CAs (cluster identity, etcd access, gate HTTPS) and each process's own certificates |

## Set up a cluster by hand

These are the steps the Ansible playbook runs, done by hand. In the commands, replace `CONTROL_PLANE_IP` with the
control-plane host's address, `WORKER_IP` with the worker's address, and `worker-1` with the worker's name.

### 1. Control-plane host

This host also runs an agent, so it runs containers too: **a single-host cluster is just this step** (then step 3).

Install the prerequisites, including etcd ([Prepare a fresh host](#prepare-a-fresh-host)),
then the `cca` binary ([Quick install](#quick-install)). Then:

```bash
# Generate the three cluster CAs, this host's certificates, the secret root key and a setup script
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

**Adding worker hosts later?** Their agents reach etcd on this host on 2379/tcp. With firewalld or ufw active,
`setup.sh` opens that port only from the networks you name: etcd has no per-client permissions yet, so every etcd
client certificate (the server's and each node's) has full access to it. Add `--etcd-client-cidr` with the network of your worker hosts to the `cca init` line, for example
`--etcd-client-cidr 192.168.1.0/24` (repeat the flag or separate several networks with commas). Without it the port
stays closed, and `setup.sh` prints the firewalld and ufw commands that open it later.

`cca init` writes three CAs, each for one purpose, and one certificate per process; every file starts with a
`# CCattler ...` line that says what it is:

| File | What it is |
|---|---|
| `cluster-identity-ca.pem` | CA of the API server and node identities; the API trusts only it |
| `etcd-access-ca.pem` | CA etcd trusts; it signs etcd's serving certificate and the etcd client certificates |
| `gate-https-ca.pem` | CA of auto gate certificates: clients of TLS gates trust it |
| `api-server-identity-certificate.pem` | `cca server`'s API certificate |
| `node-identity-for-api.pem` | `cca agent`'s identity to the API |
| `etcd-serving-certificate.pem`, `etcd-peer-certificate.pem` | etcd's serving certificate, and the one HA etcd members present to each other |
| `cca-server-etcd-client-certificate.pem`, `node-etcd-client-certificate.pem` | the server's and the agent's etcd client certificates |

Each has its key next to it with `-key.pem`. Clusters set up by an earlier release keep `ca.pem` and `node.pem`;
`cca server` warns at every start that they share one CA, and re-initializing with `cca init` separates them. On
such a cluster, gate certificates issued before the upgrade can still act as API and etcd client certificates until
they expire, up to 90 days; re-initializing ends that at once.

**Another control-plane host (HA).** `cca init` on the new host reuses the first host's CAs: on a cluster set up with
this release, give it all six flags, `--ca`, `--ca-key`, `--etcd-access-ca`, `--etcd-access-ca-key`, `--gate-https-ca`
and `--gate-https-ca-key`, with the files copied from the first host. On a cluster set up by an earlier release (one
CA), give only `--ca /etc/ccattler/pki/ca.pem --ca-key /etc/ccattler/pki/ca-key.pem`: `cca init` then writes that
cluster's file layout (`ca.pem`, `node.pem`, `etcd.pem`). Any other combination is refused.

`~/cca-init` holds the CA private keys and the secret root key. Once `setup.sh` has copied them to `/etc/ccattler`,
keep a backup of that directory somewhere safe and delete `~/cca-init`.

If something fails, read the logs: `sudo journalctl -u cca-server -u cca-agent -u etcd -e`.

### 2. Worker hosts

On each worker, install the prerequisites without etcd ([step 3 of Prepare a fresh host](#prepare-a-fresh-host))
and the `cca` binary.

Workers need etcd's port 2379/tcp on the control-plane host. If that host runs firewalld or ufw, it is open only when
`cca init` there got `--etcd-client-cidr` with the workers' network (step 1). Otherwise open it there yourself, for the
workers' network only, with the command `setup.sh` printed, for example on firewalld:

```bash
sudo firewall-cmd --permanent \
    --add-rich-rule='rule family="ipv4" source address="192.168.1.0/24" port port="2379" protocol="tcp" accept'
sudo firewall-cmd --reload
```

Create a join token **on the control-plane host**. It works once, for this worker name, for 15 minutes:

```bash
sudo cca token create --node-id worker-1 --ttl 15m
```

**On the worker**, join and point the agent at the control plane:

```bash
# Enroll: writes this node's API identity, its own etcd client certificate and both CAs to /etc/ccattler/pki
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
  --cert /etc/ccattler/pki/node-identity-for-api.pem \
  --key /etc/ccattler/pki/node-identity-for-api-key.pem \
  --ca /etc/ccattler/pki/cluster-identity-ca.pem \
  --etcd-cert /etc/ccattler/pki/node-etcd-client-certificate.pem \
  --etcd-key /etc/ccattler/pki/node-etcd-client-certificate-key.pem \
  --etcd-ca /etc/ccattler/pki/etcd-access-ca.pem \
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

`sudo cca …` works on the control-plane host because root can read the local token `/etc/ccattler/local-token`,
which `cca server` writes at every start; the CLI sends it as `user:local-admin`, with full rights. Nothing in the
setup uses a certificate as an admin credential: the node's certificates in `/etc/ccattler/pki` identify the machine
to the API and to etcd. The one exception is the API server's own certificate, `api-server-identity-certificate.pem`,
which has full rights by design; like the local token, it is readable only by root on control-plane hosts.

Until milestone M181, every etcd client certificate the cluster issues (each node's and the server's) has full access
to the store: etcd checks which CA signed a certificate, not what its holder may read or write.

Don't use the local token for daily work. Give yourself a user account instead (replace `USERNAME`):

```bash
sudo cca user create USERNAME
sudo cca role grant user:USERNAME --capability cluster.admin --scope cluster   # or e.g. workload.read for read-only
export CCA_TOKEN=$(sudo cca user token USERNAME --ttl 24h)
cca status
```

The token is valid for the `--ttl` you chose. Create a new one when it expires.

> **Note:** for now a user token does not survive a restart of `cca server`, and with several control-plane hosts
> it works only on the one that created it. After a restart or an upgrade, run the `export` line again. This is
> fixed by milestone M283; until then, scripts on the control-plane host use `sudo cca`.

`export` sets it only for the current shell: in a new terminal or after logging in again, `cca status` fails with
`client certificate required` until you run the `export` line again. To keep it across logins, put the `export` line
in `~/.bashrc`.

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
