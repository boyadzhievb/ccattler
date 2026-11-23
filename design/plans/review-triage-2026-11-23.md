# External Review Triage — 2026-11-23

Three external reviews of master `e2549e2` (an updated code review, a progress review, and a principal-engineer audit).
Each finding was checked against the code and `milestones.md` on 2026-11-23. Note: the reviews predate `800c0cc`
(M267's implementation, committed locally, not yet pushed when they were written).

## Outcome summary

- **New milestones:** M268 (docs state what is done), M269 (reconciliation consistency contract, draft in
  [reconciliation-consistency-contract.md](reconciliation-consistency-contract.md)), M270 (real-etcd test tier),
  M271 (control-plane outage behaviour), M272 (scale benchmark with a write budget), M273 (zero-gap instance moves).
- **Added to existing milestones:** `os/exec` audit → M182; M260 ↔ M208 overlap noted; M265 marked as the same item
  as M180's pinned-revision task; the flaky test re-flagged.
- **Already tracked (no change):** M181, M254, M246, M180, M184, M183, M191, M185, M203, M208.
- **Decision taken:** M267 move semantics — option A now with safeguards, option C as M273 (user, 2026-11-23).
- **Open for the user:** M267 closing criterion (one full run as M231 requires, or several consecutive full runs
  with zero unexplained churn, as review 2 recommends).

## Findings

| Review | Finding | Checked | Outcome |
|---|---|---|---|
| 1, 2, 3 | M267 rebalancing fix not in the code | True at `e2549e2`; implemented in `800c0cc`, review rework in progress | M267 (in progress) |
| 1, 2 | M265 snapshot fix missing | True; M265 is open, not started | M265; marked in M180 |
| 1, 2, 3 | M266 unchanged-state writes | True; M266 open, proposal `6e88148` | M266 |
| 1, 2 | Milestones "describe the fix as completed" | The table text is changelog prose for `release.sh`; open and released rows are mixed, no status | **M268** |
| 1, 2 | Second service port has no VIP | True (`controllers/network.go:103` uses `servicePorts[svc][0]`) | M191 (open) |
| 1, 2 | `TestControlPlaneRestart_NoDataLoss` red | Flaky under CI load; 30 local `-race` runs pass on old and new code; already listed (fixed 200 ms sleep, assert not in `waitFor`) | Runner hardening item, re-flagged; fix with M267 |
| 2 | Consecutive full runs with zero unexplained churn before trusting rebalancing | Process recommendation | **User decision** (M267 closing criterion) |
| 2 | HA correctness / validation incomplete | True | M180, M184 |
| 2, 3 | Agents have broad etcd access; mTLS is authentication, not authorization | True; threat model says so | M181 (P0 per review 3) |
| 2 | eBPF restart-safe maps, TCP/SNAT coverage | True | M183 |
| 2 | Autoscaler wired to the simulator provider only | True | M208, M260 (overlap noted) |
| 2 | No real-scale benchmark in CI | True: only in-memory load tests | **M272** |
| 3 | Declared limits not applied | True | M254 |
| 3 | Resources released before the container is gone | True | M246 |
| 3 | Rebalancing replaces instead of moving | True at `e2549e2` | M267 D2 (in `800c0cc`) |
| 3 | etcd outage questions (30 s outage, partitions, compaction, stale facts) | Not tested end to end | **M271**; stale facts → M269 |
| 3 | Transaction budget, absence guards, need a consistency contract | Absence guards already in M180; no written contract | **M269** (draft written) |
| 3 | Real-etcd tests mandatory (transactions, watch ordering, compaction, leases, CAS, restart, multi-controller) | Watch ordering and lifecycle gaps already listed in Test gaps | **M270** (absorbs those two gaps) |
| 3 | Certificate lifetime and revocation | Already in M181 | M181 |
| 3 | Security docs must separate implemented / tested / planned / accepted risk; security model names M181 as the fix while M181 is open | True | **M268** |
| 3 | Audit conditional authorization paths | Partly in M179 (conditional policies moved there) | M179 |
| 3 | Audit every `os/exec` path | No milestone | **Added to M182** |
| 3 | Write-amplification budget and measurements | No milestone | **M272** |
| 3 | Upgrade paths, mixed-version guarantees | M203 (upgrade controller) | M203 |
| 3 | Backup/restore, disaster recovery | M185 | M185 |
| 3 | SLOs, runbooks, independent operability | Not tracked as a milestone | Not scheduled; revisit after M180/M181/M184 |
| — | Zero-gap moves (from the M267 decision) | — | **M273** |

## Priorities suggested by the reviews

All three put correctness and isolation before features. Combined order they suggest: M267 → M265 → M266 → M181
→ M254 → M246 → M180/M184 → M191 → scale (M272). The current queue already has M267 → M262 rerun → M266; M268 is
small and stops the recurring misreading, so it is worth doing early.
