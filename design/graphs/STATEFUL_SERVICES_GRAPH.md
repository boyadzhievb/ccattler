# CCattler Stateful Services Function Call Graph

Machine-readable function-level index for stateful service handling across all
controllers and the scheduler. Purpose: trace exact call chains, store key
patterns, and data flows without reading source files. Load this file instead.

---

## 1. DSL → Facts (lang/)

### Parser: lang/parser.go
```
parseService → parseServiceBody
├── case "stateful":                                           :303
│   └── serviceDecl.Stateful = true
└── case "volume":                                             [volume mount binding]
    └── serviceDecl.Volumes = append(...)
```

### AST: lang/ast.go
```
ServiceDecl                                                    :74
├── Stateful bool                                              [stateful flag]
└── Volumes  []VolumeMount                                     [volume mount bindings]
```

### Compiler: lang/compiler.go
```
compileServiceFacts(serviceDecl)
└── IF serviceDecl.Stateful:                                   :357–362
    ├── Fact{ Key: desired/service/{name}/stateful,   Value: "true" }
    └── Fact{ Key: effective/service/{name}/stateful, Value: "true" }
```

---

## 2. InstanceController — Stateful Branching (controllers/instance.go)

### InstanceController.Reconcile :77
```
InstanceController.Reconcile(ctx, facts)
├── parseEffectiveServiceFacts(facts)                          :310
│   └── reads effective/service/{name}/stateful
│   └── returns (desiredCounts map[svc]int, statefulServices map[svc]bool, perNodeServices map[svc]bool)
├── parseInstanceFieldsFromFacts(facts)
├── buildInstanceMapsFromFields(fields)                        :359
├── buildActiveInstanceIDsByService(...)                        :375
│
├── reconcileScaleDowns(facts, desiredCounts, ...)             :131
│   └── FOR EACH service needing scale-down:
│       ├── IF per-node: markPerNodeExcessInstancesAsStopped   :528
│       ├── IF stateful: stopHighestOrdinalInstances           :465  [see §2.2]
│       └── ELSE: markExcessStatelessInstancesAsStopped        :501
│
├── computeServiceDeficits(desiredCounts, actualCounts, ...)   :174
│   └── FOR EACH service:
│       ├── IF stateful: deficit = min(1, want - have)         [max 1 per cycle]
│       └── ELSE: deficit = want - have
│
└── allocateCreationsFairly(deficits, budget=30, ...)          :209
    └── FOR EACH service with deficit (priority-sorted):
        ├── IF stateful:
        │   └── createNextStatefulInstance(...)                 :420  [see §2.1]
        └── ELSE:
            └── createPendingInstances(service, count)         :482
```

### 2.1 createNextStatefulInstance :420
```
createNextStatefulInstance(serviceName, wantCount, activeIDs, states, services, facts)
├── parseExistingOrdinals(serviceName, activeIDs)              :396
│   └── extract ordinal integers from "{svc}-{N}" instance IDs
│   └── sort ascending
│
├── FOR nextOrdinal in 0..wantCount-1:
│   ├── IF ordinal exists AND state == running → continue
│   ├── IF ordinal exists AND state != running → return nil    [block: wait for N before N+1]
│   └── IF ordinal missing:
│       ├── instanceID = "{serviceName}-{ordinal}"
│       ├── clear stale markers (controller_stopped, node_failure) if present
│       └── return changes:
│           ├── PUT observed/instance/{svc}-{ord}          = ""
│           ├── PUT observed/instance/{svc}-{ord}/service  = "{serviceName}"
│           ├── PUT observed/instance/{svc}-{ord}/state    = "pending"
│           └── PUT observed/instance/{svc}-{ord}/ordinal  = "{ordinal}"
│
└── return nil (all ordinals satisfied)
```

### 2.2 stopHighestOrdinalInstances :465
```
stopHighestOrdinalInstances(serviceName, existingOrdinals, count)
├── sort existingOrdinals descending
├── take first `count` ordinals
└── FOR EACH ordinal to stop:
    └── PUT derived/instance/{svc}-{ord}/controller_stopped = "true"
```

### Key invariants

- **Ordered startup**: instance N+1 is NEVER created until instance N is running
- **Reverse scale-down**: highest ordinal is stopped first
- **Budget = 1**: stateful services contribute at most 1 deficit per cycle
- **ID format**: `{serviceName}-{ordinal}` (not random UUID)

