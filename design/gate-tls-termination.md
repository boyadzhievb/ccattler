# Gate TLS Termination — M169 Design

## Problem

Gates forward traffic via L4 DNAT (nftables) or L7 cookie proxy (Go reverse proxy), but neither terminates TLS. Exposing HTTPS endpoints requires the backend application to handle its own certificates. This is awkward: every service needs cert management, and the cluster CA — which already issues node and mTLS certs — sits idle for workload-facing TLS.

## Existing Infrastructure

- **`gate` block** (M118): host/path matching, service targeting, sticky sessions
- **RouteController**: `desired/route/` → `derived/route/` backend resolution
- **Agent data plane**: nftables DNAT for non-sticky, embedded `httputil.ReverseProxy` for cookie-sticky
- **Cluster CA** (`security/ca.go`): `IssueCertificate(CommonName, DNSNames, IPAddresses, TTL)` → PEM cert+key
- **Enrollment service**: issues node certs via CSR flow

## DSL

### Auto mode — cluster CA issues cert for the gate's host

```
gate web-public {
    host "app.example.com"
    service frontend port 8080
    tls {
        mode auto
    }
}
```

### Manual mode — user-provided PEM files

```
gate internal-api {
    host "api.internal"
    service api port 3000
    tls {
        cert "/etc/cca/certs/api.pem"
        key  "/etc/cca/certs/api-key.pem"
    }
}
```

### Minimal form

```
gate web {
    host "web.example.com"
    service web port 80
    tls auto
}
```

`tls auto` is shorthand for `tls { mode auto }`.

### Constraints

- `tls` requires `host` — you need a hostname to issue a certificate for.
- `tls { cert ... key ... }` reads PEM files at `cca apply` time and stores content in facts (same pattern as `config` blocks with `from`).
- `tls auto` and `tls { cert ... }` are mutually exclusive.

## Fact Model

A `tls` block compiles to facts under `desired/route/{name}/tls/`:

```
# auto mode
desired/route/web-public/tls/mode     = "auto"

# manual mode
desired/route/internal-api/tls/mode   = "manual"
desired/route/internal-api/tls/cert   = "<PEM content>"
desired/route/internal-api/tls/key    = "<PEM content>"
```

Derived facts (written by TLS certificate controller for auto mode):

```
derived/route/web-public/tls/cert      = "<PEM content>"
derived/route/web-public/tls/key       = "<PEM content>"
derived/route/web-public/tls/not-after = "2027-01-15T12:00:00Z"
```

For manual mode the agent reads cert/key directly from `desired/route/{name}/tls/cert` and `desired/route/{name}/tls/key`.

## Architecture

### Components

1. **Parser** — `tls` sub-block inside `gate`, parsed by `parseRouteTLSField()`
2. **Compiler** — `tls` → `desired/route/{name}/tls/` facts; for manual mode, reads PEM files at compile time
3. **TLS Certificate Controller** (new) — watches `desired/route/` for `tls/mode=auto`, uses cluster CA to issue cert, writes to `derived/route/{name}/tls/`, auto-renews when approaching expiry
4. **Agent data plane** — when TLS facts exist for a route, the embedded proxy listens on HTTPS instead of HTTP; non-TLS gates remain L4 DNAT

### TLS Certificate Controller

New controller: `controllers/route_tls.go`

```
watch: desired/route/, derived/route/
write: derived/route/{name}/tls/cert, derived/route/{name}/tls/key, derived/route/{name}/tls/not-after
```

On reconcile:
1. Scan `desired/route/{name}/tls/mode` — collect routes with `auto`
2. For each auto route, check `derived/route/{name}/tls/not-after`
3. If missing or within renewal window (30 days before expiry), issue cert via `CertificateAuthority.IssueCertificate()` with `DNSNames: [host]`, `TTL: 90 days`
4. Write PEM cert+key to derived facts
5. Delete stale derived TLS facts for routes that no longer have `tls/mode=auto`

The controller needs access to the cluster CA. The server already loads it for enrollment — pass the same `*CertificateAuthority` to this controller.

### Agent Data Plane Changes

The agent data plane reconciler (`agent/dataplane.go`) already reads route facts and builds `RouteDataPlaneConfig`. Extend it:

