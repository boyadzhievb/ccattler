# Getting Started

This guide takes you from zero to a running deployment in under five minutes.

## Three runtime modes

CCattler has three ways to run workloads — understanding this upfront avoids confusion:

| Command | Runtime | What happens | Platform |
|---|---|---|---|
| `cca apply` / `cca demo` | **Simulator** | Shows the reconciliation loop in action — no real processes or containers start. | Linux, macOS |
| `cca run` | **Process** | Starts real OS processes on your machine, managed by the reconciler. The `image` field is used as the command to execute. | Linux, macOS |
| `cca run-container` | **Container** | Pulls images and starts real OCI containers via nerdctl/containerd. This is what you use in production. | Linux only |

**On macOS:** Start with `cca apply` (simulation) or `cca run` (local processes). Container mode requires Linux with nerdctl/containerd.

## Prerequisites

- **Linux or macOS** (amd64 or arm64)
- **Go 1.22+** (if building from source) or `curl` (if downloading binary)
- **containerd + nerdctl** (Linux only, for `cca run-container` — not needed for simulation or process mode)

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
sudo mv cca /usr/local/bin/
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

### Simulate it

```bash
cca apply web.ccattler
```

Runs the reconciliation loop and shows the resulting state — nothing actually starts. This works on any platform and is the quickest way to verify your config.

## Run real containers (Linux)

On Linux with nerdctl/containerd installed, this pulls the nginx image and starts real OCI containers:

```bash
cca run-container web.ccattler
```

Add `--watch` for live status updates:

```bash
cca run-container --watch web.ccattler
```

::: warning macOS
`cca run-container` requires nerdctl and containerd, which are Linux-only. On macOS, use `cca apply` for simulation or `cca run` with a local executable.
:::

## Run local processes (any platform)

`cca run` starts real OS processes — the `image` field is used as the command to execute, not as a container image. This is useful for development without containerd.

Create `server.ccattler`:

```hcl
service server {
    image "python3 -m http.server 8080"
    instances 2
    expose 8080
}
```

```bash
cca run server.ccattler
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
