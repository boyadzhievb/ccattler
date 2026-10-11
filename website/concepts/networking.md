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
| Remote (different node) | Node advertise address + host port mapping |

The `--advertise-address` flag on the agent publishes the node's LAN IP for cross-host resolution.

## VIP data plane

The data plane translates service VIPs into real backend addresses on each node. CCattler uses a **pluggable `DataPlaneProvider` interface** — the agent calls `ReconcileVIPDataPlane()` on every reconciliation cycle, and the provider programs the forwarding rules.

### Current implementation: iptables

The default provider uses iptables DNAT rules with round-robin (via the `statistic` module):

```
VIP 10.200.0.1:80 → DNAT to:
  10.100.1.5:8080 (33%)
  10.100.2.8:8080 (33%)
  10.100.3.2:8080 (34%)
```

Per-service iptables chains (`CCA_SVC_*`) are managed by the agent. A `cca0` dummy interface holds VIP addresses.

**Scale characteristics:** iptables rules grow linearly with service count — each service adds one chain with N rules (one per backend). This is the same approach Kubernetes' kube-proxy uses in iptables mode. At tens of services it's fine; at hundreds, the linear chain walk on every packet becomes a bottleneck.

### How other systems solve this

| System | Data plane | Lookup | Scaling |
|---|---|---|---|
| kube-proxy (iptables) | iptables DNAT chains | Linear chain walk | ~11 rules per service |
| kube-proxy (IPVS) | IPVS kernel module | Hash table | O(1) per lookup |
| Cilium | eBPF programs + maps | Hash map in kernel | O(1) per lookup, flat iptables |
| **CCattler (current)** | iptables DNAT chains | Linear chain walk | ~N rules per service |
| **CCattler (planned)** | eBPF programs + maps | Hash map in kernel | O(1) per lookup |

### Future: eBPF data plane

The `DataPlaneProvider` interface was designed to support alternative backends. An eBPF implementation would:

- Attach a BPF program to each pod's network interface (or the host `cca0` interface)
- Use a BPF hash map for VIP→backend lookups (O(1) regardless of service count)
- Eliminate per-service iptables chains entirely
- Require Linux kernel 5.10+ for full functionality

The Go side would use the `cilium/ebpf` library to compile and load BPF programs, implementing the same `ReconcileVIPDataPlane` / `Cleanup` contract. The rest of CCattler (endpoint controller, DNS, proxy, policies) stays unchanged.

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