---

## 3. StatefulVolumeController (controllers/stateful_volume.go)

### StatefulVolumeController.Reconcile :46
```
StatefulVolumeController.Reconcile(ctx, facts)
├── parseEffectiveServiceFacts(facts)                          [shared]
│   └── desiredCounts, statefulServices
├── parseServiceVolumeMountsWithPaths(facts)                   :84
│   └── reads desired/service/{name}/volume/{vol} → mountPath
│   └── returns map[serviceName][]volumeMountBinding{volumeName, mountPath}
├── parseVolumeTemplates(facts)                                :108
│   └── reads desired/volume/{vol}/size, desired/volume/{vol}/persistent
│   └── returns map[volumeName]volumeTemplate{size, persistent}
├── parseExistingDesiredVolumeNames(facts)                     :129
│   └── reads desired/volume/{name} root markers
│   └── returns map[volumeName]bool
│
└── FOR EACH stateful service (sorted):
    └── reconcilePerOrdinalVolumes(svc, count, mounts, templates, existing)  :144
        └── FOR ordinal in 0..count-1:
            └── FOR EACH mount:
                ├── perOrdinalVolumeName = "{svc}-{ordinal}-{volumeName}"
                ├── IF already exists: skip
                └── ELSE emit groupedChanges("vol-ordinal/{name}"):
                    ├── PUT desired/volume/{name}            = ""
                    ├── PUT desired/volume/{name}/size        = "{template.size}"
                    └── PUT desired/volume/{name}/persistent  = "{template.persistent|true}"
```

### Naming convention
```
Volume template: pgdata → { size: "100Gi", persistent: "true" }
Per-ordinal:     postgres-0-pgdata, postgres-1-pgdata, postgres-2-pgdata
```

---

## 4. StatefulDNSController (controllers/stateful_dns.go)

### StatefulDNSController.Reconcile :49
```
StatefulDNSController.Reconcile(ctx, facts)
├── parseEffectiveServiceFacts(facts)                          [shared]
│   └── statefulServices map[svc]bool
├── parseInstanceFieldsFromFacts(facts)                        [shared]
│   └── map[instanceID]map[field]value
├── parseExistingInstanceDNSEntries(facts)                     :61
│   └── reads network/dns/instance/{id} → returns map[instanceID]dnsValue
│
├── buildDesiredStatefulDNSEntries(statefulServices, instanceFields)  :75
│   └── FOR EACH instance (sorted by ID):
│       ├── skip if service not stateful
│       ├── skip if state != running
│       ├── skip if no IP
│       ├── skip if no ordinal
│       └── desiredDNS[instanceID] = "{svc}-{ord}.cca.local={ip}"
│
└── computeStatefulDNSDiffChanges(desired, existing)           :117
    ├── FOR EACH desired entry:
    │   └── IF missing or changed:
    │       └── PUT network/dns/instance/{instanceID} = "{dnsName}={ip}"
    └── FOR EACH existing entry:
        └── IF not in desired:
            └── DELETE network/dns/instance/{instanceID}
```

### DNS name format
```
postgres-0.cca.local → 10.100.3.2
postgres-1.cca.local → 10.100.1.5
```

### Constant

| Constant | Value | File | Purpose |
|---|---|---|---|
| `statefulDNSSuffix` | `".cca.local"` | `controllers/stateful_dns.go:19` | DNS suffix for per-instance names |

---

## 5. Scheduler Interaction (scheduler/scheduler.go)

### extractStatefulServices :342
```
extractStatefulServices(facts)
└── reads desired/service/{name}/stateful = "true"
└── returns map[serviceName]bool
```

### Preemption immunity — findPreemptionVictim :~280–335
```
findPreemptionVictim(candidates, constraint, statefulServices, ...)
└── FOR EACH candidate instance:
    ├── IF statefulServices[victimService]: continue            [never preempt stateful]
    └── evaluate as preemption candidate
```

### Rebalancing — findMovableInstances :520
```
findMovableInstances(instances, placementConstraints, ...)
└── FOR EACH instance:
    ├── IF constraint.mode == "per-node": continue
    ├── IF len(constraint.colocate) > 0 || len(constraint.separate) > 0: continue
    └── [NOTE: stateful instances not explicitly skipped — volume affinity makes them effectively immovable]
```

