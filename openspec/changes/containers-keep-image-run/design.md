# Design

## Context

- `ContainerdRuntime.Start` creates containers with `containerd.WithNewSpec(buildOCISpecOptions(…)...)`. `WithNewSpec` first fills containerd's default Linux spec (v2.4.2 `pkg/oci/mounts.go`: `/proc`, `/dev`, `/dev/pts`, `/dev/shm`, `/dev/mqueue`, `/sys`, and a tmpfs on `/run` with `nosuid,strictatime,mode=755,size=65536k`), then applies our options. None of them touches `/run`, so the tmpfs stays.
- `ContainerdRuntime.ExecInit` builds its own option list inline (image config, `bash -c`, host network, env, hostname, bind mounts) and keeps the same tmpfs.
- Secrets (Flow 27, [design/runtime-invariant-flows.md#flow-27-secret-delivery-m174](../../../design/runtime-invariant-flows.md)): `buildSecretOCIMounts` bind-mounts files from `/run/ccattler/secrets/<instance>/…` on the node (a memory filesystem, checked by `isMemoryFilesystem`) to `/run/cca-secrets/<name>` and explicit paths, `rbind,ro,nosuid,nodev,noexec`. Inside the container these mount points currently sit on the tmpfs; after the change runc creates them in the container's writable layer, as Docker does for `/run/secrets`. Only empty mount-point files and directories are created there, never secret values.
- Call graph: `INSTANCE_LIFECYCLE_GRAPH.md` covers `runtime.ExecInit`; `Start`'s spec building is not in a graph yet.

## Goals / Non-Goals

**Goals:**
- No mount on `/run` in either spec builder, decided in one place.
- The spec builders become testable without containerd, so the rule has a unit test.
- WordPress deploys on the testbed and joins the push-run profile.
- The three crash-loop observations are checked against the code and the testbed, and each confirmed one becomes a bug milestone.

**Non-Goals:**
- Fixing restart counting/back-off, probe targets or log retention (own milestones if confirmed).
- A DSL option to request a tmpfs on `/run`, read-only root filesystems.

## Decisions

### D1. Remove the default mount with containerd's `oci.WithoutRunMount`
One shared helper returns the options every CCattler container gets regardless of its spec (today only `oci.WithoutRunMount`); both `buildOCISpecOptions` and the init-container builder start from it. Same option the containerd CRI plugin uses for Kubernetes pods. Alternatives: build the spec from scratch without containerd defaults (rejected: we would own `/proc`, `/dev`, `/sys` and their options for no benefit); keep the tmpfs and copy the image's `/run` into it (rejected: two copies, size limit, diverges from Docker).

### D2. Image config stays first, outside the testable builders
`oci.WithImageConfig(image)` needs a real image and must run before our env/args. `Start` and `ExecInit` prepend it; `buildOCISpecOptions(spec, netnsPath)` and a new `buildInitContainerSpecOptions(execSpec, initContainerName)` return everything else. Unit tests apply those options with `oci.GenerateSpec` on a fake container (no containerd, no image) and inspect `Spec.Mounts`. Tests: `TestContainerSpecKeepsImageRun` (with a granted secret), `TestInitContainerSpecKeepsImageRun`.

### D3. WordPress joins the push profile
`PUSH_WORKLOADS="whoami,wordpress"` in `select_deploy_checks.sh` and its self-test, once a deploy run shows WordPress stable. If the push run then takes much longer than the M231 measurement (149 s deploy step), record the time; the profile stays as specified unless the user decides otherwise.

### D4. The three observations are investigated, not fixed
For each: read the code path (agent restart accounting, probe target resolution, log file handling in `runtime/containerd.go` `Logs`/`defaultLogPath`), reproduce on the testbed with an image that exits at once if needed, and record confirmed/not confirmed with evidence in tasks.md. A confirmed one gets a row and phase in milestones.md (bugs first).

## Risks / Trade-offs

Failure cases and what happens:
- **Image without a `/run` directory** (scratch images such as whoami): runc creates `/run/cca-secrets/…` mount points in the writable layer; the container starts as before. Covered by the WordPress deploy only if a secret-granted container uses a scratch image, so the unit test asserts the mount list and the testbed run checks whoami still passes.
- **`/run` in the image is a symlink or read-only**: runc resolves mount destinations inside the rootfs (securejoin), so a symlink cannot point a secret mount outside the container; a read-only image root is not supported today and does not change.
- **Workloads that write a lot to `/run`**: writes go to the writable layer (disk) instead of a 64 MB memory tmpfs: no size cap and not in memory. Same as Docker; noted in node-agent.md.
- **Containers started before the upgrade** keep their old spec until they are recreated; nothing is restarted by the upgrade itself.
- **Secrets**: values stay on the node's memory filesystem and are bind-mounted read-only; nothing secret lands in the writable layer. Reviewed by `security-audit` (secrets path).