1. Read `derived/route/{name}/tls/cert` and `desired/route/{name}/tls/cert` (derived for auto, desired for manual)
2. Add `TLSCertPEM` and `TLSKeyPEM` fields to `RouteDataPlaneConfig`
3. When TLS material is present, the embedded cookie proxy creates a `tls.Config` and calls `ServeTLS` instead of `Serve`
4. For non-sticky TLS gates (no cookie), start the embedded proxy anyway (TLS termination requires L7) — forward as plain HTTP to backends

This means: any gate with `tls` uses the embedded Go proxy. Non-TLS non-sticky gates stay pure nftables DNAT.

### Port Allocation

TLS gates need a listener port on the agent. The existing cookie proxy already allocates ports from a range. TLS gates use the same allocation mechanism.

External traffic hits port 443 (or the service VIP port). nftables DNAT redirects to the proxy's allocated port. The proxy terminates TLS and forwards to backends.

## `cca get gates`

New resource type for `cca get`:

```
$ cca get gates
NAME          HOST             PATH  SERVICE        TLS             EXPIRES     BACKENDS
web-public    app.example.com  -     frontend:8080  auto            2027-01-31  3
internal-api  api.internal     -     api:3000       manual          2027-06-01  2
api-sticky    -                -     api:3000       -               -           2
```

TLS column shows `auto`, `manual`, `auto (pending)` (not yet issued), or `-` (no TLS).
EXPIRES is read from the effective certificate (desired for manual, derived for auto).
Gates are tenant-filtered by target service ownership, like services and instances.

`cca describe gate <name>` shows routing, sticky mode, TLS mode, certificate host
and expiry (with days remaining), and the resolved backend list. Neither command
ever returns key or certificate PEM material.

## Implementation Plan

### Phase 1 — DSL + Facts
- Add `RouteTLSDecl` to AST (`lang/ast.go`)
- Parse `tls` sub-block in `parseRouteField()` (`lang/parser.go`)
- Compile TLS facts; read PEM files for manual mode (`lang/compiler.go`)
- Parser tests: `tls auto`, `tls { mode auto }`, `tls { cert ... key ... }`, validation errors
- Compiler tests: fact generation for both modes
- Formatter support (`lang/formatter.go`)
- Lint: warn if `tls` without `host`

### Phase 2 — TLS Certificate Controller
- New `controllers/route_tls.go` with `RouteTLSController`
- Issue certs via cluster CA for `auto` mode
- Auto-renew within 30-day window
- Delete stale derived TLS facts
- Unit tests: issuance, renewal, cleanup

### Phase 3 — Agent Data Plane
- Extend `RouteDataPlaneConfig` with TLS fields
- Read TLS cert/key from derived (auto) or desired (manual) facts
- Upgrade embedded proxy to serve HTTPS when TLS material present
- Start proxy for non-sticky TLS gates (TLS requires L7)
- Integration tests

### Phase 4 — CLI + Polish
- `cca get gates` — list all gates with TLS status
- Update `examples/reference.cca` with TLS examples
- Update `design/dsl-reference.md`
- `cca describe gate <name>` — show cert expiry, backends, sticky config

## Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| No ACME/Let's Encrypt | Cluster CA only | Keep it simple; ACME is a future enhancement. Cluster CA already works. |
| No Caddy/Envoy | Embedded Go proxy | Already exists for cookie sticky. Adding TLS is just `tls.Config`. No new dependency. |
| PEM in facts (manual) | Read at apply time | Same pattern as `config { from "file" }`. Secrets are encrypted at rest in etcd. |
| TLS requires host | Validation error | Can't issue a cert without a hostname. IP-based TLS is a future edge case. |
| 90-day TTL, 30-day renewal | Matches Let's Encrypt convention | Short-lived certs, automatic rotation. |
| All TLS gates use L7 proxy | Even non-sticky | TLS termination is inherently L7. Consistent behavior. |

## Security Considerations

- Manual mode PEM keys are stored in etcd encrypted at rest (existing secret encryption).
- Auto mode private keys are generated server-side by the CA, never leave the fact store unencrypted.
- The agent reads TLS material from the fact store over mTLS — same security boundary as secrets.
- Cert issuance is limited to hostnames declared in `gate` blocks — no wildcard certs.

## Not In Scope

- **ACME / Let's Encrypt**: requires external DNS challenge or HTTP-01 challenge infrastructure. Future milestone.
- **SNI-based routing without termination**: passthrough TLS. Possible future enhancement.
- **Wildcard certificates**: explicit hostnames only.
- **Client certificate verification (mTLS on the gate)**: separate from service mesh.
