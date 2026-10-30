# CCattler Stateful Services Graph Reference

Machine-readable reference for how stateful services are handled across all
controllers and the scheduler. Purpose: trace the full lifecycle of a stateful
service — from DSL declaration through ordinal instance creation, volume
provisioning, DNS registration, and scale-down — without reading controller
source files.

## 1. Overview — What "Stateful" Means

A service with `stateful` enabled gets four behaviors that stateless services
do not:

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ Stateful Service Guarantees                                                  │
│                                                                              │
│  1. ORDINAL IDs      Instance IDs are {service}-{ordinal}: postgres-0,       │
│                      postgres-1, postgres-2 (not random UUIDs)               │
│                                                                              │
│  2. ORDERED STARTUP  Instance N+1 is only created once N is running.         │
│                      (stateless: all created in parallel)                     │
│                                                                              │
│  3. REVERSE SCALE-DOWN  Highest ordinal is stopped first.                    │
│                         (stateless: pending first, then by ID)               │
│                                                                              │
│  4. PER-ORDINAL      Each ordinal gets its own volume ({svc}-{ord}-{vol})    │
│     VOLUMES + DNS    and a stable DNS name ({svc}-{ord}.cca.local)           │
│                                                                              │
│  5. PREEMPTION       Stateful instances are never preemption victims (M161). │
│     IMMUNITY         Eviction still applies (memory pressure override).      │
└──────────────────────────────────────────────────────────────────────────────┘
```

## 2. DSL Declaration

```
# lang/parser.go:303
service postgres {
    image "postgres:16"
    instances 3
    stateful                          ← boolean keyword, no value
    volume pgdata "/var/lib/postgresql/data"
}

volume pgdata {
    size "100Gi"
    persistent true
}
```

**AST**: `lang/ast.go:74` — `ServiceDecl.Stateful bool`

**Parser**: `lang/parser.go:303` — `case "stateful": serviceDecl.Stateful = true`

**Compiler**: `lang/compiler.go:357–362` — Emits two facts:
```
desired/service/postgres/stateful  = "true"
effective/service/postgres/stateful = "true"
```

The effective fact is written directly by the compiler (stateful is a static
flag, not resolved by the IntentResolverController).

## 3. Fact Store Keys

### Service-Level Keys

| Key Pattern | Producer | Consumer |
|---|---|---|
| `desired/service/{name}/stateful` | Compiler | — |
| `effective/service/{name}/stateful` | Compiler | InstanceController, StatefulVolumeController, StatefulDNSController, Scheduler |
| `desired/service/{name}/volume/{vol}` | Compiler | StatefulVolumeController |

### Instance-Level Keys (Stateful-Specific)

| Key Pattern | Producer | Consumer |
|---|---|---|
| `observed/instance/{svc}-{ord}/ordinal` | InstanceController | StatefulDNSController, Agent |
| `observed/instance/{svc}-{ord}/state` | Agent | InstanceController, StatefulDNSController |
| `observed/instance/{svc}-{ord}/ip` | Agent | StatefulDNSController |

### Volume Keys (Per-Ordinal)

| Key Pattern | Producer | Consumer |
|---|---|---|
| `desired/volume/{svc}-{ord}-{vol}` | StatefulVolumeController | StorageController |
| `desired/volume/{svc}-{ord}-{vol}/size` | StatefulVolumeController | StorageController |
| `desired/volume/{svc}-{ord}-{vol}/persistent` | StatefulVolumeController | StorageController |
| `observed/volume/{svc}-{ord}-{vol}/state` | Agent | StatefulVolumeController |
| `observed/volume/{svc}-{ord}-{vol}/instance` | Agent | — |

### DNS Keys (Per-Instance)

| Key Pattern | Producer | Consumer |
|---|---|---|
| `network/dns/instance/{instanceID}` | StatefulDNSController | DNS Forwarder (agent) |

**Key functions** (`types/keys.go`):
```
KeyDesiredServiceStateful(name)    → desired/service/{name}/stateful      (:800)
KeyEffectiveServiceStateful(name)  → effective/service/{name}/stateful    (:807)
KeyObservedInstanceOrdinal(id)     → observed/instance/{id}/ordinal       (:566)
KeyNetworkDNSInstance(id)          → network/dns/instance/{id}
```

## 4. Instance Controller — Ordinal Lifecycle

**File**: `controllers/instance.go`

### Reading Stateful Flag

`parseEffectiveServiceFacts()` (:310) reads `effective/service/{name}/stateful`
and returns a `map[string]bool` of stateful services. This is the single source
of truth for all stateful branching in the instance controller.

### Ordinal ID Assignment

**`createNextStatefulInstance()`** (:420):
```
Input:  serviceName="postgres", wantCount=3, existingOrdinals=[0,1]
Logic:  for nextOrdinal in 0..wantCount-1:
          if ordinal exists and instance is running → continue
          if ordinal exists and instance is NOT running → return nil (block)
          if ordinal is missing → create it
