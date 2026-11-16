# Design

## Context

See proposal.md (Why) and `specs/service-scaling/spec.md` for the required behaviour. The end-to-end chain is [Flow 31: Instance Count Limits](../../../design/runtime-invariant-flows.md#flow-31-instance-count-limits-m217) in `design/runtime-invariant-flows.md`; this document explains the choices behind it.

Current state, verified in code:

- `handleScale` (`api/server.go`) validates only `instances >= 0` and the name format, then does two separate `Put`s whose errors are logged and ignored before answering `ok: true`.
- A count reaches instances through two controllers. The intent resolver (`controllers/intent.go`) turns every `intent/user/service/{name}/instances` into `effective/service/{name}/instances`, clamped by `scale` policy bounds and tenant quota; it never checks the service declaration. The instance controller (`controllers/instance.go:89`) iterates only names that have an effective count.
- Apply has two paths: through the policy gate (`tenant/policy_gate.go`, production) and `applyDirect` (no gate). `lang.Compile` already refuses `instances < 0`.
- The intent resolver's write domain is `effective/service/` only (`controllers/topological_sort.go`).

## Goals / Non-Goals

**Goals:**
- One limit rule, one place: both the scale and the apply paths call the same check.
- Fail closed: an approval that is missing, mismatched or held by the wrong principal never writes anything.
- Orphans created before this change disappear without an operator step.

**Non-Goals:**
- A configurable limit (decided with the user: constants for now).
- Changing the autoscaler, tenant quotas or job parallelism.

## Decisions

### D1. Two constants and one check function in `types`
`ServiceInstanceWarningThreshold = 500` and `ServiceInstanceApprovalLimit = 600`, plus one function that takes a count and an approved count and returns accepted / accepted-with-warning / needs-approval / approval-mismatch. The capability check stays at the API layer, because only it knows the principal.
Alternative: a cluster setting fact writable by `cluster.admin` — rejected for now (user decision); the constants are the single place to change.

### D2. Approval repeats the exact count
`approved_instances` (scale) and `--approve-above-limit N` (apply) must equal the count. A plain boolean `force` would let a typo through, which is the case the approval exists for. No absolute ceiling: a deliberate large service is the admin's call (user decision).

### D3. `cluster.admin` checked only when approval is used
A request at or below 600 needs only `scaling.write` (scale) or `workload.create` (apply), as today. Above 600 the API additionally calls `AuthorizeAPI(principal, cluster.admin, cluster)`. The route table entry for `/api/scale` stays `scaling.write`; `AUTHZ_GRAPH.md` gets a note that approval adds `cluster.admin`.

### D4. Scale writes in one transaction guarded by the declaration
`handleScale` reads `desired/service/{name}`: absent → 404. It then runs one `Transaction` whose compare is the declaration's revision and whose success branch puts both count facts. Compare failed → 409; store error → 500. This uses the existing `StateStore.Transaction`, so no new store API.
Alternative: check-then-put without a compare — rejected: an apply that removes the service between the two steps would recreate the orphan.

### D5. Apply checks the limit before the gate's authorization stage
The configuration is parsed and compiled first (existing stages), then every service's largest count among `instances`, `scale min` and `scale max` goes through D1. A refusal names the service and its count and writes nothing. Both apply paths (gate and direct) call the same helper. The approval travels as a new optional `approved_instances` field of the apply request body; `cca apply --simulate` runs the same rule without the capability check, since it never reaches a server.

### D6. Orphans: effective count 0, not deletion
For a name with an intent but no declaration, the intent resolver writes `effective/service/{name}/instances = 0`. The instance controller then stops every instance of that name through its normal scale-down. Deleting the effective count instead would hide the name from the instance controller and leave the instances running forever (`controllers/instance.go:89`). The stale intents stay: the resolver may not write `intent/` (write domain), they are harmless with a 0 effective count, and declaring the service again overwrites the user intent through apply.
Alternative: the instance controller stops instances of any service without an effective count — rejected: job instances have no effective count and would be stopped.

### D7. Approval is audited through the API authorizer's audit logger
Handlers already record operations whose capability check alone is not enough of a record through `APIAuthorizer.RecordAuditEntry` (join tokens, M219), and `cca server` wires that logger. Approvals use it too: `action=scale.approve` / `apply.approve`, the principal, `target=service/{name}`, `policy=instances={count}`, ALLOW or DENY. No new logger or setter.

## Risks / Trade-offs

- [Clients that scaled before applying now get 404] → documented as BREAKING in the proposal; the M214 replay and e2e scripts are checked for this order.
- [An approved 100000 still overloads the cluster] → documented in Flow 31 and the website as not a capacity promise (user decision); tenant quota still clamps per team.
- [The stale `effective/.../instances = 0` and intent facts of an orphan name stay in etcd] → a few keys per orphan name; removed if the name is declared and later deleted through the normal path. Accepted.
- [Autoscaler steps above 600] → not checked per step; `scale max` above 600 already needs approval at apply, which bounds the autoscaler. Recorded as a non-goal.

## Migration Plan

No data migration. On upgrade the intent resolver sets existing orphans to 0 in its first cycle, and the instance controller stops them. Rollback: the previous binary resumes the old behaviour; orphans stopped meanwhile stay stopped.

## Follow-ups

- `tenant/lifecycle.go` deletes a removed service's effective count, which by D6's reasoning leaves its instances running. Separate bug milestone, found while designing this change.
