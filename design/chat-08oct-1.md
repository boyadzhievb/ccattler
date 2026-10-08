Yes. I would give the lead developer a small set of targeted patches rather than another broad refactor. The highest-value changes are around **atomic batching, state-marker lifecycle, watch cleanup, and separating agent telemetry from controller state**.

The snippets below are written against the current `master` structure, especially `controllers/runner.go`, `controllers/instance.go`, `controllers/failure.go`, and `agent/agent.go`. [runner.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/runner.go?utm_source=chatgpt.com) [instance.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/instance.go?utm_source=chatgpt.com)

## 1. Stop the runner from splitting logical operations

This is the one I would fix first.

Right now `runner.go` does:

```go
if len(changes) > maxTransactionChanges {
    changes = changes[:maxTransactionChanges]
}
```

That is unsafe because a logical operation can span several `Change`s. For example, a stateful instance creation currently emits four changes, while a failure replacement emits four. A blind slice can commit 1–3 changes from such a group.

### Minimal design change

Add a group identifier to `Change`:

```go
type Change struct {
	Type  store.OpType
	Key   string
	Value []byte

	// Group identifies a logical atomic operation.
	// Empty means this change is independently commit-able.
	Group string
}
```

Then use a helper:

```go
func groupedChanges(group string, changes ...Change) []Change {
	for i := range changes {
		changes[i].Group = group
	}
	return changes
}
```

A stateful creation becomes:

```go
func createNextStatefulInstance(
	serviceName string,
	wantCount int,
	existingOrdinals []int,
	stateByInstanceID map[string]types.InstanceState,
) []Change {
	// ...

	instanceID := fmt.Sprintf("%s-%d", serviceName, nextOrdinal)

	return groupedChanges(
		"instance-create/"+instanceID,
		Change{Type: store.OpPut, Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
		Change{Type: store.OpPut, Key: types.KeyObservedInstanceService(instanceID), Value: []byte(serviceName)},
		Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstancePending))},
		Change{Type: store.OpPut, Key: types.KeyObservedInstanceOrdinal(instanceID), Value: []byte(strconv.Itoa(nextOrdinal))},
	)
}
```

Failure replacement:

```go
func (failureController *FailureController) stopAndReplace(
	instanceID string,
	serviceName string,
) []Change {
	replacementID := failureController.NewID()

	return groupedChanges(
		"failure-replace/"+instanceID,
		Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedInstanceControllerStopped(instanceID),
			Value: []byte("true"),
		},
		Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstance(replacementID),
			Value: []byte(""),
		},
		Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceService(replacementID),
			Value: []byte(serviceName),
		},
		Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceState(replacementID),
			Value: []byte(string(types.InstancePending)),
		},
	)
}
```

Then change the runner so it selects **complete groups**, not arbitrary changes:

```go
func takeWholeGroups(changes []Change, limit int) ([]Change, int) {
	selected := make([]Change, 0, min(len(changes), limit))
	deferred := 0

	for i := 0; i < len(changes); {
		group := changes[i].Group
		j := i + 1

		// Empty group = one standalone change.
		if group != "" {
			for j < len(changes) && changes[j].Group == group {
				j++
			}
		}

		groupSize := j - i

		// A single logical operation larger than the transaction budget
		// is a controller bug/configuration error.
		if groupSize > limit {
			deferred += groupSize
			i = j
			continue
		}

		if len(selected)+groupSize > limit {
			deferred += len(changes) - i
			break
		}

		selected = append(selected, changes[i:j]...)
		i = j
	}

	return selected, deferred
}
```

And replace the current truncation with:

```go
changes, deferred := takeWholeGroups(changes, maxTransactionChanges)

if deferred > 0 {
	logging.Default().Debug(
		"deferred atomic change groups",
		"controller", controller.Name(),
		"deferred_changes", deferred,
	)
}
```

I would also **remove the assumption that every controller must independently squeeze itself below the runner limit**. The runner should be the final safety boundary.

### Important detail

Don't sort purely by key before doing this:

```go
sortChangesByKey(changes)
```

because that destroys group adjacency.

Either sort by `(Group, Key)`:

```go
sort.SliceStable(changes, func(i, j int) bool {
	if changes[i].Group != changes[j].Group {
		return changes[i].Group < changes[j].Group
	}
	return changes[i].Key < changes[j].Key
})
```

