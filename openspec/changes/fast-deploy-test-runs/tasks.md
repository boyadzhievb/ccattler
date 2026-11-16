# Tasks

M231. Every group lands its own docs. Script changes are tried on the actual testbed before they are committed (project rule). Design decisions D1–D7 are in `design.md`.

## 0. Roadmap and baseline

- [x] 0.1 `milestones.md`: M231 row (changelog text) and Phase 231 linking this change — 2026-11-16: row after M230, Phase 231 before Phase 230
- [x] 0.2 Record the baseline in this file: run 43876417058 step times (598 s total), taken from the job's step timing — 2026-11-16, from `gh api …/actions/jobs/133330557364` and the deploy step's log timestamps: build 5 s; deploy step 422 s, of which VMs + Ansible ~2 min (PLAY RECAP 13:32:04), airflow 2 min 12 s, whoami + whoami-multiport 16 s, gate-tls deploy 22 s, Phase 69 → HTTP checks ~15 s, backend churn 16 s, node failure 44 s, multi-port 3 s, gate TLS T1–T18 1 min 20 s, final status/stability the rest; adversarial replay 158 s (18 cases; its "32 passed" counts step checks); job total 598 s

## 1. Testbed sharing

- [x] 1.1 Verify how many runners carry the `deploy` label (`gh api repos/{owner}/{repo}/actions/runners`, or ask the user if the token cannot list them); record it; if more than one, D4 uses one shared group. Give `health-check.yml` the manual concurrency group. Verify the YAML with `actionlint` or a dispatch — 2026-11-16: runners: `m4800` (online; self-hosted, Linux, X64, deploy, m4800), `y510p` (online; e2e, ha), `vmware-e2e-runner` (offline, no deploy label). One `deploy` runner, so D4's two groups stand. `health-check.yml` left without a group, on purpose (design Risks updated): the single runner already serializes it with deploy runs, and in the manual group its hourly runs could replace a pending full run. No workflow change in this task

## 2. Scripts

- [x] 2.1 `cluster_test.sh --skip-checks <groups>` (D1): `gate-tls`, `node-failure`, `multiport`, `warmzero`; unknown group names are an error; `skipped check groups: …` printed before Step 5 and in the summary. Self-test `test/e2e/cluster_test_selftest.sh` checks the flag parsing and the report without a cluster. Verify on the deploy testbed by hand with `--skip-checks gate-tls` — 2026-11-16: parsing, validation and the report in `test/e2e/check_groups.sh` (sourced), self-test named `test/e2e/check_groups_selftest.sh` (8 checks pass, shellcheck clean); `cluster_test.sh` gates the five slow blocks (node failure, multi-port data plane and cross-node, warm zero, gate TLS) on `check_skipped`; an unknown name exits 1 before anything is deployed, in both flag forms; shellcheck warnings unchanged (6 before, 6 after). By hand on the deploy testbed as bojan (`--workload whoami,gate-tls --skip-checks gate-tls,node-failure`): `skipped check groups: gate-tls,node-failure` logged before the checks and in the final status, `Skipping gate TLS checks — --skip-checks gate-tls`, quick checks and churn passed, `TEST PASSED`, 3 min (17:08:52–17:11:51); runner's files restored afterwards
- [x] 2.2 `adversarial/run.sh --case <patterns>` (D3): glob on case names, comma-separated, zero matches fail, setup and cleanup always run. Self-test `test/e2e/adversarial/run_selftest.sh` (case selection without a cluster). Verify on the deploy testbed with `--case 'scale-*'` (2 cases) — 2026-11-16: selection runs before the cluster is touched; `--list-cases` prints it (used by the self-test: 5 checks pass, shellcheck clean). On the testbed: `Running 2 of 18 adversarial cases (--case 'scale-*')`, `Results: 7 passed, 0 failed, 0 skipped` (7 step checks), exit 0; runner's files restored
- [x] 2.3 `design/TESTING.md`: the check groups, the flags, what always runs — 2026-11-16: "Check groups and narrowed runs (M231)" under the deploy cluster section: what always runs, the four groups with their measured times, the skip report, `--case`, `--list-cases`, the two self-tests

## 3. WordPress workload

- [x] 3.1 `test/e2e/workloads/wordpress.cca` and `wordpress.workload` (D6): images from the LAN registry, 5 expected containers, HTTP check through the VIP and the gate; images pushed to the LAN registry — 2026-11-16: `wordpress.cca` = the example with `{{ .registry }}` images and gate host `wordpress.home.arpa` (renders to `192.168.124.1:5000/…` with `deploy.ccv`); `wordpress/nginx.conf` copied to the control node by the manifest's `workload_pre_apply` hook; 10 secrets generated (hex); `test/e2e/wordpress_checks.sh` (Step 5.18, runs whenever the workload is deployed: 200 or the fresh-install 302 through the nginx VIP and through the gate on an agent node's :443, 300 s for the first database start). `mariadb:11` and `wordpress:6-apache` added to `scripts/setup-deploy-host.sh` and pushed to the registry (`nginx:1.27` was there). `cca lint` cannot check templated workloads (it does not render `{{ }}`; same for `whoami.cca`), so the file was checked with `cca apply --dry-run --values deploy.ccv`
- [ ] 3.2 Deploy WordPress by hand on the deploy testbed (manual-first rule); record every problem found and its fix or follow-up here; `deploy-run:wordpress-manual`
- [ ] 3.3 Measure a run with workloads whoami + wordpress and the slow groups skipped; record the time; if WordPress is not stable, record why and keep it out of the push profile

## 4. Workflow

- [ ] 4.1 Dispatch form (D2): five checkboxes, `adversarial_cases`; mapping to `--skip-checks` and `--case`; job summary (D7). Verify `deploy-run:full-default` (form unchanged → summary `full run`), `deploy-run:skip-gate-tls`, `deploy-run:all-unticked`, `deploy-run:replay-pattern`
- [ ] 4.2 Push trigger (D4, D5): branches, `paths-ignore`, the two concurrency groups, the push profile, changed replay cases. Verify `deploy-run:push-fast`, `deploy-run:push-changed-case`, `deploy-run:push-docs-only`, `deploy-run:push-vs-manual`

## 5. Rule and review

- [x] 5.1 CLAUDE.md and `design/TESTING.md`: the run that closes a milestone is a full run (summary says `full run`); partial and push runs are for iterating — 2026-11-16: CLAUDE.md "Independent Review" gets the rule next to "Only executed things count"; `TESTING.md` "Deploy Testbed runs: manual, push, full" (what each trigger runs, the definition of full, which run may close a milestone)
- [ ] 5.2 Independent review: `code-reviewer` (workflow and scripts) and `security-audit` (self-hosted runner triggered on push); record every finding as fixed or rejected with one line here
- [ ] 5.3 A full Deploy Testbed run with the final workflow, summary `full run`, green

## Workflow follow-up

- Release with `scripts/release.sh M231 <version>`, archive this change (syncing `deploy-test-runs`), then rebase `m230-hierarchical-service-names` on `master` and resume M230.