---

## 6. Store Key Reference

### Service-Level Keys

| Key Pattern | Writer | Reader | Purpose |
|---|---|---|---|
| `desired/service/{name}/stateful` | Compiler | — | Stateful declaration |
| `effective/service/{name}/stateful` | Compiler | InstanceController, StatefulVolumeController, StatefulDNSController, Scheduler | Stateful flag (single source of truth) |
| `desired/service/{name}/volume/{vol}` | Compiler | StatefulVolumeController | Volume mount binding |

### Instance-Level Keys (Stateful-Specific)

| Key Pattern | Writer | Reader | Purpose |
|---|---|---|---|
| `observed/instance/{svc}-{ord}/ordinal` | InstanceController | StatefulDNSController, Agent | Ordinal number |
| `observed/instance/{svc}-{ord}/state` | Agent | InstanceController, StatefulDNSController | Instance state |
| `observed/instance/{svc}-{ord}/ip` | Agent | StatefulDNSController | Container IP |
| `observed/instance/{svc}-{ord}/service` | InstanceController | All controllers | Service membership |

### Volume Keys (Per-Ordinal)

| Key Pattern | Writer | Reader | Purpose |
|---|---|---|---|
| `desired/volume/{svc}-{ord}-{vol}` | StatefulVolumeController | StorageController | Volume root marker |
| `desired/volume/{svc}-{ord}-{vol}/size` | StatefulVolumeController | StorageController | Storage size |
| `desired/volume/{svc}-{ord}-{vol}/persistent` | StatefulVolumeController | StorageController | Persistence flag |
| `observed/volume/{svc}-{ord}-{vol}/state` | Agent | StatefulVolumeController | Volume provision state |

### DNS Keys (Per-Instance)

| Key Pattern | Writer | Reader | Purpose |
|---|---|---|---|
| `network/dns/instance/{instanceID}` | StatefulDNSController | DNS Forwarder (agent) | Per-instance stable DNS |

---

## 7. Key Functions Reference

### types/keys.go

| Function | Line | Key Pattern |
|---|---|---|
| `KeyDesiredServiceStateful(name)` | :800 | `desired/service/{name}/stateful` |
| `KeyEffectiveServiceStateful(name)` | :807 | `effective/service/{name}/stateful` |
| `KeyObservedInstanceOrdinal(id)` | :566 | `observed/instance/{id}/ordinal` |
| `KeyNetworkDNSInstance(id)` | — | `network/dns/instance/{id}` |
| `KeyDesiredVolume(name)` | — | `desired/volume/{name}` |
| `KeyDesiredVolumeSize(name)` | — | `desired/volume/{name}/size` |
| `KeyDesiredVolumePersistent(name)` | — | `desired/volume/{name}/persistent` |

---

## 8. Lifecycle Sequences

### Scale-up 0 → 3 (stateful)
```
Cycle 1: createNextStatefulInstance → ordinal 0 missing → create postgres-0 (pending)
         StatefulVolumeController → create postgres-0-pgdata, postgres-1-pgdata, postgres-2-pgdata
         Scheduler → place postgres-0 → Agent starts it

Cycle 2: postgres-0 running → ordinal 1 missing → create postgres-1 (pending)
         StatefulDNSController → network/dns/instance/postgres-0 = "postgres-0.cca.local=10.100.3.2"
         Scheduler → place postgres-1 → Agent starts it

Cycle 3: postgres-1 running → ordinal 2 missing → create postgres-2 (pending)
         StatefulDNSController → network/dns/instance/postgres-1 = "postgres-1.cca.local=10.100.1.5"

Cycle 4: postgres-2 running → all ordinals up
         StatefulDNSController → network/dns/instance/postgres-2 = "postgres-2.cca.local=10.100.2.3"
```

### Scale-down 3 → 1
```
Cycle 1: stopHighestOrdinalInstances([0,1,2], count=2)
         → PUT derived/instance/postgres-2/controller_stopped = "true"
         → PUT derived/instance/postgres-1/controller_stopped = "true"

Agent stops postgres-2 and postgres-1.
StatefulDNSController: DELETE network/dns/instance/postgres-2
StatefulDNSController: DELETE network/dns/instance/postgres-1

Volumes postgres-2-pgdata and postgres-1-pgdata remain in desired state (persist for future scale-up).
```

