# CCattler Project Status

## Current Milestone

M27 — Cloud Adapters (Phase 30)

- 5/5 items done
- Status: complete
- Items: CloudProviderAdapter interface, AWS STS adapter, GCP STS adapter, Azure AD adapter, CredentialStore with AES-256-GCM encryption

## Recent Completions

- [2026-09-19] M27 Phase 30 — Cloud Provider Adapters: CloudProviderAdapter interface, AWS/GCP/Azure adapters with validation stubs, SimulatorCloudAdapter, CredentialStore with AES-256-GCM, 11 tests
- [2026-09-19] M26 Phase 29 — OIDC Infrastructure: WorkloadTokenIssuer with ECDSA P-256 signing, MintWorkloadToken, OIDC discovery + JWKS endpoints, 10 tests
- [2026-09-19] M25 Phase 28 — Identity DSL & Facts: cloud_identity and credential_broker AST nodes, parsers, compiler with provider validation (aws/gcp/azure), fact keys, scan prefixes, 16 tests
- [2026-09-19] M24 Phase 27 — Storage Resilience: VolumeMigrating state, pre-migration snapshots, usage monitoring, online resize, replication state tracking
- [2026-09-19] M23 Phase 26 — P0 Correctness: observation-based state, no 127.0.0.1 fallback, watch overflow handling, init restart-safety, decoupled probes, race/restart tests
- [2026-09-19] M22 Phase 25 item 1 — Prometheus `/metrics` endpoint with counters, gauges, histograms
- [2026-09-19] M21 Phase 24 — describe, events, diff, errors, completions, column formatting, API describe
- [2026-09-18] v0.20.2 — Tech debt cleanup, AI agent team infrastructure
- [2026-09-18] M20 Phase 23 — cross-host endpoints, DNS server, HTTP reverse proxy, placement constraints

## Known Issues

(none currently tracked)

## Metrics

- Source files: 89
- Test files: 49
- Total Go lines: ~44K
- Latest release: v0.20.1
- Milestones complete: M1–M27
