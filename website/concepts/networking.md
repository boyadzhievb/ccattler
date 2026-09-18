# Networking

Service networking in CCattler is built on derived facts — endpoints are computed from running instances, not manually managed. When instances move, endpoints update automatically.

## Endpoints as derived facts

The endpoint controller watches for running, healthy instances and creates endpoint facts:

```
service web, instances 3, expose 8080
  →
endpoint(web) = {10.0.1.4:8080, 10.0.2.8:8080, 10.0.3.2:8080}
```

Instance disappears → endpoint set updates automatically. No manual service registration.

## Service VIPs

Each service gets a virtual IP from the `10.200.0.0/24` pool. VIPs are stable across instance changes — clients connect to the VIP and traffic is distributed to healthy backends.

## DNS resolution

CCattler runs a DNS server (enabled with `cca server --dns`) that resolves service names:

```bash
dig web.ccattler.local @localhost -p 15353
# Returns: 10.200.0.1 (the service VIP)
```

Any service name resolves to its VIP. No service mesh sidecar needed.

## HTTP reverse proxy

Each agent runs an HTTP reverse proxy (enabled with `cca agent --proxy`) that routes by Host header:

```bash
curl -H "Host: web" http://node-address:80
```

The proxy performs round-robin load balancing across healthy backends. When instances are added or removed, the backend pool updates automatically.

## Cross-host networking

For multi-node clusters, the endpoint controller resolves backend addresses differently depending on where the instance runs:

| Instance location | Backend address |
|---|---|
| Local (same node) | Container IP:port |
| Remote (different node) | Node advertise address + Docker host port |

The `--advertise-address` flag on the agent publishes the node's LAN IP for cross-host resolution.

## VIP data plane

The data plane uses iptables DNAT rules with round-robin (via the `statistic` module):

```
VIP 10.200.0.1:80 → DNAT to:
  10.100.1.5:8080 (33%)
  10.100.2.8:8080 (33%)
  10.100.3.2:8080 (34%)
```

Per-service iptables chains (`CCA_SVC_*`) are managed by the agent. A `cca0` dummy interface holds VIP addresses.

## Identity-based network policies

Network policies reference service identities, not IP addresses or label selectors:

```hcl
network {
    allow frontend/web -> payments/checkout port 443
    allow payments/checkout -> payments/database port 5432
    deny frontend/web -> payments/database
}
```

When instances move between nodes, the policies don't change — only the derived firewall rules change. Every workload gets a SPIFFE identity (`spiffe://ccattler/payments/database`), and mTLS between services is automatic.
