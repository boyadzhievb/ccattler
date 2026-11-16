# Proposal

Implements M231 (new milestone, test tooling, added to `milestones.md` by this change). Decided with the user 2026-11-16; M230 is paused on its branch `m230-hierarchical-service-names` until this change is done, so M230's own deploy runs are already faster.

## Why

Every Deploy Testbed run is the full suite: 598 s in run 43876417058. VMs and Ansible ~2 min, the Airflow workload 2 min 12 s, the quick checks ~15 s, backend churn 16 s, node failure 44 s, multi-port 3 s, gate TLS 1 min 20 s, adversarial replay 2 min 38 s. A new check or replay case can only be tried by running all of it, and nothing runs unless the user dispatches it by hand, so a broken commit is found late.

## What Changes

- **Dispatch form**: the Deploy Testbed "Run workflow" form keeps `workload`, `clear_first` and `dataplane`, and gets five checkboxes, all on by default: gate TLS, node failure, multi-port, warm zero, adversarial replay. A text field `adversarial_cases` runs only the replay cases whose name matches a pattern (empty = all). Running the form unchanged is a full run, as today.
- **Scripts**: `test/e2e/cluster_test.sh` can skip those check groups and prints which ones it skipped; `test/e2e/adversarial/run.sh --case <pattern>` runs only matching cases (identity setup and cleanup still run). The quick checks (Phase 69 inspection, API authorization, VIP and DNS, HTTP, cross-node path, backend churn) and the final status and stability check always run.
- **Push runs**: a push to `master` or to a milestone branch (not tags) starts a fast run automatically: workload whoami (WordPress was planned too; it joins with M232, see design D6a), quick checks only, and only the replay cases the push adds or changes. Docs-only pushes (`openspec/`, `design/`, `website/`, `*.md`) start nothing. Only the newest push runs; a push never cancels or replaces a full run started by hand.
- **WordPress as a deploy workload**: `examples/wordpress.cca` (2026-11-15; MariaDB, two WordPress instances sharing an uploads volume, two nginx with a config file, readiness probes, a gate) becomes `test/e2e/workloads/wordpress`. It has never run on a cluster, so it is deployed by hand on the testbed first and every problem found is recorded before it is automated.
- **Rule**: partial and push runs are for iterating; the Deploy Testbed run that closes a milestone is a full run. Written into `design/TESTING.md` and CLAUDE.md.

Out of scope:

- The E2E workflows on the LAN testbed (`e2e.yml`, `e2e-extended.yml`, `e2e-ha.yml`): they call `cluster_test.sh` with its defaults and keep running everything.
- Reusing a running cluster across runs (skipping VMs and Ansible): each run still deploys the new binary on a fresh cluster.
- Making Airflow faster; it stays a manual option for full runs and load.

## Capabilities

### New Capabilities

- `deploy-test-runs`: what a Deploy Testbed run executes, how a user narrows a manual run, what a push starts, and which run counts as the one that closes a milestone.

### Modified Capabilities

None.

## Impact

- `.github/workflows/deploy-testbed.yml` (inputs, push trigger, path filter, concurrency, replay case selection); `.github/workflows/health-check.yml` is checked for testbed sharing.
- `test/e2e/cluster_test.sh` (skip flags, skip report), `test/e2e/adversarial/run.sh` (`--case`).
- New `test/e2e/workloads/wordpress.cca` and `wordpress.workload`; images in the LAN registry.
- `design/TESTING.md`, CLAUDE.md (full run before a milestone closes), `milestones.md` (M231 row and phase).
- The testbed is busier: every code push to `master` or a milestone branch uses it for a few minutes.
