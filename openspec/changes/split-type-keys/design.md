# Design

## Context

See proposal.md (Why). Current state, checked in the code on 2026-11-24 at 92feee5:

- `types/keys.go`: 2,634 lines, 376 top-level declarations (336 functions plus const blocks and one type,
  `GateProxyState`). Its sections follow the order in which features were added, not subjects. Probe keys are at
  :1446 and :1508, instance keys at :301, :1025, :1115 and :1754, and the rollout keys sit between cloud identity
  and node drain. Some sections are marked with `// ---` separators or one-line floating comments
  (`// Tenant lifecycle keys.`), some with nothing.
- Readers of the file by name: `integration/doc_crosscheck_test.go` `TestDocCrossCheckFactPrefixesExist` checks
  that the ten layer prefixes (`PrefixDesired = "desired"` … `PrefixDerived = "derived"`) are in `types/keys.go`.
  On branch `m230-hierarchical-service-names` (M230, paused), `types/service_names_test.go`
  `TestEveryServiceKeyConstructorUsesARegisteredRoot` parses `keys.go` with `parser.ParseFile` and requires at
  least 90 `Key*Service*` constructors. No script or workflow reads the file.
- Graph references with line numbers: `AUDIT_GRAPH.md:548-549`, `STATEFUL_SERVICES_GRAPH.md:254,352` (already stale:
  it says :800 for `KeyDesiredServiceStateful`, which is at :883), `OBSERVABILITY_GRAPH.md:388`.
- No flow in `design/runtime-invariant-flows.md` changes. No store key is read or written differently, so there
  are no new writers, no fencing and no change budget to state.

## Goals / Non-Goals

**Goals:**
- Each subject's keys are in one file named after it, and `keys.go` says which file holds what.
- The move is checkable by a mechanical comparison against the base commit, not only by review.

**Non-Goals:**
- Reordering keys within a subject beyond keeping their original relative order.
- Splitting the shared scan-prefix block (D2).
- Editing the M230 branch (D4).

## Decisions

### D1. Files by subject, not by layer
A file holds every layer of one subject (desired, effective, intent, observed, derived), because callers work on
one subject across layers: the rollout controller reads desired update pacing and writes derived rollout state. A
split by layer (`keys_desired.go`, `keys_observed.go`, …) would keep each subject spread over five files.
Kubernetes groups the same way: each resource's storage and registry code lives in its own package
(`pkg/registry/core/pod/…`), not in one package per layer.

Assignment rules, applied in order to each top-level declaration's first name (first match wins). The patterns
are extended regular expressions on the Go identifier (`\|` in the table is a plain `|`):

| # | File | Pattern | Subject |
|---|---|---|---|
| 1 | `keys.go` | `^(Root\|PrefixDesired\|ScanDesiredServices)$` (the `Root` const, the layer-prefix block, the shared scan block) | shared |
| 2 | `keys_auth.go` | `^(KeyAuth\|ScanAuth)` | roles, grants, groups, policies, users |
| 3 | `keys_route.go` | `RouteTLSKeySecret\|SecretNameRouteTLSKey` | route TLS key secret names |
| 4 | `keys_secret.go` | `Keyring\|SecretGeneration\|SecretDelivery\|SecretWait\|LegacySecret\|GateKey\|GateProxy\|LegacyKeyFile\|NodeReport` | secret generations, delivery, gate keys, keyring |
| 5 | `keys_probe.go` | `Probe` | probe config and results |
| 6 | `keys_rollout.go` | `Rollout\|ServiceUpdate\|Disruption` | update pacing, disruption budget, rollout state |
| 7 | `keys_cloud.go` | `Cloud\|Credential\|ExposeExternal` | cloud provider, load balancers, routes, cloud identity, credentials |
| 8 | `keys_placement.go` | `Placement\|Scheduler\|CapacityRequest\|ClusterAutoscale\|DesiredGroup\|Priority` | placement, groups, priority, unplaced demand, capacity requests |
| 9 | `keys_tenant.go` | `Tenant\|Export\|Import\|ServiceOwner` | tenants, quotas, usage, export/import |
| 10 | `keys_volume.go` | `Volume` | volumes |
| 11 | `keys_route.go` | `Route` | routes |
| 12 | `keys_job.go` | `Job` | jobs and cron jobs |
| 13 | `keys_service_scale.go` | `Scale\|Autoscaler\|EffectiveServiceResources\|ObservedMetric\|LastRequestTime\|ActivationState\|ServiceInstances$` | instance counts, scaling policy, autoscaler intents, metrics, scale-to-zero |
| 14 | `keys_network.go` | `Network\|Endpoint\|DNS\|VIP` | endpoints, subnets, allocations, VIPs, DNS, network policy |
| 15 | `keys_instance.go` | `^Key(Observed\|Derived)Instance\|CrashLoop` | instance facts (before rule 16, so `KeyObservedInstanceNode` stays here) |
| 16 | `keys_node.go` | `Node\|Lease` | nodes, leases, utilization, drain, pressure |
| 17 | `keys_instance.go` | `Instance` | remaining instance keys |
| 18 | `keys_service.go` | `Service\|Connection` | the rest of a service: image, command, logging, lifecycle, expose, resources, health, tags, stateful, config, secrets, connections, init steps |