Output: create postgres-2 with ordinal=2

Key invariant: instance N+1 is NEVER created until instance N is running.
This means scaling from 0→3 takes 3 reconciliation cycles minimum.
```

Instance creation emits:
```
observed/instance/postgres-2        = ""
observed/instance/postgres-2/service = "postgres"
observed/instance/postgres-2/state   = "pending"
observed/instance/postgres-2/ordinal = "2"
```

If a previous incarnation left stale markers (`controller_stopped` or
`node_failure`), they are atomically deleted in the same change group.

### Ordered Startup Sequence

```
Cycle 1:  postgres-0 created (pending)     → agent starts it
Cycle 2:  postgres-0 running               → postgres-1 created (pending)
Cycle 3:  postgres-1 running               → postgres-2 created (pending)
Cycle 4:  postgres-2 running               → all ordinals up
```

Each cycle is gated by the 50ms debounce + agent heartbeat (5s) + container
startup time. Typical startup for a 3-instance stateful service: ~20–30s.

### Scale-Down: Reverse Ordinal Order

**`stopHighestOrdinalInstances()`** (:465):
```
Input:  existingOrdinals=[0,1,2], count=1
Output: controller_stopped(postgres-2) = "true"

Always stops the highest ordinal first.
Scale 3→1 over two cycles: stop 2, then stop 1.
```

Compare with stateless scale-down (`markExcessStatelessInstancesAsStopped`
:501) which prefers pending → then by ID.

### Budget Allocation

**`computeServiceDeficits()`** (:174): Stateful services contribute at most 1
deficit per cycle (because only one ordinal can be created at a time due to
ordered startup). This means:
```
stateful postgres wants 5, has 2 → deficit = 1 (not 3)
stateless nginx    wants 5, has 2 → deficit = 3
```

**`allocateCreationsFairly()`** (:209): When processing stateful services in
the allocation loop, calls `createNextStatefulInstance` which returns at most 1
instance. The remaining budget goes to other services.

## 5. Stateful Volume Controller

**File**: `controllers/stateful_volume.go`

### Purpose

Creates per-ordinal volume declarations so each stateful instance gets its own
persistent storage. The storage controller then materializes them via the
StorageProvider.

### Watch Prefixes

```
effective/service/      ← stateful flag + instance count
desired/service/        ← volume mount bindings
desired/volume/         ← volume templates (size, persistent)
```

### Reconciliation Flow

```
                    ┌──────────────────────────────┐
                    │   StatefulVolumeController    │
                    │          Reconcile            │
                    └──────┬───────────────────────┘
                           │
                ┌──────────▼──────────────┐
                │ parseEffectiveServiceFacts│ → statefulServices, desiredCounts
                │ parseServiceVolumeMounts │ → {postgres: [{pgdata, /var/lib/...}]}
                │ parseVolumeTemplates     │ → {pgdata: {size:"100Gi", persistent:"true"}}
                │ parseExistingDesired     │ → existing volume names
                └──────────┬──────────────┘
                           │
         for each stateful service, for each ordinal 0..count-1:
                           │
                ┌──────────▼──────────────┐
                │ reconcilePerOrdinalVolumes│
                │                          │
                │ volumeName = "{svc}-{ord}-{vol}"  │
                │ e.g. "postgres-0-pgdata"          │
                │                          │
                │ if already exists → skip │
                │ else → emit:             │
                │   desired/volume/{name}           = ""
                │   desired/volume/{name}/size      = "100Gi"
                │   desired/volume/{name}/persistent = "true"
                └──────────────────────────┘
