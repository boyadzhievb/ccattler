# Spec Delta

## Purpose

Defines which HTTP routes of the CCattler server are public, what every other route requires, and the rule that CLI commands reach cluster state only through those authorized routes, so the CLI and the API share one authorization model.

## ADDED Requirements

### Requirement: Public route allowlist
Only these routes SHALL answer a request without a capability check: `/healthz`, `/api/auth/whoami`, `/api/auth/oidc-config`, `/api/enroll` (join-token protected), `/.well-known/openid-configuration` and `/oidc/jwks`. `/metrics` is out of scope (M182). Adding a route to the allowlist requires a reviewed change to this list.

#### Scenario: Allowlisted route answers a principal without grants
- **WHEN** an authenticated user with no grants sends `GET /healthz`
- **THEN** the server answers 200
- Test: `api/TestEveryRouteRefusesPrincipalWithoutGrants` (allowlist branch)

#### Scenario: A new unlisted route without a check fails the build
- **WHEN** a route is registered on the server mux that is not in the allowlist and answers a principal without grants with 2xx
- **THEN** the sweep test fails and names the route and method
- Test: `api/TestEveryRouteRefusesPrincipalWithoutGrants`

### Requirement: Every other route refuses a principal without the required capability
Every route on the server's HTTP mux that is not on the allowlist SHALL answer 401 or 403 to an authenticated principal holding no grants, for every HTTP method, and SHALL NOT change any fact in the store.

#### Scenario: Ungranted user reads cluster status
- **WHEN** a user with no grants sends `GET /api/status`
- **THEN** the server answers 403 and the body names the missing `workload.read` grant
- Test: `api/TestEveryRouteRefusesPrincipalWithoutGrants`; e2e: adversarial replay positive control (`verify_identities`, adv-nongrant)

#### Scenario: Ungranted user sends a write to every route
- **WHEN** a user with no grants sends GET, POST, PUT and DELETE to every non-allowlisted route
- **THEN** every answer is 401 or 403, and the store revision is unchanged afterwards
- Test: `api/TestEveryRouteRefusesPrincipalWithoutGrants`

### Requirement: No legacy status endpoint
The server SHALL NOT serve `/status`. Cluster status SHALL be available only from `GET /api/status`, which applies the caller's capability and team visibility.

#### Scenario: Legacy path is gone
- **WHEN** any client sends `GET /status`
- **THEN** the server answers 404 and returns no cluster data
- Test: `cmd/cca/TestServerMuxServesOnlyAPIServerRoutes`

#### Scenario: Team-scoped reader runs cca status
- **WHEN** a user with `workload.read` at `team/frontend` runs `cca status`
- **THEN** the output lists only services and instances of team `frontend`
- Test: `cmd/cca/TestStatusCommandShowsOnlyVisibleTeams`

#### Scenario: User without grants runs cca status
- **WHEN** a user with no grants runs `cca status`
- **THEN** the command exits non-zero and prints the 403 error, not cluster data
- Test: `cmd/cca/TestStatusCommandShowsOnlyVisibleTeams` (ungranted case); e2e: adversarial replay positive control

### Requirement: Configuration changes go through the API
`cca apply` and `cca diff` SHALL send the configuration to `POST /api/apply` and `POST /api/diff` on every host, including control-plane hosts with etcd access. They SHALL NOT open the store directly. Local simulation SHALL run only with an explicit `--simulate`.

#### Scenario: Apply on a control-plane host
- **WHEN** an operator runs `cca apply web.cca` on a host whose etcd endpoints file is present
- **THEN** the configuration reaches the store only through `/api/apply`: the writes appear in the audit log under the operator's principal and pass the policy gate
- Test: `cmd/cca/TestApplyCommandUsesAPIEvenWithEtcdEndpoints`; e2e: Deploy Testbed `cluster_test.sh` apply step plus audit check

#### Scenario: Apply by a principal without workload.create
- **WHEN** a user holding only `workload.read` runs `cca apply web.cca`
- **THEN** the command exits non-zero with the API's 403 error and no fact changes
- Test: `cmd/cca/TestApplyCommandUsesAPIEvenWithEtcdEndpoints` (refused case); e2e: adversarial replay case `reader-cannot-apply`

#### Scenario: Configuration references a local file
- **WHEN** a configuration declares a gate certificate or a config value read from a local file and is applied
- **THEN** the CLI sends the file's contents with the request, and the server never reads a path given by the client
- Test: `cmd/cca/TestApplyCommandSendsLocalFileContents`

#### Scenario: A removed gate setting is removed through the API
- **WHEN** a configuration that declared a gate with `tls auto` is re-applied through `/api/apply` with the gate's `tls` removed
- **THEN** the gate's `desired/route/{gate}/tls/` facts are deleted and the gate itself remains, on both the direct and the policy-gate path
- Test: `api/TestServerApplyPrunesRemovedGateSettings`

#### Scenario: Gate private key never in the apply body
- **WHEN** a manual-tls gate names a key file and is applied through `/api/apply`
- **THEN** the server stores the certificate sent in `files`, reads and stores no key, and refuses the request if the key file's contents are in `files`
- Test: `api/TestServerApplyAcceptsKeyFileWithoutReadingIt`

#### Scenario: Simulation is explicit
- **WHEN** a user runs `cca apply web.cca` with no reachable API and without `--simulate`
- **THEN** the command exits non-zero naming the API address, and prints no simulated cluster status
- Test: `cmd/cca/TestApplyCommandFailsWithoutAPIUnlessSimulate`

### Requirement: Join tokens are managed through the API
Creating, listing and revoking node join tokens SHALL go through API routes that require `cluster.admin`. Each operation SHALL be recorded in the audit log with the token's identifier, never its secret value.

#### Scenario: Admin creates a join token
- **WHEN** a cluster admin runs `cca token create`
- **THEN** a token record exists under `enrollment/token/`, the command prints the token once, and an audit entry names the admin
- Test: `api/TestJoinTokenRoutesRequireClusterAdmin`; e2e: Ansible worker join on the Deploy Testbed

#### Scenario: Non-admin tries to create a join token
- **WHEN** a user with `api-writer` capabilities runs `cca token create`
- **THEN** the command exits non-zero with 403 and no token record is written
- Test: `api/TestJoinTokenRoutesRequireClusterAdmin`; e2e: adversarial replay case `writer-cannot-create-join-token`

### Requirement: Local-only commands say so
The root key commands (`cca secret generate-root-key`, `rotate-root-key`, `root-key-status`) SHALL remain local operations on a control-plane host. When run without local etcd and root key access, they SHALL exit non-zero with a message saying they must run on a control-plane node.

#### Scenario: Root key status from a workstation
- **WHEN** a user runs `cca secret root-key-status` on a host without etcd endpoints or root key file
- **THEN** the command exits non-zero with "run on a control-plane node", not a raw etcd connection error
- Test: `cmd/cca/TestRootKeyCommandsExplainControlPlaneOnly`

### Requirement: Route table matches the registered routes
`design/graphs/AUTHZ_GRAPH.md` SHALL list every route registered on the server mux with its methods, required capability, scope and calling CLI command. A route missing from the table, or a table row with no registered route, SHALL fail the build.

#### Scenario: Route added without documentation
- **WHEN** a developer registers a new route and does not add it to the AUTHZ route table
- **THEN** the cross-check test fails and names the route
- Test: `api/TestDocCrossCheckAuthzRouteTable`
