# Spec Delta

## Purpose

Defines what a Deploy Testbed run executes, how a user narrows a manual run, what a push starts automatically, and which run counts as the one that closes a milestone, so new checks can be tried quickly without weakening the full verification before a release.

## ADDED Requirements

### Requirement: A manual run is full unless narrowed
A Deploy Testbed run started from the form with no option changed SHALL run every check group and every replay case. Each slow group (gate TLS, node failure, multi-port, warm zero, adversarial replay) SHALL have its own checkbox, on by default.

#### Scenario: Unchanged form
- **WHEN** a user starts Deploy Testbed without changing any input
- **THEN** the run log lists every check group as run and none as skipped, and the replay runs all cases
- Check: `deploy-run:full-default` (Deploy Testbed run recorded in the change's tasks)

#### Scenario: Gate TLS unticked
- **WHEN** a user unticks gate TLS
- **THEN** the gate TLS checks do not run, the log prints `skipped check groups: gate-tls`, and every other group runs
- Check: `deploy-run:skip-gate-tls`, `test/e2e/cluster_test_selftest.sh` (skip report)

### Requirement: The quick checks always run
Phase 69 inspection, API authorization, VIP and DNS, HTTP through the VIP, the cross-node path, backend churn and the final status and stability check SHALL run in every Deploy Testbed run, manual or push.

#### Scenario: Everything unticked
- **WHEN** a user unticks every checkbox
- **THEN** the quick checks and the stability check still run, and the log names the skipped groups
- Check: `deploy-run:all-unticked`

### Requirement: Replay cases can be selected by name
The replay SHALL run only the cases whose name matches the given pattern when one is given, and all cases otherwise. Identity setup and cleanup SHALL run either way, and a pattern that matches no case SHALL fail the run instead of passing with nothing tested.

#### Scenario: One family of cases
- **WHEN** a run is started with `adversarial_cases = scale-*`
- **THEN** only `scale-undeclared-service` and `scale-upper-bound` run, and the result line counts only them
- Check: `deploy-run:replay-pattern`, `test/e2e/adversarial/run_selftest.sh`

#### Scenario: Pattern matches nothing
- **WHEN** the pattern matches no case name
- **THEN** the replay exits non-zero naming the pattern
- Check: `test/e2e/adversarial/run_selftest.sh`

### Requirement: A code push starts a fast run
A push to `master` or to a milestone branch that changes anything outside `openspec/`, `design/`, `website/` and Markdown files SHALL start a Deploy Testbed run with the workloads whoami and WordPress, the quick checks only, and the replay cases the push adds or changes. Docs-only pushes and tags SHALL start nothing. Of several pending push runs only the newest SHALL run, and a push run SHALL never cancel or replace a run started by hand.

#### Scenario: Code push
- **WHEN** a commit changing Go code is pushed to `master`
- **THEN** a Deploy Testbed run starts with workloads whoami and WordPress, logs `skipped check groups: gate-tls,node-failure,multiport,warmzero`, and runs no replay case unless the push changed `cases.yml`
- Check: `deploy-run:push-fast`

#### Scenario: Push with a changed replay case
- **WHEN** a pushed commit adds the case `scale-upper-bound` to `test/e2e/adversarial/cases.yml`
- **THEN** the push run replays exactly that case
- Check: `deploy-run:push-changed-case`

#### Scenario: Docs-only push
- **WHEN** a push changes only files under `openspec/` and `design/`
- **THEN** no Deploy Testbed run starts
- Check: `deploy-run:push-docs-only`

#### Scenario: Push while a manual full run is in progress
- **GIVEN** a manual full run is running
- **WHEN** two code pushes follow
- **THEN** the full run completes, the older push run is cancelled or never starts, and the newest push run runs after the full run
- Check: `deploy-run:push-vs-manual`

### Requirement: WordPress is a deploy workload
The deploy test SHALL be able to deploy WordPress (MariaDB, two WordPress instances sharing an uploads volume, two nginx with a configuration file, a gate) and SHALL check that the site answers through its service VIP and through the gate.

#### Scenario: WordPress deployed
- **WHEN** a run includes the workload `wordpress`
- **THEN** its five instances reach running and stay stable, and an HTTP request through the VIP and through the gate gets a WordPress page
- Check: `deploy-run:wordpress-manual` (first, by hand), then `deploy-run:push-fast`

### Requirement: A milestone closes on a full run
The Deploy Testbed run that closes a milestone SHALL be a full run: at least the default workloads (airflow, whoami, whoami-multiport, gate-tls), no check group skipped, and every replay case. A narrowed or push run SHALL NOT be recorded as that run.

#### Scenario: Fewer workloads than the default
- **WHEN** a manual run is started with workload `whoami` and every box ticked
- **THEN** its summary says `partial run`, because the gate TLS and multi-port checks had no workload to run against
- Check: `test/e2e/select_deploy_checks_selftest.sh`

#### Scenario: Run summary says whether it was full
- **WHEN** any Deploy Testbed run finishes
- **THEN** its job summary states `full run` or `partial run` and lists the skipped groups and the replay filter
- Check: `deploy-run:full-default`, `deploy-run:push-fast`