or build an explicit `[]ChangeGroup`.

I slightly prefer an actual `ChangeGroup` type long-term:

```go
type ChangeGroup struct {
	ID      string
	Changes []Change
}
```

but adding `Group string` is much smaller for the current codebase.

---

## 2. Fix the `controller_stopped` poisoning of stateful ordinals

This is the other correctness issue I'd treat as urgent.

Currently a failed stateful instance such as:

```text
postgres-2
```

can get:

```text
derived/instance/postgres-2/controller_stopped = true
```

Later the stateful controller can legitimately recreate `postgres-2`, but the old derived marker remains. The effective state therefore remains stopped forever.

The fix is simple conceptually:

> **A new incarnation of a reusable stateful ordinal must clear old failure/replacement markers in the same transaction as recreation.**

I'd make the creation group look like this:

```go
func createNextStatefulInstance(
	serviceName string,
	wantCount int,
	existingOrdinals []int,
	stateByInstanceID map[string]types.InstanceState,
	fieldsByInstanceID map[string]map[string]string,
) []Change {
	ordinalSet := make(map[int]bool, len(existingOrdinals))
	for _, ordinal := range existingOrdinals {
		ordinalSet[ordinal] = true
	}

	for nextOrdinal := 0; nextOrdinal < wantCount; nextOrdinal++ {
		if ordinalSet[nextOrdinal] {
			instanceID := fmt.Sprintf("%s-%d", serviceName, nextOrdinal)

			if stateByInstanceID[instanceID] != types.InstanceRunning {
				return nil
			}

			continue
		}

		instanceID := fmt.Sprintf("%s-%d", serviceName, nextOrdinal)
		group := "instance-create/" + instanceID

		changes := groupedChanges(
			group,
			Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstance(instanceID),
				Value: []byte(""),
			},
			Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstanceService(instanceID),
				Value: []byte(serviceName),
			},
			Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstanceState(instanceID),
				Value: []byte(string(types.InstancePending)),
			},
			Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstanceOrdinal(instanceID),
				Value: []byte(strconv.Itoa(nextOrdinal)),
			},
		)

		previous := fieldsByInstanceID[instanceID]

		if previous["controller_stopped"] == "true" {
			changes = append(changes, Change{
				Type:  store.OpDelete,
				Key:   types.KeyDerivedInstanceControllerStopped(instanceID),
				Group: group,
			})
		}

		if previous["node_failure"] == "true" {
			changes = append(changes, Change{
				Type:  store.OpDelete,
				Key:   types.KeyDerivedInstanceNodeFailure(instanceID),
				Group: group,
			})
		}

		return changes
	}

	return nil
}
```

The critical part is that marker deletion happens **atomically with recreation**.

I'd add this regression test immediately:

```go
func TestStatefulOrdinalCanBeReusedAfterControllerReplacement(t *testing.T) {
	// postgres-0 is failed
	// FailureController marks controller_stopped=true
	// replacement is created
	// desired count later increases so postgres-0 is reused
	// InstanceController must clear controller_stopped
	// effective state must become pending/running rather than stopped
}
```

That test would have caught the bug directly.

The relevant current failure logic is here: [failure.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/failure.go?utm_source=chatgpt.com)

---

## 3. Fix the controller watch leak during restart

There is a subtle lifecycle bug in `runControllerLoop`.

The loop creates watches using the outer `ctx`:

```go
watchEventChannel, err := controllerRunner.store.Watch(
	ctx,
	prefix,
	store.WatchOption{Prefix: true},
)
```

When one watch closes, the function returns and the controller restarts. But the other watches from the old loop are still attached to the original context.

This is especially bad with the in-memory store because old watcher registrations can remain around until the entire runner shuts down.

### Fix

Give each reconciliation loop its own child context:

```go
func (controllerRunner *Runner) runControllerLoop(
	ctx context.Context,
	controller Controller,
) error {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reconcileTrigger := make(chan struct{}, 1)
	watchLost := make(chan struct{}, 1)

	for _, prefix := range controller.Watch() {
		watchEventChannel, err := controllerRunner.store.Watch(
			watchCtx,
			prefix,
			store.WatchOption{Prefix: true},
		)
		if err != nil {
			return err
		}

		go func(ch <-chan store.Event) {
			for {
				select {
				case <-watchCtx.Done():
					return

				case event, ok := <-ch:
					if !ok {
						select {
						case watchLost <- struct{}{}:
						default:
						}
						return
					}

					select {
					case reconcileTrigger <- struct{}{}:
					default:
					}
				}
			}
		}(watchEventChannel)
	}

	// ...
}
```

