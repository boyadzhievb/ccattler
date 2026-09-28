# Disaster Recovery

This runbook covers catastrophic failure scenarios and their recovery procedures. For routine etcd snapshot backup and restore, see [Backup & Restore](/operations/backup). For individual component issues, see [Troubleshooting](/operations/troubleshooting).

## Complete control plane failure

**What happens:** All control plane servers are down, etcd is unreachable. No scheduling, no controller reconciliation, no API access.

**Workload impact:** Running workloads continue operating. Node agents cache the last known desired state and maintain containers from that cache. Health checks, restarts, and config delivery continue locally. No new scheduling, scaling, or placement decisions occur until the control plane returns.

**What stops working:**
- New deployments and scaling changes
- Node failure detection and instance rescheduling
- Endpoint updates and DNS changes
- Certificate rotation (existing certificates remain valid until expiry)

### Recovery steps

1. Identify which control plane nodes are down and why (hardware, network, power):

```bash
# From a machine with network access to the control plane
for server_host in cp1 cp2 cp3; do
    ssh "$server_host" "systemctl status cca-server && systemctl status etcd" 2>&1 || echo "$server_host unreachable"
done
```

2. Restore the underlying infrastructure (power, network, disks).

3. Start etcd first, then the CCattler server:

```bash
sudo systemctl start etcd
# Verify etcd is healthy
etcdctl endpoint health \
  --endpoints=https://127.0.0.1:2379 \
  --cert=/etc/ccattler/etcd-client.pem \
  --key=/etc/ccattler/etcd-client-key.pem \
  --cacert=/etc/ccattler/ca.pem

sudo systemctl start cca-server
```

4. If etcd data is intact, agents reconnect automatically. Their observed state flows back into the store, and the reconciliation loop converges the cluster.

