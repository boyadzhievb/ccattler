I checked the latest `master`. The current head is **M75, `49224c3a`**, and it is substantially better. The core changes from the last review are present: atomic groups, telemetry separation, independent heartbeat, unreachable-node protection, and oversized-group rejection. [M75 commit](https://github.com/boyadzhievb/ccattler/commit/49224c3a61c2d023d5a6353f30b68177cec5f8f5?utm_source=chatgpt.com)

The current GitHub `Test`, E2E, CodeQL, Release, and Pages workflows are all green. The standard E2E is still a 2-node test, while the 50-node convergence test lives under `loadtest` and is skipped in `-short` mode.

## The good news

The transaction-group change is now correct.

This is the important part:

```go
func takeWholeGroups(changes []Change, limit int) ([]Change, int, error) {
    ...
    if groupSize > limit {
        return nil, 0, fmt.Errorf(
            "atomic change group %q has %d changes, exceeds transaction budget %d",
            groupID, groupSize, limit)
    }
    ...
}
```

And the controllers now group their multi-key operations. The stateful ordinal regression tests were also added.

The telemetry move is also correctly wired through the key helpers, so the API continues to use `KeyObservedInstanceCPU()` / `KeyObservedInstanceMemory()` while those helpers now resolve to `observed/telemetry/...`. That avoids leaving the read side pointing at the old location.

The 50-node load test is especially useful: it exercises **50 nodes / 1000 instances / 5 killed nodes** and waits for exact recovery. That is much closer to the original problem than the previous tests.

## One remaining P1: `PublishAliveState` is still racy

M75 changed:

```go
if existingState == types.NodeDraining ||
   existingState == types.NodeDisabled ||
   existingState == types.NodeUnreachable {
    return
}
```

That prevents the common case, but it does **not** make the operation atomic.

There is still a:

```text
GET state
    ↓
controller writes unreachable
    ↓
PUT alive
```

race.

So this is still possible:

```text
Agent heartbeat goroutine        NodeFailureController

GET state == alive
                                  CAS → unreachable
PUT state = alive
```

The agent wins with an unconditional write and resurrects the node.

I would change `PublishAliveState` to a conditional transaction:

```go
func (nodeReporter *NodeReporter) PublishAliveState(ctx context.Context) {
	const key = /* types.KeyObservedNodeState(...) */

	current, err := nodeReporter.factStore.Get(ctx, key)

	switch {
	case err == store.ErrKeyNotFound:
		succeeded, txErr := nodeReporter.factStore.Transaction(
			ctx,
			[]store.Compare{{Key: key, Revision: 0}},
			[]store.Op{{
				Type:  store.OpPut,
				Key:   key,
				Value: []byte(string(types.NodeAlive)),
			}},
			nil,
		)
		if txErr != nil {
			logging.Default().Error("failed to publish initial node state", "error", txErr.Error())
		}
		_ = succeeded

	case err != nil:
		logging.Default().Error("failed to read node state", "error", err.Error())

	default:
		existingState := types.NodeState(current.Value)

		if existingState == types.NodeDraining ||
			existingState == types.NodeDisabled ||
			existingState == types.NodeUnreachable {
			return
		}

		succeeded, txErr := nodeReporter.factStore.Transaction(
			ctx,
			[]store.Compare{{
				Key:      key,
				Revision: current.Revision,
			}},
			[]store.Op{{
				Type:  store.OpPut,
				Key:   key,
				Value: []byte(string(types.NodeAlive)),
			}},
			nil,
		)

		if txErr != nil {
			logging.Default().Error("failed to publish node state", "error", txErr.Error())
			return
		}
		if !succeeded {
			// Another controller changed the state after our read.
			return
		}
	}
}
```

Even better architecturally: make the heartbeat loop do only:

```go
nodeReporter.WriteHeartbeat(ctx)
```

and let `NodeFailureController` own `unreachable → alive`. `PublishAliveState` is then primarily a startup/registration operation.

## The biggest remaining architecture problem: storage

M75 did **not** fix this.

`StorageController.Reconcile()` still calls external provider methods before the runner commits the transaction:

```go
storageController.storageProvider.ResizeVolume(...)
```

and:

```go
storageController.storageProvider.SnapshotVolume(...)
```

So this can still happen:

```text
reconcile
  ↓
SnapshotVolume()
  ↓
transaction CAS fails
  ↓
no store state committed
  ↓
retry
  ↓
SnapshotVolume() again
```

The migration snapshot name is based on:

```go
time.Now().UnixMilli()
```

which makes the repeated operation materially different each time.

This deserves a proper intent/executor pattern:

```text
derived/volume/<name>/operation
    operation_id
    type
    target
    status
```

Then:

```go
type VolumeOperation struct {
    ID         string
    VolumeName string
    Kind       string
    TargetSize int64
}
```

and the executor makes the external call idempotent using the stable operation ID.

I would treat this as the next serious correctness PR.

## The 50-node test is good — but it doesn't test everything

The new convergence test is:

```text
50 nodes
1000 instances
200ms agent interval
5 killed nodes
10 controller retries
input-key guards disabled
lease timeout = 5 minutes
```

The important caveat is this line:

```go
cluster.SetMaxInputKeyGuards(0)
```

That means the load test does **not** validate the default production input-guard behavior.

Also, `KillNode()` injects failure state directly rather than letting heartbeat expiry cause the failure. So the new independent heartbeat goroutine isn't actually being stress-tested by this scenario.

I'd add a second test specifically for:

```text
50 agents
normal production guard configuration
heartbeat interval ≈ 5s
lease timeout ≈ 30s
long/sluggish reconciliation
no false node failures
```

That would validate the thing M75 changed most substantially.

## One easy P2 fix

`SetHeartbeatInterval()` accepts arbitrary durations:

```go
func (nodeAgent *Agent) SetHeartbeatInterval(heartbeatFrequency time.Duration) {
    nodeAgent.heartbeatFrequency = heartbeatFrequency
}
```

Then:

```go
time.NewTicker(nodeAgent.heartbeatFrequency)
```

will panic for `0` or negative durations.

I'd guard it:

```go
func (nodeAgent *Agent) SetHeartbeatInterval(d time.Duration) {
    if d <= 0 {
        d = heartbeatInterval
    }
    nodeAgent.heartbeatFrequency = d
}
```

or return an error if configuration invalidity should be surfaced.

## One remaining event-log resilience issue

The event projector still fans in five independent watches. If one etcd watch is compacted/closed while the others remain alive, `mergeWatchChannels()` waits for **all** channels to close.

That means one dead/compacted source can stop that source from ever being consumed again without restarting the projector.

I'd give the projector its own reconnect loop per prefix, analogous to what was added to the main runner.

## Current verdict

I'd put M75 at roughly:

**Transaction correctness: ✅ much improved**

**50-node convergence: ✅ materially improved**

**Telemetry contention: ✅ major improvement**

**Heartbeat starvation: ✅ fixed**

**Node resurrection: ⚠️ small but real CAS race remains**

**Storage transactional safety: ❌ still unresolved**

**High-scale production validation: ⚠️ improved test exists, but isn't in normal CI and doesn't exercise all production settings**

So I would **not reopen the old transaction-group issue**. That part is now in good shape. The next developer effort should go toward the **storage side-effect model** and the **atomic liveness transition**, then add a production-config 50-node test.
