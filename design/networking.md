# Networking

CCattler's network model has three layers: container overlay IPs, service VIPs, and the physical node network. The overlay provides each container with a routable IP. VIPs give services stable addresses. The physical network carries encapsulated overlay traffic between nodes.

Updated: 2026-10-18

---

## Network Architecture

```
                     ┌─────────────────────────────────────────────────┐
                     │              Physical Network                    │
                     │  (not managed by CCattler, any L2/L3 fabric)    │
                     │                                                  │
                     │  node-1: 192.168.1.10    node-2: 192.168.1.11  │
                     └──────┬──────────────────────────┬───────────────┘
                            │                          │
                     ┌──────┴──────┐            ┌──────┴──────┐
                     │  VXLAN      │            │  VXLAN      │
                     │  cca-vxlan0 │◀──UDP 4789─▶│  cca-vxlan0 │
                     └──────┬──────┘            └──────┬──────┘
                            │                          │
                     ┌──────┴──────┐            ┌──────┴──────┐
                     │  Bridge     │            │  Bridge     │
                     │  cca-net    │            │  cca-net    │
                     │ 10.100.1.0/24│           │ 10.100.2.0/24│
                     └──┬─────┬───┘            └──┬─────┬───┘
                        │     │                   │     │
                     ┌──┴┐  ┌┴──┐              ┌──┴┐  ┌┴──┐
                     │C1 │  │C2 │              │C3 │  │C4 │
                     │.2 │  │.3 │              │.2 │  │.3 │
                     └───┘  └───┘              └───┘  └───┘
```

### Layer 1: Container Overlay IPs (cluster CIDR)

Every container instance gets a unique IP from the cluster CIDR (default `10.100.0.0/16`). Each node owns a `/24` subnet carved from this range. The `NetworkProvider` interface (`network/network.go`) manages allocation:

- `AllocateIP(nodeID, instanceID)` -- assigns an IP from the node's `/24`, idempotent
- `ReleaseIP(nodeID, instanceID)` -- returns the IP to the pool
- `NodeSubnet(nodeID)` -- returns the node's assigned subnet (e.g. `10.100.1.0/24`)

Implementations: `SimulatorNetworkProvider` (in-memory, for tests), containerd CNI bridge (production). The bridge `cca-net` is created by nerdctl with the node's subnet as the bridge CIDR.

Container IPs are written to the store as `observed/instance/{id}/ip` by the node agent after container start.

### Layer 2: Service VIPs (VIP CIDR)

Each service with exposed ports gets a stable virtual IP from the VIP CIDR (default `10.200.0.0/24`). The `NetworkController` (`controllers/network.go`) allocates VIPs:

1. Watches `endpoint/service/{name}/*` for services with backends
2. Allocates the next free VIP from the pool
3. Writes `network/vip/{service}` and `network/dns/{service}` facts

The `NftablesDataPlane` (`network/nftables_dataplane.go`) programs DNAT rules on each node so that traffic to a VIP is forwarded to backend container endpoints. The `cca0` dummy interface holds VIP addresses so the kernel accepts traffic destined for them.

DNS: the built-in DNS server (`network/dns.go`) resolves `<service>.cca.local` to the service's VIP by reading `network/vip/{service}` facts from the store.

### Layer 3: Physical Node Network

Physical node IPs are not managed by CCattler. Each node's routable address is provided via `--advertise-address` and published to the store as `observed/node/{id}/advertise_address`. This address is used for VXLAN tunnel endpoints and, in local mode, for host-port-based cross-node DNAT.

---

## The Two CIDRs

| CIDR | Default | Purpose | Equivalent |
|------|---------|---------|------------|
| `--cluster-cidr` | `10.100.0.0/16` | Container overlay IPs | Kubernetes `--cluster-cidr` |
| `--vip-cidr` | `10.200.0.0/24` | Service virtual IPs | Kubernetes `--service-cluster-ip-range` |

There is no node CIDR. Physical node addresses come from the existing network infrastructure and are discovered via `--advertise-address`.

Both CIDRs are configured at cluster init (`cca init --cluster-cidr X --vip-cidr Y`) and must not overlap with each other or with the physical node network. The agent receives `--cluster-cidr` to configure the bridge; the server receives `--vip-cidr` for VIP allocation.

---

## Network Modes

The agent supports two network modes, selected via `--network-mode`:

| Mode | Flag | Use case | Cross-node traffic |
|------|------|----------|-------------------|
| **local** (default) | `--network-mode local` | Single-node, dev, CI | Host-port DNAT via advertise address |
| **vxlan** | `--network-mode vxlan` | Multi-node production | VXLAN encapsulation over UDP 4789 |

### Local Mode (M121, current)

Each agent programs nftables DNAT rules using the `NftablesDataPlane`. For instances on the local node, DNAT targets the container's bridge IP directly. For instances on remote nodes, DNAT targets `<remote-advertise-address>:<host-port>`. This requires every exposed port to have a host-port mapping, and traffic goes through double NAT (VIP DNAT on the source node, port mapping on the destination node).

Limitations: requires host-port allocation (port exhaustion risk), double NAT breaks connection tracking, container IPs are not directly routable between nodes.

### VXLAN Mode (M122)

The agent creates a VXLAN tunnel mesh so that container IPs are directly routable between all nodes. No host-port mappings needed for inter-service traffic. The nftables data plane can DNAT directly to container IPs on any node.

---

## Cross-Node Data Path (VXLAN)

Packet flow: container on node-1 (`10.100.1.2`) sends to container on node-2 (`10.100.2.3`).

```
Container C1 (10.100.1.2)
    │
    │ dst: 10.100.2.3:8080
    ▼
Bridge cca-net (10.100.1.1)
    │
    │ route: 10.100.2.0/24 via cca-vxlan0
    ▼
VXLAN interface cca-vxlan0
    │
    │ encapsulate: VXLAN header (VNI 42)
    │ outer src: 192.168.1.10 (node-1 advertise addr)
    │ outer dst: 192.168.1.11 (node-2 advertise addr)
    │ outer proto: UDP, dst port 4789
    ▼
Physical NIC (eth0)
    │
    │ ═══════ physical network ═══════
    ▼
Physical NIC (eth0) on node-2
    │
    │ outer dst: 192.168.1.11, UDP 4789
    ▼
VXLAN interface cca-vxlan0
    │
    │ decapsulate: strip VXLAN header
    │ inner dst: 10.100.2.3:8080
    ▼
Bridge cca-net (10.100.2.1)
    │
    ▼
Container C3 (10.100.2.3)
```

### VIP traffic with VXLAN

When a container accesses a VIP, nftables DNAT resolves it to the backend container IP. If the backend is on a remote node, the packet (now addressed to the remote container IP) hits the VXLAN route and is tunneled:

```
Container C1 → dst: 10.200.0.1:80 (VIP)
    │
    ▼ nftables prerouting: DNAT to 10.100.2.3:8080
    │
    │ dst: 10.100.2.3:8080
    ▼ route: 10.100.2.0/24 via cca-vxlan0
    │
    ▼ VXLAN encap → UDP to node-2 → VXLAN decap → bridge → container C3
```

This eliminates the double-NAT problem of local mode. The nftables data plane does not need to know whether the backend is local or remote -- it always DNATs to the container IP, and the routing table handles delivery.

---

## Subnet Allocation Lifecycle

Subnets are allocated as facts in the store and consumed by agents.

### Allocation (server-side)

When a node registers, the server-side network controller writes a subnet assignment:

```
PUT desired/network/subnet/{node-id} → "10.100.X.0/24"
```

The controller tracks allocated subnets to prevent overlap. Allocation uses the next free third-octet value (1-254). Subnet assignments are permanent -- a node keeps its subnet even after restart or temporary disconnect.

### Consumption (agent-side)

The agent reads its subnet fact at startup and on every reconciliation cycle:

```
GET desired/network/subnet/{node-id} → "10.100.3.0/24"
```

The subnet determines:
1. The bridge `cca-net` CIDR (passed to nerdctl/containerd CNI)
2. The IP range for `AllocateIP` on this node
3. The VXLAN interface address (`.1` of the subnet, e.g. `10.100.3.1`)

### Peer discovery

The agent discovers all peer nodes and their subnets by scanning:

```
Scan desired/network/subnet/*     → {node-id: subnet} for all nodes
Scan observed/node/*/advertise_address → {node-id: physical-ip} for all nodes
```

These two fact sets are the complete input for VXLAN route programming.

---

## Agent VXLAN Reconciliation

