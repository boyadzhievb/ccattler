# CCattler Backup & Disaster Recovery Graph

Machine-readable reference for backup, snapshot, and disaster recovery
mechanisms. Purpose: understand what state is recoverable, how backups
work at each layer, and what recovery procedures exist — without reading
source files or the operations runbook.

## State Layers and Recoverability

```
┌─────────────────────────────────────────────────────────────────────┐
│                        RECOVERY STACK                               │
│                                                                     │
│  Layer 1: etcd (fact store)                                         │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  All cluster state: desired, effective, observed, intent,    │   │
│  │  placement, leases, audit events, config, secrets, roles     │   │
│  │                                                              │   │
│  │  Backup method: etcdctl snapshot save                        │   │
│  │  Recovery:      etcdctl snapshot restore                     │   │
│  │  Status:        Manual via etcdctl (M184 will add cca CLI)   │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  Layer 2: CA certificates & keys                                    │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  /etc/ccattler/ca.pem           — CA certificate             │   │
│  │  /etc/ccattler/ca-key.pem       — CA private key             │   │
│  │  /etc/ccattler/etcd-client.pem  — etcd client cert           │   │
│  │  /etc/ccattler/etcd-client-key.pem — etcd client key         │   │
│  │  /etc/ccattler/node.pem         — node certificate           │   │
│  │  /etc/ccattler/node-key.pem     — node private key           │   │
│  │                                                              │   │
│  │  Backup method: tar/cp of /etc/ccattler/*.pem                │   │
│  │  Recovery:      restore files, restart server/agent          │   │
│  │  If lost:       full re-enrollment of every node             │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  Layer 3: .cca configuration files                                  │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  Service definitions, network config, secrets, routes        │   │
│  │                                                              │   │
│  │  Backup method: version control (git)                        │   │
│  │  Recovery:      cca apply <files>                            │   │
│  │  This is the "source of truth" — even without etcd, these    │   │
│  │  files can rebuild desired state from scratch.               │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  Layer 4: Volume data (StorageProvider)                              │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  Stateful volume content (databases, files, etc.)            │   │
│  │                                                              │   │
│  │  Backup method: StorageProvider.SnapshotVolume()             │   │
│  │  Recovery:      restore from snapshot (provider-specific)    │   │
│  │  Status:        Simulator records snapshots; real backends   │   │
│  │                 would use block-level snapshots              │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  NOT backed up (ephemeral / reconstructible):                       │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  • Container images (pulled from registry on demand)         │   │
│  │  • Running container state (rebuilt by reconciliation)       │   │
│  │  • Node leases (re-established on agent restart)             │   │
│  │  • Observed facts (re-reported by agents on reconnect)       │   │
│  │  • Scheduler cache (rebuilt from facts on startup)           │   │
│  │  • Metrics & traces (ephemeral, scraped by Prometheus)       │   │
│  │  • Network state: veth, bridge, VXLAN, nftables, eBPF maps  │   │
│  │    (reconstructed by network controller + data plane)        │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

## etcd Snapshot: Current State

CCattler does **not** yet have built-in `cca backup` commands. etcd
backups are performed using `etcdctl` directly. M184 (Etcd Backup &
Restore) will add `cca backup create/restore` with scheduled automatic
backups and retention policies.

### Manual Snapshot Procedure

```
┌────────────┐    etcdctl snapshot save     ┌──────────────────┐
│   etcd     │ ─────────────────────────── → │ snapshot.db file │
│  cluster   │                               │ (on disk)        │
│ (one node) │    etcdctl snapshot restore   │                  │
│            │ ← ─────────────────────────── │                  │
└────────────┘                               └──────────────────┘
```

### Snapshot Commands

```bash
# Save snapshot (run on any etcd member)
etcdctl snapshot save /var/lib/etcd-backup/snapshot-$(date +%Y%m%d-%H%M%S).db \
  --endpoints=https://127.0.0.1:2379 \
  --cert=/etc/ccattler/etcd-client.pem \
  --key=/etc/ccattler/etcd-client-key.pem \
  --cacert=/etc/ccattler/ca.pem

# Verify snapshot integrity
etcdctl snapshot status /var/lib/etcd-backup/snapshot-YYYYMMDD-HHMMSS.db \
  --write-out=table

