# Reconciliation

The reconciliation loop is the heart of CCattler. Every capability — scheduling, scaling, networking, storage, failure recovery — runs through this single mechanism.

## The loop

```
facts → rules/queries → new facts → actions → real world → observed facts → repeat
```

At each tick:
1. Controllers read the current state from the fact store
2. They compare desired state against observed state
3. They propose changes to close the gap
4. Changes are committed transactionally
5. Node agents actuate the changes in the real world
6. Agents report new observed state back to the store

The loop repeats continuously. A 30-second periodic resync runs as a safety net, ensuring convergence even if watch events are missed.

## Five architectural rules

### 1. No component may assume another component performed an action

Components only react to state. The scheduler writes a placement fact; the network controller notices it. They never call each other.

### 2. Desired state and observed state are always separate

Never overwrite desired with reality. The difference between them is the work.

### 3. Idempotency is the fundamental primitive

Every operation is `ensure_X()`, not `do_X()`. Safe to repeat. `ensure_running(container)`, not `start_container()`.

### 4. Store facts and desired results, not commands

`desired(instance, running)` not `start(instance)`. This makes the system naturally retryable — if an action fails, the gap between desired and actual persists and the agent tries again.

### 5. Controllers return proposed changes, not arbitrary mutations

`reconcile(facts) → []change`, validated and committed transactionally by the command layer.

## Controller interface

Every controller implements:

```go
type Controller interface {
    Name() string
    Watch() []string          // fact prefixes to observe
    Reconcile(facts) []Change // proposed changes
}
```

The runner framework handles the watch → reconcile → commit loop. Controllers are stateless — they can crash and restart without losing information.

## Deterministic testing

The entire control plane is deterministic: given state A + observation B + policy C → desired state D. Tests don't need a cluster:

```
INPUT:  nodes=[n1:4cpu, n2:4cpu], service=web, desired=5, cpu=1
EXPECT: placement=[n1:3, n2:2]

INPUT:  n2=dead
EXPECT: placement=[n1:4, unsatisfied=1]

INPUT:  n2=alive
EXPECT: placement=[n1:3, n2:2]
```

## Controller resilience

Controllers restart with exponential backoff: 100ms → 500ms → 1s → 2s → 5s → 30s max. The runner manages controller lifecycles and cancels all controllers on failure.

Multiple controller replicas can run simultaneously — leader election ensures only one is active. If the leader crashes, another takes over.

## Chaos mode

```bash
cca chaos
```

Randomly injects: node failures, network delays, process crashes, stale observations, controller restarts, duplicate events, lost messages, slow storage.

The only assertion: **eventually observed state converges to desired state.**
