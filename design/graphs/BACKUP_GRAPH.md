# CCattler Backup & Recovery Call Graph Reference

Machine-readable function call index for backup, volume snapshots, chaos testing,
and disaster recovery. Purpose: avoid re-reading source files to trace recovery
flow. Load this file instead.

## StorageProvider Interface (storage/storage.go)

### StorageProvider → pluggable volume operations
```
StorageProvider interface                            [storage/storage.go:12]
├── CreateVolume(ctx, volumeName, sizeBytes) error
├── DeleteVolume(ctx, volumeName) error
├── AttachVolume(ctx, volumeName, nodeID) (string, error)
├── DetachVolume(ctx, volumeName, nodeID) error
├── IsAttached(ctx, volumeName) (bool, string, error)
├── SnapshotVolume(ctx, volumeName, snapshotName) error
├── VolumeUsage(ctx, volumeName) (usedBytes, totalBytes, error)
└── ResizeVolume(ctx, volumeName, newSizeBytes) error
```

## SimulatorStorageProvider (storage/simulator.go)

### CreateVolume → add volume to in-memory state
```
SimulatorStorageProvider.CreateVolume(_, volumeName, sizeBytes) [storage/simulator.go:52]
├── mutex.Lock
├── IF alreadyExists → return nil                   [idempotent]
└── volumes[volumeName] = &simulatedVolume{sizeBytes}
```

### DeleteVolume → remove volume
```
SimulatorStorageProvider.DeleteVolume(_, volumeName) [storage/simulator.go:69]
├── mutex.Lock
├── IF !volumeExists → return nil                   [idempotent]
├── IF attachedNodeID != "" → return error          [cannot delete attached volume]
└── delete(volumes, volumeName)
```

### AttachVolume → bind volume to node
```
SimulatorStorageProvider.AttachVolume(_, volumeName, nodeID) [storage/simulator.go:91]
├── mutex.Lock
├── IF !volumeExists → return error
├── IF attached to different node → return error    [ReadWriteOnce enforcement]
├── existingVolume.attachedNodeID = nodeID
└── return "/mnt/volumes/{volumeName}"
```

### DetachVolume → unbind volume from node
```
SimulatorStorageProvider.DetachVolume(_, volumeName, nodeID) [storage/simulator.go:113]
├── mutex.Lock
├── IF !volumeExists → return nil                   [idempotent]
└── IF attachedNodeID == nodeID → clear attach state
```

### SnapshotVolume → record snapshot metadata
```
SimulatorStorageProvider.SnapshotVolume(_, volumeName, snapshotName) [storage/simulator.go:150]
├── mutex.Lock
├── IF !volumeExists → return error
└── snapshots[snapshotName] = &simulatedSnapshot{volumeName}
    [simulator only records metadata, no real data copy]
```

### ForceDetach → reclaim volume from unreachable node
```
SimulatorStorageProvider.ForceDetach(_, volumeName)  [storage/simulator.go:235]
├── mutex.Lock
├── IF !volumeExists → return nil
└── clear attachedNodeID + mountPath
```

### ResizeVolume → expand volume capacity
```
SimulatorStorageProvider.ResizeVolume(_, volumeName, newSizeBytes) [storage/simulator.go:210]
├── mutex.Lock
├── IF injectedResizeError → return error, clear injection
├── IF !volumeExists → return error
├── IF newSizeBytes < current → return error        [shrink not allowed]
└── existingVolume.sizeBytes = newSizeBytes
```

## StatefulVolumeController (controllers/stateful_volume.go)

### Reconcile → create per-ordinal volumes for stateful services
```
StatefulVolumeController.Reconcile(_, facts)        [controllers/stateful_volume.go:46]
├── parseEffectiveServiceFacts(facts)               [→ desiredCounts, statefulServices]
├── parseServiceVolumeMountsWithPaths(facts)        [→ serviceName→[]volumeMountBinding]
├── parseVolumeTemplates(facts)                     [→ volumeName→volumeTemplate]
├── parseExistingDesiredVolumeNames(facts)          [→ set of existing volume names]
└── FOR EACH stateful service (sorted):
    └── reconcilePerOrdinalVolumes(serviceName, count, mounts, templates, existing)
        [emits: Put desired/volume/{svc}-{ordinal}-{vol}/size, .../persistent]
```

## StateStore Interface (store/store.go)