# Restore from snapshot (all etcd members must be stopped first)
sudo systemctl stop etcd
sudo rm -rf /var/lib/etcd/default.etcd
etcdctl snapshot restore /var/lib/etcd-backup/snapshot-YYYYMMDD-HHMMSS.db \
  --data-dir=/var/lib/etcd/default.etcd \
  --name=<member-name> \
  --initial-cluster=<member-name>=https://<ip>:2380 \
  --initial-advertise-peer-urls=https://<ip>:2380
sudo systemctl start etcd
```

### What a Snapshot Contains

| Prefix | Description | Critical? |
|--------|-------------|-----------|
| `desired/service/` | Service definitions from .cca files | Yes — but rebuildable from git |
| `desired/connection/` | Connection definitions | Yes — rebuildable from git |
| `desired/route/` | Route definitions | Yes — rebuildable from git |
| `desired/config/` | Config maps and secrets | Yes — secrets may not be in git |
| `effective/service/` | Resolved instance counts, resources | No — recomputed by IntentResolver |
| `intent/user/service/` | User intent (scale commands) | Yes — manual scale overrides |
| `intent/autoscaler/service/` | Autoscaler intent | No — recomputed by autoscaler |
| `observed/node/` | Node states, resources | No — re-reported by agents |
| `observed/instance/` | Instance states | No — re-reported by agents |
| `placement/` | Scheduler assignments | No — re-placed by scheduler |
| `endpoint/` | Service endpoints | No — recomputed by endpoint controller |
| `audit/` | Audit log entries | Partially — historical audit lost if not backed up |
| `auth/` | RBAC grants, tokens | Yes — must be restored or re-granted |
| `lease/` | Leader election, node heartbeats | No — re-established on startup |

## Store Interface: No Snapshot API

The `StateStore` interface (`store/store.go:117`) does **not** include
snapshot or backup methods. Current methods:

```
StateStore interface:
  Get(key) → Fact
  Put(key, value) → revision
  Delete(key)
  Scan(prefix) → []Fact
  ScanWithRevision(prefix) → ScanResult{Facts, Revision}
  Watch(key, opts) → chan Event
  GrantLease / PutWithLease / RenewLease / RevokeLease
  Transaction(compares, onSuccess, onFailure) → bool
  Revision() → int64
  Close()
```

Snapshots are external to the store interface — `etcdctl` operates at
the etcd server level, not through the Go client API. The MemoryStore
uses "snapshot" terminology (`snapshotMatchingFacts` at
`store/memory.go:312`) for consistent reads, not for backup purposes.

## Volume Snapshots (StorageProvider)

The `StorageProvider` interface (`storage/storage.go:38`) includes:

```go
SnapshotVolume(ctx, volumeName, snapshotName) error  // storage/storage.go:41
```

### Implementations

| Backend | File | Snapshot Behavior |
|---------|------|-------------------|
| SimulatorStorageProvider | `storage/simulator.go:150` | Records snapshot metadata only, no real data copy |
| (Future: local disk) | — | Would use filesystem snapshots (LVM, btrfs) |
| (Future: cloud block) | — | Would use cloud API (EBS snapshot, Azure Disk snapshot) |

### Simulator Snapshot Tracking

```go
// storage/simulator.go:20-22
type simulatedSnapshot struct {
    volumeName string
}