The VXLAN reconciler (`agent/vxlan_reconciler.go`) is a sub-reconciler of the node agent, called on every reconciliation cycle alongside the data plane reconciler. It is fully idempotent.

### Constants

```go
const vxlanInterfaceName  = "cca-vxlan0"  // VXLAN tunnel interface name
const vxlanVNI            = 42            // VXLAN network identifier
const vxlanDestinationPort = 4789         // standard VXLAN UDP port (RFC 7348)
const vxlanMTUOverhead    = 50            // VXLAN encapsulation overhead in bytes
const bridgeMTUWithVXLAN  = 1450          // bridge MTU when VXLAN is active (1500 - 50)
```

### Reconciliation loop

Each cycle:

1. **Ensure VXLAN interface exists.** `ip link add cca-vxlan0 type vxlan id 42 dstport 4789 local <advertise-address> nolearning`. Assign the node's subnet gateway IP (e.g. `10.100.3.1/24`). Bring it up.

2. **Read peer facts.** Scan `desired/network/subnet/*` and `observed/node/*/advertise_address` from the store. Build a map of `{nodeID: {subnet, physicalIP}}` for all nodes except self.

3. **Program routes.** For each peer: `ip route replace <peer-subnet> via <peer-subnet-gateway> dev cca-vxlan0 onlink`. Example: `ip route replace 10.100.2.0/24 via 10.100.2.1 dev cca-vxlan0 onlink`.

4. **Program FDB entries.** For each peer: `bridge fdb replace 00:00:00:00:00:00 dev cca-vxlan0 dst <peer-physical-ip>`. This tells the VXLAN driver where to send encapsulated packets for unknown destinations in each peer's subnet.

5. **Remove stale routes and FDB entries.** Compare the current routing table and FDB against the desired peer set. Remove entries for nodes that no longer exist.

6. **Set bridge MTU.** When VXLAN mode is active, set the `cca-net` bridge MTU to 1450.

### Shutdown

On agent shutdown: `ip link delete cca-vxlan0`. The kernel automatically removes all associated routes and FDB entries.

### Failure handling

- If the VXLAN interface already exists with correct parameters, creation is a no-op (`ip link add` returns EEXIST, ignored).
- If a peer's advertise address is missing, that peer is skipped (no route programmed). Traffic to that node's containers will be unroutable until the peer publishes its address.
- If the store is unreachable, the reconciler keeps the last-known routes. Existing tunnels continue to work.

---

## MTU Considerations

VXLAN adds 50 bytes of overhead to every packet:

| Component | Bytes |
|-----------|-------|
| Outer Ethernet header | 14 |
| Outer IP header | 20 |
| Outer UDP header | 8 |
| VXLAN header | 8 |
| **Total overhead** | **50** |

With a standard 1500-byte physical MTU, the effective container MTU is 1450. The agent sets this on the `cca-net` bridge when VXLAN mode is active so that containers generate correctly-sized frames without fragmentation.

If the physical network supports jumbo frames (MTU 9000), the bridge MTU can be set higher. This is configurable via `--bridge-mtu` on the agent, but the default of 1450 is safe for any network.

---

## Fact Store Keys (Network)

Additions for M122 are marked with `[M122]`.

```
desired/network/subnet/{node-id}           [M122] → "10.100.X.0/24"  (subnet allocation)
observed/node/{node-id}/advertise_address          → "192.168.1.10"   (physical IP)
observed/instance/{id}/ip                          → "10.100.1.2"     (container IP)
network/vip/{service}                              → "10.200.0.1"     (service VIP)
network/vip/{service}/port                         → "80"             (VIP port)
network/dns/{service}                              → "10.200.0.1"     (DNS → VIP mapping)
endpoint/service/{name}/{instance-id}              → "10.100.1.2:8080" (endpoint)
endpoint/service/{name}/{instance-id}/{port}       → "10.100.1.2:8080" (multi-port endpoint)
```

Network policy rules live under `policy/network/` -- see `security/network_policy.go`.

---

## Network Policy

CCattler implements identity-based network policy via `NetworkPolicyEngine` (`security/network_policy.go`). Policies are stored as facts under `policy/network/{rule-name}` and evaluated against `{source-service, target-service, port}` tuples.

Default policy is **deny-all** unless an explicit allow rule matches. Deny rules take priority over allow rules. The policy engine is currently evaluated at the application layer; M122 does not change this. Future work: compile policies to nftables rules for kernel-level enforcement.

