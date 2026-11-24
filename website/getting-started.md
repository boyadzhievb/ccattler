# Getting Started

This guide takes you from zero to a running deployment in under five minutes.

## Four ways to run workloads

CCattler has four ways to run workloads — understanding this upfront avoids confusion:

| Command | Runtime | What happens | Platform |
|---|---|---|---|
| `cca apply --simulate` / `cca demo` | **Simulator** | Shows the reconciliation loop in action — no real processes or containers start. | Linux, macOS |
| `cca apply` | **Your cluster** | Sends the config to the cluster's server; its agents pull images and start containers. Needs a running cluster ([Installation](/installation#set-up-a-cluster-by-hand)). | Linux |
| `cca run` | **Process** | Starts real OS processes on your machine, managed by the reconciler. The `image` field is used as the command to execute. | Linux, macOS |
| `cca run-container` | **Container** | Pulls images and starts real OCI containers via docker, nerdctl, or lima. This is what you use in production. | Linux, macOS |

**On macOS:** simulation, process and container modes work (a cluster needs Linux). Container mode requires Docker Desktop or Lima (`brew install lima`).

## Prerequisites

- **Linux or macOS** (amd64 or arm64)
- **Go 1.22+** (if building from source) or `curl` (if downloading binary)
- **Container runtime** (for `cca run-container`): Docker Desktop (macOS/Linux), nerdctl+containerd (Linux), or Lima (macOS: `brew install lima`)
- **`sudo`, `curl` and `tar`**: a fresh install may lack them. See [Prepare a fresh host](/installation#prepare-a-fresh-host).

## Install

### Download binary

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

This detects your OS and architecture, downloads the right binary, and installs it to `/usr/local/bin`. Works on Linux and macOS (Intel and Apple Silicon).

### Build from source

```bash
git clone https://github.com/boyadzhievb/ccattler
cd ccattler
go build -o cca ./cmd/cca/
sudo install -o root -g root -m 0755 cca /usr/local/bin/
# SELinux hosts (RHEL, AlmaLinux, Rocky): give it the label systemd needs
if command -v selinuxenabled >/dev/null && selinuxenabled; then sudo restorecon /usr/local/bin/cca; fi
```

### Verify

```bash
cca version
```

## Run the built-in demo

The fastest way to see CCattler in action — no cluster needed, works on any platform:

```bash
cca demo
```

This runs a simulated deployment: 3 instances of a web service reconciled across simulated nodes. You'll see the reconciliation loop in real time — instances created, placed on nodes, and marked as running.

## Write your first config

Create a file called `web.cca`:

```hcl
service web {
    image "docker.io/library/nginx:1.28"
    instances 3
    expose 80
    resources {
        cpu 100m
        memory 64Mi
    }
}
```

This declares a service named `web` running 3 instances of nginx, each requesting 100m CPU and 64Mi memory, with
nginx's port 80 exposed.

Write the image with its full registry path (`docker.io/library/nginx:1.28`, not `nginx:1.28`) and in quotes. A value
containing `/` must be quoted.

### Simulate it

```bash
cca apply --simulate web.cca
```

Runs the reconciliation loop in memory and shows the resulting state — nothing actually starts. This works on any
platform and is the quickest way to verify your config.

### Deploy it to your cluster

On a cluster set up as in [Installation](/installation#set-up-a-cluster-by-hand), as the user from its step 3 (with `CCA_TOKEN` exported in this shell):

```bash
cca apply web.cca
cca get instances           # wait until all three are running
cca describe service web    # shows the service's virtual IP (VIP)
curl http://<VIP>:80/       # from any cluster host: nginx answers
```

The DNS name `web.cca.local` resolves inside containers, not on the host itself.

## Run real containers without a cluster

With a container runtime installed (Docker, nerdctl, or Lima), this pulls the nginx image and starts real OCI containers
on your machine, without a server or agent:

```bash
sudo cca run-container web.cca
```

Each instance's port is published on the host, counting up from the exposed port: the three instances get host ports
80, 81 and 82. Ports below 1024 need root, so run it with `sudo` (or change `expose` to a port above 1024 for an image
that listens there), and make sure port 80 is free.

Add `--watch` for live status updates:

```bash
sudo cca run-container --watch web.cca
```

::: tip No container runtime?
If you don't have Docker, nerdctl, or Lima installed, use `cca apply --simulate` for simulation or `cca run` with a local executable. The install script will tell you what's available.
:::

## Run local processes (any platform)

`cca run` starts real OS processes — the `image` field is used as the command to execute, not as a container image. This is useful for development without containerd.

Create `server.cca`:

```hcl
service server {
    image "python3 -m http.server 8080"
    instances 2
    expose 8080
}
```

```bash
cca run server.cca
```

This starts 2 instances of `python3 -m http.server 8080` as OS processes, managed by the reconciler.

::: tip
For `cca run`, the `image` field is the command that gets executed. Use a command available in your `$PATH`. If you accidentally use a container image name like `nginx:1.28`, cca will tell you to use `cca run-container` instead.
:::

## Explore

Once your service is running:

```bash
# Cluster overview
cca status

# List services
cca get services

# List instances
cca get instances

# Resource utilization
cca top
```

## Try more demos

```bash
# Distributed: 6 instances across 3 simulated nodes
cca demo-distributed

# Networking: VIPs, DNS, load balancing
cca demo-network

# Storage: persistent volumes survive node failures
cca demo-storage

# Chaos: random failures — does the system converge?
cca chaos
```

## Scale a service

```bash
cca scale web 10
```

The instance controller sees `desired=10, actual=3` and creates 7 more instances. The scheduler places them across available nodes.

## Next steps

- [Installation](/installation) — production deployment with Ansible, demo clusters with Vagrant
- [Concepts](/concepts/) — understand facts, reconciliation, and controllers
- [First Service guide](/guides/first-service) — deeper walkthrough with health checks, config, and secrets
- [CLI Reference](/reference/cli) — every command and flag
