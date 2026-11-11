# Config & Secrets

Config and secrets are two separate mechanisms. Controllers reason about references and desired state. The node agent is the only component that materializes config and secrets into workloads.

## Config

Config is declarative desired state, not imperative "set this environment variable":

```
config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "8080"

    file "/etc/api/config.yaml" = "..."
}

service api {
    image "my-api:v3"
    config api
}
```

The fact store holds config facts (`desired/service/api/config/...`), not rendered environment variables. The scheduler and controllers don't care how config becomes runtime input — that's the node agent's job.

Config supports two delivery modes:
- **Environment variables** — `env "KEY" = "value"`
- **Config files** — `file "/path" = "content"`

## Secrets

Secrets are references, never values. The fact store holds grants, not plaintext:

```
secret database.password

service api {
    secret database.password {
        mount "/run/secrets/database-password"
    }
}
```

Stored as:
```
secret(database.password)              — secret exists
secret_grant(api, database.password)   — api may access it
```

Never:
```
secret(database.password, "super-secret-password")   — WRONG
```

Actual secret values live in the encrypted secret subsystem (envelope encryption with KMS). The scheduler and network controller never see plaintext secrets.

**File-mounted secrets are preferred over environment variables:**
- Avoids exposing secrets through environment inspection
- Avoids accidental logging of environment variables
- Makes rotation easier (overwrite file, signal process)
- Works with applications that already consume secret files
- Gives the agent control over permissions (`0400`, memory-backed, ephemeral)

## Materialization Boundary

```
             CONTROL PLANE
                   │
      ┌────────────┴────────────┐
      │                         │
 desired config           secret reference
      │                         │
      └────────────┬────────────┘
                   │
             Fact Store
                   │
                   ▼
              Node Agent
                   │
         ┌─────────┴─────────┐
         │                   │
    config resolver     secret resolver
         │                   │
         └─────────┬─────────┘
                   ▼
              Container
```

Don't put rendered config into the fact store. Store `desired(instance, running)` + `config(api, ...)` + `secret_grant(api, database.password)`. The agent resolves effective configuration at reconciliation time.

## Secret Lifecycle

Secret access is tied to container lifecycle:

```
instance api-7 assigned to node-3
  → node-3 obtains authorized secret copy
  → secret materialized locally
  → container starts
  → container stops
  → secret material removed
```

If the container moves from node-3 to node-5, node-5 gets a fresh authorized copy. The old node removes its copy. This fits the zero-trust + short-lived credentials model.

## Connection URL Assembly

See [runtime-invariant-flows.md](runtime-invariant-flows.md) section 9 for the detailed compiler → agent → container flow.

---

## Implementation status vs this document (audit 2026-11-03)

The sections above describe the intended model. The code does not enforce most of it. M174 closes these gaps; until then, treat the "Actual" column as the security model.

| Documented | Actual (code as of `0dea8fa`) |
|---|---|
| Master key never leaves the control plane; "the scheduler and network controller never see plaintext" | Every node receives the cluster master key in the enrollment response (`api/server.go` `handleEnroll` → `SecretMasterKey`), `cca join` writes it to `/etc/ccattler/secret-master.key`, and the agent decrypts locally (`agent.NewLocalSecretProvider`). With raw etcd access (threat model T5), any node can decrypt every secret of every tenant, and every cloud credential under `credentials/`. |
| A node obtains only secrets for instances assigned to it | The agent checks only the service grant (`desired/service/{svc}/secret/{name}`), not that an instance of that service is placed on this node. It reads ciphertext straight from etcd. |
| Master key rotation without data loss | `POST /api/secret/rotate` re-wraps DEKs under a new key held only in memory: the new key is not written to disk, so a restart loads the old key and every re-wrapped secret becomes unreadable. Other API replicas and all agents keep the old key. The field is updated without a lock. |
| Envelope encryption | Envelope = wrapped DEK + AES-GCM ciphertext, with no key version and no binding to the secret name: after an interrupted rotation a reader cannot tell which key wrapped a secret, and an etcd writer can move one secret's envelope under another secret's name. |
| Secret files are `0400`, memory-backed, ephemeral, removed when the container stops | Decrypted secrets and connection passwords are written as config files under `/var/lib/ccattler/logs/{instance}/config/` with mode `0755` on disk (`runtime/containerd.go`), removed only with the log directory. |
| (not documented) gate TLS keys | `RouteTLSController` writes generated private keys as plain facts at `derived/route/{name}/tls/key`; manual keys from DSL are plain facts at `desired/route/{name}/tls/key`. M173 hides them from the state/watch API, but they are plaintext in etcd. |
| (not documented) agent without a key file | `loadOrGenerateSecretMasterKey` silently creates a new random key, so decryption fails later with no clear cause. |
| (not documented) agent secret API | `SecretAPIServer` (agent, `:9772`) serves `GET /v1/secrets/{name}` to containers by decrypting locally with the master key; the per-instance bearer token is passed to the container as the `CCA_SECRET_TOKEN` environment variable. |
| (not documented) cloud credentials | The `node-agent` store role reads `credentials/` directly. |