### StateStore → no snapshot methods
```
StateStore interface                                [store/store.go:117]
├── Get(ctx, key) → *Fact, error
├── Put(ctx, key, value) → revision, error
├── Delete(ctx, key) → error
├── Scan(ctx, prefix) → []Fact, error
├── ScanWithRevision(ctx, prefix) → *ScanResult, error
├── Watch(ctx, key, opts) → <-chan Event, error
├── GrantLease(ctx, ttlSeconds) → leaseID, error
├── PutWithLease(ctx, key, value, leaseID) → revision, error
├── RenewLease(ctx, leaseID) → ttl, error
├── RevokeLease(ctx, leaseID) → error
├── Transaction(ctx, compares, onSuccess, onFailure) → bool, error
├── Revision(ctx) → int64, error
└── Close() → error
[NOTE: no Snapshot/Backup methods — etcd snapshots are external via etcdctl]
```

## EtcdStore Connection (store/etcd.go)

### NewEtcdStore → connect to etcd cluster
```
NewEtcdStore(config)                                [store/etcd.go:67]
├── clientv3.New(clientv3.Config{
│       Endpoints, DialTimeout, Username, Password, TLS,
│   })
├── Status(endpoints[0])                            [verify connectivity within DialTimeout]
│   └── IF error → Close() → return error
└── return EtcdStore{etcdClient, keyPrefix, writeLimiter: newWriteRateLimiter()}
```

### Write Rate Limiter → protects etcd from write floods
```
EtcdStore.Put / Delete / PutWithLease / Transaction
├── checkClosed
├── writeLimiter.allowWrite()                       [store/write_rate_limiter.go:66]
│   ├── refill tokens (elapsed × ratePerSecond, cap at burstSize)
│   ├── IF tokens < 1 → return ErrWriteRateLimited
│   └── consume 1 token → proceed
└── (actual etcd operation)
```

## Chaos Testing (chaos/)

### ChaosRunner.Run → orchestrate failure injection
```
ChaosRunner.Run(ctx)                                [chaos/chaos_runner.go]
├── FOR duration (config.Duration):
│   ├── selectRandomScenario()
│   │   └── pick from config.EnabledScenarios
│   ├── injectFailure(scenario)
│   │   ├── ScenarioNodeKill → cluster.KillNode(randomAliveNode)
│   │   ├── ScenarioNodePartition → cluster.PartitionNode(randomAliveNode)
│   │   ├── ScenarioControllerRestart → cluster.RestartControllers(ctx)
│   │   ├── ScenarioScaleChange → cluster.SetServiceScale(ctx, svc, newCount)
│   │   └── ScenarioNodeRecovery → cluster.RestartNode(ctx, randomDeadNode)
│   ├── waitForConvergence(ctx, config.ConvergenceTimeout)
│   │   └── poll cluster.CheckConvergence every 50ms
│   ├── record ChaosEvent{Elapsed, Scenario, Target, Converged, ConvergenceTime}
│   └── time.After(config.InjectionInterval)
└── return events
```

### SimulatedChaosCluster → in-process cluster control
```
SimulatedChaosCluster                               [chaos/chaos_cluster.go]
├── NodeIDs() → []string
├── IsNodeAlive(nodeID) → bool
├── KillNode(nodeID)
│   └── cancel node's agent context
├── RestartNode(ctx, nodeID)
│   └── create new agent, start in goroutine
├── PartitionNode(nodeID)
│   └── partitionedStores[nodeID].Partition()       [blocks all store ops]
├── HealNode(nodeID)
│   └── partitionedStores[nodeID].Heal()            [unblocks store ops]
├── RestartControllers(ctx)
│   ├── controllerCancel()                          [stop current runner]
│   ├── time.After(10ms)                            [settle delay]
│   └── start new Runner in goroutine
├── SetServiceScale(ctx, serviceName, count)
│   └── store.Put(effective/service/{name}/instances, count)
├── CheckConvergence(ctx) → (bool, string)
│   └── FOR EACH service:
│       compare desired count vs running instances on alive nodes
└── ServiceNames() → []string
```

### PartitionedStore → network partition simulation
```
PartitionedStore                                    [chaos/partitioned_store.go]
├── wraps a real StateStore
├── Partition() → set isPartitioned = true
│   └── all Get/Put/Delete/Scan/Watch return ErrPartitioned
├── Heal() → set isPartitioned = false
│   └── operations pass through to underlying store
└── all StateStore methods check isPartitioned first
```

### ComputeRecoveryMetrics → aggregate chaos results
```
ComputeRecoveryMetrics(events)                      [chaos/recovery_report.go:58]
├── count SuccessfulRecoveries / FailedRecoveries
├── RecoverySuccessRate = successful / total * 100
├── sort successful durations
├── MeanConvergenceTime = sum(durations) / count
├── P50ConvergenceTime = percentileDuration(sorted, 50)
├── P95ConvergenceTime = percentileDuration(sorted, 95)
├── P99ConvergenceTime = percentileDuration(sorted, 99)
└── MaxConvergenceTime = sorted[last]
```

