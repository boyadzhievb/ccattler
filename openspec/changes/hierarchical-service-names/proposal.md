# Proposal

Implements M230 (Phase 230, Hierarchical Service Names Overwrite Other Services) from `milestones.md`, a bug found by the M217 security audit (2026-11-16). Option A, decided with the user 2026-11-16; cleanup of existing damage is M206's job (option 2).

## Why

Service facts live at `desired/service/{name}/{field}`, and a service name may contain `/` (`payments/checkout`, the team is the first segment). So the declaration of `web/image` is the same key as service `web`'s image: applying both leaves `web`'s image empty (verified: the compiled facts write `desired/service/web/image` twice, `"good:1"` then `""`). Any `workload.create` holder can damage a service that shares the prefix, and the code that reads keys back uses four different rules to split name from field, so the same key means different services to different controllers.

## What Changes

- **Name rule**: the first segment of a service name is never checked; no later segment may equal a reserved top-level service field root (`image`, `instances`, `resources`, `scale`, `placement`, …). `payments/checkout`, `frontend/web`, `payments/checkout/api` stay valid; `web/image`, `web/instances`, `payments/checkout/image` are refused at every write boundary (configuration apply and `--simulate`, `cca lint`, the scale API).
- **One root list, one splitter** in `types`: field roots are the top-level attribute roots of every service key constructor (desired, effective, intent, derived), not every final segment, so `payments/cpu` stays a valid name. Every reader of service fact keys splits through one left-to-right function; the three hand-written field lists, the search-from-the-end rule and the map-order-dependent `splitServiceFactKey` go away.
- **Declaration markers**: a service counts as declared only by its empty-valued root marker, not by any key at that path (today a `web` image fact makes `web/image` look declared).
- **Behaviour change**: hierarchical services now get their instance counts, autoscaling and scale policy through the intent resolver, which today splits at the first `/` and ignores them.
- **Existing collisions**: `cca server` reports at startup every stored service name that breaks the rule, with the colliding key and the valid service it may have damaged. Applies that would write such a colliding key are refused, so nothing deepens the damage. No store key migration, no automatic cleanup.
- **M206 requirement** (added to the roadmap, not built here): pruning must be able to remove an invalid legacy service while restoring or keeping the facts of the surviving one, in either declaration order, with an explicit removal shown in `cca diff`.
- **Docs and graphs**: `design/etcd-schema.md` (layout, roots, rule), `design/multi-tenancy.md`, and a call graph section listing every reader that infers a service name and field from a fact key.

Out of scope:

- Removing or renaming an existing invalid service (M206, see above). A general `cca service remove` needs resource ownership, deletion authorization, controller-driven stopping and exact fact attribution; it is not rushed in as a shortcut.
- Option B, a separate key level for fields (`desired/service/{name}/spec/{field}` or escaped names) with a store migration: a possible future milestone.
- Name collisions of other hierarchical entities (volumes, routes): this change covers service keys only; other entities keep the generic first-slash splitter, documented as distinct.
- `/api/scale` refusing hierarchical names (`types.ValidateResourceName` forbids `/`): unchanged here, recorded as a follow-up.

## Comparison

| System | How a name and its attributes are kept apart |
|---|---|
| Kubernetes | One object per key (`/registry/{resource}/{namespace}/{name}`); attributes live inside the stored object, never in the key. Names are DNS labels without `/`. |
| Consul | One catalog entry per service definition; the KV store is free-form and leaves layout to the user. |
| etcd convention | Keys are opaque; the application must make its layout unambiguous. |
| CCattler (this change) | Keeps the flat per-field layout and hierarchical names, and makes the layout unambiguous by rule: a reserved field root can never appear inside a name, so the first one after the first segment always starts the field. |

## Capabilities

### New Capabilities

- `service-naming`: which service names are valid, how a service fact key is split into service name and field, what declares a service, and how names stored before this rule are reported and protected.

### Modified Capabilities

- `service-scaling`: hierarchical services get their instance counts resolved like flat ones (added requirement).

## Impact

- `types` (field roots, name validation, the shared splitter), `lang` (compile-time name check), `cmd/cca` (`cca lint`, apply `--simulate`, startup report in `cca server`), `api` (apply refusal on colliding keys).
- Every reader of service fact keys: `controllers/` (helpers, intent, autoscale, drain, warmzero, rollout, eviction, network, cloud_*, credential_broker, stateful_*), `scheduler/scheduler.go`, `api/status.go`, `api/handler_gc.go`, `tenant/` (scoped_query, fair_scheduler, quota, garbage_collector, lifecycle), `security/` (fact_authorizer, secret_delivery). The inventory is design task 1.
- Docs: `design/etcd-schema.md`, `design/multi-tenancy.md`, `design/runtime-invariant-flows.md` (new flow), call graph section; `milestones.md` M206 requirement.
- Users: a configuration with a service name like `web/image` that used to apply is now refused; clusters that already hold one get a startup report.