// storage/simulator.go:32-33
snapshots map[string]*simulatedSnapshot
```

The stateful volume controller (`controllers/stateful_volume.go`) calls
`SnapshotVolume` before force-detaching a volume during migration. The
fact `last_snapshot` records when the snapshot was taken.

## Disaster Recovery Scenarios

Full procedures documented in `website/operations/disaster-recovery.md`.
Summary of recovery flow per scenario:

```
┌─────────────────────────────────────────────────────────────────────┐
│                     DISASTER RECOVERY MATRIX                        │
├─────────────────────────┬───────────────────────────────────────────┤
│ Scenario                │ Recovery Path                             │
├─────────────────────────┼───────────────────────────────────────────┤
│ Control plane failure   │ Restart etcd → restart cca-server →      │
│ (infra intact)          │ agents reconnect automatically            │
├─────────────────────────┼───────────────────────────────────────────┤
│ etcd data loss          │ Restore snapshot → restart server →      │
│ (snapshot available)    │ agents re-report → reconciliation burst   │
├─────────────────────────┼───────────────────────────────────────────┤
│ etcd data loss          │ Fresh etcd → cca apply from git →        │
│ (no snapshot)           │ re-enroll all nodes → full rebuild        │
├─────────────────────────┼───────────────────────────────────────────┤
│ Split-brain / network   │ Self-healing: agents cache state, upon   │
│ partition               │ reconnect → dedup excess instances        │
├─────────────────────────┼───────────────────────────────────────────┤
│ Leader failover         │ Automatic: standby acquires etcd lease    │
│ (HA mode)               │ within lease TTL (15s default)            │
├─────────────────────────┼───────────────────────────────────────────┤
│ Mass node failure       │ Failure controller reschedules →          │
│                         │ manual node replacement if capacity short │
├─────────────────────────┼───────────────────────────────────────────┤
│ CA certificate loss     │ Restore from backup → restart server →   │
│ (backup available)      │ re-enroll expired-cert nodes              │
├─────────────────────────┼───────────────────────────────────────────┤
│ CA certificate loss     │ New CA → re-enroll every node →          │
│ (no backup)             │ equivalent to cert-level cluster rebuild  │
└─────────────────────────┴───────────────────────────────────────────┘
```

### Recovery Flow Diagram

```
            ┌──────────────┐
            │ INCIDENT     │
            │ DETECTED     │
            └──────┬───────┘
                   │
            ┌──────▼───────┐
            │ Assess scope │
            │ • etcd ok?   │
            │ • CA ok?     │
            │ • Nodes ok?  │
            └──────┬───────┘
                   │
         ┌─────────┴─────────┐
         │                   │
    etcd intact         etcd lost
         │                   │
    ┌────▼────┐      ┌──────▼──────┐
    │ Restart │      │ Snapshot    │
    │ services│      │ available?  │
    └────┬────┘      └──┬──────┬──┘
         │           yes│      │no
         │              │      │
         │    ┌─────────▼┐  ┌──▼──────────┐
         │    │ Restore  │  │ Fresh etcd  │
         │    │ snapshot │  │ + re-enroll │
         │    └─────┬────┘  │ + cca apply │
         │          │       └──────┬──────┘
         │          │              │
         └──────────┴──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ Agents       │
                    │ reconnect    │
                    │ + re-report  │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ Reconcile    │
                    │ converges    │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ Post-incident│
                    │ verification │
                    │ (cca status) │
                    └──────────────┘