5. If etcd data is lost, see [etcd data loss](#etcd-data-loss-without-snapshot) below.

## etcd data loss

### With a snapshot available

Follow the restore procedure in [Backup & Restore](/operations/backup). After restore:

1. Start the server:

```bash
sudo systemctl start cca-server
```

2. Agents reconnect and re-report observed state. The reconciliation loop compares restored desired state against current observed state and converges.

3. Expect a burst of reconciliation activity. Instances that were created after the snapshot will appear as untracked and be cleaned up or adopted depending on whether their service still exists in the restored state.

4. Run [post-incident verification](#post-incident-verification) to confirm convergence.

### Without a snapshot

This is the worst-case scenario. All cluster state must be rebuilt from your `.ccattler` configuration files and current node state.

1. Stop all control plane components:

```bash
sudo systemctl stop cca-server
```

2. Remove the corrupted etcd data:

```bash
sudo rm -rf /var/lib/etcd/default.etcd
```

3. Initialize a fresh etcd instance:

```bash
sudo systemctl start etcd
etcdctl endpoint health \
  --endpoints=https://127.0.0.1:2379 \
  --cert=/etc/ccattler/etcd-client.pem \
  --key=/etc/ccattler/etcd-client-key.pem \
  --cacert=/etc/ccattler/ca.pem
```

4. Start the CCattler server:

```bash
sudo systemctl start cca-server
```

5. Re-enroll all nodes. On each node:

```bash
# On the server, create a join token for each node
cca token create --node-id node-1

# On the node
sudo systemctl stop cca-agent
cca join <server-address> <token> --node-id node-1 --ca-cert /etc/ccattler/ca.pem
sudo systemctl start cca-agent
```

6. Re-apply all service definitions from your configuration files:

```bash
cca apply services.ccattler
cca apply network.ccattler
# Apply all configuration files from version control
```

7. The scheduler places instances on the re-enrolled nodes. Agents detect containers already running from before the outage and reconcile — matching existing containers to new desired state where possible instead of restarting everything.

8. Run [post-incident verification](#post-incident-verification).

**Prevention:** Automate etcd snapshots (see [Backup & Restore](/operations/backup)) and store snapshots off-host. Keep all `.ccattler` files in version control.

## Split-brain and network partition

**What happens:** A network partition separates some nodes from the control plane. The control plane still functions, but partitioned nodes cannot report state or receive updates.

### Partitioned node behavior

1. The agent's etcd lease expires (default 30 seconds without heartbeat).
2. The control plane marks the node as `unreachable`.
3. After the unreachable grace period, the failure controller creates replacement instances on reachable nodes.
4. The partitioned agent continues running existing workloads from cached desired state. It does not stop containers because it cannot confirm whether the control plane has rescheduled them.

### What this means for traffic

- Endpoints on partitioned nodes are removed from the service endpoint set. DNS and VIP routing stop sending traffic to those nodes.
- If the application on the partitioned node is still running, it serves any traffic that reaches it directly, but service discovery no longer routes to it.

### Recovery when the partition heals

1. Agents on previously partitioned nodes reconnect and renew their leases.
2. Nodes transition from `unreachable` back to `ready`.
3. The reconciler detects duplicate instances — the originals that kept running on the partitioned nodes and the replacements created during the partition.
4. The scheduler resolves the overcount by terminating excess instances based on placement policy and spread.
5. Endpoints update to reflect the converged set of instances.

### Manual intervention if automatic recovery stalls

Check for duplicate instances:

```bash
cca get instances | grep -E "running|pending"
```

If extra instances remain beyond the desired count after two reconciliation cycles (60 seconds):

```bash
# Check the reconciliation loop is running
curl -s http://localhost:9770/metrics | grep reconciliation_cycle
```

If the reconciliation metrics show the loop is active but instances are not converging, check the event log for errors:

```bash
cca events
cca logs <service-name>
```

## Controller leader failover

In HA mode, multiple CCattler server replicas run simultaneously. Only the leader processes controller reconciliation. Other replicas are standby.

### How failover works

1. The leader holds an etcd lease with a TTL (default 15 seconds).
2. If the leader crashes or loses connectivity, its lease expires.
3. A standby replica acquires the lease and becomes the new leader.
4. The new leader starts a full reconciliation cycle immediately.

### Verifying the active leader

```bash
# Check which server instance holds the leader lease
etcdctl get /ccattler/leader \
  --endpoints=https://127.0.0.1:2379 \
  --cert=/etc/ccattler/etcd-client.pem \
  --key=/etc/ccattler/etcd-client-key.pem \
  --cacert=/etc/ccattler/ca.pem
```

```bash
# Check server health endpoint on each replica
for server_host in cp1 cp2 cp3; do
    echo "$server_host: $(curl -sk https://$server_host:9770/state | head -c 200)"
done
```

### Failover did not happen

If the old leader is down but no new leader has taken over:

1. Verify standby replicas are running:

```bash
for server_host in cp1 cp2 cp3; do
    ssh "$server_host" "systemctl status cca-server" 2>&1
done
```

2. Check that standby replicas can reach etcd:

```bash
# On each standby server
etcdctl endpoint health \
  --endpoints=https://127.0.0.1:2379 \
  --cert=/etc/ccattler/etcd-client.pem \
  --key=/etc/ccattler/etcd-client-key.pem \
  --cacert=/etc/ccattler/ca.pem
```

3. If etcd itself has lost quorum (more than half the etcd members are down), no leader election can succeed. Restore etcd quorum first:

```bash
etcdctl member list \
  --endpoints=https://127.0.0.1:2379 \
  --cert=/etc/ccattler/etcd-client.pem \
  --key=/etc/ccattler/etcd-client-key.pem \
  --cacert=/etc/ccattler/ca.pem
```

4. Check server logs for election errors:

```bash
journalctl -u cca-server --since "10 minutes ago" | grep -i "leader\|election\|lease"
```

## Mass node failure

Multiple worker nodes go down simultaneously (datacenter outage, shared switch failure, power loss).

### Immediate impact

- All instances on failed nodes are lost.
- The failure controller creates replacement instances for all affected services.
- Surviving nodes must absorb the rescheduled workload.
- If surviving capacity is insufficient, some instances remain in `pending` state.

### Triage and recovery

1. Assess the damage:

```bash
# Which nodes are down
cca get nodes

# How many instances are affected
cca get instances | grep -c "failed\|pending"

# Current cluster capacity
cca top nodes
```

2. Identify capacity shortfall. If many instances are pending, check whether surviving nodes have room:

```bash
cca top nodes
```

3. Prioritize critical services. If capacity is exhausted, scale down non-critical services to free resources for critical ones:

```bash
cca scale <non-critical-service> 0
cca scale <critical-service> <desired-count>
```

4. Add replacement nodes if available:

```bash
# On the server
cca token create --node-id replacement-node-1

# On the new node
cca join <server-address> <token> --node-id replacement-node-1 --ca-cert /etc/ccattler/ca.pem
sudo systemctl start cca-agent
```

5. As failed nodes recover, restart agents on them:

```bash
sudo systemctl start cca-agent
```

Agents re-register, report capacity, and the scheduler begins placing pending instances on the returned nodes.

6. Once all nodes are back and instances are running, restore the original scaling targets:

```bash
cca apply services.ccattler
```

7. Run [post-incident verification](#post-incident-verification).

## Certificate expiry and CA loss

CCattler uses mTLS for all cluster communication. Certificates are short-lived (default 1 hour) with automatic rotation.

### Certificate expiry

**Symptom:** Agents disconnect, API calls fail with TLS errors, `journalctl -u cca-agent` shows certificate verification failures.

**Cause:** Automatic certificate rotation failed, or the CA that signed the certificates is unreachable.

1. Check certificate expiry on the affected node:

```bash
openssl x509 -in /etc/ccattler/node.pem -noout -dates
```

2. If the CA is still running and reachable, restart the agent to trigger re-enrollment:

```bash
sudo systemctl restart cca-agent
```

3. If the agent cannot reconnect (CA unreachable or certificate rejected), re-enroll the node:

```bash
# On the server
cca token create --node-id <node-id>

# On the node
sudo systemctl stop cca-agent
cca join <server-address> <token> --node-id <node-id> --ca-cert /etc/ccattler/ca.pem
sudo systemctl start cca-agent
```

### CA certificate loss

If the control-plane CA private key is lost, no new certificates can be issued. Existing certificates continue working until they expire (1 hour by default), then all mTLS communication fails.

**Recovery:**

1. If you have a CA certificate backup (see [Backup & Restore](/operations/backup)):

```bash
# Restore CA certificates from backup
tar xzf ccattler-certs-<date>.tar.gz -C /

# Restart the server
sudo systemctl restart cca-server
```

2. Agents with expired certificates will need re-enrollment. For each affected node:

```bash
cca token create --node-id <node-id>
# On the node
cca join <server-address> <token> --node-id <node-id> --ca-cert /etc/ccattler/ca.pem
sudo systemctl restart cca-agent
```

3. If there is no CA backup, you must generate a new CA and re-enroll every node in the cluster. This is equivalent to a full cluster rebuild from the certificate perspective:

```bash
# Stop the server
sudo systemctl stop cca-server

# Generate new CA (follow your initial cluster setup procedure)
# Start the server with the new CA
sudo systemctl start cca-server

# Re-enroll every node
for node_identifier in node-1 node-2 node-3; do
    cca token create --node-id "$node_identifier"
    # Distribute token and re-join from each node
done
```

4. Workloads continue running on nodes during this process. They are unaffected until the agent restarts — running containers do not depend on the control plane to keep operating.

**Prevention:** Back up CA certificates regularly and store them offline. See [Backup & Restore](/operations/backup) for the certificate backup command.

## Post-incident verification

After any incident, run through these checks to confirm the cluster has converged.

### Cluster overview

```bash
cca status
```

Verify all services show expected instance counts and all nodes show `ready`.

### Node health

```bash
cca get nodes
```

All nodes should be `ready`. Investigate any node still showing `unreachable` or `draining`.

### Instance state

```bash
cca get instances
```

Watch for:
- **`pending`** instances — indicates insufficient capacity or scheduling constraint issues
- **`failed`** instances — check service logs with `cca logs <service>`
- Instance counts matching desired counts per service

### Resource utilization

```bash
cca top
cca top nodes
cca top workloads
```

Verify no node is at capacity exhaustion. Check that workload resource usage is within expected bounds.

### Event log

```bash
cca events
```

Look for:
- Repeated failure/restart cycles
- Scheduling failures
- Certificate errors
- Reconciliation errors

### Reconciliation health

Confirm the reconciliation loop is running and processing changes:

```bash
curl -s http://localhost:9770/metrics | grep reconciliation
```

Key metrics to check:
- `reconciliation_cycle_total` — should be incrementing
- `reconciliation_cycle_duration_seconds` — should be stable, not growing
- `reconciliation_errors_total` — should not be increasing

### Fact store consistency

```bash
# Watch for active fact changes — should settle within 30 seconds after convergence
cca watch
```

If facts are still churning after two minutes, the cluster has not converged. Check controller logs:

```bash
journalctl -u cca-server --since "5 minutes ago" | grep -i "error\|fail\|reconcil"
```

### DNS and networking

If services use DNS or VIPs, verify they resolve correctly:

```bash
# DNS resolution
dig <service-name>.ccattler.local @<server-ip> -p 15353

# VIP connectivity
curl http://<vip-address>:<port>/health
```

### Convergence checklist

After all checks pass:

1. All nodes `ready`
2. All instances `running`, counts match desired
3. No `pending` or `failed` instances
4. Reconciliation metrics stable
5. Fact store changes settled
6. DNS and VIPs serving traffic
7. Events show no ongoing errors
