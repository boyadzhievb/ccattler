# Proposal

Implements M219 (Phase 219, CLI/API Authorization Alignment) from `milestones.md`. It takes over M218's `cca apply` remote-path item, because the same code path is the authorization bypass.

## Why

The CLI and the API do not follow one authorization model. On the deploy cluster (2026-11-14) a user with no grants ran `cca status` and got the full cluster status, because the legacy `/status` endpoint authenticates but checks no capability and applies no team filter; `/api/status` refuses the same user with 403. On a control-plane host, `cca apply`, `cca diff` and `cca token` open etcd directly, so they skip capability checks, the policy gate and the audit log. No test covers routes outside `api.Server`, and the M216 sweep only tries write methods as a reader.

## What Changes

- **BREAKING**: the legacy `/status` endpoint is removed. `cca status` renders its text from `GET /api/status`, which applies capability checks and team visibility. The HA e2e scripts read `/api/status`, and `scripts/caddy-sync.sh` drops its unused `/status` call.
- **BREAKING**: `cca apply` and `cca diff` always go through `POST /api/apply` and `POST /api/diff`. They no longer write to etcd directly, even on a control-plane host. Local simulation runs only with an explicit `--simulate`. Local files referenced from a configuration are read by the CLI and sent with the request.
- `cca token create/list/revoke` (join tokens) uses new API routes that require `cluster.admin` and are audited.
- The root key commands (`generate-root-key`, `rotate-root-key`, `root-key-status`) stay local-only by design, as offline break-glass operations. Off a control-plane host they say so instead of printing raw etcd errors.
- One public allowlist: `/healthz`, `/api/auth/whoami`, `/api/auth/oidc-config`, `/api/enroll`, `/.well-known/openid-configuration` and `/oidc/jwks`. Every other route refuses an authenticated principal without the required capability.
- `design/graphs/AUTH_GRAPH.md` is split into `AUTHN_GRAPH.md`, `AUTHZ_GRAPH.md` and `SECRETS_GRAPH.md`. `AUTHZ_GRAPH.md` holds the route table: route → method → capability → scope → CLI command. A test keeps the table in sync with the registered routes.

Out of scope:

- `/metrics` authentication (M182).
- Streams that keep running after their user is deleted (M216 open item).
- Store-layer isolation for controllers and agents (M181).
- Other M218 items: `whoami`, `get secrets`, and the `--team`/`readiness` reproductions.
- Direct capability grants in the DSL (open question in `design/plans/question-roles-and-groups.md`).

## Comparison

| System | Model |
|---|---|
| Kubernetes | `kubectl` never talks to etcd. Every request goes through the API server's authentication → authorization → admission → audit. `etcdctl` is reserved for operator backup and restore. |
| Vault, Consul, AWS CLI | The CLI is a pure API client. Operator-only actions (Vault `operator rekey`, Consul snapshot) are still API calls with their own policy. |

CCattler follows the same rule: the CLI is an API client. The only exception is the offline root key rotation, which must work while every API replica is stopped.

## Capabilities

### New Capabilities
- `api-route-authorization`: which routes are public, which capability each other route needs, and the rule that CLI commands reach cluster state only through authorized API routes.

### Modified Capabilities

None (no specs exist yet in `openspec/specs/`).

## Impact

- **Code:**
  - `cmd/cca`: `command_server.go` (legacy mux, `buildClusterStatusJSON` and `buildStatusTextOutput` removed), `command_status.go`, `command_apply.go`, `command_misc.go` (diff, token).
  - `api/`: join token routes, sweep test, route table cross-check.
- **Scripts:** `scripts/caddy-sync.sh`, `test/e2e/vm-ha/ha_test.sh`, `test/e2e/baremetal_ha_test.sh`.
- **Ansible:** the worker role's `cca token create` runs on the control plane with the local admin token, so it goes through the API.
- **e2e:** `cluster_test.sh` applies through the API with the local admin token, so applies now appear in the audit log and pass the policy gate.
- **Docs:** `cli-reference.md`, `security-model.md`, `TESTING.md` and the three call graphs.
- **Upgrade:** the server must be upgraded before the CLI, because a new CLI's `apply` needs `/api/apply` with client-side file contents.