Now, when one watch fails:

```text
watch closes
    ↓
runControllerLoop returns
    ↓
defer cancel()
    ↓
ALL sibling watches are cancelled
    ↓
new runControllerLoop starts cleanly
```

I'd add a test that deliberately closes one watch and verifies the old subscriptions disappear before restart.

The current implementation is in: [runner.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/runner.go?utm_source=chatgpt.com)

---

## 4. Move telemetry out of `observed/instance`

This is probably the biggest **scale** improvement.

Right now the agent writes:

```text
observed/instance/<id>/cpu
observed/instance/<id>/memory
```

but multiple controllers watch:

```text
observed/instance/
```

So telemetry becomes controller input.

At 5,000 workloads, writing CPU and memory every 200ms creates an enormous amount of store traffic. Even though those writes aren't logically relevant to InstanceController or FailureController, they still generate revisions and watch events.

### I would introduce

```text
observed/telemetry/instance/<id>/cpu
observed/telemetry/instance/<id>/memory
observed/telemetry/node/<id>/workload_count
```

For example:

```go
func KeyObservedTelemetryInstanceCPU(instanceID string) string {
	return fmt.Sprintf(
		"observed/telemetry/instance/%s/cpu",
		instanceID,
	)
}

func KeyObservedTelemetryInstanceMemory(instanceID string) string {
	return fmt.Sprintf(
		"observed/telemetry/instance/%s/memory",
		instanceID,
	)
}
```

Then:

```go
if _, err := nodeReporter.factStore.Put(
	ctx,
	types.KeyObservedTelemetryInstanceCPU(workloadStatus.ID),
	[]byte(strconv.FormatInt(resourceStats.CPUMillicores, 10)),
); err != nil {
	// ...
}
```

And update the CLI/read path to look there.

This gives you a clean ownership boundary:

```text
observed/instance/
    lifecycle/runtime facts

observed/telemetry/
    high-frequency metrics
```

The controllers no longer wake up on every telemetry sample.

This is more valuable than simply increasing controller retry counts.

---

## 5. Decouple heartbeat from reconciliation

The agent currently does this in one ticker:

```go
PublishAliveState()
WriteHeartbeat()
executeReconciliationCycle()
CollectAndReportTelemetry()
dataPlaneReconciler.Reconcile()
```

At large scale, one slow reconciliation or telemetry collection can delay the next heartbeat.

That can produce:

```text
agent busy
   ↓
heartbeat delayed
   ↓
NodeFailureController says "unreachable"
   ↓
instances marked failed
   ↓
FailureController replaces them
   ↓
agent eventually resumes and sees replacement/failure state
```

I'd make heartbeat independently scheduled.

### Agent