### Scale-up after scale-down (1 → 3)
```
Cycle 1: postgres-0 running → ordinal 1 missing → create postgres-1
         Volume postgres-1-pgdata already exists → no new volume
         Agent attaches existing volume

Cycle 2: postgres-1 running → ordinal 2 missing → create postgres-2
         Volume postgres-2-pgdata already exists → reattached
         Data from previous incarnation preserved
```

### Node failure recovery
```
t=0    Node-2 dies (hosts postgres-1)
t=30s  FailureController → derived/instance/postgres-1/node_failure = "true"
t=31s  InstanceGCController → cleans up postgres-1 observed facts
t=32s  InstanceController: ordinal 1 missing → create postgres-1 (pending)
       → atomically deletes stale node_failure marker
t=32s  Scheduler → places postgres-1 on healthy node
       Volume postgres-1-pgdata migrates to new node
       DNS postgres-1.cca.local updates to new IP
```

---

## Reverse Index

| Function | Called By |
|---|---|
| `parseEffectiveServiceFacts` | InstanceController.Reconcile, StatefulVolumeController.Reconcile, StatefulDNSController.Reconcile |
| `createNextStatefulInstance` | InstanceController.allocateCreationsFairly |
| `stopHighestOrdinalInstances` | InstanceController.reconcileScaleDowns |
| `parseExistingOrdinals` | createNextStatefulInstance |
| `computeServiceDeficits` | InstanceController.Reconcile |
| `reconcilePerOrdinalVolumes` | StatefulVolumeController.Reconcile |
| `parseServiceVolumeMountsWithPaths` | StatefulVolumeController.Reconcile |
| `parseVolumeTemplates` | StatefulVolumeController.Reconcile |
| `buildDesiredStatefulDNSEntries` | StatefulDNSController.Reconcile |
| `computeStatefulDNSDiffChanges` | StatefulDNSController.Reconcile |
| `parseExistingInstanceDNSEntries` | StatefulDNSController.Reconcile |
| `extractStatefulServices` | Scheduler.attemptPlacements |
| `findPreemptionVictim` | Scheduler.placeBatchedInstances |

---

## File Index

| File | Key Functions | Role |
|---|---|---|
| `lang/ast.go:74` | `ServiceDecl.Stateful` | AST field |
| `lang/parser.go:303` | `case "stateful"` | Parser keyword |
| `lang/compiler.go:357–362` | emit desired + effective | Compiler fact emission |
| `types/keys.go:800,807,566` | `KeyDesiredServiceStateful`, `KeyEffectiveServiceStateful`, `KeyObservedInstanceOrdinal` | Key functions |
| `controllers/instance.go:310` | `parseEffectiveServiceFacts` | Reads stateful flag |
| `controllers/instance.go:420` | `createNextStatefulInstance` | Ordered startup, ordinal ID |
| `controllers/instance.go:465` | `stopHighestOrdinalInstances` | Reverse scale-down |
| `controllers/instance.go:174` | `computeServiceDeficits` | Budget=1 for stateful |
| `controllers/instance.go:396` | `parseExistingOrdinals` | Extract ordinals from IDs |
| `controllers/stateful_volume.go:46` | `StatefulVolumeController.Reconcile` | Per-ordinal volumes |
| `controllers/stateful_volume.go:84` | `parseServiceVolumeMountsWithPaths` | Volume mount bindings |
| `controllers/stateful_volume.go:108` | `parseVolumeTemplates` | Volume size/persistent |
| `controllers/stateful_volume.go:144` | `reconcilePerOrdinalVolumes` | Naming: {svc}-{ord}-{vol} |
| `controllers/stateful_dns.go:49` | `StatefulDNSController.Reconcile` | DNS entry lifecycle |
| `controllers/stateful_dns.go:75` | `buildDesiredStatefulDNSEntries` | DNS name construction |
| `controllers/stateful_dns.go:117` | `computeStatefulDNSDiffChanges` | DNS diff engine |
| `scheduler/scheduler.go:342` | `extractStatefulServices` | Scheduler reads stateful flag |
| `scheduler/scheduler.go:~280–335` | `findPreemptionVictim` | Preemption immunity check |
| `scheduler/scheduler.go:520` | `findMovableInstances` | Rebalancing (no explicit skip) |