### RecoveryReport.ToJSON → serialize results
```
RecoveryReport.ToJSON()                             [chaos/recovery_report.go:115]
└── json.MarshalIndent(report, "", "  ")
```

## Store Key Patterns (backup-relevant)

### Recoverable State (in etcd snapshot)
| Key Prefix | Critical? | Rebuildable? | Method |
|---|---|---|---|
| `desired/service/` | Yes | From .cca files in git | `cca apply` |
| `desired/connection/` | Yes | From .cca files in git | `cca apply` |
| `desired/route/` | Yes | From .cca files in git | `cca apply` |
| `desired/config/` | Yes | Secrets may not be in git | Manual re-entry |
| `desired/volume/` | Yes | From .cca files | `cca apply` |
| `auth/` | Yes | Not in git | Must restore or re-grant |
| `intent/user/service/` | Yes | Manual scale overrides | `cca scale` |
| `audit/` | Partial | Historical lost if not backed up | N/A |

### Auto-Reconstructible State (no backup needed)
| Key Prefix | Reconstructed By |
|---|---|
| `effective/service/` | IntentResolverController |
| `observed/node/` | Agent heartbeats on reconnect |
| `observed/instance/` | Agent re-reports on reconnect |
| `placement/` | Scheduler on next cycle |
| `endpoint/` | EndpointController |
| `derived/` | Various controllers |
| `lease/` | Agent/leader re-establish |
| `intent/autoscaler/` | AutoscaleController |

### Volume Snapshot Keys
| Key Pattern | Written By | Read By |
|---|---|---|
| `desired/volume/{name}/size` | `StatefulVolumeController` (per-ordinal), compiler | StorageController |
| `desired/volume/{name}/persistent` | `StatefulVolumeController`, compiler | StorageController |

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `SimulatorStorageProvider.CreateVolume` | StorageController.Reconcile |
| `SimulatorStorageProvider.DeleteVolume` | StorageController.Reconcile |
| `SimulatorStorageProvider.AttachVolume` | Agent (`ensureVolumesAttachedForInstance`) |
| `SimulatorStorageProvider.DetachVolume` | StorageController.Reconcile |
| `SimulatorStorageProvider.SnapshotVolume` | StatefulVolumeController (before force-detach) |
| `SimulatorStorageProvider.ForceDetach` | StorageController.Reconcile (unreachable node) |
| `SimulatorStorageProvider.ResizeVolume` | StorageController.Reconcile (expansion) |
| `SimulatorStorageProvider.VolumeUsage` | Agent (`publishVolumeUsageMetrics`) |
| `StatefulVolumeController.Reconcile` | Runner via `attemptSingleReconciliation` |
| `NewEtcdStore` | `runServerCommand` (cmd/cca/command_server.go) |
| `ChaosRunner.Run` | chaos benchmark tests, `cca benchmark` CLI |
| `ComputeRecoveryMetrics` | chaos benchmark tests (after ChaosRunner.Run) |
| `RecoveryReport.ToJSON` | chaos benchmark tests (for output) |
| `PartitionedStore.Partition` | `SimulatedChaosCluster.PartitionNode` |
| `PartitionedStore.Heal` | `SimulatedChaosCluster.HealNode` |
| `SimulatedChaosCluster.KillNode` | `ChaosRunner.injectFailure` (ScenarioNodeKill) |
| `SimulatedChaosCluster.RestartNode` | `ChaosRunner.injectFailure` (ScenarioNodeRecovery) |
| `SimulatedChaosCluster.PartitionNode` | `ChaosRunner.injectFailure` (ScenarioNodePartition) |
| `SimulatedChaosCluster.RestartControllers` | `ChaosRunner.injectFailure` (ScenarioControllerRestart) |
| `SimulatedChaosCluster.SetServiceScale` | `ChaosRunner.injectFailure` (ScenarioScaleChange) |
| `SimulatedChaosCluster.CheckConvergence` | `ChaosRunner.waitForConvergence` |

## Error Propagation Paths

### Volume operations
```
StorageProvider.CreateVolume
└── volume already exists → no-op (idempotent)

StorageProvider.AttachVolume
├── volume not found → return error
│   └── Agent: ensureVolumesAttachedForInstance → BAIL (skip instance startup)
└── attached to different node → return error (ReadWriteOnce)
    └── Agent: ensureVolumesAttachedForInstance → BAIL (skip instance startup)

StorageProvider.DeleteVolume
├── volume attached → return error
│   └── StorageController: log error, retry next cycle
└── volume not found → no-op (idempotent)

StorageProvider.SnapshotVolume
└── volume not found → return error
    └── caller: log error, continue
```