```go
const defaultHeartbeatInterval = 5 * time.Second

func (nodeAgent *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(defaultHeartbeatInterval)
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

Start it independently:

```go
func (nodeAgent *Agent) Run(ctx context.Context) error {
	nodeAgent.nodeReporter.RegisterNode(ctx)
	nodeAgent.nodeReporter.PublishAliveState(ctx)
	nodeAgent.nodeReporter.WriteHeartbeat(ctx)

	go nodeAgent.heartbeatLoop(ctx)
	go nodeAgent.probeScheduler.Run(
		ctx,
		nodeAgent.findInstancesPlacedOnThisNode,
	)

	// main watch/reconcile loop...
}
```

Then remove heartbeat from:

```go
case <-ticker.C:
```

so the main loop only handles reconciliation/telemetry/data-plane work.

I would also stop doing an unconditional:

```go
Put(node/state, alive)
```

on every periodic tick. Let heartbeat establish liveness and let the NodeFailureController own lifecycle transitions.

Current agent code: [agent.go](https://github.com/boyadzhievb/ccattler/blob/master/agent/agent.go?utm_source=chatgpt.com)

---

## 6. Make `ReadInstance` use the same effective-state logic as `ListInstances`

There is an API correctness mismatch.

`ListInstances()` merges:

```text
observed state
+
derived/controller_stopped
+
derived/node_failure
```

but `ReadInstance()` currently reads observed state directly.

That means the same instance can appear as:

```text
GET /instance/foo -> running
LIST /instances  -> stopped
```

I'd extract a common helper.

For example:

```go
func effectiveInstance(
	ctx context.Context,
	stateStore store.StateStore,
	id string,
) (*Instance, error) {
	// Load observed fields.
	// Load derived markers.
	// Apply effectiveInstanceStateFromFields.
}
```

Then:

```go
func ReadInstance(
	ctx context.Context,
	stateStore store.StateStore,
	id string,
) (*Instance, error) {
	// scan observed + derived fields
	// ...
	instance.State = effectiveInstanceStateFromFields(fields)
	return instance, nil
}
```

Or better, have both `ReadInstance` and `ListInstances` use one shared reconstruction function.

Current implementation: [codec.go](https://github.com/boyadzhievb/ccattler/blob/master/types/codec.go?utm_source=chatgpt.com)

---

## 7. Stop doing external storage side effects inside `Reconcile`

This is a more architectural fix, but it matters.

Current `StorageController` does:

```go
storageController.storageProvider.ResizeVolume(...)
```

and:

```go
storageController.storageProvider.SnapshotVolume(...)
```

before the runner commits the resulting transaction. [storage.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/storage.go?utm_source=chatgpt.com)

That creates this failure mode:

```text
Reconcile
  ↓
snapshot volume
  ↓
build Change[]
  ↓
CAS fails
  ↓
nothing committed
  ↓
next reconcile
  ↓
snapshot again
```

Even worse, the runner may defer some changes because of the transaction budget.

### Better state machine

Have the controller publish an intent:

```text
derived/volume/<volume>/operation
    kind=resize
    target_size=100Gi
    operation_id=...
```

Then a worker executes it:

```go
func (executor *StorageExecutor) Reconcile(ctx context.Context, op VolumeOperation) error {
	switch op.Kind {
	case "resize":
		if err := executor.provider.ResizeVolume(
			ctx,
			op.Volume,
			op.TargetSize,
		); err != nil {
			return err
		}

		// Commit observed success + clear operation.
	}
	return nil
}
```

For snapshots, make the request ID deterministic:

```go
snapshotName := fmt.Sprintf(
	"%s-pre-migration-%s",
	volumeName,
	operationID,
)
```

not:

```go
time.Now().UnixMilli()
```

That makes retries much more naturally idempotent.

---

## 8. Add a regression test specifically for the 50-node failure mode

I'd create one end-to-end-ish test that intentionally generates:

```text
50 agents
+
200ms heartbeat/telemetry
+
controller reconciliation
+
mass failure
+
replacement
+
stateful service reuse
```

and assert:

```go
abandonedReconciliations == 0
```

rather than merely:

```go
len(instances) == desired
```

I'd also collect:

```text
reconciliation_conflicts_total
reconciliation_duration_seconds
watch registrations
transaction attempts
changes deferred by budget
agent heartbeat delay
```

A particularly useful assertion is:

```go
if abandoned > 0 {
	t.Fatalf("controller abandoned %d reconciliations", abandoned)
}
```

and:

```go
if maxHeartbeatGap > 2*heartbeatInterval {
	t.Fatalf("heartbeat starvation: max gap %s", maxHeartbeatGap)
}
```

That directly tests the original production problem instead of just eventual state.

---

# Suggested order for the lead developer

I'd split these into PRs so each one is easy to review:

**PR 1 — correctness**

```text
Change.Group
atomic transaction batching
stateful marker cleanup
ReadInstance effective state
tests
```

**PR 2 — contention**

```text
per-reconcile watch context
telemetry prefix separation
independent heartbeat
reduced controller watch churn
50-node stress test
```

**PR 3 — side-effect safety**

```text
storage operation intents
idempotent snapshot/resize execution
cloud autoscale operation intents
```

The single most important change is **replacing `changes[:maxTransactionChanges]` with whole-operation batching**. The most important scale change is **getting high-frequency agent telemetry out of the controller write/watch domain**. Raising the retry count from 5 to 20 would be much less effective than those two changes.
