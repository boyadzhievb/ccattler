# CLI Reference

The `cca` command-line tool manages CCattler clusters and workloads.

## Store auto-detection

Commands that connect to the fact store (apply, server, agent, token, etc.) resolve the etcd endpoint automatically — no `--store` or `--endpoints` flags needed on joined nodes.

**Resolution order** (first match wins):

1. `--endpoints` flag (explicit override)
2. `CCA_ENDPOINTS` environment variable
3. `/etc/ccattler/endpoints` file (written by `cca join` and Ansible)
4. Falls back to local simulation (memory store)

```bash
# On a joined node — endpoints auto-detected:
cca apply airflow.cca
cca secret set db-password mypass

# On a developer machine — env var:
export CCA_ENDPOINTS=http://192.168.124.10:2379
cca apply airflow.cca

# Explicit override (still works):
cca apply airflow.cca --endpoints http://10.0.0.1:2379
```

The `--store` flag is no longer required. When endpoints are available (from any source), the store backend is automatically set to `etcd`. Without endpoints, commands run in local simulation mode with an in-memory store.

## Configuration management

### `cca apply <file>`

Parse and apply a `.cca` configuration file. Runs in simulated mode — compiles the config into facts, runs the reconciliation loop, prints the resulting state, and exits. Supports template rendering with `--values`, `--set`, and `--set-from-env`. When a template `foo.cca` has a sibling directory `foo/values.ccv`, that file is auto-loaded as the defaults layer.

```bash
cca apply web.cca
cca apply airflow.cca                                       # auto-loads airflow/values.ccv
cca apply --values airflow/values-prod.ccv airflow.cca      # defaults + prod overrides
cca apply --set worker.instances=10 airflow.cca             # defaults + CLI override
cca apply --set-from-env airflow.image=CI_TAG airflow.cca   # defaults + env var override
```

| Flag | Description |
|---|---|
| `--values`, `-f` | Values file (`.ccv`), can be repeated — later files override earlier |
| `--set` | Override a single value: `key=value`, dot paths supported |
| `--set-from-env` | Read value from environment variable: `key=ENV_VAR` |
| `--dry-run` | Print rendered DSL to stdout without applying |

**Precedence** (lowest to highest): auto-loaded `values.ccv` < `--values` files (in order) < `--set` < `--set-from-env`.

### `cca render <file>`

Render a template with values and print the resulting DSL to stdout. Useful for debugging templates before applying. Same auto-loading behavior: `foo/values.ccv` is loaded automatically for `foo.cca`.

```bash
cca render airflow.cca                                      # auto-loads airflow/values.ccv
cca render --values airflow/values-prod.ccv airflow.cca     # defaults + prod overrides
cca render --set worker.instances=20 airflow.cca            # defaults + CLI override
```

Accepts the same `--values`, `--set`, and `--set-from-env` flags as `cca apply`.

### `cca run [--watch] <file>`

Start real OS processes managed by the reconciler.

```bash
cca run web.cca            # run and exit
cca run --watch web.cca    # run with live status updates
```

### `cca run-container [--watch] <file>`

Start real OCI containers via nerdctl/containerd, managed by the reconciler.

```bash
cca run-container web.cca
cca run-container --watch web.cca
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
| `--endpoints` | auto-detected | etcd endpoints (override auto-detection) |

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
| `--kms-provider` | | KMS backend for secret decryption: `aws-kms` or `gcp-kms` |
| `--kms-key` | | KMS key ARN (AWS) or resource name (GCP) |
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

### `cca secret set <name> <value>`

Store an encrypted secret.

```bash
cca secret set database-password 'hunter2'
cca secret set fernet-key "$(openssl rand -base64 32)"
```

The value is encrypted at rest using AES-256-GCM envelope encryption. The master key is stored at `.ccattler/secret-master.key` on the server, or delegated to cloud KMS (see [agent KMS flags](#cca-agent)).

### `cca secret get <name>`

Retrieve a decrypted secret value.

```bash
cca secret get database-password
```

### `cca secret list`

List all stored secret names (values are not shown).

```bash
cca secret list
```

### `cca secret delete <name>`

Remove a secret from the encrypted store.

```bash
cca secret delete database-password
```

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
cca benchmark [--json]  # chaos benchmark with recovery metrics
```
