# Troubleshooting

This guide covers how to diagnose and fix common issues in a running CCattler cluster. It is organized in three tiers: quick diagnosis commands, instance-level debugging, and low-level container and etcd inspection.

## Quick diagnosis

Start here. These commands give you a cluster-wide picture in seconds.

### Cluster overview

```bash
# Full cluster status — services, instances, nodes, networking
cca status

# Service convergence: desired vs running
cca get services

# Instance state, health, placement, and reason
cca get instances

# Include stopped/failed instances (hidden by default)
cca get instances --all

# Node health and capacity
cca get nodes

# Resource utilization
cca top nodes
cca top workloads
```

### Live event stream

The event stream shows state transitions as they happen — the fastest way to understand what the cluster is doing right now.

```bash
# Recent events (batch)
cca events

# Live stream (blocks, Ctrl-C to stop)
cca events --follow

# Filter to one service
cca events --follow --service webserver
```

Event output format:
```
TIMESTAMP                 KIND                    TARGET                          DETAIL
2026-10-28 12:21:07.701   instance.running        scheduler-eb7509a28e            is now running (init/step/1)
2026-10-28 12:21:35.738   instance.failed         worker-43a2115efd               has failed (init/step/0)
```

The TARGET column shows `<service>-<instanceID>` — you can double-click to select and copy the instance name for use in other commands.

### Fact store watch

For deeper inspection, watch raw fact store changes. This shows every key write as it happens.

```bash
# All changes
cca watch

# Watch a specific instance
cca watch observed/instance/<instanceID>

# Watch all placements
cca watch placement/

# Watch a specific service's desired state
cca watch desired/service/<name>
```

---

## Instance-level debugging

When you've identified a problematic instance from the quick diagnosis, drill in.

### Container logs

```bash
# By service name (streams all running instances)
cca logs webserver

# By container name (service-instanceID from events or get instances)
cca logs webserver-eb7509a28e

# Follow mode (like tail -f)
cca logs webserver -f

# Last 100 lines
cca logs webserver --tail 100

# Logs since a timestamp
cca logs webserver --since "2026-10-28T12:00:00Z"

# Logs from a previous (terminated) instance
cca logs webserver --previous

# Logs for all services matching a tag
cca logs --tag tier=frontend
```

**"workload not found" error:** If `cca logs` returns this, the container may not exist yet (instance is in init phase, pulling an image, or was recently stopped). The CLI will print a hint showing the instance's current state.

### Node runtime inspection

Query a node's agent debug API for live runtime stats:

```bash
# Detailed runtime state on a node
cca node-inspect <node-id>

# Shows: workload count, running/stopped containers, log dirs, CPU, memory
```

### Exec into a running container

```bash
# Run a command inside a container
cca exec webserver-eb7509a28e -- /bin/sh

# Non-interactive (single command)
cca exec webserver-eb7509a28e -- cat /etc/hosts
```

### Instance describe

```bash
# Detailed view of a single instance
cca describe instance webserver-eb7509a28e

# Detailed view of a service (all instances, config, probes)
cca describe service webserver
```

---

## Why is my instance stuck?

### Stuck in `pending`

The instance was created but not yet scheduled to a node.

**Check:**
1. **No nodes available** — `cca get nodes`. Are any nodes `alive`?
2. **Insufficient resources** — `cca top nodes`. Does any node have enough free CPU/memory?
3. **Placement constraints** — Does the service have `require` labels? Do any nodes match?
4. **Scheduler not running** — Is the server process healthy? `cca status`

```bash
# See unplaced instances and reasons
cca watch derived/unplaced/
```

### Stuck in `pulling`

The node is downloading the container image.

**Check:**
1. Is the image reference correct? Typo in name or tag?
2. Can the node reach the registry? `cca exec <instance> -- nslookup <registry>`
3. Check agent logs for pull errors: `journalctl -u cca-agent` on the node

### Stuck in `image-pull-backoff`

Image pull failed and the agent is waiting before retrying. Backoff: 10s, 20s, 40s, 80s, 160s, 5m max.

```bash
# Check pull status fact
cca watch observed/instance/<id>/pull-status
```

