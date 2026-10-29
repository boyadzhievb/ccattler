# CCattler Anti-Pattern Rules

Enforced from Phase 50. Violations are caught in code review and CI linting.

## No Dead Code

Every exported type, function, and constant must have a production caller. Test-only helpers live in `_test.go` files. Unreferenced code is deleted, not commented out.

## No Magic Numbers

Timeouts, thresholds, intervals, and retry counts must be package-level named constants. Raw numeric literals in logic are forbidden.

```go
// BAD
if elapsed > 30*time.Second {

// GOOD
const nodeLeaseTimeout = 30 * time.Second
if elapsed > nodeLeaseTimeout {
```

## Function Length Limit: 80 Lines

Break long functions into sub-functions with descriptive names. Test functions are exempt.

## No Copy-Paste Duplication

Same pattern appearing 3+ places must become a shared helper. Near-duplicate blocks longer than 10 lines must be refactored.

## Typed Enums Over Raw Strings

Instance states, node states, probe types, rollout phases — all use typed string constants, never raw string literals.

```go
// BAD
if state == "running" {

// GOOD
if state == InstanceStateRunning {
```

## Every Exported Type Must Be Wired

Show where each exported type is instantiated in production. Orphan types that exist "for future use" are deleted.

## Integration Test Assertions Must Be Inside `waitFor`

Never assert async facts bare after a `waitFor` that checked a different fact. Every assertion on asynchronous state belongs inside its own `waitFor` block.

```go
// BAD — fact B may not exist yet when fact A settles
waitFor(func() bool { return store.Get("factA") == "ready" })
assert(store.Get("factB") == "done")

// GOOD
waitFor(func() bool { return store.Get("factB") == "done" })
```

## Fact-Based Architecture Anti-Patterns

These patterns violate CCattler's core architectural rules:

### No Imperative Commands in the Store

Store facts, not commands. `desired(instance, running)` not `start(instance)`. A fact describes what the world should look like. A command describes what to do — and commands are not idempotent.

### No Cross-Component Assumptions

No component may assume another performed an action. React to state only. If the scheduler hasn't placed an instance yet, the agent waits — it doesn't call the scheduler.

### No Mixed Desired/Observed Writes

Controllers that read `desired/` must never write to `desired/` (except rollout controller's documented image rollback). Desired state comes from users and policy; observed state comes from agents; derived state comes from controllers.

### No Lock-and-Hold Concurrency

Use optimistic concurrency via CAS transactions. Never hold a lock across a reconciliation cycle.

### No Non-Deterministic Reconciliation

Controllers must produce the same output for the same input. Sort map keys before processing. Use deterministic tie-breaking in placement decisions.
