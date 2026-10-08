# M74 — Atomic Change Groups & Stateful Marker Lifecycle

## Problem

Two correctness bugs remain after M73:

1. **Runner budget truncation splits atomic operations.** The runner caps change
   sets with `changes[:maxTransactionChanges]`, which can split multi-key logical
   operations (e.g., a 4-key instance replacement commits the stop marker but not
   the replacement creation). This causes orphan partial instances and slow
   convergence at scale.

2. **Stale derived markers poison stateful ordinal recreation.** Stateful instance
   IDs are `{service}-{ordinal}`. When `postgres-2` fails, the FailureController
   writes `derived/instance/postgres-2/controller_stopped = true`. If the ordinal
   is later recreated (scale-up or recovery), the stale marker makes
   `effectiveInstanceState` return "stopped" for the new incarnation, permanently
   blocking it.

Additionally, `ReadInstance()` doesn't use `effectiveInstanceState`, creating an
API consistency bug where `ReadInstance()` and `ListInstances()` disagree on state.

## Fix

### Step 1: Add Group field to Change

Add a `Group string` field to the `Change` struct. Changes sharing the same
non-empty Group value form an atomic unit that must not be split by budget
truncation. A `groupedChanges(group, ...changes)` helper assigns the group.

### Step 2: Group-aware runner truncation

Replace `changes[:maxTransactionChanges]` with `takeWholeGroups(changes, limit)`
that only selects complete groups. Sort by `(Group, Key)` to keep groups
contiguous after sorting.

### Step 3: Controller change grouping

- InstanceController: group 3-key stateless creations and 4-key stateful creations
- FailureController: group 4-key replacements, 2-key drains, 2-key recoveries
- Other multi-key controllers: group as appropriate

### Step 4: Stateful marker cleanup on recreation

When `createNextStatefulInstance` recreates a stateful ordinal, it deletes any
stale `controller_stopped` and `node_failure` markers in the same atomic group.

### Step 5: Fix ReadInstance effective state

`ReadInstance()` scans derived markers alongside observed state and uses
`effectiveInstanceStateFromFields` for the state field.

### Step 6: Fix watch context leak

Add per-loop child context in `runControllerLoop` so that when one watch closes,
all sibling watches are cancelled before restart.

## Acceptance criteria

- `TestTransactionBudgetingConvergence` (50 nodes, 1000 workloads, 5 killed) passes within 300s
- `TestStatefulOrdinalReusedAfterControllerStopped` regression test passes
- `ReadInstance` and `ListInstances` agree on effective state
- No watch goroutine leaks after repeated watch closures
