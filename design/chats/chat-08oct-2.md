I checked the current `master` again. The new head is **M74, `dab8d1c9`**, and this is a meaningful improvement over M73. The main requested fixes are actually present, and the repository's current Test, E2E, CodeQL, Release, and Pages workflows are all green. The E2E job is still only a **2-node** scenario, and the normal test job still runs `go test -race -short`, so the original 50+ node / high-frequency contention case is not being exercised by CI. [M74 commit](https://github.com/boyadzhievb/ccattler/commit/dab8d1c91af9c6193c96e298c0c6a398ed358e3b?utm_source=chatgpt.com)

### What M74 fixed well

The `Change.Group` implementation is good. `takeWholeGroups()` now prevents a multi-key operation from being cut in half, and the tests explicitly verify that groups remain intact.

The stateful ordinal lifecycle fix is also correct in principle: when an ordinal is recreated, stale `controller_stopped` and `node_failure` markers are deleted in the same grouped transaction.

The per-loop watch context in `runControllerLoop()` fixes the watcher leak I called out previously.

`ReadInstance()` now uses the same effective-state semantics as `ListInstances()`, which removes that API inconsistency.

So I would mark these previous findings as **resolved**.

### But I found four important things still worth fixing

#### 1. Network deletion is still not atomic

This is the clearest M74 oversight.

Creation is correctly grouped:

```go
changes = append(changes, groupedChanges("vip-create/"+serviceName,
    Change{Type: store.OpPut, Key: types.KeyNetworkVIPService(serviceName), ...},
    Change{Type: store.OpPut, Key: types.KeyNetworkVIPServicePort(serviceName), ...},
    Change{Type: store.OpPut, Key: types.KeyNetworkDNS(serviceName), ...},
)...)
```

But deletion does this:

```go
groupedChanges("vip-remove/"+serviceName,
    Change{Type: store.OpDelete, Key: types.KeyNetworkVIPService(serviceName)},
    Change{Type: store.OpDelete, Key: types.KeyNetworkVIPServicePort(serviceName)},
)

...

Change{
    Type: store.OpDelete,
    Key: types.KeyNetworkDNS(serviceName),
}
```

So the DNS deletion is a standalone operation.

Under the 60-change budget, the runner sorts all empty-group changes before grouped changes. With enough stale services, it can therefore commit DNS deletions while deferring the corresponding VIP deletions.

I'd change it to:

```go
changes = append(changes, groupedChanges("vip-remove/"+serviceName,
    Change{
        Type: store.OpDelete,
        Key: types.KeyNetworkVIPService(serviceName),
    },
    Change{
        Type: store.OpDelete,
        Key: types.KeyNetworkVIPServicePort(serviceName),
    },
    Change{
        Type: store.OpDelete,
        Key: types.KeyNetworkDNS(serviceName),
    },
)...)
```

That should get a regression test with, say, 30 stale services and a transaction budget of 60.

---

#### 2. Storage still performs external side effects before the CAS commit

This one survived M74.

`StorageController` still calls:

```go
storageController.storageProvider.ResizeVolume(...)
```

and:

```go
storageController.storageProvider.SnapshotVolume(...)
```

inside `Reconcile()`.

So the sequence can still be:

```text
Reconcile
    ↓
SnapshotVolume()
    ↓
build Change[]
    ↓
CAS fails
    ↓
nothing committed
    ↓
reconcile again
    ↓
SnapshotVolume() again
```

The same issue exists with the runner deferring groups: the provider side effect can occur for work that isn't actually committed.

This is the biggest remaining architectural correctness issue. I would not call storage reconciliation transaction-safe until those operations are represented as durable intents or are made explicitly idempotent.

A practical intermediate step is to generate a stable operation ID from the volume and desired state:

```go
operationID := fmt.Sprintf(
    "migrate:%s:%s",
    volumeName,
    observedInfo.node,
)
```

and use that operation ID for deterministic/idempotent provider operations rather than:

```go
time.Now().UnixMilli()
```

The full fix is an intent/executor state machine.

---

#### 3. The original high-scale write-contention problem is still largely present

M74 fixed **transaction fragmentation**, but it did not fix **write amplification**.

The agent still writes per-workload telemetry into:

```text
observed/instance/<id>/cpu
observed/instance/<id>/memory
```