Connection URLs are already delivered as files (`/run/connections/{name}`, Flow 9), and `env` values are literal: no code path places a secret value in a container environment variable. The only credential in the environment is `CCA_SECRET_TOKEN`.

## M174 design: secrets architecture

**Status:** direction approved 2026-11-05, open questions A–C decided the same day. Second review 2026-11-06 (decisions R1–R8 below) changed the root key and the migration. This section, [Flow 27](runtime-invariant-flows.md#flow-27-secret-delivery-m174) and the image pull flows ([14](runtime-invariant-flows.md#14-image-pull-lifecycle), [22](runtime-invariant-flows.md#22-image-pull-secrets)) are the specification for M174.

### Review decisions (2026-11-05)

| # | Decision | Outcome |
|---|---|---|
| 1 | Root key → KEK → DEK hierarchy | Approved. Root key separate from the CA key. |
| 2 | Envelope v2 bound to its store key (AAD) | Approved. Store keys are the tenant-scoped canonical names. |
| 3 | Rotation as a fact, done by a leader-elected controller | Approved, with create-only KEK writes, revision guards, bounded batches, idempotent resume, grace-period retirement. |
| 4 | Control plane decrypts, delivers plaintext to agents over mTLS | Approved. Failure behavior must be explicit (see "Availability"). Per-node envelope encryption is not part of M174. |
| 5 | Passwords in environment variables | Changed: files are the only delivery mechanism. Environment delivery is not added; if ever added, only as an explicit opt-in with a documented warning. |
| 6 | `tls { key secret "name" }` | Approved. Inline PEM stays for compatibility and is sealed on apply; Git-managed configs use secret references. |
| 7 | Control plane → agents → rotation → remove worker keys | Approved, with a strict cutover gate before rotation (step 3 is the rollback boundary). |
| A | Services without an owner | Built-in, reserved tenant `default`; not a global namespace. |
| B | Gate TLS key distribution | Every current gate-serving agent node receives the keys of the gates it serves; documented residual risk. |
| C | Legacy secret API token (`CCA_SECRET_TOKEN`) | Kept for one release as a per-service opt-in, deprecated, served through control-plane delivery; removed in the release after M174. |

### Second review decisions (2026-11-06)

| # | Item | Decision |
|---|---|---|
| R1 | Root key | Freshly generated. The existing `secret-master.key` was copied to every worker and is treated as compromised: it becomes the **legacy key**, used only on the control plane to read v1 envelopes during migration, and never wraps a KEK or a v2 envelope. |
| R2 | Secret ownership and shares | CLI operations, not a new DSL construct: `cca secret set --tenant/--cluster`, `cca secret share/unshare`. The DSL keeps declaring references. |
| R3 | Write scope of `default` | `secret.write` at `team/default` (cluster admins have it through their cluster grant). |
| R4 | Gate keys during API outages | Cached in tmpfs (`/run/ccattler/gate-keys/`), not process memory only. |
| R5 | Retry after refused delivery | Event-driven on placement/grant/share changes, plus a 5-minute fallback; refusal audit records rate-limited. |
| R6 | Image pull ordering | Secret delivery precedes `pulling`, also when the image is cached. |
| R7 | Cutover verification | Measurable conditions only (version reports, persisted `legacy_v1_envelope_count = 0`, migration status, no plaintext gate key facts, no legacy key file on workers). |
| R8 | Naming sequence | v2 envelopes first bound to the legacy store key, later re-encrypted under the canonical name by `migrate-names`. |

### Goals

1. Master key isolation: node agents never hold a key that can decrypt secrets.
2. Scoped delivery: an agent receives a secret only for an instance placed on its node whose service holds the grant.
3. Durable keyring: key versions survive restarts and are the same on every API replica.
4. Crash-safe rotation: an interrupted rotation leaves every secret readable and resumes.
5. Gate TLS private keys are encrypted at rest and never plain facts.
6. Enrollment no longer distributes key material.
7. Secrets belong to a tenant; another tenant's workloads cannot use them without an explicit share.

Non-goals: protecting secrets from a compromised control plane (it holds the keys by design); store access isolation for agents (M181); per-node envelope encryption (a possible later availability improvement). M174 makes secrets confidential even while agents can still read etcd, because ciphertext without a key is useless.

**Security boundary.** A compromised node can obtain the plaintext currently authorized for instances placed on it, and the private keys of the gates it serves (decision B). It cannot decrypt other secrets in etcd, and its node certificate does not give it another tenant's secrets or any key-encryption key.

**Limit until M181 (found during implementation, 2026-11-06).** Delivery authorizes from store facts (placement, `observed/instance/{id}/service`, grants). Agents still have unrestricted etcd write access until store access isolation (M181, threat model T5), so a compromised node can forge those facts and obtain any secret through delivery. M174 removes the key from workers (stolen key files, etcd read access and etcd backups no longer expose secrets); the delivery checks become binding once M181 restricts agent writes. M181 must also make the instance → service mapping a control-plane-only fact.

### Key hierarchy

```
root key (fresh, control-plane only: /etc/ccattler/secret-root.key, or a KMS key)
  └── wraps KEK versions, stored in etcd: keyring/kek/{version}
        └── wraps a per-secret DEK, stored in the envelope (v2)
              └── encrypts the secret value (AES-256-GCM, AAD = store key)

legacy key (old secret-master.key, control-plane only, migration only)
  └── opens v1 envelopes; wraps nothing new
```

- **Root key (R1):** a fresh, cryptographically random 32-byte key generated on the control plane, independent of the CA key and of the legacy key. File `/etc/ccattler/secret-root.key`, mode `0400`, owned by root. Ansible's control-plane role provisions the same file to every HA replica through the same trusted path as the CA material. It is never sent to agents. With `--kms-provider`, the root key is a KMS key and never touches disk.
- **Legacy key (R1):** the existing `secret-master.key` is copied to every worker today, and agents can still read etcd until M181, so it must never protect anything new. On the control plane it is loaded only to open v1 envelopes; it never wraps a KEK, a DEK of a v2 envelope or `keyring/root_check`. It is deleted from the control plane once the migration reports `legacy_v1_envelope_count = 0` (see "Migration").
- **Startup checks:** a missing, unreadable, wrongly sized or group/world-readable root key file is a startup error (no silent generation). Each replica opens `keyring/root_check` (a fixed value sealed with the root key, written when the keyring is created); failing to open it is a startup error, so a replica with a different root key never serves. A root key byte-identical to the legacy key is a startup error.
- **Limitation:** whoever copied the legacy key and v1 ciphertext before migration can still decrypt those historical copies. Migration makes the legacy key useless against the current store; it cannot revoke what was already exfiltrated.
- **Keyring in etcd:** `keyring/kek/{version}` = KEK wrapped by the root key, `keyring/active` = active version, `keyring/kek/{version}/state` = `active | retiring | retired`. Every API replica reads and watches it, so all replicas use the same versions without copying files.
- **Envelope v2:** `magic | format=2 | kek_version | wrapped_dek | nonce | ciphertext`. The AES-GCM additional data is the canonical store key (e.g. `secrets/tenant/payments/database.password`), so an envelope only opens under its own name. Moving or renaming a secret is a re-encryption with a new DEK, not a re-wrap. Envelopes without the magic are v1 (DEK wrapped directly by the legacy key, no AAD); the control plane reads them until migration re-encrypts them, and never writes a new one.

### Tenant-scoped secret names

Canonical store keys:

```
secrets/tenant/{tenant}/{name}     tenant secret, e.g. secrets/tenant/payments/database.password
secrets/cluster/{name}             cluster secret, e.g. secrets/cluster/registry-credentials
secrets/route/{gate}/tls/key       sealed gate TLS key (written by the system, not by users)
```

Reserved name prefix (since M176): secret names starting with `route/` belong to the system. `/api/secret` (and so `cca secret set/get/list/delete`) hides and refuses them, the compiler rejects them as service secrets and as `key secret`, and they are never delivered to instances. A user secret created under `route/…` before M176 stays in the store but is unreachable: re-create it under another name before upgrading, and update the configurations that reference it. The reservation is temporary: it exists because user and system secrets share the flat `secrets/{name}` namespace today, and is dropped in M179 once user secrets live under `secrets/tenant/…` / `secrets/cluster/…` and `migrate-names` has moved every legacy key.

Rules:

- **Ownership (decision A):** services and secrets resolve their tenant in this order: (1) explicit `owner`; (2) the tenant inferred from a hierarchical name such as `payments/api`; (3) the built-in tenant `default`. `default` is reserved (no user-declared `tenant default`) and is an ordinary tenant for isolation, not a global namespace: flat, unowned services use `secrets/tenant/default/…` and cannot reach other tenants' secrets.
- **Secret ownership is set by the CLI (R2).** The DSL declares references and consumption; values, ownership and shares are administered with `cca secret` (full syntax in [cli-reference.md](cli-reference.md#secrets-m174)):
  ```
  cca secret set database.password --tenant payments     # secrets/tenant/payments/database.password
  cca secret set registry.credentials --cluster          # secrets/cluster/registry.credentials
  cca secret set legacy-app.password                     # secrets/tenant/default/legacy-app.password
  cca secret share database.password --tenant payments --with-tenant frontend
  cca secret unshare database.password --tenant payments --with-tenant frontend
  ```
  Omitting both flags means `default`; `--tenant` and `--cluster` are mutually exclusive. The value is read from stdin or `--from-file`, never from the command line.
- **Write permissions (R3):**

  | Secret | Create, update, delete, share, unshare |
  |---|---|
  | `default` tenant | `secret.write` at `team/default` |
  | tenant `payments` | `secret.write` at `team/payments` |
  | cluster | `secret.write` at `cluster` |

  A cluster admin holds all of these through the cluster grant.
- **Enforcing ownership:** a production cluster can require an explicit owner (or hierarchical name) for every service through an admission policy ([multi-tenancy.md](multi-tenancy.md) "Policy Gates"), rejecting services that would fall into `default`. Off by default, so existing configurations keep working.
- **Resolution:** in a service, an unqualified reference (`secret database.password`) resolves to the service's own tenant: `secrets/tenant/{service tenant}/database.password`. Qualified references are explicit: `secret cluster/registry-credentials`, `secret tenant/platform/web-gate-key`.
- **No implicit visibility:** a reference to another tenant's secret is valid only if the owner created a share (`cca secret share` → `desired/secret_share/payments/database.password/frontend`; `unshare` deletes it). Creating a share needs `secret.write` in the owner's scope; applying the consuming reference needs `secret.use` in the consumer's scope. Cluster secrets need a grant at `cluster` scope for the applying user.
- **Checks in two places:** the compiler/apply path rejects unresolvable or unshared references with a clear error; the delivery endpoint re-checks the grant and share on every request, so a removed share stops delivery on the next start.
- **Grant facts** carry the canonical name: `desired/service/{svc}/secret/{canonical name}`; connection auto-grants (Flow 9) do the same.

**Migrating unqualified names.** Existing `secrets/{name}` keys are not reinterpreted as tenant or cluster secrets. `cca secret migrate-names --plan` lists every legacy secret with the services and tenants that use it and writes a mapping file; the operator edits it and runs `cca secret migrate-names --apply mapping.ccv`. Every legacy secret must have an explicit target (`tenant/{t}/{name}` or `cluster/{name}`); apply refuses while any is unmapped. Apply re-encrypts each value under its new key (AAD changes) and rewrites grants and connection references in one transaction per secret. Until mapped, a legacy secret keeps today's behavior (only services with an existing grant can use it) and no new grant may reference it.

**Naming sequence (R8).** Before `migrate-names` runs, a legacy-named secret is still re-encrypted to v2 by the key migration: its DEK is wrapped through the fresh keyring and its AAD is the legacy store key (`secrets/database.password`). `migrate-names` later, per secret: opens the current envelope, creates a new DEK, encrypts under the canonical key as AAD (`secrets/tenant/payments/database.password`), writes the new envelope and updates grants and connection references in one transaction, and removes the legacy key in that same commit. It is a re-encryption, never an etcd key move.

### Rotation (crash-safe)

**M178 decisions (2026-11-11).** Full flow: [runtime-invariant-flows.md Flow 30](runtime-invariant-flows.md#flow-30-keyring-rotation--root-key-rotation-m178).

| Item | Decision |
|---|---|
| When KEK rotation is allowed | Only with `keyring/legacy` = `none` or `retired`, so it never overlaps the v1 migration. |
| Who does it | A new `keyring-rotation` controller with its own write domain; `keyring-migration` is unchanged. |
| KEK version states | `pending → active → draining → retiring → deleted`. `draining` = replaced but still named by envelopes. |
| Root key rotation | Offline: every API replica stopped, one transaction, `cca secret rotate-root-key`. Compared with Kubernetes (staged, unchecked), Consul (staged, verified), Vault and AWS KMS (key never leaves a KMS/HSM): the file root key is the only mode in use (the KMS providers need the `cloudprovider` build and are untested), and offline has the fewest states. `root_check` makes a wrong key file stop a replica instead of corrupting data. |
| Automation | One playbook (`rotate-root-key.yml`): snapshot, stop one by one, rotate, copy, start one by one, clean up. It aborts and restarts on the old key on any failure before the transaction. The logic and refusals are in `cca`, not in the playbook. |
| Staged online root key rotation | Not built. Reconsider only if the ~1 minute API pause matters. |
| Old etcd snapshots | After a root key rotation they open only with the old root key, which is deleted. The playbook takes a fresh snapshot at the end. |

Rotation is a fact, not a command: `POST /api/secret/rotate` writes `intent/keyring/rotation = {target_version}` and returns. A leader-elected `keyring-rotation` controller does the work:

1. Create the new KEK, wrap it with the root key, write `keyring/kek/{new}` with a create-only transaction. A crash here leaves an unused version.
2. Set `keyring/active = {new}`. New secrets use it from now on; every replica sees the change through its watch.
3. Re-wrap envelopes whose `kek_version` is older, in bounded batches (`keyringRewrapBatchSize`, e.g. 100 per transaction round): unwrap the DEK with the old KEK, wrap with the new one, write with a revision guard (CAS). A guard conflict re-reads that envelope in the next round; concurrent user writes are never overwritten. Only the wrapped DEK changes.
4. When no envelope references a version and every live replica reports the new active version, set it `retiring`; delete it after `--keyring-retire-grace` (default 7 days) and a last full scan. A version that is still referenced is never deleted.

Every envelope names its KEK version, so after a crash at any step every secret is readable, and the controller resumes by scanning for older versions (idempotent: re-running a finished step changes nothing). Root key rotation (re-wrap only `keyring/kek/*` and `keyring/root_check`) is a separate, offline operator command (`cca secret rotate-root-key`, run by `rotate-root-key.yml`) and never runs automatically.

### Scoped delivery to agents

- **Endpoint:** `POST /api/node/secrets` with body `{instance_id}` on the API server, mTLS only, `node:` principals only (users and service tokens are refused). It returns every secret the instance needs: mounted secrets, connection passwords and the image pull secret. Connection and image pull secrets need a grant like mounted ones (an `image_pull_secret` without a grant refuses delivery, the instance waits with `not_authorized`). Agents reach the endpoint at `--api-server` (default: the address `cca join` saved in `/etc/ccattler/server-address`).
- **Authorization per request:** the instance is placed on the calling node (`placement/instance/{id}` = caller's node ID); the node is enrolled and not disabled; for each secret the instance's service holds the grant, and for another tenant's secret the share exists. Partial results are not returned: any missing grant fails the request with the names that failed (values never).
- **Audit:** each delivery and each refusal is recorded (node, instance, service, secret names, outcome; never values).
- **Narrow store access:** delivery runs in the API server process but through its own store principal `service:secret-delivery`, not the generic `api-server` role (which keeps having no rule for `secrets/`, `credentials/` or `keyring/`). The `secret-delivery` role reads `secrets/`, `credentials/`, `keyring/`, `placement/instance/`, `desired/service/`, `desired/secret_share/`, `desired/connection/` and node state, and writes only its audit stream. The keyring migration controller is bounded by its runner-enforced write domain (`secrets/`, `credentials/`, `observed/keyring/migration`) rather than a store role: controllers run on the runner's store, so a role would never be bound (decided 2026-11-10; store isolation for controllers is M181).
- **Gate keys (decision B):** `POST /api/node/gate-keys` returns the private keys of the gates the calling node serves. Every agent's gate proxy serves every gate, and an external connection can arrive at a node with no local instance of the gate's service, so every gate-serving agent needs every gate key. Restricting keys to nodes running the service would break TLS on the others; narrowing it needs an explicit set of ingress-serving nodes, which is outside M174.
  - Residual risk (accepted for M174): compromising an agent exposes the TLS identities of the gates it serves, not application secrets, not the keyring, not the root key.
  - Gate-key delivery is authorized (enrolled, non-disabled node with the gate proxy enabled) and audited separately from application secret delivery.
  - Cached on the node in tmpfs (R4), see "Materialization".
- **Cloud credentials** (`credentials/`) go through the same endpoint and checks; the `node-agent` store role loses its `credentials/` read.
- **The agent never reads `secrets/`, `credentials/` or `keyring/`**, holds no key and does not decrypt. Connection URLs are still assembled on the agent from the delivered password and the (non-secret) connection definition.

### Materialization on the node

- All delivered material is written to `/run/ccattler/secrets/{instance}/` (tmpfs; directory `0700` owned by root, files `0444`) and bind-mounted read-only at the declared paths (`/run/secrets/...`, `/run/connections/...`). Nothing secret is written under `/var/lib/ccattler/logs/` or any other disk path. The agent refuses to write material unless `/run/ccattler/secrets` is tmpfs or ramfs, and forces the directory to `0700`. Hierarchical names (`payments/db-password`) are stored with `/` encoded as `%2F`. Explicit mount paths are cleaned; `/proc`, `/sys`, `/dev` and `/run/cca-secrets` are refused. The legacy API checks the current grant on every read. Files are `0444` rather than `0400` because the agent cannot know the image's user (Airflow runs as uid 50000); host users still cannot reach them through the root-only directory, and inside the container they are reachable only at their read-only mount. The runtimes bind-mount these files (`Spec.SecretMounts`); they never copy secret values (the former `ConfigFiles` path wrote them under the logs directory or `/tmp`, mode `0755`).
- **Gate keys (R4)** are cached separately at `/run/ccattler/gate-keys/{gate}/certificate.pem` and `private-key.pem` (tmpfs; directories `0700`, private key `0400`, root). Only the agent's gate proxy reads them; they are never mounted into containers. The agent reconciles the directory against the delivered set: a removed gate's directory is deleted, a rotated key is replaced (write new file, rename over the old one).
- Connection URLs with embedded passwords are files only (decision 5). Secret values never appear in logs, `cca` status/describe output, events or traces.
- **Legacy secret API (decision C):** file delivery is the default. The agent secret API (`:9772`, `GET /v1/secrets/…`) and its `CCA_SECRET_TOKEN` / `CCA_SECRET_URL` environment variables are given only to instances of services that opt in (`legacy_secret_api true` in the service block). For those:
  - the token stays per instance, is revoked when the instance stops, and only authorizes that instance's granted secrets;
  - the API answers from the instance's delivered material in tmpfs, which came through `POST /api/node/secrets`; the agent never decrypts and holds no key, so keeping the token does not keep the old master-key architecture;
  - the agent logs a deprecation warning once per service at start, and `cca apply` warns on `legacy_secret_api`.
  The opt-in, the API and the environment variables are removed in the release after M174.
- Cleanup: the instance's directory is removed after its container has stopped and will not be restarted on this node (stopped, removed or re-placed elsewhere).

### Availability and failure behavior

The control plane must be reachable to deliver a secret. Required behavior:

| Situation | Behavior |
|---|---|
| Container running, API becomes unreachable | Keep running; nothing is removed or restarted. |
| New instance needs secrets | State `waiting-for-secrets` until delivery succeeds; never pull or start without them (R6: delivery precedes `pulling`, also when the image is cached). |
| Container restarts (crash or liveness) and needs secrets | Material still in tmpfs from this boot → reuse it, no fetch needed. Missing → fetch first. |
| Agent process restarts, same boot | Reconcile (below); do not delete valid material, do not stop healthy containers. |
| Node reboots | tmpfs is empty; every instance fetches again before starting. |
| API stays unreachable | Retry with backoff (`secretDeliveryRetryInitial` 2s, capped at `secretDeliveryRetryMax` 30s); stay `waiting-for-secrets`. A store or decrypt error on the API server is a 500 and handled the same way, never as a refusal. |
| Granted secret has no value (`secret_missing`) | Retry every 30s (the agent cannot watch `secrets/`, and setting the value after `cca apply` is common). |
| Agent cannot read its own desired facts (store error) | Not ready; retried next cycle. Never treated as "needs no secrets". |
| Material exists but differs from the current requirements (grant removed or added, connection/pull secret not granted) | New delivery before start; a config-change restart also drops the material so the restart gets current values. |
| Delivery refused (no grant, no share, not placed here) | `waiting-for-secrets` with reason `not_authorized` and the secret names. Retried immediately when the instance's placement, its service's grant facts or a relevant share change, and at the latest every `secretDeliveryRefusedRetry` (5 min) as a fallback (R5). The agent watches only those metadata facts, never `secrets/`. The API rate-limits refusal audit records per (node, instance, reason). |
| Agent process restarts, same boot, gates configured | Reload gate keys from `/run/ccattler/gate-keys/` and serve TLS; refresh through `/api/node/gate-keys` when the API is reachable. |
| API unreachable, gate keys cached | Keep serving gates with the cached keys. |
| Node reboots, API unreachable | No gate keys; TLS gates stay unavailable on this node until delivery succeeds. |
| Gate removed or its key rotated | Next gate-key reconcile removes or replaces the cached material. |

`waiting-for-secrets` is a new typed instance state (`types.InstanceWaitingForSecrets`), published with `observed/instance/{id}/secret-wait` = `{reason, secrets, next_retry}` (reason `api_unreachable | not_authorized | secret_missing | delivery_not_configured`; the last one means the agent has no node certificate or API address) and a human-readable status reason.| not_authorized | secret_missing`) and `observed/instance/{id}/next_retry`. It is not a failure: it does not count toward crash-loop backoff, the instance is not replaced or rescheduled because of it, and the node-failure controller ignores it. `cca get instances` shows it with the reason. Lifecycle: `placed → waiting-for-secrets → pulling → starting → running` (instances that need no secrets skip the state).

Gate-key readiness is reported separately from agent health: `observed/node/{id}/gate_keys = ready | waiting` with reason, so a node that runs workloads but cannot terminate TLS is visible.

**Agent restart on the same boot:** the agent lists `/run/ccattler/secrets/*` and its running containers. A directory whose instance is still running and still placed on this node is kept as is. A directory with no running instance and no placement on this node is removed. Running containers are not touched; their material is refreshed only when they next start.

**Re-placement:** the new node fetches its own copy before starting. The old node removes its directory after the old instance has stopped.

**Limitation:** a node that is disconnected keeps the plaintext already delivered to it until it can clean up. Revocation cannot erase data on an unreachable machine; removing a grant takes effect on the next delivery.

### Gate TLS private keys

- `RouteTLSController` seals generated keys at `secrets/route/{gate}/tls/key`; the certificate and expiry stay plain facts.
- Manual keys reference a secret:
  ```
  gate web-public {
      host "app.example.com"
      service web port 8080
      tls {
          cert "/etc/ccattler/certs/web.pem"
          key secret "platform/web-gate-key"
      }
  }
  ```
  An inline PEM key is still accepted and is sealed by the apply path before anything is written, so no `desired/route/*/tls/key` fact exists. Encrypting on apply does not make a key safe to commit: configurations kept in Git (M207) must use `key secret`, and `cca apply` warns when a file with an inline key comes from a Git source.
- Agents fetch gate keys through `POST /api/node/gate-keys` and cache them in tmpfs (`/run/ccattler/gate-keys/`, R4); the gate proxy needs them, containers do not. Existing plaintext key facts are migrated: sealed, then deleted.
- **M176 decisions (2026-11-07):**
  - Inline keys: `cca apply` writes the gate facts, then sends the key to `PUT /api/gate-key` (`secret.write`, cluster scope); the server seals it at `secrets/route/{gate}/tls/key`. The CLI never holds an encryption key.
  - Cutover: existing plaintext key facts are sealed at once but deleted only when every enrolled, non-disabled node reports `observed/node/{id}/gate_key_delivery = v1`, so old agents keep TLS during a rolling upgrade. Keys issued, renewed or applied in that window reach upgraded agents only.
  - Delivery returns certificate and key together; the agent never pairs a certificate fact with a cached key.
  - Full flow: [runtime-invariant-flows.md Flow 28](runtime-invariant-flows.md#flow-28-gate-key-sealing--delivery-m176).

### Enrollment and Ansible

- `handleEnroll` drops `SecretMasterKey`; `cca join` no longer writes a key file; agents ignore `/etc/ccattler/secret-master.key` and log once that the file is unused. Removing existing copies from workers is an explicit migration step (Ansible task), not done silently by the agent.
- The worker role's join task uses `creates: /etc/ccattler/pki/node.pem` instead of `secret-master.key`.
- The control-plane role generates `secret-root.key` once (fresh random bytes, never derived from or copied from `secret-master.key`) and provisions it (`0400`, root) to every control-plane node.
- A migration task removes `secret-master.key` from workers and asserts it is absent; after retirement, the same for control-plane nodes.

### Migration (upgrade order) and cutover gates

**M177 decisions (2026-11-10):** a fresh cluster (no legacy key file, no envelope under `secrets/` or `credentials/`) creates the keyring at once, writes v2 from the first secret and records the migration as completed; it never has a legacy key and needs no `migrate-keys`. The steps below apply to upgraded clusters. M177 also fixes an HA bug found during planning: the server generated `secret-master.key` silently when missing and the HA role never copied it, so replicas could hold different keys; the root key is provisioned to every replica and checked with `keyring/root_check`, and v1 envelopes the leader's legacy key cannot open are reported as `unreadable` instead of blocking silently. Full flow: [runtime-invariant-flows.md Flow 29](runtime-invariant-flows.md#flow-29-root-key-keyring--legacy-key-migration-m177).

1. **Upgrade the control plane.** Ansible generates a fresh root key once and provisions it to every replica (R1). The keyring is created under it (`keyring/kek/1`, `keyring/active`, `keyring/root_check`); the legacy key is loaded only to open v1 envelopes. Delivery endpoints and the keyring controller run. Secret writes stay v1 for now, because agents that are not yet upgraded still decrypt locally.
2. **Upgrade agents.** They switch to delivery and report `observed/node/{id}/secret_delivery = v2` and `observed/node/{id}/legacy_key_file = present | absent`. Plaintext gate key facts are sealed and deleted.
3. **Migration gate, then key migration.** `cca secret migrate-keys --confirm-backup` writes `intent/keyring/legacy_migration`. From then on every write is v2. The keyring controller opens each v1 envelope with the legacy key and re-encrypts it as v2 (new DEK under the active KEK, AAD = its current store key, R8) in bounded batches with revision guards, and persists `observed/keyring/migration` = `{state, legacy_v1_envelope_count, migrated, started, completed}` after every batch. Interrupted migration resumes from the remaining v1 envelopes. **This is the rollback boundary.**
4. **Retire the legacy key.** Ansible deletes `secret-master.key` from workers and asserts its absence; once the retirement gate passes, `cca secret retire-legacy-key` writes `keyring/legacy = retired`, replicas stop loading the legacy key and refuse to open v1 envelopes, and Ansible deletes the file from the control plane. From here the old worker copies cannot decrypt anything in the store.

**Migration gate** — `migrate-keys` is refused (`409`, listing what is missing) unless all hold:

- every API replica reports `observed/keyring/replica/{id}` with the M174 version, the active KEK version and a passed `root_check`;
- every enrolled, non-disabled node reports `secret_delivery = v2`, and no agent of an older version is registered;
- `--confirm-backup` is passed, stating that a verified etcd snapshot exists and the restore procedure has been tested (runbook in `design/TESTING.md`).

**Retirement gate (R7)** — `retire-legacy-key` is refused (`409`) unless all hold:

- `observed/keyring/migration` has `state = completed` and `legacy_v1_envelope_count = 0` (persisted by the controller, recomputed by a full scan before the gate passes);
- no plaintext `desired/route/*/tls/key` or `derived/route/*/tls/key` fact exists;
- every enrolled node reports `legacy_key_file = absent` (Ansible removed the file and verified it).

`cca secret rotate` is refused until the migration has completed. After step 3 there is no binary rollback: recovery is a forward fix, or a coordinated restore of the pre-migration etcd snapshot together with the previous binaries. Neither gate claims that previously copied keys or historical ciphertext are erased.

### Tests

Acceptance (from the A–C decisions):

- An unowned service retrieves a secret from `default` and is refused one from `payments`.
- A node with no local instance of a gate's service terminates TLS for that gate.
- A workload using the legacy token retrieves an authorized secret through control-plane delivery, on a node with no key file and no keyring access.

Further:

- Envelope v2: AAD binding (an envelope moved to another key fails to open), v1 still readable, version recorded; rename re-encrypts.
- Root key: missing, wrong size, wrong mode, wrong key (`root_check`), identical to the legacy key → startup error.
- Legacy key (R1): the legacy key cannot unwrap any `keyring/kek/*`, cannot open `root_check` and cannot open any v2 envelope; every legacy secret's value is unchanged after migration; after retirement a v1 envelope is refused.
- Migration: kill mid-batch and restart → resumes, `legacy_v1_envelope_count` persisted and reaches 0; writes during migration are v2; `migrate-keys` refused without `--confirm-backup` or with one agent not on delivery; `retire-legacy-key` refused while one v1 envelope, one plaintext gate key fact or one node reporting `legacy_key_file = present` remains.
- Rotation: kill at each step and restart; every secret readable; resume completes; batches bounded; CAS conflict retried; referenced versions never deleted; replicas read the same versions.
- Tenants: unqualified name resolves to own tenant; another tenant's secret refused without share, delivered with share, refused again after `unshare`; cluster secret needs cluster grant; `secret set` without flags writes to `default` and needs `secret.write` at `team/default`; `--tenant` with `--cluster` is rejected; `migrate-names --apply` refuses unmapped secrets and leaves no legacy key behind.
- Delivery: wrong node, unplaced instance, missing grant, disabled node, user principal: refused and audited; correct case returns only granted secrets; `secret-delivery` role cannot write outside its audit stream; `api-server` role still cannot read `secrets/`.
- Availability: API down with running container → keeps running; new instance → `waiting-for-secrets`, no replacement, starts when API returns; agent restart keeps material of running instances and removes orphans; node reboot re-fetches before start; refused delivery retried on a share/grant change and by the 5-minute fallback, refusal audit rate-limited.
- Ordering: an instance with a cached image and an image pull secret still passes `waiting-for-secrets` before `pulling`/`starting`.
- Gate keys during outages: agent restart with the API down keeps serving TLS from `/run/ccattler/gate-keys/`; reboot with the API down reports `gate_keys = waiting`; a removed gate's directory is deleted.
- Materialization: tmpfs, `0700`/`0400`, removed after stop; nothing under the logs directory; no secret value in logs, events or `cca` output.
- Enrollment response has no key; a joined worker has no key file.
- Gate keys: no plaintext key fact after reconcile and after apply of an inline PEM; gate TLS e2e (T1–T15) unchanged; M176 e2e T16 (keys delivered to every agent node, migration completed, no plaintext facts), T17 (acceptance B: a node without a local backend terminates TLS), T18 (agent restart with cca-server down keeps TLS from the tmpfs cache).
- Deploy cluster: secrets and connection strings still reach Airflow through migration and retirement; rotation during traffic.
