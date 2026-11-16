# Design

## Context

See proposal.md (Why) and the two delta specs for the required behaviour. The end-to-end chain is a new Flow 32 in `design/runtime-invariant-flows.md` ("Service Names and Fact Keys"), written as task 1.1; this document explains the choices behind it.

Current state, verified in code (2026-11-16):

- 97 service key constructors in `types/keys.go` across `desired/`, `effective/`, `intent/user/`, `intent/autoscaler/` and `derived/` service prefixes; their first field segments form about 34 roots (`scale` 17 constructors, `placement` 10, `probe` 9, `rollout` 8, `init` 6, `resources` 6, …).
- Four splitting rules for service keys:
  - `tenant/scoped_query.go` `extractServiceNameFromPath`: 19 hand-listed fields, searched from the end.
  - `tenant/fair_scheduler.go` `extractServiceNameAndField`: 19 hand-listed suffixes, searched from the start.
  - `controllers/helpers.go` `splitFactKeyIntoEntityAndSuffix`: first `/`. Used through `collect*ValuesBySuffix` by the intent resolver, so hierarchical services never get counts.
  - `controllers/helpers.go` `splitServiceFactKey`: iterates a map of known names and takes the first prefix match, so `payments` vs `payments/checkout` depends on Go's map order.
- Ad-hoc `strings` parsing in 31 non-test files that read service prefixes (`scheduler/scheduler.go` 41 operations, `api/status.go` 19, `controllers/autoscale.go` 15, `cmd/cca/command_server.go` 11, …). Not all of them split service keys; task 2.1 classifies each.
- `controllers/intent.go` `collectDeclaredServiceKeys` counts any key under `desired/service/` as a declaration (M217), not only empty-valued markers.
- `lang.WriteCompiledFacts` writes facts one by one in compile order; a service's marker comes first.

## Goals / Non-Goals

**Goals:**
- After this change no new write can create a key that two services claim, and every reader agrees which service a key belongs to.
- Hierarchical services work end to end, counts included.
- Damage already in a store is visible and cannot get worse.

**Non-Goals:**
- Repairing or removing damaged services (M206, see Follow-ups).
- Changing the key layout (option B).

## Decisions

### D1. Field roots are registered once, in `types`, and a test reads the constructors
`types` gets one set of service field roots (the first field segment of every service key). A test parses `types/keys.go` with `go/parser`, finds every `Key*Service*` constructor, takes the literal format string after `service/%s/`, and fails when its first segment is not registered. Dynamic suffixes (`expose/%d`, `tag/%s`, `volume/%s`, `placement/…`) are covered because only the literal root before the first `%` matters.
Alternative: a hand-kept list only — rejected, it is how the current three lists went stale (they miss `command`, `connection`, `probe`, `rollout`, …).

### D2. One splitter, left to right, first segment exempt
`types.SplitServiceFactKey(key, prefix) (serviceName, fieldPath string, isServiceKey bool)`: strip the prefix; the first segment always belongs to the name; scan the following segments left to right; the first one that is a registered root starts the field path; no root means the key is the declaration marker (empty field path). Valid names never contain a root after the first segment (D3), so the first root found is always the start of the field.
The generic first-slash splitter stays for non-service entities (nodes, instances, volumes, routes) and is renamed so it cannot be used for service prefixes by accident; `collect*ValuesBySuffix` gets a service variant.
Alternative: keep the search from the end (`scoped_query.go`) — rejected: a dynamic field path (`config/env/image`) would be cut at the wrong place.

### D3. The name rule lives in `types` and every write path calls it
`types.ValidateServiceName(name)` refuses a name whose segment after the first is a registered root, naming that segment. Callers: `lang.Compile` (covers apply through the policy gate and direct, `cca apply --simulate`, `cca run`), `cca lint`, `handleScale` (which already refuses `/` through `ValidateResourceName`; the new check runs as well so the rule does not depend on that). The first segment is exempt, so a flat service named `image` stays valid (its keys are `desired/service/image/image`, which D2 splits correctly).

### D4. A declaration is an empty-valued marker
`collectDeclaredServiceKeys` and any other "is it declared" check accept `desired/service/{name}` only with an empty value. Order cases, both tested: `web` then `web/image` leaves `desired/service/web/image = ""` (marker wins, `web`'s image is damaged); `web/image` then `web` leaves it `"nginx:1.28"` (`web/image` is not declared any more). In both cases no field fact declares a service.

### D5. Stored invalid names are found through their image fact
Markers can be overwritten (D4), so the startup scan does not rely on them: every compiled service has an image fact, `desired/service/{name}/image`. The scan takes every key under `desired/service/` that ends in `/image`, treats the part before it as a candidate name, and reports the candidates that break D3, with the colliding key (`desired/service/{prefix}/{root}`) and the service `{prefix}` it may have damaged. Reported at `cca server` startup through the logger; the server keeps running (user decision).

### D6. Applies cannot deepen stored damage
On both apply paths (the M217 `AdmissionCheck` hook on the policy gate, and the direct path), the server runs the D5 scan once per apply and refuses when a compiled key equals a stored invalid name's colliding key or lies under its subtree. `cca apply --simulate` uses its own empty store and is unaffected.
Alternative: refuse the whole cluster's applies while any invalid name exists — rejected: unrelated services must stay manageable.

### D7. Migrate readers by package, behind the round-trip test
Every reader classified in task 2.1 as splitting a service key moves to D2. The round-trip test (flat, 2- and 3-segment names, markers, every root, nested and dynamic paths, every prefix family) and a cross-reader test with overlapping names (`payments`, `payments/checkout`) guard the migration. The intent resolver's count collection moves too, which turns on counts for hierarchical services (spec service-scaling).

## Risks / Trade-offs

- [A cluster with a service named e.g. `team/scale` stops accepting that configuration] → reported by name at startup and in the apply error; the user renames. Expected rare in a beta; the startup report on the deploy testbed shows whether any exist.
- [Migrating ~30 readers can change behaviour in places the tests do not cover] → each package task names its tests; the cross-reader test runs every migrated reader on the same overlapping names; a Deploy Testbed run before release.
- [Hierarchical services suddenly get autoscaling and quota] → intended (spec), but listed as a behaviour change in the changelog row.
- [D5 misses a service without an image fact] → the compiler refuses a service without an image, so only hand-written facts could lack one; acceptable.
- [The D6 scan adds a `desired/service/` scan per apply] → one prefix scan, small next to compile; measured in task 4.2.

## Migration Plan

No data migration. On upgrade the startup report lists any invalid stored names; applies touching them are refused until M206 provides removal. Rollback: the previous binary resumes the old splitting rules; no facts were rewritten.

## Follow-ups

- M206 must deliver removal of an invalid legacy service that restores or keeps the surviving service's facts in either declaration order, shown explicitly in `cca diff` (requirement added to M206 in task 6.3).
- `/api/scale` refuses hierarchical names (`ValidateResourceName`); allowing them is a separate decision.
- Option B (a separate key level for fields) if the root list proves too restrictive.
