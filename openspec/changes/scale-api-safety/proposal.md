# Proposal

Implements M217 (Phase 217, Scale API Safety) from `milestones.md`, a bug found in the M214 adversarial run (2026-11-12).

## Why

`POST /api/scale` accepts any count for any name. In M214 run 1, `scale a-web 100000` created 436 real instances before the nodes were full, `scale nosuch 2` created instances of a service nobody declared, and a failed store write still answered `ok: true`. `instances` in a configuration has no upper check either, and nothing removes the orphan instances already created.

## What Changes

- **Instance count limits** for every service, on both paths that set a count (`cca scale` / `POST /api/scale` and `cca apply` / `POST /api/apply`):
  - below 500: accepted;
  - 500 to 600: accepted, and the response carries a warning that `cca` prints;
  - above 600: refused, unless the request repeats the exact count as approval and the caller holds `cluster.admin`. An approved count is written to the audit log.
  - No absolute ceiling. The documentation states that the limit stops mistakes and is not a capacity promise: counts far above what the nodes hold stay pending, and very large counts can overload etcd and the controllers.
- **Scale API correctness**: an undeclared service is 404 and nothing is written; the two count facts are written in one transaction; a store error is 500; the service changing or disappearing during the request is 409.
- **BREAKING (API)**: `POST /api/scale` answers 404 for a name with no service declaration, where it answered 200. Callers that scaled a name before applying its configuration must apply first.
- **CLI**: `cca scale` asks the user to type the count again when it is above the limit (terminal only), otherwise takes `--approve-above-limit <count>`. `cca apply` takes the same flag and prints warnings.
- **Orphan clean-up**: the intent controller resolves counts only for declared services. For a name with a count but no declaration, it sets the count to 0 so the instance controller stops the orphan instances, and removes the stale intents. This also clears orphans left by earlier scale calls.
- **Docs**: CLI reference, DSL reference and the website explain the warning, the limit, the approval and the capacity caveat.

Out of scope:

- The autoscaler's own steps: `scale { max }` is checked at apply time, each autoscaler step is still bounded only by that max.
- A job's `parallelism` (no limit today; jobs are not services).
- Tenant instance quotas: they stay as they are and still clamp the effective count.
- Making very large counts work (etcd and controller scalability).
- Instances left running after `tenant` lifecycle deletes a service's effective count (`tenant/lifecycle.go`): same family of bug, different trigger; recorded as a follow-up in the design.

## Comparison

| System | Model |
|---|---|
| Kubernetes | No per-Deployment replica cap. A namespace `ResourceQuota` (`count/pods`) refuses pods beyond the quota at admission; nothing asks for approval. |
| Nomad | No cap on `count`; quotas (Enterprise) limit resources per namespace. |
| AWS ECS | Fixed service quota of 5,000 tasks per service; raising it is a support request, not a flag. |
| CCattler (this change) | Soft limit with an explicit, audited `cluster.admin` approval that repeats the count, plus the existing tenant quota. The approval catches typos (`100000` for `100`) without capping deliberate large services. |

## Capabilities

### New Capabilities

- `service-scaling`: how a service's instance count is set and bounded: the scale API's validation and error answers, the warning threshold and the approval limit on the scale and apply paths, and the rule that only declared services get instances.

### Modified Capabilities

None. `api-route-authorization` is unchanged: `/api/scale` still requires `scaling.write`; the `cluster.admin` approval is an additional rule of `service-scaling`.

## Impact

- `api/server.go` (`handleScale`, apply path), `tenant/policy_gate.go` (apply through the gate), `lang` (count collection per service), `controllers/intent.go`.
- `cmd/cca`: `cca scale`, `cca apply` flags and prompt.
- New constants in `types` for the two thresholds.
- Docs: `design/runtime-invariant-flows.md` Flow 31, `design/cli-reference.md`, `design/dsl-reference.md`, `design/graphs/AUTHZ_GRAPH.md` route table note, website CLI reference and scaling guide.
- API clients: 404 for undeclared services (see BREAKING above).
