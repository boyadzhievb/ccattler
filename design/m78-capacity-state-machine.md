# M78 — Durable Capacity Request State Machine

## Motivation

Finding #4 in `design/chat-09oct-2.md` identifies that `ClusterAutoscaleController` calls
`infraProvider.RequestNode()` and `RemoveNode()` directly inside `Reconcile()`, returns
`nil, nil`, and has no durable state for capacity requests. This violates architectural
rule 5, is crash-unsafe, and can't distinguish capacity shortages from unsatisfiable
placement constraints. Karpenter's capacity lifecycle model is the design reference.

## Changes

### 1. Scheduler emits unplaced-demand facts

When the scheduler cannot place an instance, it writes structured facts explaining why:

```
derived/scheduler/unplaced/<instanceID>/reason        "insufficient_capacity" | "unsatisfiable_constraint"
derived/scheduler/unplaced/<instanceID>/requirements   JSON {service, cpu, memory, architecture}
```

- `insufficient_capacity`: candidates exist but none has enough resources, or no alive nodes
- `unsatisfiable_constraint`: no node matches architecture/label/restrict constraints

Stale unplaced facts are cleaned up when an instance gets placed.

### 2. Capacity request lifecycle

The autoscaler writes durable capacity request facts instead of calling the provider:

```
derived/capacity/request/<requestID>/state           pending | launching | ready | failed
derived/capacity/request/<requestID>/requirements    JSON {cpu, memory, architecture}
derived/capacity/request/<requestID>/reason          human-readable
derived/capacity/request/<requestID>/node_id         provider-assigned node ID (after launch)
```

State machine: `pending → launching → ready` (success) or `pending → failed` (provider error).

### 3. Extended InfrastructureProvider

```go
RequestNodeWithRequirements(ctx, requestID, CapacityRequestRequirements) (string, error)
```

Replaces `RequestNode(ctx)`. The `requestID` enables provider-level idempotency.

### 4. Autoscaler post-commit pattern

- **Reconcile**: reads unplaced-demand facts, filters to `insufficient_capacity` only, emits
  capacity request facts for needed nodes (capped at max), emits removal requests for idle
  auto-nodes below min threshold. Returns `[]Change`.
- **ExecutePostCommitOperations**: calls provider for `pending` requests, checks observed
  state for `launching` requests, transitions accordingly.

### 5. Scale-down via post-commit

Idle auto-node removal also uses durable intent: Reconcile emits a removal capacity request,
post-commit calls `RemoveNode`, transitions to `ready` on success.

## Deferred

- Consolidation/disruption budgets
- Cost-aware node type selection
- Retry policy with exponential backoff
- NodePool abstraction (spot vs on-demand, GPU vs CPU)
- Real cloud provider implementations
