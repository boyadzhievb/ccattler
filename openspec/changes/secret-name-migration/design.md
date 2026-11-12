# Design

Context: M179 `openspec/changes/tenant-scoped-secrets/design.md` and `design/config-secrets.md` "Migrating unqualified names" / "Naming sequence (R8)". Decisions here carry over the M179 review rounds 1–2 (2026-11-12) findings CR-H2, SA-M6, N2, N3, M3, M4, SA MEDIUM-7. Not yet reviewed itself.

## Decisions

| Decision | Choice | Why / alternative |
|---|---|---|
| Executor | `--apply` writes `intent/secret_names/{legacy name} = {target, plan_revision, legacy_revision, mapping_hash}` and returns; a leader-elected `secret-name-migration` controller does the work and reports in `observed/secret_names/` | Same as M177/M178 (`keyring-migration`, `keyring-rotation`): no long HTTP request, one executor across replicas, `reconcile → []Change` (CLAUDE.md rule 5). |
| Intent authorization | `intent/secret_names/` → `cluster.admin` in `RequiredCapabilityForFactWrite`; the controller re-validates every intent on each cycle (target names, reserved names, hash) and ignores invalid ones with an observed error | `intent/` is otherwise `workload.create`, and the generic roles can write `intent/` (review SA MEDIUM-7). |
| Phases per secret | 1) Create the canonical envelope (create-only, new DEK, canonical key as AAD), canonical grants (same `reference`, `mount_path`) and shares for consumers in other tenants, in batches below the transaction limit. 2a) Delete old grants in batches. 2b) One small transaction guarded on the legacy envelope revision deletes the legacy key and any remaining old grants. | Phase 2 as one transaction exceeds 128 ops for many consumers (review N3). |
| Dual names | Between phase 1 and 2b a service may hold both grants for one reference; delivery and the agent de-duplicate by reference and prefer the canonical grant | One mount per reference (review M4). |
| No restarts | M179's config hash covers `(reference, mount_path)` only, so phases 1–2 do not change it | Review N2. |
| Preconditions | `keyring/legacy` ∈ {none, retired}; no KEK rotation intent; M179 mixed-version guard passes; no `keyring/name_conflict/` records | v1 envelopes do not bind the store key; keyring controllers CAS-rewrite `secrets/`. |
| Concurrent writes | A legacy `secret set` between phase 1 and 2b fails the 2b guard; the controller re-copies the value (phase 1 again, updating the canonical envelope with a revision guard) | No lost update. |
| Mapping validation | Same validators as the v2 API; no duplicate sources or targets; no existing target; the plan revision and each legacy revision must match or the secret is re-planned; a target tenant none of the consumers uses needs `--allow-foreign-tenant` (audited) | A wrong mapping would move a secret to readers who should not see it. |
| Plan authorization | `--plan` needs `secret.metadata.read` at cluster; `--apply` needs `secret.write` at cluster | The plan lists every tenant's consumers. |
| Pre-M176 `route/` user keys | Listed by `--plan` as unreachable; `cluster.admin` deletes them with `cca secret delete-conflict` | They cannot be mapped (the name is reserved). |
| Audit | One record per moved secret: principal of the intent, mapping hash, source, target | |

## Risks / Trade-offs

| Failure case | Behaviour |
|---|---|
| Controller crash in any phase | Create-only writes and revision guards make each phase idempotent; the next leader resumes from the store state. |
| Leader change mid-batch | M180 fencing is not yet in place; batches are idempotent, so a double run writes nothing new. |
| Intent tampered after validation | The controller re-validates each cycle and checks the mapping hash in the audit record. |
