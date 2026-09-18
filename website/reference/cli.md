# CLI Reference

The `cca` command-line tool manages CCattler clusters and workloads.

## Configuration management

### `cca apply <file>`

Parse and apply a `.ccattler` configuration file. Runs in simulated mode — compiles the config into facts, runs the reconciliation loop, prints the resulting state, and exits.

```bash
cca apply web.ccattler
```

### `cca run [--watch] <file>`

Start real OS processes managed by the reconciler.

```bash
cca run web.ccattler            # run and exit
cca run --watch web.ccattler    # run with live status updates
```

### `cca run-container [--watch] <file>`

Start real Docker containers managed by the reconciler.

```bash
cca run-container web.ccattler
cca run-container --watch web.ccattler
```

## Cluster operations

### `cca server`

Run the control plane (controllers + API server).

```bash
cca server                                    # default: localhost:9770
cca server --listen 0.0.0.0:9770              # bind to all interfaces
cca server --listen 0.0.0.0:9770 --tls        # auto-generate CA + certs
cca server --listen 0.0.0.0:9770 --tls --dns  # enable DNS server
```

| Flag | Default | Description |
|---|---|---|
| `--listen` | `0.0.0.0:9770` | Address to bind the API server |
| `--tls` | off | Auto-generate ephemeral CA and server certificate |
| `--cert` | | Path to PEM server certificate (external PKI) |
| `--key` | | Path to PEM server private key |
| `--ca` | | Path to PEM CA certificate |
| `--dns` | off | Enable DNS server for `*.ccattler.local` resolution |
| `--store` | `memory` | Store backend: `memory` or `etcd` |
| `--endpoints` | `localhost:2379` | etcd endpoints (when `--store etcd`) |

### `cca agent`

Run a node agent (observer, reconciler, reporter).

```bash
cca agent --node-id worker-1
cca agent --node-id worker-1 --proxy
cca agent --node-id worker-1 --advertise-address 192.168.1.11
```

| Flag | Default | Description |
|---|---|---|
| `--node-id` | required | Unique identifier for this node |
| `--runtime` | `container` | Runtime adapter: `container` or `process` |
| `--proxy` | off | Enable HTTP reverse proxy on this node |
| `--advertise-address` | | LAN IP for cross-host traffic resolution |
| `--cert` | | Path to PEM client certificate |
| `--key` | | Path to PEM client private key |
| `--ca` | | Path to PEM CA certificate |

## Node enrollment

### `cca token create`

Generate a join token for node enrollment.

```bash
cca token create --node-id worker-1 --ttl 15m
```

| Flag | Default | Description |
|---|---|---|
| `--node-id` | | Restrict token to a specific node ID |
| `--ttl` | `15m` | Token expiration time |

### `cca token list`

List active (non-expired) join tokens with masked values.

```bash
cca token list
```

### `cca token revoke <token>`

Revoke an active join token.

```bash
cca token revoke cca_abc123...
```

### `cca join <server> <token>`

Enroll a node — contacts the server, presents the token, receives certificates.

```bash
cca join 192.168.1.10:9770 cca_abc123... --node-id worker-1
cca join 192.168.1.10:9770 cca_abc123... --node-id worker-1 --ca-cert ca.pem
```

| Flag | Default | Description |
|---|---|---|
| `--node-id` | required | Node identifier |
| `--ca-cert` | | CA certificate to verify server (recommended for production) |

## Inspection

### `cca status`

Cluster overview — services, instances, nodes, health.

### `cca get services`

List all services with desired and running instance counts.

### `cca get instances`

List all instances with service, node, state, IP, and health.

### `cca get nodes`

List all nodes with capacity, utilization, and state.

### `cca get secrets`

List secrets and their grants (which services can access them).

### `cca get config`

List config entries (environment variables and config files).

### `cca scale <service> <count>`

Change the desired instance count for a service.

```bash
cca scale web 10
```

### `cca top [nodes|workloads]`

Resource utilization tables.

```bash
cca top              # summary
cca top nodes        # per-node CPU, memory, workload count
cca top workloads    # per-instance CPU and memory
```

### `cca logs [service]`

View the cluster event log, optionally filtered by service.

```bash
cca logs         # all events
cca logs web     # events for the web service
```

### `cca watch [prefix]`

Stream fact store changes in real time.

```bash
cca watch                    # all changes
cca watch observed/instance  # instance state changes only
```

## Metrics

### `cca metric set <service> <metric> <value>`

Inject a simulated metric value (for testing autoscaling).

```bash
cca metric set web cpu 90
cca metric set web requests_per_second 600
```

## Demo commands

```bash
cca demo                # basic: 3 instances reconciled
cca demo-distributed    # 6 instances across 3 simulated nodes
cca demo-network        # VIPs, DNS, load balancing
cca demo-storage        # persistent volumes survive node failures
cca chaos               # random failures — test convergence
```
