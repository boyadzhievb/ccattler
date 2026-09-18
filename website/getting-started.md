# Getting Started

This guide takes you from zero to a running CCattler service in under five minutes.

## Prerequisites

- **Linux or macOS** (amd64 or arm64)
- **Go 1.22+** (if building from source) or `curl` (if downloading binary)
- **Docker** or **containerd** (for container runtime — optional for simulation mode)

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
# cca v0.20.1
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

## Apply it

### Simulated mode (no Docker needed)

```bash
cca apply web.ccattler
```

This parses the config, compiles it into facts, runs the reconciliation loop, and shows the resulting state — all simulated locally.

### Real processes

```bash
cca run web.ccattler
```

Starts actual OS processes managed by the reconciler. Use `--watch` to see live status updates.

### Real containers (requires Docker)

```bash
cca run-container web.ccattler
```

Pulls the nginx image and starts real Docker containers.

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
