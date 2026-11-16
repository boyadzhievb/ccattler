# Spec Delta

## MODIFIED Requirements

### Requirement: A code push starts a fast run
A push to `master` or to a milestone branch that changes anything outside `openspec/`, `design/`, `website/` and Markdown files SHALL start a Deploy Testbed run with the workloads whoami and WordPress, the quick checks only, and the replay cases the push adds or changes. Docs-only pushes and tags SHALL start nothing. Of several pending push runs only the newest SHALL run, and a push run SHALL never cancel or replace a run started by hand.

#### Scenario: Code push
- **WHEN** a commit changing Go code is pushed to `master`
- **THEN** a Deploy Testbed run starts with workloads whoami and WordPress, logs `skipped check groups: gate-tls,node-failure,multiport,warmzero`, runs the WordPress checks, and runs no replay case unless the push changed `cases.yml`
- Check: `deploy-run:push-fast`, selection self-test `push uses whoami and wordpress`

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