---

## Source Files

| File | Purpose |
|------|---------|
| `network/network.go` | `NetworkProvider` interface, CIDR constants |
| `network/simulator.go` | In-memory IP allocator for tests |
| `network/dataplane.go` | `DataPlaneProvider` interface, VIP/route config types |
| `network/nftables_dataplane.go` | nftables DNAT rule programming |
| `network/dns.go` | Built-in DNS server (`*.cca.local` → VIP) |
| `controllers/network.go` | VIP allocation controller |
| `controllers/network_policy.go` | Network policy nftables rule generation |
| `security/network_policy.go` | Network policy engine (identity-based) |
| `agent/dataplane.go` | Agent data plane sub-reconciler |
| `agent/vxlan_reconciler.go` | **[M122]** Agent VXLAN sub-reconciler |

---

## Future: Cilium eBPF Integration

The long-term data plane target is Cilium in **kvstore mode**, using CCattler's etcd directly as the Cilium kvstore. This replaces VXLAN + nftables + application-layer policy with a single eBPF-based data plane.

### Why Cilium kvstore mode

Cilium can operate without Kubernetes by pointing it at an external etcd. In this mode:

- Cilium agent reads identity and endpoint information from etcd
- CCattler writes endpoint facts to etcd; Cilium consumes them
- No Kubernetes API server required
- Network policy is enforced at the eBPF level (kernel, not application)

### What Cilium replaces

| Current component | Cilium replacement |
|---|---|
| VXLAN reconciler (ip link/route) | Cilium VXLAN or Geneve tunnels |
| nftables DNAT (VIP load balancing) | eBPF socket-level load balancing |
| Application-layer network policy | eBPF per-packet policy enforcement |
| `cca-net` bridge + CNI | Cilium CNI plugin |

### What CCattler keeps

- VIP allocation controller (writes facts consumed by Cilium)
- DNS server (resolves `*.cca.local` to VIPs)
- Subnet allocation (drives Cilium IPAM)
- All control plane logic (scheduling, placement, reconciliation)

### Integration boundary

CCattler writes endpoint and policy facts to etcd. Cilium reads them. No direct API calls between the two. This fits the fact-based model: Cilium is another consumer of the fact store, not a component that CCattler calls.

### Prerequisites

- Kernel 5.10+ for eBPF (6.1+ for conntrack kfuncs)
- Cilium agent installed on each node
- etcd schema mapping: CCattler endpoint facts → Cilium-compatible kvstore entries

This is deferred until benchmarks show nftables is insufficient at scale. See `design/ebpf-data-plane.md` for the full analysis.

---

## Why Native VXLAN (Not Flannel/Calico)

### No external daemon

Flannel and Calico each run their own daemon on every node, watching their own state store (etcd or a custom datastore). This creates a dual-authority problem: two systems managing overlapping network state. When Flannel's flanneld and CCattler's agent both believe they own the VXLAN interface, conflicts are inevitable.

CCattler's VXLAN reconciler is a sub-reconciler inside the existing agent. One daemon, one state store, one reconciliation loop.

### No dual state authority

Flannel stores subnet allocations in its own etcd prefix (`/coreos.com/network/subnets/`). Calico stores IP pools in its own CRDs or etcd prefix. Both require synchronization with CCattler's fact store. Any desync (network partition, restart ordering) causes split-brain networking.

CCattler stores subnet allocations as facts (`desired/network/subnet/{node-id}`). The VXLAN reconciler reads these facts directly. Single source of truth.

### Fits the fact-based model

The VXLAN reconciler follows the same pattern as every other CCattler reconciler:

1. Read facts (subnets, peer addresses)
2. Compute desired state (routes, FDB entries, VXLAN interface)
3. Ensure desired state matches reality (idempotent `ip route replace`, `bridge fdb replace`)
4. Report observed state back to the store

No imperative API calls, no event-driven callbacks, no external watch loops. Just `reconcile(facts) -> []changes`.

### Minimal code surface

The VXLAN reconciler is approximately 200 lines of Go -- interface creation, route programming, FDB management, and cleanup. Flannel is 15,000+ lines. Calico is 100,000+ lines. For a single-overlay use case (VXLAN with static routes), the native implementation is simpler to audit, debug, and maintain.