```

## Self-Healing Properties

CCattler's fact-based architecture provides inherent recovery
properties that reduce the blast radius of most failures:

| Property | Mechanism | File Reference |
|----------|-----------|----------------|
| Agent cached state | Agents continue running workloads from last known desired state when control plane is unreachable | `agent/` (agent caches desired facts) |
| Observed state re-reporting | Agents re-report all observed facts on reconnect | `agent/` (lease renewal triggers full report) |
| Reconciliation convergence | Controllers re-derive all effective state from desired + observed | `controllers/runner.go` (30s resync interval) |
| Scheduler re-placement | Scheduler re-places unscheduled instances on next cycle | `scheduler/scheduler.go` |
| Endpoint reconstruction | Endpoint controller rebuilds service endpoints from instance facts | `controllers/endpoint.go` |
| Network reconstruction | Network controller + data plane rebuild all VXLAN, bridge, nftables, eBPF state | `controllers/network.go`, `network/` |
| DNS reconstruction | Cluster DNS rebuilt from effective facts | `controllers/stateful_dns.go` |

### Convergence Timeline After etcd Restore

```
T+0s     etcd restored, cca-server started
T+0-5s   Server connects to etcd, starts controller runner
T+5-30s  Agents detect server available, renew leases
T+5-30s  Agents re-report observed/node/* and observed/instance/*
T+30s    First full resync cycle fires (controllers/runner.go resync=30s)
T+30-60s Controllers reconcile desired vs observed → changes applied
T+60s    Cluster should be converged (check: cca status, cca watch)
```

## Chaos Testing Integration

The chaos benchmark framework (`chaos/recovery_report.go`) validates
recovery from injected failures:

```go
// chaos/recovery_report.go:15-31
RecoveryReport {
    NodeCount           int
    ServiceCount        int
    TotalInstances      int
    DeployConvergenceTime Duration
    ChaosEvents         []ChaosEvent
    Metrics             RecoveryMetrics
    FinalConverged      bool
}

// chaos/recovery_report.go:35-53
RecoveryMetrics {
    TotalInjections        int
    SuccessfulRecoveries   int
    FailedRecoveries       int
    RecoverySuccessRate    float64  // percentage
    MeanConvergenceTime    Duration
    P50ConvergenceTime     Duration
    P95ConvergenceTime     Duration
    P99ConvergenceTime     Duration
    MaxConvergenceTime     Duration
}
```

This provides measurable evidence of recovery capability across
controller failure isolation, HA resilience, and chaos recovery.

## Constants and Timeouts

| Constant | Value | Source | Purpose |
|----------|-------|--------|---------|
| etcd dial timeout | 5s | `store/etcd.go:23` | Connection establishment |
| etcd watch buffer | 64 | `store/etcd.go:28` | Event delivery buffering |
| Controller resync interval | 30s | `controllers/runner.go` | Forced full reconciliation |
| Controller debounce | 50ms | `controllers/runner.go` | Quiet period between cycles |
| CAS retry max | 5 | `controllers/runner.go` | Transaction retry limit |
| Node lease TTL | 30s | Agent config | Unreachable detection |
| Leader lease TTL | 15s | `controllers/leader.go` | Leader failover detection |
| Cert default validity | 1h | `security/ca.go` | Short-lived for rotation |

## Future: M184 — Etcd Backup & Restore (Not Yet Implemented)

From `milestones.md`:

```
M184 — Etcd Backup & Restore
  • cca backup create     — trigger etcd snapshot
  • cca backup restore    — restore from snapshot
  • Scheduled automatic backups with cron-like configuration
  • Retention policy (keep N snapshots, max age)
  • Point-in-time restore
```

### Planned Architecture

```
                          ┌───────────────┐
                          │ cca backup    │
                          │ create        │
                          └───────┬───────┘
                                  │
                          ┌───────▼───────┐
                          │ etcdctl       │
                          │ snapshot save │
                          └───────┬───────┘
                                  │
              ┌───────────────────┼───────────────────┐
              │                   │                   │
       ┌──────▼──────┐   ┌───────▼───────┐   ┌──────▼──────┐
       │ Local disk  │   │ Remote store  │   │ Cloud blob  │
       │ /var/lib/   │   │ (rsync/scp)   │   │ (S3/GCS/    │
       │ etcd-backup │   │               │   │  Azure Blob)│
       └─────────────┘   └───────────────┘   └─────────────┘
```

This will add:
- `StateStore` extension or wrapper for snapshot operations
- Scheduled backup controller (cron-like, runs on leader only)
- Retention GC (delete old snapshots beyond policy)
- `cca backup list` to see available snapshots
- Backup status facts in the store for observability

## File Reference

| File | Role |
|------|------|
| `store/etcd.go` | etcd client, connection, write rate limiter |
| `store/store.go:117` | StateStore interface (no snapshot methods) |
| `store/memory.go:312` | MemoryStore.snapshotMatchingFacts (read consistency, not backup) |
| `storage/storage.go:38` | StorageProvider interface with SnapshotVolume |
| `storage/simulator.go:150` | SimulatorStorageProvider.SnapshotVolume (records metadata) |
| `chaos/recovery_report.go` | Recovery benchmarking types and metrics |
| `controllers/leader.go` | Leader election (HA failover) |
| `controllers/ha_runner.go` | HA controller runner |
| `security/ca.go` | CA certificate management |
| `security/rotator.go` | Certificate rotation |
| `security/enrollment.go` | Node enrollment (re-enrollment after CA loss) |
| `website/operations/disaster-recovery.md` | Full recovery runbook (7 scenarios) |
| `cmd/cca/command_init.go:441` | etcdctl health check during init |