Result on 92feee5 (lines include doc comments): `keys.go` 4 declarations (~170 lines, plus the file map),
`keys_cloud.go` 40 (~280), `keys_service.go` 39 (~240), `keys_instance.go` 33 (~225), `keys_placement.go` 31,
`keys_secret.go` 31, `keys_service_scale.go` 28, `keys_volume.go` 27, `keys_job.go` 24, `keys_rollout.go` 22,
`keys_route.go` 22, `keys_tenant.go` 20, `keys_node.go` 19, `keys_probe.go` 12, `keys_network.go` 14,
`keys_auth.go` 11. No declaration is unmatched. If 3f or anything else lands first and adds a declaration, the same
rules place it. A declaration the rules place wrongly by subject is moved by hand and listed in tasks.md with one
line of reasoning.

Alternative: fewer, larger files (service, node and instance, infrastructure, security). Rejected: `keys_service.go`
alone would be about 900 lines, and the point is that a reader opens one small file per subject.

### D2. `keys.go` keeps the shared parts and a file map
`keys.go` keeps `Root`, the layer-prefix block (so `TestDocCrossCheckFactPrefixesExist` keeps passing unchanged) and
the shared scan block (`ScanDesiredServices` … `ScanDerivedSchedulerRebalanced`, :409-510). The scan block stays
whole: it is the one list of every area that controllers watch, and splitting it would add `const (` blocks to
every file for no reader benefit. Under the import, a comment lists each `keys_*.go` file and its subject (the
table above, one line each). Declarations keep their original relative order within each file. Separator comments
(`// ---`) and floating section comments are dropped: the file name replaces them.

Changed during apply, 2026-11-24 (main session, after the implementer's report; the user can reopen it):
- **Floating comments are kept.** Every floating comment except the `// ----` separator lines is kept unchanged.
  Each goes right before the base declaration it preceded, in whatever file that declaration lands. The first
  version of D2 would have dropped 34 lines, among them real documentation: the key layouts (`Pattern:
  desired/service/{name}/probe/{probeType}/{field}` and 11 more), the probe types, and the connection,
  utilization, telemetry and init-step explanations. Only the 10 separator lines are removed.
- **The file map goes under `package types`.** `keys.go` uses no `fmt` and has no import.

### D3. Proof of a pure move: the line-multiset check
Every non-blank line of the base `keys.go` must still exist, the same number of times, across the new
`types/keys*.go` files, apart from the explained header and comment lines:

```bash
BASE=<commit before the move>
diff \
  <(git show "$BASE":types/keys.go | grep -v '^$' | sort) \
  <(ls types/keys*.go | grep -v '_test\.go$' | xargs cat | grep -v '^$' | sort)
```

Allowed differences, each listed in tasks.md: per new file, the copyright and SPDX lines, `package types` and
`import "fmt"` (only where `fmt` is used); the file map comment; the dropped `// ----` separator lines (D2 as
changed on 2026-11-24: floating comments are kept, so they are not an allowed difference). Any other line in the diff means a changed body, doc comment or name, and fails the task. `go build ./...`
then proves that no declaration was lost or duplicated and that no caller needs a change. `git diff --stat` must
show only `types/keys*.go` and the docs.

Alternative: a golden test that calls every constructor and compares its path. Rejected for this change: it needs a
hand-written call for each of the 336 constructors, and keeping it would be a new rule for every new key. It is
needed if option B (shared helpers that rewrite bodies) is ever done.

### D4. M230 is told, not edited
The M230 test reads `keys.go` only. After the split, it finds fewer than 90 service constructors and fails, which
is loud, not silent. This change adds a note to Phase 230 in `milestones.md`: when the branch is rebased,
`TestEveryServiceKeyConstructorUsesARegisteredRoot` parses every non-test `.go` file in `types` (a decision recorded
in M230's `design.md` at that time). The branch is not edited here. M230's own artifacts differ between master and
the branch, and editing them on master would conflict at the rebase.

## Risks / Trade-offs

- [A body or doc comment is changed by accident during the move] → D3's line-multiset diff catches any change to
  a line. The reviewer reruns it on the base commit.
- [The work-in-progress 3f diff touches `OBSERVABILITY_GRAPH.md`, and M266 or other changes touch `types`] → this
  change starts only after 3f is committed, on a clean tree. A change that later adds a key to `keys.go` by habit is
  caught in review via the file map; no test enforces placement here (code-rule checks are M259).
- [M230 rebase conflict] → none in Go code (the branch does not touch `keys.go`); only its test needs the D4
  change.
- [Blame history] → `git log --follow` follows only one file; `git blame -C -C` finds moved lines. Accepted.

## Migration Plan

Source-only. No store, API or binary behaviour change, so there is no rollout or upgrade order. Rollback is a
revert of the commit.