```

**Naming convention**: `{service}-{ordinal}-{volumeName}`
- `postgres-0-pgdata`, `postgres-1-pgdata`, `postgres-2-pgdata`

### Volume Template Inheritance

The per-ordinal volume inherits size and persistent flags from the original
volume declaration:
```
volume pgdata { size "100Gi" persistent true }
  ↓ inherited by
desired/volume/postgres-0-pgdata/size       = "100Gi"
desired/volume/postgres-0-pgdata/persistent = "true"
```

If `persistent` is not set in the template, defaults to `"true"` (:170).

## 6. Stateful DNS Controller

**File**: `controllers/stateful_dns.go`

### Purpose

Creates per-instance stable DNS entries for stateful services. Each running
ordinal gets a DNS name `{service}-{ordinal}.cca.local`.

### Watch Prefixes

```
effective/service/         ← stateful flag
observed/instance/         ← state, IP, ordinal
network/dns/instance/      ← existing DNS entries
```

### DNS Name Format

```
postgres-0.cca.local → 10.100.3.2
postgres-1.cca.local → 10.100.1.5
postgres-2.cca.local → 10.100.2.3
```

Suffix constant: `statefulDNSSuffix = ".cca.local"` (:19)

### Entry Lifecycle

```
┌─────────────────────────────────────────┐
│ buildDesiredStatefulDNSEntries (:75)    │
│                                         │
│ for each instance:                      │
│   if service is not stateful → skip     │
│   if state != running → skip            │
│   if no IP → skip                       │
│   if no ordinal → skip                  │
│                                         │
│   entry = "postgres-0.cca.local=10.100.3.2"  │
│   key   = network/dns/instance/{id}     │
└────────────┬────────────────────────────┘
             │
┌────────────▼────────────────────────────┐
│ computeStatefulDNSDiffChanges (:117)    │
│                                         │
│ - Create missing entries (PUT)          │
│ - Update entries with changed IP (PUT)  │
│ - Delete entries for stopped instances  │
│   (DELETE)                              │
└─────────────────────────────────────────┘
```

**Key**: The DNS entry value is `"{dnsName}={ipAddress}"`, a concatenated
string that the agent's DNS forwarder parses.

### DNS Stability Guarantee

When a stateful instance restarts on the same node, it keeps the same ordinal
(e.g. `postgres-0`) and gets its DNS entry updated with the new IP. When it
moves to a different node (after node failure), the DNS entry follows it — same
ordinal, new IP. Clients using `postgres-0.cca.local` always reach the correct
instance.

## 7. Scheduler Interaction

**File**: `scheduler/scheduler.go`

### Stateful Service Detection

The scheduler reads stateful flags from desired service facts:

**`extractStatefulServices()`** (:342):
```go
// Reads desired/service/{name}/stateful = "true"
// Returns map[string]bool
```

### Preemption Immunity (M161)

**`findPreemptionVictim()`** (:~280–335): When the scheduler searches for a
preemption victim, stateful services are explicitly skipped:
```go
if statefulServices[victimService] {
    continue  // never preempt stateful instances
}
```

**Rationale**: Stateful instances have volume affinity and ordered startup
semantics. Preempting a stateful instance would break the startup order
invariant and potentially leave orphaned volumes in an inconsistent state.

### Rebalancing

**`findMovableInstances()`** (:520): The rebalancer does NOT currently skip
stateful instances explicitly. However, stateful services with volume affinity
are effectively immovable because the scheduler does not implement volume
migration during rebalance. Per-node services are skipped (:543).

**Note**: This is a gap — stateful instances could theoretically be selected for
rebalance moves. In practice, the volume controller would need to support
migration for this to work. Future work should add a `statefulServices` check
to `findMovableInstances`.

## 8. Scale-Down Behavior

### Stateful vs Stateless vs Per-Node Comparison

```
Scale-Down Decision Tree (controllers/instance.go:152-165):

  Is per-node service?
    YES → markPerNodeExcessInstancesAsStopped()
          Priority: node_failure > pending > running
    NO  →
      Is stateful service?
        YES → stopHighestOrdinalInstances()
              Always stops highest ordinal first: 2, then 1, then 0
        NO  → markExcessStatelessInstancesAsStopped()
              Prefers stopping pending over running, then by ID
