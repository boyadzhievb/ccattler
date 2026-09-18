# Troubleshooting

## Diagnostic commands

```bash
# Cluster overview — shows services, instances, nodes
cca status

# Node state and capacity
cca get nodes

# Instance state, health, and placement
cca get instances

# Resource utilization
cca top
cca top nodes
cca top workloads

# Real-time fact store changes
cca watch

# Event log (optionally filtered by service)
cca logs
cca logs web
```

## Common issues

### Node not joining

**Symptom:** `cca join` fails or times out.

**Check:**
1. Is the server running and reachable? `curl -k https://<server>:9770/state`
2. Is the token valid? `cca token list` on the server
3. Is the CA certificate correct? Use `--ca-cert` flag with the server's CA

**Token expired:** tokens are short-lived (default 15 minutes). Create a new one with `cca token create`.

**Token already used:** tokens are single-use. Create a new one.

### Node shows unreachable

**Symptom:** `cca get nodes` shows a node as `unreachable`.

**Cause:** The node agent's etcd lease expired (no heartbeat for 30 seconds).

**Check:**
1. Is the agent running? `systemctl status cca-agent` on the node
2. Can the agent reach etcd? Network connectivity between agent and control plane
3. Check agent logs: `journalctl -u cca-agent`

**Resolution:** Restart the agent. It will re-register and renew its lease. Instances that were rescheduled during the outage will be cleaned up by the reconciler.

### Instances stuck in pending

**Symptom:** `cca get instances` shows instances in `pending` state.

**Causes:**
1. **No nodes available** — check `cca get nodes` for healthy nodes
2. **Insufficient resources** — nodes don't have enough CPU/memory. Check `cca top nodes`
3. **Placement constraints** — `require` labels don't match any node. Check node labels and service placement configuration
4. **Scheduler not running** — verify the server is healthy

### Health checks failing

**Symptom:** Instances show `unhealthy` or `not-ready`.

**Check:**
1. Is the application actually serving on the configured port and path?
2. Are probes configured correctly? Verify port, path, and interval in the `.ccattler` file
3. Check instance logs for application errors

### VIPs not working

**Symptom:** `curl http://10.200.0.x:port` times out.

**Check:**
1. Are endpoints populated? `cca watch endpoint/` to see endpoint facts
2. Is the proxy running? `cca agent --proxy` must be enabled
3. Are iptables rules created? `sudo iptables -t nat -L CCA_SERVICES` on the node
4. For cross-host: is `--advertise-address` set on agents?

### DNS not resolving

**Symptom:** `dig web.ccattler.local @server-ip -p 15353` returns no answer.

**Check:**
1. Is DNS enabled? `cca server --dns` must be set
2. Does the service have running endpoints? `cca get instances` for the service
3. Is the DNS port reachable? Default is UDP 15353

## Controller behavior

### Controller restart backoff

Controllers restart with exponential backoff when they fail:

```
100ms → 500ms → 1s → 2s → 5s → 30s (max)
```

### 30-second resync

Even if watch events are missed, every controller runs a full reconciliation every 30 seconds. If a change isn't taking effect, wait for the next resync cycle.

### Multiple controller replicas

In HA mode, controller replicas use leader election. Only the leader processes changes. If the leader crashes, another replica takes over within one lease TTL.

## Getting help

- [GitHub Issues](https://github.com/boyadzhievb/ccattler/issues) — report bugs and request features
- [Examples](/examples) — working configuration files for common scenarios