### etcd connection errors (backup context)
```
NewEtcdStore
├── clientv3.New fails → return error
│   └── server startup fails
└── Status(endpoints[0]) fails → Close + return error
    └── server startup fails with "Is etcd running? Check: systemctl status etcd"
```

### Chaos injection errors
```
ChaosRunner.Run
├── KillNode → cancel context (always succeeds)
├── PartitionNode → set flag (always succeeds)
├── RestartControllers → cancel + restart (settle delay 10ms)
│   └── IF runner.Run fails → LOG ERROR
├── SetServiceScale → store.Put
│   └── IF store error → panic (test infrastructure)
└── waitForConvergence → timeout
    └── ChaosEvent.Converged = false (test records failure)
```

## Recovery Data Flow Chains

### etcd snapshot restore → full convergence
```
etcdctl snapshot restore → etcd starts with snapshot data
  → cca-server connects to etcd (NewEtcdStore)
  → HARunner starts, acquires leadership
  → Runner.Run starts all controllers
  → Controllers read desired/* from restored snapshot
  → IntentResolverController writes effective/* (recomputed)
  → InstanceController creates observed/instance/{id}/state=pending
  → Scheduler writes placement/node/{nodeID}/{id}
  → Agents reconnect, renew leases
  → Agents read placement/node/{nodeID}/, start instances
  → Agents write observed/instance/{id}/state=running
  → Cluster converged (~30-60s)
```

### .cca file rebuild (no snapshot available)
```
Fresh etcd → cca-server starts with empty store
  → cca apply <files> writes desired/* facts
  → re-enroll all nodes (cca join <server> <token>)
  → Agents get certificates, start heartbeating
  → Controllers see desired/* + observed/node/* → reconcile
  → Full cluster rebuild from source of truth
```

### Node failure → automatic recovery
```
Agent stops heartbeating
  → lease/node/{id} timestamp goes stale
  → NodeFailureController.Reconcile reads lease timestamps
  → identifyUnreachableNodes: age > 30s → Change: state=unreachable
  → markInstancesOnUnreachableNodesAsFailed: Change: node_failure=true
  → FailureController.Reconcile: creates replacement instances
  → Scheduler places replacements on alive nodes
  → Agents start replacement instances
  → Cluster reconverged
```

## Constants Reference

| Constant | Value | Location | Purpose |
|---|---|---|---|
| `defaultEtcdDialTimeout` | 5s | store/etcd.go:23 | etcd connection establishment |
| `defaultEtcdWatchBuffer` | 64 | store/etcd.go:28 | Watch event channel buffer |
| `defaultWriteRatePerSecond` | 500 | store/write_rate_limiter.go:17 | Sustained etcd write rate |
| `defaultNodeFailureLeaseTimeout` | 30s | controllers/nodefailure.go:22 | Heartbeat expiry threshold |
| `defaultControllerResyncInterval` | 30s | controllers/runner.go:30 | Full reconciliation period |
| `chaosConvergencePollInterval` | 50ms | chaos/chaos_runner.go:40 | Convergence check frequency |
| `defaultChaosAgentInterval` | 50ms | chaos/chaos_cluster.go:26 | Agent reconcile interval in chaos |
| `defaultChaosControllerDebounce` | 10ms | chaos/chaos_cluster.go:30 | Controller debounce in chaos |
| `defaultChaosLeaseTimeout` | 300ms | chaos/chaos_cluster.go:35 | Fast failure detection in chaos |
| `controllerRestartSettleDelay` | 10ms | chaos/chaos_cluster.go:39 | Pause between cancel and restart |

## File Reference

| File | Purpose |
|---|---|
| `store/store.go` | StateStore interface (no snapshot methods) |
| `store/etcd.go` | EtcdStore: connection, CRUD, watch, transactions |
| `store/write_rate_limiter.go` | Token-bucket write rate limiter |
| `storage/storage.go` | StorageProvider interface with SnapshotVolume |
| `storage/simulator.go` | SimulatorStorageProvider: in-memory volume tracking |
| `controllers/stateful_volume.go` | Per-ordinal volume creation for stateful services |
| `chaos/chaos_runner.go` | ChaosRunner: random failure injection + convergence check |
| `chaos/chaos_cluster.go` | SimulatedChaosCluster: in-process cluster control |
| `chaos/partitioned_store.go` | PartitionedStore: network partition simulation |
| `chaos/recovery_report.go` | RecoveryReport + RecoveryMetrics: chaos results |
| `controllers/nodefailure.go` | Node failure detection: heartbeat → unreachable → instance failure |
| `controllers/failure.go` | FailureController: replacement pipeline for failed instances |
| `security/enrollment.go` | Node enrollment: join tokens, re-enrollment after CA loss |
| `security/rotator.go` | CertificateRotator: auto-renewal before expiry |