```

### Stateful Scale-Down Example

```
State: postgres-0 (running), postgres-1 (running), postgres-2 (running)
Action: scale 3 → 1

Cycle 1: stopHighestOrdinalInstances(existingOrdinals=[0,1,2], count=2)
  → controller_stopped(postgres-2) = true
  → controller_stopped(postgres-1) = true

Agent stops postgres-2 and postgres-1.
postgres-0 remains running.

Volumes postgres-2-pgdata and postgres-1-pgdata remain in desired state
(volumes are not deleted on scale-down — they persist for future scale-up).
DNS entries for postgres-1.cca.local and postgres-2.cca.local are deleted
by the StatefulDNSController once instances are no longer running.
```

### Scale-Up After Scale-Down

```
State: postgres-0 (running), volumes postgres-1-pgdata and postgres-2-pgdata
       still exist from previous incarnation
Action: scale 1 → 3

Cycle 1: createNextStatefulInstance → postgres-0 running → postgres-1 created
  Volume postgres-1-pgdata already in desired state → no new volume needed
  Agent attaches the existing volume

Cycle 2: postgres-1 running → postgres-2 created
  Volume postgres-2-pgdata already in desired state → reattached

Result: Data from previous incarnation is preserved.
```

## 9. Node Failure Recovery

When a node hosting a stateful instance fails:

```
Timeline:

t=0    Node-2 dies (hosts postgres-1)
t=30s  FailureController: heartbeat expired → node=unreachable
       → derived/instance/postgres-1/node_failure = "true"

t=30s  Agent on Node-2 stops publishing observed state.
       InstanceController sees postgres-1 as failed.

t=31s  InstanceController: postgres-1 is marked node_failure.
       It does NOT create postgres-2 (ordered startup gate).
       It waits for postgres-1 to be cleaned up.

t=31s  InstanceGCController: cleans up postgres-1 observed facts.
       Instance is removed from active set.

t=32s  InstanceController: postgres-1 missing from active set.
       deficit = 1. createNextStatefulInstance:
       - ordinal 0 exists and running → continue
       - ordinal 1 missing → create postgres-1
       Atomically deletes stale node_failure marker.

t=32s  Scheduler: places postgres-1 on a healthy node.
       Volume postgres-1-pgdata migrates to new node.
       DNS postgres-1.cca.local updates to new IP.

Key: The same ordinal ID (postgres-1) is reused. The volume and DNS
name follow the ordinal, not the node.
```

## 10. End-to-End Lifecycle Diagram

```
USER DSL                    COMPILER                    FACT STORE
─────────                   ─────────                   ──────────
service postgres {    →     desired/service/postgres/stateful = "true"
  image "postgres:16"       effective/service/postgres/stateful = "true"
  instances 3               desired/service/postgres/instances = "3"
  stateful                  effective/service/postgres/instances = "3"
  volume pgdata "..."       desired/service/postgres/volume/pgdata = "/var/lib/..."
}
volume pgdata {       →     desired/volume/pgdata = ""
  size "100Gi"              desired/volume/pgdata/size = "100Gi"
  persistent true           desired/volume/pgdata/persistent = "true"
}

    ┌─────────────────────────────────────────────────────────────┐
    │              CONTROLLER RECONCILIATION LOOP                  │
    │                                                              │
    │  ┌─────────────────────────────────────────┐                │
    │  │    StatefulVolumeController              │                │
    │  │    Reads: effective/svc/stateful,        │                │
    │  │           desired/svc/volume,            │                │
    │  │           desired/volume templates       │                │
    │  │    Writes: desired/volume/postgres-0-pgdata              │
    │  │            desired/volume/postgres-1-pgdata              │
    │  │            desired/volume/postgres-2-pgdata              │
    │  └─────────────────────────────────────────┘                │
    │                          │                                   │
    │  ┌─────────────────────────────────────────┐                │
    │  │    InstanceController                    │                │
    │  │    Cycle 1: create postgres-0 (pending)  │                │
    │  │    Cycle 2: postgres-0 running           │                │
    │  │             → create postgres-1 (pending)│                │
    │  │    Cycle 3: postgres-1 running           │                │
    │  │             → create postgres-2 (pending)│                │
    │  └─────────────────────────────────────────┘                │
    │                          │                                   │
    │  ┌─────────────────────────────────────────┐                │
    │  │    Scheduler                             │                │
    │  │    Places each pending instance on       │                │
    │  │    least-loaded node.                    │                │
    │  │    Stateful instances are preemption-    │                │
    │  │    immune (M161).                        │                │
    │  └─────────────────────────────────────────┘                │
    │                          │                                   │
    │  ┌─────────────────────────────────────────┐                │
    │  │    StatefulDNSController                 │                │
    │  │    Once instance is running with IP:     │                │
    │  │    network/dns/instance/{id} =           │                │
    │  │      "postgres-0.cca.local=10.100.3.2"  │                │
    │  └─────────────────────────────────────────┘                │
    │                                                              │
    │  ┌─────────────────────────────────────────┐                │
    │  │    Agent (on assigned node)              │                │
    │  │    - Attaches per-ordinal volume         │                │
    │  │    - Starts container with mount         │                │
    │  │    - Publishes: state=running, ip=...    │                │
    │  └─────────────────────────────────────────┘                │
    └─────────────────────────────────────────────────────────────┘