and controllers still watch broad `observed/instance/` prefixes.

So telemetry remains part of the controller's event stream.

At a 200 ms reporting interval:

```text
5000 workloads × 2 telemetry writes × 5/sec
≈ 50,000 writes/sec
```

before considering other agent writes.

And the heartbeat is still in the same main ticker as:

```text
reconcile
telemetry
data-plane reconciliation
```

so a long reconciliation cycle can still delay heartbeats.

I still recommend the split:

```text
observed/instance/
    lifecycle/runtime state

observed/telemetry/instance/
    cpu/memory

observed/telemetry/node/
    workload count
```

Then controllers don't watch telemetry at all.

And heartbeat should be independent:

```go
func (nodeAgent *Agent) heartbeatLoop(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            nodeAgent.nodeReporter.WriteHeartbeat(ctx)
        }
    }
}
```

This is the change I'd prioritize next for the original 50+ node problem.

---

#### 4. `PublishAliveState()` can still resurrect an unreachable node

The agent currently skips only:

```go
NodeDraining
NodeDisabled
```

and otherwise writes:

```go
NodeAlive
```

That includes:

```text
NodeUnreachable
```

So you still have a race like:

```text
heartbeat becomes stale
        ↓
NodeFailureController → unreachable
        ↓
agent wakes up
        ↓
PublishAliveState()
        ↓
node becomes alive again
```

The better rule is that **heartbeat proves liveness; it should not independently override lifecycle state**.

At minimum:

```go
if existingState == types.NodeDraining ||
   existingState == types.NodeDisabled ||
   existingState == types.NodeUnreachable {
    return
}
```

Better still, have the liveness controller own the `alive ↔ unreachable` transition and make agent registration a separate operation.

---

### One smaller runner issue

`takeWholeGroups()` currently does this if a single atomic group exceeds the budget:

```go
if groupSize > limit {
    deferred += groupSize
    scanIndex = groupEnd
    continue
}
```

That means a malformed/new controller can create a 61-change atomic group and the runner will silently defer it forever.

I'd make that a hard error:

```go
if groupSize > limit {
    return nil, fmt.Errorf(
        "atomic change group %q has %d changes, exceeds transaction budget %d",
        groupID, groupSize, limit,
    )
}
```

A group larger than the maximum transaction is not something that can converge by retrying.

### One more thing CI doesn't prove

M74's CI is green, which is good, but the E2E workflow explicitly runs:

```text
e2e cluster test (2-node)
```

and the main test workflow uses:

```text
go test -race -short
```

So I would **not yet consider the original 50-node livelock problem experimentally closed**. The new atomic-group unit tests prove the batching algorithm, but they don't prove behavior under the workload that originally caused the problem.

The next test I'd add is roughly:

```text
50 nodes
200ms agent telemetry/heartbeat
1000–5000 instances
mass placement churn
simultaneous failure/replacement
controller conflict counters
zero abandoned reconciliations
bounded heartbeat gap
```

### My current verdict

**M74 fixes the three most obvious correctness bugs from the previous review.**

I would now rank the remaining work:

| Priority | Issue                                                                |
| -------- | -------------------------------------------------------------------- |
| P1       | Separate high-frequency telemetry from `observed/instance/`          |
| P1       | Decouple heartbeat from heavy reconciliation                         |
| P1       | Remove pre-CAS storage side effects / make them idempotent           |
| P1/P2    | Make VIP + port + DNS deletion one atomic group                      |
| P2       | Prevent agent from resurrecting `NodeUnreachable`                    |
| P2       | Fail fast on an atomic group larger than the transaction budget      |
| P2       | Add an actual 50+ node contention/load test to CI or nightly testing |

So: **M74 is substantially better, and the transaction batching design is now on the right track. I still wouldn't declare the high-scale controller-livelock problem solved until the telemetry/heartbeat path is addressed and a real 50+ node stress run passes.** The etcd side still has a default `--max-txn-ops` of 128, and its transaction model requires unique mutation keys, so the conservative transaction budgeting and duplicate-key checks remain appropriate. ([etcd.io][1])

[1]: https://etcd.io/docs/v3.6/op-guide/configuration/?utm_source=chatgpt.com "Configuration options | etcd"
