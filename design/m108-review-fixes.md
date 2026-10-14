# M108 — External Review Fixes

Source: two external reviews from 2026-10-14 (chat-14oct.md, chat-14oct-2.md).

## Strategic findings (chat-14oct.md)

Not part of M108 — tracked here for planning.

### S1: 5,000-workload benchmark unverified
The 200-node/5000-instance loadtest (`loadtest/loadtest_test.go`) has never had a recorded successful run. STATUS.md reported timeout at ~2400/5000 in 30 minutes. CI runs `-short` which skips it. Need a passing, repeatable benchmark run.

### S2: Application-level E2E assertions missing
E2E tests verify container counts, node registration, VIP iptables rules, and API endpoints — but never assert that an HTTP request traverses the service network and reaches the application. The Airflow health check in `deploy/ansible/deploy-deploy.yml` uses `|| true`, meaning failure is ignored. Need hard HTTP assertions through VIP/proxy to app endpoints as a release gate.

### S3: Cloud autoscaling incomplete
The `infra/` package has `infra.go` (interface) and `simulator.go` only. No AWS or GCP `InfraProvider` implementation for node provisioning/retirement. `cloud/azure.go` explicitly returns "not yet implemented". The `cloud/` package has AWS/GCP code for load balancers but not for the separate node-provisioning interface.

### S4: STATUS.md stale
`design/STATUS.md` says M80. Should reflect M104+. Quick fix — update the file.

### S5: VitePress duplicate heading — FIXED
Duplicate `v1-33-0-beta` heading in `website/changelog.md` fixed in commit `e74a298`.

---

## Code findings (chat-14oct-2.md)

Three findings from external code review. Implement in order: Fix 3 → Fix 2 → Fix 1 (smallest to largest).

## Fix 3 (P2): Init container network error swallowing

**Problem**: `ExecInit` in `runtime/container.go:489-491` silently discards `ensureNetworkExists` errors. Init containers run without network, causing confusing failures instead of a clear error.

**File**: `runtime/container.go`

**Change**: Return the error instead of silently proceeding:
```go
// Current (broken):
if initNetworkError := containerRuntime.ensureNetworkExists(ctx); initNetworkError == nil {
    args = append(args, "--network", containerRuntime.networkName)
}

// Fixed:
if containerRuntime.networkName != "" {
    if initNetworkError := containerRuntime.ensureNetworkExists(ctx); initNetworkError != nil {
        return fmt.Errorf("init network setup: %w", initNetworkError)
    }
    args = append(args, "--network", containerRuntime.networkName)
}
```

**Test**: Add network failure propagation test in `runtime/conformance_test.go`.

**Contrast**: `Start()` at line 211-213 already returns error correctly — this makes `ExecInit` consistent.

---

## Fix 2 (P2): Connection URL credential escaping

**Problem**: `buildConnectionURL` in `agent/agent.go:881-907` uses `strings.Builder` to construct URLs with raw user/password. Credentials containing `@`, `:`, `/`, `?`, `#` produce unparseable URLs.

**File**: `agent/agent.go`

**Change**: Replace manual string building with `net/url`:
```go
u := &url.URL{
    Scheme: connectionDef.driver,
    Host:   connectionDef.host,
}
if connectionDef.port > 0 {
    u.Host = net.JoinHostPort(connectionDef.host, strconv.Itoa(connectionDef.port))
}
if connectionDef.user != "" {
    if password != "" {
        u.User = url.UserPassword(connectionDef.user, password)
    } else {
        u.User = url.User(connectionDef.user)
    }
}
if connectionDef.database != "" {
    u.Path = "/" + connectionDef.database
}
return u.String()
```

**Test**: Add `TestBuildConnectionURL` in `agent/agent_test.go` covering:
- Normal case (no special chars)
- Password with `@`, `:`, `/`, `?`, `#`
- User with `@`
- Empty user / empty password / empty database / port=0

---

## Fix 1 (P1): Multi-port service consistency

**Problem**: Container runtime, agent data plane, endpoint controller, and cloud LB controller all assume a single port per service. A service exposing ports 8080 and 9090 only gets one host port tracked, published, and load-balanced.

### Three independent bugs:

**Bug A — Container runtime stores only first host port**
- File: `runtime/container.go`
- `instanceHostPorts` is `map[string]int` (one port per instance)
- `buildRunArguments` maps ALL container ports to host ports but only records the first
- **Fix**: Change to `map[string]map[int]int` (instanceID → containerPort → hostPort). Record all port mappings. Change `HostPortForInstance` return type.

**Bug B — Agent publishes single hostport fact**
- File: `agent/dataplane.go:226-233`
- Publishes `observed/instance/{id}/hostport` as a single int
- **Fix**: Publish per-port facts: `observed/instance/{id}/hostport/{containerPort}` → hostPort value
- Add key helper: `types.KeyObservedInstanceHostPortForPort(instanceID string, containerPort int)` in `types/keys.go`

**Bug C — Cloud LB controller overwrites ports**
- File: `controllers/cloud_loadbalancer.go:277-299`
- `extractExternalServicePorts` uses `externalServices[serviceName] = externalServiceConfig{...}` — overwrites on each port
- **Fix**: Change `externalServiceConfig` to hold a slice of port/protocol pairs. Append instead of overwrite. `Reconcile` loop emits one `pendingCloudLBOperation` per port per service.

### Downstream consumers to update:

**Endpoint controller** (`controllers/endpoint.go:124, 152`):
- Currently reads single `hostPort` from instance fields
- Stamps the same host port for every exposed port (wrong — port 8080 and 9090 both get the same host port)
- **Fix**: Read per-port host port facts. When building endpoints with host addresses, look up the correct host port for each exposed port.

**Instance field parser** (`controllers/helpers.go`):
- `parseInstanceFieldsFromFacts` puts `hostport → value` in the field map
- **Fix**: Parse `hostport/{containerPort}` sub-keys into a nested structure or a map field like `hostport/8080 → 32001`

### Implementation order (bottom-up):

1. `types/keys.go` — add `KeyObservedInstanceHostPortForPort(instanceID, port)` and scan constant
2. `runtime/container.go` — change `instanceHostPorts` type, update `HostPortForInstance` signature
3. `agent/dataplane.go` + `agent/agent.go` — publish per-port facts, update callers of `HostPortForInstance`
4. `controllers/helpers.go` — parse per-port host port data from instance fields
5. `controllers/endpoint.go` — use per-port host port for cross-host endpoint resolution
6. `controllers/cloud_loadbalancer.go` — collect all external ports, emit per-port LB ops

### Test files to update:

- `controllers/cloud_loadbalancer_test.go` — add multi-port external service test
- `controllers/endpoint_test.go` — update `TestEndpointMultiPortService`, `TestEndpointCrossHostAddress` for per-port host ports
- `agent/agent_test.go` — update host port publishing assertions
- `runtime/container_args_test.go` — port mapping tests
- `runtime/conformance_test.go` — if `HostPortForInstance` signature changes

### Backward compatibility note:

Old single `observed/instance/{id}/hostport` facts must still be parsed (or explicitly migrated/cleaned up). During rollout, the endpoint controller should fall back to the single-key format if per-port keys are absent. After a full cycle, all agents will publish the new format.
