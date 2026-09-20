# Getting Started

This guide takes you from zero to running real containers in under five minutes.

## Three runtime modes

CCattler has three ways to run workloads — understanding this upfront avoids confusion:

| Command | Runtime | What happens |
|---|---|---|
| `cca apply` / `cca demo` | **Simulator** | Shows the reconciliation loop in action — no real processes or containers start. Useful for learning how CCattler works. |
| `cca run` | **Process** | Starts real OS processes on your machine, managed by the reconciler. No containerd needed. |
| `cca run-container` | **Container** | Pulls images and starts real OCI containers via nerdctl/containerd. This is what you use in production. |

If you just want to see CCattler deploy real containers, skip to [Run real containers](#run-real-containers) after installing.

## Prerequisites

- **Linux or macOS** (amd64 or arm64)
- **Go 1.22+** (if building from source) or `curl` (if downloading binary)
- **containerd + nerdctl** (for `cca run-container` — not needed for simulation or process mode). Docker Desktop includes containerd; standalone containerd + nerdctl also works.

## Install

### Download binary

```bash
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

This downloads the `cca` binary to `/usr/local/bin`.

### Build from source

```bash
git clone https://github.com/boyadzhievb/ccattler
cd ccattler
go build -o cca ./cmd/cca/
sudo mv cca /usr/local/bin/
```

### Verify

```bash
cca version
# cca v0.38.0
```

## Run the built-in demo

The fastest way to see CCattler in action — no cluster needed:

```bash
cca demo
```

This runs a simulated deployment: 3 instances of a web service reconciled across simulated nodes. You'll see the reconciliation loop in real time — instances created, placed on nodes, and marked as running.

## Write your first config

Create a file called `web.ccattler`:

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}
```

This declares a service named `web` running 3 instances of `nginx:1.28`, each consuming 500m CPU and 512Mi memory, with port 8080 exposed.

## Run real containers

This is the command you'll use in practice — it pulls the nginx image and starts real OCI containers via nerdctl/containerd:

```bash
cca run-container web.ccattler
```

Add `--watch` for live status updates:

```bash
cca run-container --watch web.ccattler
```

::: tip
`cca run-container` requires containerd and nerdctl (included with Docker Desktop, or installable standalone). If you don't have containerd, use `cca run` to start real OS processes instead, or `cca apply` for simulation mode.
:::

## Other runtime modes

### Simulation (no containers, no processes)

```bash
cca apply web.ccattler
```

Parses the config, runs the reconciliation loop, and shows the resulting state — nothing actually starts. Useful for understanding the state model.

### OS processes (no containerd needed)

```bash
cca run web.ccattler
```

Starts actual OS processes managed by the reconciler.

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