```

## 11. Controller Responsibilities Summary

| Controller | Role for Stateful Services | File |
|---|---|---|
| InstanceController | Ordinal IDs, ordered startup, reverse scale-down | `controllers/instance.go` |
| StatefulVolumeController | Per-ordinal volume creation | `controllers/stateful_volume.go` |
| StatefulDNSController | Per-instance stable DNS entries | `controllers/stateful_dns.go` |
| Scheduler | Placement (standard), preemption immunity (M161) | `scheduler/scheduler.go` |
| EvictionController | Can still evict stateful instances under memory pressure | `controllers/eviction.go` |
| FailureController | Marks node_failure on stateful instances (same as stateless) | `controllers/failure.go` |
| InstanceGCController | Cleans up stopped stateful instance facts | `controllers/instance_gc.go` |

## 12. Constants

| Constant | Value | Location | Purpose |
|---|---|---|---|
| `statefulDNSSuffix` | `.cca.local` | `controllers/stateful_dns.go:19` | DNS suffix for per-instance names |
| Reconcile debounce | 50ms | `controllers/runner.go` | Min time between cycles |
| Resync interval | 30s | `controllers/runner.go` | Forced full reconciliation |
| Max creations/cycle | 30 | `controllers/instance.go` | Budget limit (stateful gets at most 1) |

## 13. File Index

| File | Lines | Role |
|---|---|---|
| `lang/ast.go:74` | `Stateful bool` | AST field on ServiceDecl |
| `lang/parser.go:303` | `case "stateful"` | Parser keyword |
| `lang/compiler.go:357-362` | Emit desired + effective | Compiler fact emission |
| `types/keys.go:800,807` | Key functions | desired/effective stateful keys |
| `types/keys.go:566` | `KeyObservedInstanceOrdinal` | Ordinal fact key |
| `controllers/instance.go:310` | `parseEffectiveServiceFacts` | Reads stateful flag |
| `controllers/instance.go:420` | `createNextStatefulInstance` | Ordered startup |
| `controllers/instance.go:465` | `stopHighestOrdinalInstances` | Reverse scale-down |
| `controllers/instance.go:174` | `computeServiceDeficits` | Budget=1 for stateful |
| `controllers/stateful_volume.go:46` | `Reconcile` | Per-ordinal volumes |
| `controllers/stateful_volume.go:144` | `reconcilePerOrdinalVolumes` | Naming: {svc}-{ord}-{vol} |
| `controllers/stateful_dns.go:49` | `Reconcile` | DNS entry lifecycle |
| `controllers/stateful_dns.go:75` | `buildDesiredStatefulDNSEntries` | DNS name construction |
| `scheduler/scheduler.go:342` | `extractStatefulServices` | Scheduler reads stateful flag |
| `scheduler/scheduler.go:318` | `findPreemptionVictim` | Preemption immunity check |
