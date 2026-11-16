# Tasks

M231. Every group lands its own docs. Script changes are tried on the actual testbed before they are committed (project rule). Design decisions D1–D7 are in `design.md`.

## 0. Roadmap and baseline

- [ ] 0.1 `milestones.md`: M231 row (changelog text) and Phase 231 linking this change
- [ ] 0.2 Record the baseline in this file: run 43876417058 step times (598 s total), taken from the job's step timing

## 1. Testbed sharing

- [ ] 1.1 Verify how many runners carry the `deploy` label (`gh api repos/{owner}/{repo}/actions/runners`, or ask the user if the token cannot list them); record it; if more than one, D4 uses one shared group. Give `health-check.yml` the manual concurrency group. Verify the YAML with `actionlint` or a dispatch

## 2. Scripts

- [ ] 2.1 `cluster_test.sh --skip-checks <groups>` (D1): `gate-tls`, `node-failure`, `multiport`, `warmzero`; unknown group names are an error; `skipped check groups: …` printed before Step 5 and in the summary. Self-test `test/e2e/cluster_test_selftest.sh` checks the flag parsing and the report without a cluster. Verify on the deploy testbed by hand with `--skip-checks gate-tls`
- [ ] 2.2 `adversarial/run.sh --case <patterns>` (D3): glob on case names, comma-separated, zero matches fail, setup and cleanup always run. Self-test `test/e2e/adversarial/run_selftest.sh` (case selection without a cluster). Verify on the deploy testbed with `--case 'scale-*'` (2 cases)
- [ ] 2.3 `design/TESTING.md`: the check groups, the flags, what always runs

## 3. WordPress workload

- [ ] 3.1 `test/e2e/workloads/wordpress.cca` and `wordpress.workload` (D6): images from the LAN registry, 5 expected containers, HTTP check through the VIP and the gate; images pushed to the LAN registry
- [ ] 3.2 Deploy WordPress by hand on the deploy testbed (manual-first rule); record every problem found and its fix or follow-up here; `deploy-run:wordpress-manual`
- [ ] 3.3 Measure a run with workloads whoami + wordpress and the slow groups skipped; record the time; if WordPress is not stable, record why and keep it out of the push profile

## 4. Workflow

- [ ] 4.1 Dispatch form (D2): five checkboxes, `adversarial_cases`; mapping to `--skip-checks` and `--case`; job summary (D7). Verify `deploy-run:full-default` (form unchanged → summary `full run`), `deploy-run:skip-gate-tls`, `deploy-run:all-unticked`, `deploy-run:replay-pattern`
- [ ] 4.2 Push trigger (D4, D5): branches, `paths-ignore`, the two concurrency groups, the push profile, changed replay cases. Verify `deploy-run:push-fast`, `deploy-run:push-changed-case`, `deploy-run:push-docs-only`, `deploy-run:push-vs-manual`

## 5. Rule and review

- [ ] 5.1 CLAUDE.md and `design/TESTING.md`: the run that closes a milestone is a full run (summary says `full run`); partial and push runs are for iterating
- [ ] 5.2 Independent review: `code-reviewer` (workflow and scripts) and `security-audit` (self-hosted runner triggered on push); record every finding as fixed or rejected with one line here
- [ ] 5.3 A full Deploy Testbed run with the final workflow, summary `full run`, green

## Workflow follow-up

- Release with `scripts/release.sh M231 <version>`, archive this change (syncing `deploy-test-runs`), then rebase `m230-hierarchical-service-names` on `master` and resume M230.
