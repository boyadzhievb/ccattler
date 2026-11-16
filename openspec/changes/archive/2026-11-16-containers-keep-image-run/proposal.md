# Proposal

## Why

Milestone **M232 — Containers Get a tmpfs Over /run** (bug, milestones.md Phase 232). The first WordPress deploy (M231 task 3.2, 2026-11-16) found it: every CCattler container gets an empty 64 MB tmpfs mounted on `/run`, which hides whatever the image ships there. MariaDB's entrypoint needs `/run/mysqld` from its image, so it exits 1 about every second, and WordPress never gets a database. Any image with content under `/run` (or `/var/run`, usually a symlink to it) is affected; Airflow's Postgres only works because its entrypoint creates its directory itself.

Cause, verified in the code: `runtime/containerd.go` builds the container spec with `containerd.WithNewSpec`, which starts from containerd's default spec (containerd v2.4.2 `pkg/oci/mounts.go`: tmpfs on `/run`, `mode=755,size=65536k`), and neither the main-container builder (`buildOCISpecOptions`) nor the init-container builder (`ExecInit`) removes it.

How others do it: Docker and nerdctl start containers with the image's own `/run` (no tmpfs); Kubernetes (containerd CRI plugin) also removes the default `/run` mount (`oci.WithoutRunMount`) and mounts only what the pod asks for. CCattler is the odd one out; matching them is what users expect when they bring an image that runs under Docker.

## What Changes

- Main containers and init containers see the image's `/run` as shipped: the default tmpfs on `/run` is removed from both spec builders, in one shared place.
- Secret files (`/run/cca-secrets/<name>` and explicit mount paths, Flow 27) keep working: they stay read-only bind mounts from the node's tmpfs (`/run/ccattler/secrets/…`), so secret values still never reach disk; only the empty mount points are created in the container's writable layer.
- WordPress joins the Deploy Testbed push-run profile (`PUSH_WORKLOADS="whoami,wordpress"`), as planned in M231 (design D6a).
- Three observations from the same crash loop are verified, and each one that is confirmed becomes its own bug milestone (not fixed here): the instance stayed `running` with `Restarts: 0` and was restarted without back-off; its readiness probe targeted an old IP; each restart replaced the container log, so `cca logs` never showed the error.

Out of scope: fixing those three observations (separate milestones if confirmed); a DSL option to request a tmpfs on `/run` (nobody has asked; add it when a workload needs one); read-only root filesystems.

## Capabilities

### New Capabilities
- `container-filesystem`: what a container sees of its image's filesystem and which paths CCattler mounts over it.

### Modified Capabilities
- `deploy-test-runs`: the push run deploys WordPress as well as whoami.

## Impact

- Code: `runtime/containerd.go` (both spec builders), `test/e2e/select_deploy_checks.sh` (+ self-test).
- Tests: a new unit test on the built spec; WordPress deploy checks on the testbed (`deploy-run:wordpress`).
- Docs: `design/node-agent.md` (container mounts), Flow 27 (secret mount points no longer sit on a tmpfs inside the container), `design/TESTING.md` (push profile), M231's archived design D6a reference.
- Behaviour change for existing workloads: anything that relied on `/run` being empty and writable as tmpfs now writes to the container's writable layer instead (same as under Docker). No CCattler-provided workload depends on it.
