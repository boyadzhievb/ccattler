# Proposal

Implements M209 (Phase 209) from `milestones.md`. Split from M179 (`tenant-scoped-secrets`) on 2026-11-12; depends on it. Draft: not yet reviewed.

## Why

After M179, legacy `secrets/{name}` keys keep working only for the grants that already name them: they have no owning tenant, cannot be shared, and cannot gain new consumers. Operators need a safe way to give each one an owner without downtime.

## What Changes

- `cca secret migrate-names --plan` lists every legacy secret with its revision, consumer services and their tenants, and writes a mapping file.
- `cca secret migrate-names --apply mapping.ccv` validates the mapping and writes it as intent facts; a leader-elected `secret-name-migration` controller re-encrypts each secret under its canonical key, rewrites grants, creates shares for consumers in other tenants, and removes the legacy key, in resumable batches.
- Running instances do not restart (M179's config hash ignores canonical names).

Out of scope: anything M179 does; unreachable pre-M176 `route/` user keys beyond listing them with an admin delete path.

## Capabilities

### New Capabilities
- `secret-name-migration`: moving legacy secret names to tenant or cluster ownership.

### Modified Capabilities

None.

## Impact

- New controller with write domain `secrets/`, `desired/service/*/secret/`, `desired/secret_share/`, `observed/secret_names/`; API handlers and CLI.
- Store keys: `intent/secret_names/{legacy name}`, `observed/secret_names/{legacy name}`.
- Docs: `config-secrets.md`, Flow 31 in `runtime-invariant-flows.md`, `etcd-schema.md`, `cli-reference.md`.
