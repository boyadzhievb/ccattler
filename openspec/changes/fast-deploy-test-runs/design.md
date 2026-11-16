# Design

## Context

See proposal.md (Why) and `specs/deploy-test-runs/spec.md`. There is no runtime flow to change; the testing procedure lives in `design/TESTING.md`, which this change updates.

Current state, verified 2026-11-16:

- `.github/workflows/deploy-testbed.yml`: `workflow_dispatch` only, three inputs (`workload` choice, `clear_first`, `dataplane`), concurrency group `testbed-deploy` without cancel, one job on `[self-hosted, deploy]`. Steps: build, `cluster_test.sh --env deploy --skip-build --workload … --dataplane …` (422 s), `adversarial/run.sh --env deploy` (158 s).
- `.github/workflows/health-check.yml` also runs on the `deploy` runner, with no concurrency group.
- `cluster_test.sh` parses flags in a `case` loop (`--extended`, `--upgrade`, `--workload`, …). Check groups are steps 5.5–5.17, each gated by whether its workload is deployed (e.g. `WHOAMI_DEPLOYED`). Every run wipes etcd and starts a fresh cluster.
- `adversarial/run.sh` iterates all cases in `cases.yml` (`run_case`), skipping `known_failure` ones; identity setup before, cleanup after.
- `examples/wordpress.cca` is compiled by `integration/examples_test.go` but has never been deployed.

## Goals / Non-Goals

**Goals:**
- A user can try one new check or replay case in a few minutes.
- Every code push gets an automatic, fast signal.
- The full run stays the default and stays the gate for closing a milestone.

**Non-Goals:**
- Faster full runs (cluster reuse, parallel checks).
- Changing what any check verifies.

## Decisions

### D1. Skip flags, not an allow-list
`cluster_test.sh` gets `--skip-checks <groups>` (comma-separated: `gate-tls,node-failure,multiport,warmzero`). Each slow step's existing condition gets `&& ! check_skipped <group>`. A skip-list keeps every existing caller (E2E workflows, manual runs) running everything without change; an allow-list would make the default depend on every caller listing all groups. The script prints `skipped check groups: …` (or `none`) before Step 5 and in its final summary.

### D2. Checkboxes map to the skip-list in the workflow
The five booleans default to `true`; one workflow step turns the unticked ones into `--skip-checks` and decides whether to run the replay step. `adversarial_cases` (string, empty = all) becomes `run.sh --case`. Inputs: 3 existing + 5 + 1 = 9, under the long-standing limit of 10.

### D3. Replay case filter by shell glob on the case name
`run.sh --case <pattern>` matches case names with a bash glob (`[[ $name == $pattern ]]`); several patterns comma-separated. Zero matches is an error: a filter that tests nothing must not report success. Identity setup and cleanup always run.

### D4. Push runs: one workflow, two triggers, two concurrency groups
The same workflow gets an `on: push` trigger with `branches: [master, 'm[0-9]*-*']` and `paths-ignore` for docs. Concurrency becomes `group: ${{ github.event_name == 'push' && 'testbed-deploy-push' || 'testbed-deploy' }}` with `cancel-in-progress: ${{ github.event_name == 'push' }}`: push runs replace each other, manual runs never get cancelled, and they never share a group, so a push cannot replace a pending manual run. Both groups use the same runner; one runner runs one job at a time, so they never share the testbed concurrently — verified in task 1.1 (if there is more than one `deploy` runner, both triggers use one group without cancel instead).
Alternative: a separate workflow file for push runs — rejected: the steps would be copied and drift (CLAUDE.md duplication rule).

### D5. Changed replay cases from a YAML diff
For a push, the workflow takes `cases.yml` at `github.event.before` and at `HEAD`, and with `yq` lists case names that are new or whose content changed; that list becomes `--case`. Empty list → the replay step is skipped and the summary says so. A force push or a first push (`before` is all zeros) falls back to all cases.

### D6. WordPress first by hand, then automated
`test/e2e/workloads/wordpress.cca` is the example with the LAN registry image names; `wordpress.workload` lists the three images, 5 expected containers, and an HTTP check through the VIP and the gate (WordPress answers its install page). Task 3.2 deploys it by hand on the deploy testbed (manual-first rule) before it enters the push profile. Expected risks there: the shared uploads volume on NFS, MariaDB start time, the nginx configuration file delivery.

### D7. The run summary says full or partial
The workflow writes to `$GITHUB_STEP_SUMMARY`: trigger, workloads, skipped groups, replay filter, and `full run` only when nothing is skipped and the replay ran all cases. Milestone checklists cite only runs whose summary says `full run`.

## Risks / Trade-offs

- [Testbed busier, a manual run waits behind a push run] → push runs are short and only the newest runs; the user can still cancel one from the Actions page.
- [WordPress makes push runs slower or flaky] → measured in task 3.3; if it is unstable, it stays manual until fixed and the push profile uses whoami alone (recorded, not silent).
- [A push run deploys unmerged branch code to the shared testbed] → intended (milestone branches); the next run redeploys anyway.
- [Self-hosted runner and `push`] → pushes come only from the repository's own branches; fork pull requests do not trigger `push`.
- [Health check runs at the same time] → it cannot: task 1.1 found exactly one `deploy` runner (m4800), which runs one job at a time. `health-check.yml` deliberately gets no concurrency group: GitHub keeps only the newest pending run per group, so an hourly health check in the manual group could replace a full run waiting behind it (changed during task 1.1).

## Migration Plan

No data. Roll back by reverting the workflow file; the new script flags are optional and default to today's behaviour.
