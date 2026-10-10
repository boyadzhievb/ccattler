# CCattler Project Status

## Current Milestone

M80 — Transaction Authorization Fix & Cloud Provider CI (Phase 80)

- Status: complete
- Items: fix AuthorizedStore.Transaction failure-branch authorization bypass, regression test, cloud provider CI workflow (AWS keys + GCP Workload Identity Federation)

## Recent Completions

- [2026-10-10] M80 Phase 80 — Security: fix transaction authorization bypass in AuthorizedStore (onFailure branch unchecked), cloud provider CI workflow with AWS and GCP
- [2026-10-10] M79 Phase 79 — Correctness VI: capacity request duplicate-key conflict, cloud LB backend update for existing LBs, orphaned pending ensure cleanup, storage volume deletion with pending operations
- [2026-10-09] M78 Phase 78 — Durable Capacity Request State Machine: capacity lifecycle states, request tracking, state transitions
- [2026-10-09] M77 Phase 77 — Cluster Autoscaling Controller: capacity demand detection, scale-up/scale-down decisions, infrastructure provider integration
- [2026-09-19] M30 Phase 33 — Identity RBAC & CLI: credential-broker role, node-agent credential access, cca get cloud-identities
- [2026-09-19] M29 Phase 32 — Agent Credential Materialization: CredentialProvider interface, AWS/GCP/Azure file formats
- [2026-09-19] M28 Phase 31 — Credential Broker: CredentialBrokerController issues/refreshes/garbage-collects cloud credentials
- [2026-09-19] M27 Phase 30 — Cloud Provider Adapters: CloudProviderAdapter interface, AWS/GCP/Azure adapters, CredentialStore AES-256-GCM
- [2026-09-19] M26 Phase 29 — OIDC Infrastructure: WorkloadTokenIssuer ECDSA P-256, OIDC discovery + JWKS endpoints
- [2026-09-19] M25 Phase 28 — Identity DSL & Facts: cloud_identity and credential_broker AST/parser/compiler

## Known Issues

- Load test: 200-node/5000-workload test times out at Phase 1 (~2400/5000 in 30 min). Reconciliation throughput bottleneck under investigation.
- Azure cloud provider: stub implementation only (all methods return "not implemented").
- Cloud autoscaling: infrastructure interface is simulator-backed; real provider implementations deferred.

## Metrics

- Source files: 158
- Test files: 131
- Total Go lines: ~94K
- Test count: 1,602 across 20 packages
- Latest release: v1.14.2-beta
- Milestones complete: M1–M80