**Common causes:**
- Bad image reference (typo, tag doesn't exist)
- Registry authentication failed (check image pull secrets)
- Registry unreachable (network, DNS, firewall)

### Stuck in init step

The instance is running init steps before starting the main container. Status reason shows `init:N/M <label>`.

```bash
# Check init progress
cca watch observed/instance/<id>/init-progress

# Check init step states
cca watch observed/instance/<id>/init/
```

**Common causes:**
1. **Connection probe failing** — the target service isn't ready yet (normal during first boot). Init steps retry with backoff: 1s, 2s, 4s, 8s, 16s, 30s max. Connection probes default to 30 retries (~15 min window).
2. **Exec step failing** — the init command returned non-zero. Check the init-progress fact for the error message. For `check-migrations`, the database migration may not have completed yet on another node.
3. **Max retries exceeded** — init step permanently failed. Re-apply the service definition to reset.

### Stuck in `starting`

The container was created but `runtime.Status()` is returning an error. The container may actually be running.

```bash
# Check if container exists on the node
sudo ctr -n ccattler containers list
sudo ctr -n ccattler tasks list

# Check what etcd says
cca watch observed/instance/<id>/state
```

### Running but `unhealthy`

Probes are failing. Check which probe:

```bash
# Probe progress (shows failure counts and errors)
cca watch observed/instance/<id>/probe-progress

# Individual probe states
cca watch observed/instance/<id>/probe/
```

**Probe types:**
- **Startup probe** — gates liveness and readiness. Must pass before other probes run. Failure beyond threshold triggers replacement.
- **Liveness probe** — fails → drain grace period → replacement.
- **Readiness probe** — fails → removed from endpoints. Instance keeps running (no restart).

### Instance keeps restarting (crash loop)

The failure controller replaces failed instances with exponential backoff: 10s, 20s, 40s, 80s, 160s, 5m max.

```bash
# Check crash-loop counter
cca watch derived/crash-loop/<service-name>

# See replacement events
cca events --service <name>

# Check exit reason
cca watch observed/instance/<id>/exit-reason
```

**Common causes:**
- Application crashes on startup (check `cca logs`)
- Missing config or secrets (check service definition)
- Port already in use (another instance on the same node)

---

## Low-level inspection

When `cca` commands aren't enough, inspect the container runtime and etcd directly. Run these commands on the node where the instance is placed.

### containerd commands

CCattler uses the `ccattler` namespace in containerd.

```bash
# List all containers
sudo ctr -n ccattler containers list

# List only container IDs
sudo ctr -n ccattler containers list -q

# List running tasks
sudo ctr -n ccattler tasks list

# Check a specific container's state
sudo ctr -n ccattler containers info <container-id>

# Check task state (PID, status)
sudo ctr -n ccattler tasks ps <container-id>

# View container logs (raw, from containerd)
sudo ctr -n ccattler tasks exec --exec-id debug <container-id> cat /proc/1/fd/1

# List images cached on this node
sudo ctr -n ccattler images list

# List snapshots (disk usage)
sudo ctr -n ccattler snapshots list

# Check for orphan log directories
ls -la /var/lib/ccattler/logs/
```

### etcdctl commands

etcd stores all cluster state. All keys are under the `/ccattler/` prefix.

```bash
# Set up TLS flags (required for mTLS clusters)
export ETCDCTL_CACERT=/etc/ccattler/pki/ca.pem
export ETCDCTL_CERT=/etc/ccattler/pki/node.pem
export ETCDCTL_KEY=/etc/ccattler/pki/node-key.pem
export ETCDCTL_ENDPOINTS=https://127.0.0.1:2379

# Check etcd health
etcdctl endpoint health

# List all keys under a prefix
etcdctl get --prefix /ccattler/observed/instance/<id>/ --keys-only

# Read a specific key
etcdctl get /ccattler/observed/instance/<id>/state

# Watch a key for changes (real-time)
etcdctl watch --prefix /ccattler/observed/instance/<id>/

# List all services
etcdctl get --prefix /ccattler/desired/service/ --keys-only

# List all placements
etcdctl get --prefix /ccattler/placement/instance/ --print-value-only

# Check node heartbeats
etcdctl get --prefix /ccattler/observed/node/ --keys-only

# Count keys under a prefix
etcdctl get --prefix /ccattler/observed/instance/ --keys-only | wc -l
```

**Key prefixes:**

| Prefix | Contents |
|--------|----------|
| `desired/service/` | Service definitions (image, instances, config) |
| `desired/route/` | Route definitions (host, service, port) |
| `observed/instance/` | Instance state, health, IP, probes |
| `observed/node/` | Node state, heartbeat, resources |
| `placement/instance/` | Instance → node assignments |
| `placement/node/` | Node → instance assignments |
| `derived/instance/` | Controller markers (stopped, failed, init phase) |
| `derived/endpoint/` | Service endpoints (IP:port per instance) |
| `derived/vip/` | Virtual IPs assigned to services |
| `derived/crash-loop/` | Crash-loop counters per service |
| `network/node/` | Overlay subnet assignments |

### systemd service logs

```bash
# Server logs (control plane)
journalctl -u cca-server --no-pager -n 100
journalctl -u cca-server -f    # follow

# Agent logs (node agent)
journalctl -u cca-agent --no-pager -n 100
journalctl -u cca-agent -f     # follow

# etcd logs
journalctl -u etcd --no-pager -n 100

# Filter by time
journalctl -u cca-agent --since "2026-10-28 12:00:00" --until "2026-10-28 13:00:00"
```

### Agent debug API

Each node agent exposes a debug API (default port 9771). This is what `cca node-inspect` queries.

```bash
# Runtime stats (container counts, resource usage)
curl -s http://localhost:9771/debug/stats | jq .

# Container logs via debug API
curl -s "http://localhost:9771/debug/logs?instance=<id>&tail=100"

# Image pull
curl -X POST http://localhost:9771/debug/images/pull \
  -d '{"image":"myapp:1.0"}'
```

---

## Common issues

### Node not joining

**Symptom:** `cca join` fails or times out.

**Check:**
1. Is the server running and reachable? `curl -k https://<server>:9770/status`
2. Is the token valid? `cca token list` on the server
3. Is the CA certificate correct? Use `--ca-cert` flag with the server's CA

**Token expired:** tokens are short-lived (default 15 minutes). Create a new one with `cca token create`.

**Token already used:** tokens are single-use. Create a new one.

### Node shows unreachable

**Symptom:** `cca get nodes` shows a node as `unreachable`.

**Cause:** The node agent's etcd lease expired (no heartbeat for 30 seconds).

**Check:**
1. Is the agent running? `systemctl status cca-agent` on the node
2. Can the agent reach etcd? Check network connectivity
3. Check agent logs: `journalctl -u cca-agent`

**Resolution:** Restart the agent. It will re-register and renew its lease. Instances that were rescheduled during the outage will be cleaned up by the reconciler.

### etcd leadership loss / timeouts

**Symptom:** Agent logs show `etcdserver: request timed out` or `failed to write heartbeat`.

**Causes:**
1. **API request flood** — too many concurrent TLS handshakes exhausting goroutines. Check for burst callers (monitoring, reverse proxy sync).
2. **Disk I/O** — etcd is sensitive to disk latency. Check `iostat` on the control plane node.
3. **Network partition** — etcd members can't reach each other.

**Check:**
```bash
# etcd member health
etcdctl endpoint health --cluster

# etcd performance
etcdctl endpoint status --write-out=table

# Check for TLS errors in server logs
journalctl -u cca-server | grep "tls:"
```

### VIPs not working

**Symptom:** `curl http://10.200.0.x:port` times out.

**Check:**
1. Are endpoints populated? `cca watch derived/endpoint/`
2. Are nftables rules created? `sudo nft list table cca` on the node
3. For cross-host: is `--advertise-address` set on agents?
4. Is the overlay network working? Can containers on different nodes ping each other?

### DNS not resolving

**Symptom:** Container can't resolve service names.

**Check:**
1. Is DNS enabled? `cca server --dns` must be set
2. Does the service have running endpoints? `cca get instances` for the service
3. Is the DNS forwarder running on the agent? Check agent logs
4. Test from inside a container: `cca exec <name> -- nslookup <service>.cca.local`

### Container accumulation

**Symptom:** Node runs out of disk/memory. `cca node-inspect` shows high stopped count or log dir count.

**Check:**
```bash
# Runtime stats per node
cca node-inspect <node-id>

# All instances including stopped
cca get instances --all

# Raw container count
sudo ctr -n ccattler containers list -q | wc -l

# Orphan log directories
ls /var/lib/ccattler/logs/ | wc -l
```

**Resolution:** The agent GC should clean orphan containers automatically. If containers accumulate, check for crash loops (`cca events --service <name>`) and consider the circuit breaker threshold.

---

## Controller behavior

### Reconciliation cycle

Controllers reconcile on two triggers:
1. **Watch events** — fact store changes trigger immediate reconciliation (debounced 50ms)
2. **Resync ticker** — full reconciliation every 30 seconds regardless of watch events

If a change isn't taking effect, wait 30 seconds for the resync.

### Controller restart backoff

Controllers restart with exponential backoff when they fail:

```
100ms → 500ms → 1s → 2s → 5s → 30s (max)
```

### Agent reconciliation

The node agent reconciles on a 1-second ticker and on watch events. Each cycle:
1. Finds instances placed on this node
2. Starts instances that should be running but aren't
3. Stops instances that are running but shouldn't be
4. Publishes observed state to the store

### HA mode

In HA mode, controller replicas use leader election. Only the leader processes changes. If the leader crashes, another replica takes over within one lease TTL.
