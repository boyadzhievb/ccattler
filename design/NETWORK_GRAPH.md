# CCattler Network Graph Reference

Machine-readable network topology reference. Purpose: understand the complete
networking stack without reading source files. Load this file instead.

## Node-Level Network Topology

Every node running a CCattler agent has the following network interfaces:

```
┌─────────────────────────────────────────────────────────────────────┐
│                          NODE (Linux host)                          │
│                                                                     │
│  ┌──────────┐                                                       │
│  │  eth0    │  Physical NIC — node's LAN address (advertiseAddress) │
│  │ 192.168. │  Used as: VXLAN tunnel endpoint (VTEP),               │
│  │ 124.20   │  etcd peer, agent ↔ server communication              │
│  └────┬─────┘                                                       │
│       │                                                             │
│  ┌────┴──────────────────────────────────────────────────────┐      │
│  │                    Linux Routing Table                     │      │
│  │  10.100.3.0/24 → dev cca0 (local containers)             │      │
│  │  10.100.1.0/24 via 10.100.1.0 dev cca-vxlan0 onlink      │      │
│  │  10.100.2.0/24 via 10.100.2.0 dev cca-vxlan0 onlink      │      │
│  │  default → dev eth0                                       │      │
│  └───────────────────────────────────────────────────────────┘      │
│       │           │              │                                   │
│  ┌────┴─────┐ ┌───┴────────┐ ┌──┴──────────┐                       │
│  │ cca0     │ │ cca-vxlan0 │ │ cca-vip0    │                       │
│  │ (bridge) │ │ (vxlan)    │ │ (dummy)     │                       │
│  │ 10.100.  │ │ no IP      │ │ 10.200.0.x  │                       │
│  │ 3.1/24   │ │ MTU 1450   │ │ /32 per VIP │                       │
│  │ gateway  │ │ VNI=42     │ │             │                       │
│  └────┬─────┘ └────────────┘ └─────────────┘                       │
│       │                                                             │
│  ┌────┴──────────────────────────────────────┐                      │
│  │        Container veth pairs               │                      │
│  │                                           │                      │
│  │  vethXXXX ←→ eth0 (in container netns)    │                      │
│  │  10.100.3.2    metadata-db                │                      │
│  │                                           │                      │
│  │  vethYYYY ←→ eth0 (in container netns)    │                      │
│  │  10.100.3.3    worker                     │                      │
│  │                                           │                      │
│  │  vethZZZZ ←→ eth0 (in container netns)    │                      │
│  │  10.100.3.4    scheduler                  │                      │
│  └───────────────────────────────────────────┘                      │
│                                                                     │
│  DNS Forwarder: 192.168.124.20:53 (agent's advertise address)       │
│  nftables table: ip cca { prerouting, output, postrouting, ... }    │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### Interface summary

| Interface | Type | Created By | IP Address | Purpose |
|---|---|---|---|---|
| `eth0` (or `ens*`) | Physical | OS | Node LAN IP | VXLAN underlay, etcd, SSH |
| `cca0` | Bridge | CNI bridge plugin | `{node-subnet}.1/24` | Gateway for containers, intra-node container traffic |
| `cca-vxlan0` | VXLAN | `agent/vxlan.go` | None (pure tunnel) | Cross-node container-to-container overlay |
| `cca-vip0` | Dummy | nftables/iptables dataplane | `{VIP}/32` per service | Anchors VIP addresses for DNAT interception |
| `vethXXXX` | veth pair | CNI bridge plugin | Container-side: `{node-subnet}.N/24` | Per-container connection to bridge |

### CNI configuration (generated in memory)

```json
{
  "cniVersion": "1.0.0",
  "name": "{networkName}",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "cca0",
      "isGateway": true,
      "ipMasq": true,
      "ipam": {
        "type": "host-local",
        "subnet": "{node-subnet-CIDR}",
        "routes": [{"dst": "0.0.0.0/0"}]
      }
    },
    {
      "type": "portmap",
      "capabilities": {"portMappings": true}
    }
  ]
}
```

**Source:** `runtime/containerd.go:560` `buildCNIConflist()`

---

## IP Allocation

### Cluster CIDR → Node Subnet → Container IP

```
Cluster CIDR: 10.100.0.0/16 (default, --cluster-cidr)
     │
     ├── Node-1: 10.100.1.0/24  (third octet from FNV hash of nodeID)
     │   ├── .1  → cca0 bridge (gateway)
     │   ├── .2  → container-A
     │   ├── .3  → container-B
     │   └── .N  → host-local IPAM assigns sequentially
     │
     ├── Node-2: 10.100.3.0/24
     │   ├── .1  → cca0 bridge
     │   ├── .2  → container-C
     │   └── ...
     │
     └── Node-3: 10.100.7.0/24
         └── ...

VIP CIDR: 10.200.0.0/24 (default, --vip-cidr)
     ├── 10.200.0.1 → service "web"
     ├── 10.200.0.2 → service "metadata-db"
     └── ...
```

### Subnet allocation flow

```
Agent startup (agent/vxlan.go)
├── EnsureSubnetPublished
│   ├── factStore.Get(network/node/{nodeID}/subnet)    [check existing]
│   ├── IF exists: done
│   └── IF not:
│       ├── factStore.ScanWithRevision(network/node/*)  [all allocated]
│       ├── collectOccupiedThirdOctets                  [build taken set]
│       ├── hashNodeIDToOctetOffset(nodeID)             [FNV32a → start offset]
│       ├── findNextFreeThirdOctetForNode               [probe from offset]
│       └── factStore.Transaction(CAS: revision=0)      [atomic claim]
│           └── Retry up to 10 times on contention
```

### Container IP allocation

```
Agent reconcile → reconcileDesiredInstance → buildRuntimeSpecForInstance
├── networkProvider.AllocateIP(ctx, nodeID, instanceID)
│   └── host-local IPAM (CNI plugin) assigns next free IP in node's /24
└── runtime.Start(spec)
    └── cniNetwork.Setup(ctx, instanceID, netnsPath)
        └── CNI bridge plugin creates veth pair, assigns IP, adds to cca0
```

**Store keys:**
| Key | Written By | Value |
|---|---|---|
| `network/node/{nodeID}/subnet` | VXLANReconciler | `10.100.N.0/24` |
| `observed/instance/{id}/ip` | Agent (publishInstanceStateToStore) | `10.100.N.M` |
| `network/vip/service/{name}` | NetworkController | `10.200.0.X` |
| `network/vip/service/{name}/port` | NetworkController | port number |

---

## DNS Resolution Chain

### Full DNS path

```
Container process
│
│ reads /etc/resolv.conf:
│   nameserver {agent-advertise-address}
│   search cca.local
│
├── lookup "metadata-db.cca.local"
│   │
│   └──[UDP]→ DNSForwarder @ {agent-IP}:53
│              │
│              ├── cache hit? → return cached response (TTL ≤ 30s)
│              │
│              ├── name ends in ".cca.local"?
│              │   └── YES → forward to ccaUpstream
│              │       │
│              │       └──[UDP]→ DNSServer @ {control-plane-IP}:15353
│              │                  │
│              │                  ├── strip ".cca.local" → serviceName
│              │                  ├── resolver.ResolveVIP(serviceName)
│              │                  │   └── factStore.Get(network/vip/service/{name})
│              │                  │
│              │                  ├── IF VIP found:
│              │                  │   └── reply A record: VIP, TTL=30s
│              │                  └── IF not found:
│              │                      └── reply NXDOMAIN
│              │
│              └── name does NOT end in ".cca.local"?
│                  └── forward to systemUpstream
│                      └── first nameserver from /etc/resolv.conf
│                          (excluding agent's own address to prevent loop)
│
└── lookup "metadata-db-0.cca.local" (stateful per-instance DNS)
    └── same path, but DNSServer resolves via VIP
        (per-instance DNS names are mapped to VIPs by StatefulDNSController)
```

### DNS components

| Component | Location | Listen Address | Purpose |
|---|---|---|---|
| **DNSServer** | Control plane | `:15353` (UDP) | Resolves `*.cca.local` → VIP using store-backed resolver |
| **DNSForwarder** | Each agent node | `{advertiseAddress}:53` (UDP) | Caching split-horizon forwarder |
| **StoreBackedResolver** | Control plane | (library) | Reads `network/vip/service/*` and `endpoint/service/*` from store |

### DNS data flow (how VIP records get created)

```
User runs: cca apply service.cca
│
├── writes desired/service/{name}/ports  [exposed port]
│
├── NetworkController.Reconcile
│   ├── reads desired/service/{name}/ports
│   ├── allocates VIP from --vip-cidr pool
│   └── writes:
│       ├── network/vip/service/{name}       = "10.200.0.1"
│       ├── network/vip/service/{name}/port  = 5432
│       └── network/dns/{name}               = "10.200.0.1"
│
└── DNSServer serves A record for {name}.cca.local → 10.200.0.1
```

### Stateful per-instance DNS (StatefulDNSController)

```
StatefulDNSController.Reconcile
├── reads effective/service/{name}/stateful   [is service stateful?]
├── reads observed/instance/{id}/state        [must be running]
├── reads observed/instance/{id}/ip           [must have IP]
├── reads observed/instance/{id}/ordinal      [must have ordinal]
└── writes network/dns/instance/{id}
    = "{service}-{ordinal}.cca.local={ipAddress}"

Example: "metadata-db-0.cca.local=10.100.1.2"
```

### Container resolv.conf injection

```
runtime/containerd.go:500  (during container start)
├── generateResolvConf(dnsAddress, searchDomain)
│   → "nameserver {agent-advertise-address}\nsearch cca.local\n"
├── write to /var/lib/ccattler/logs/{instanceID}/config/resolv.conf
└── bind-mount into container at /etc/resolv.conf
```

**Source:** `runtime/containerd.go:610` `generateResolvConf()`

---

## VXLAN Overlay

### Purpose

The VXLAN overlay enables container-to-container communication across nodes.
Without it, container IPs (e.g. 10.100.1.2) are only reachable from the node
that hosts them. The overlay encapsulates L2 frames in UDP (port 4789) and
sends them to the destination node's physical IP.

### Interface creation

```
Agent startup → vxlanReconciler.ensureVXLANInterfaceExists
├── interfaceAlreadyExists("cca-vxlan0")?
│   └── YES: skip
│
├── detectPhysicalInterfaceForAddress(advertiseAddress)
│   └── scans net.Interfaces() to find which NIC has the agent's IP
│
├── ip link add cca-vxlan0 type vxlan
│     id 42                          [VNI — shared by all cluster nodes]
│     dstport 4789                   [standard VXLAN UDP port]
│     dev {physicalInterface}        [underlay device]
│     local {advertiseAddress}       [local VTEP address]
│
├── ip link set cca-vxlan0 address 02:XX:XX:XX:XX:XX
│   └── MAC derived from FNV32a hash of nodeID (locally-administered)
│
├── ip link set cca-vxlan0 mtu 1450
│   └── 1500 - 50 bytes VXLAN encapsulation overhead
│
├── ip link set cca-vxlan0 up
│
└── sysctl -w net.ipv4.conf.cca-vxlan0.proxy_arp=1
    └── Required for cross-node L2 resolution: when a remote node
        ARPs for a container IP on this node's bridge (cca0), proxy
        ARP lets the kernel respond on cca-vxlan0 on behalf of the
        container, completing the ARP → FDB → VXLAN tunnel chain.
```

**Constants (agent/vxlan.go):**
| Constant | Value | Purpose |
|---|---|---|
| `vxlanInterfaceName` | `"cca-vxlan0"` | VXLAN tunnel interface name |
| `vxlanVNI` | `42` | VXLAN Network Identifier (shared cluster-wide) |
| `vxlanDestinationPort` | `4789` | UDP port for encapsulated traffic |
| `vxlanBridgeMTU` | `1450` | MTU (1500 - 50 byte overhead) |

### Peer discovery and route programming

```
vxlanReconciler.Reconcile (called every 1s from dataPlaneLoop)
│
├── discoverPeerNodes
│   ├── factStore.Scan(observed/node/*)
│   │   └── builds map[nodeID] → advertiseAddress
│   ├── factStore.Scan(network/node/*)
│   │   └── builds map[nodeID] → subnetCIDR
│   └── excludes self (local nodeID)
│   └── returns []peerNodeInfo{nodeID, advertiseAddress, subnetCIDR}
│
├── programPeerRoutes (for each peer — Flannel model)
│   ├── deriveVXLANMACAddress(peer.nodeID) → deterministic MAC
│   ├── ip neigh replace {subnetBase} dev cca-vxlan0 lladdr {peerMAC} nud permanent
│   │   └── e.g. "ip neigh replace 10.100.1.0 dev cca-vxlan0 lladdr 02:0e:d2:21:7d:bc nud permanent"
│   ├── ip route replace {peerSubnet} via {subnetBase} dev cca-vxlan0 onlink
│   │   └── e.g. "ip route replace 10.100.1.0/24 via 10.100.1.0 dev cca-vxlan0 onlink"
│   ├── bridge fdb append 00:00:00:00:00:00 dev cca-vxlan0 dst {peerAddress}
│   │   └── BUM flood entry, skips if exists
│   └── bridge fdb replace {peerMAC} dev cca-vxlan0 dst {peerAddress}
│       └── unicast FDB: known-destination traffic goes direct to peer VTEP
│
└── removeStaleRoutes
    ├── ip route show dev cca-vxlan0 → installed subnets
    ├── ip route del {staleSubnet} dev cca-vxlan0
    └── ip neigh del {staleGateway} dev cca-vxlan0
```

### Store keys for VXLAN

| Key Pattern | Written By | Read By | Value |
|---|---|---|---|
| `network/node/{nodeID}/subnet` | VXLANReconciler.EnsureSubnetPublished | VXLANReconciler.discoverPeerNodes | `10.100.N.0/24` |
| `observed/node/{nodeID}/address` | DataPlaneReconciler.PublishAdvertiseAddress | VXLANReconciler.discoverPeerNodes | `192.168.124.20` |

---

## Cross-Node Packet Path

### Container-A (node-1) → Container-B (node-2)

```
Container-A (10.100.1.2) on node-1
│
│ sends packet to 10.100.3.4 (Container-B on node-2)
│
├── [1] eth0 (container netns) → vethXXXX → cca0 bridge (node-1)
│   └── bridge sees dst IP 10.100.3.4, not in bridge FDB
│   └── packet exits bridge to routing table
│
├── [2] Linux routing table on node-1:
│   │  10.100.3.0/24 via 10.100.3.0 dev cca-vxlan0 onlink
│   └── matches → forward via gateway 10.100.3.0 on cca-vxlan0
│
├── [3] L2 resolution via static neighbor (Flannel model — no ARP needed):
│   │  kernel looks up next-hop 10.100.3.0 in neighbor table
│   │  permanent entry: 10.100.3.0 → node-2's cca-vxlan0 MAC (derived from nodeID)
│   └── no runtime ARP, no proxy ARP dependency
│
├── [4] cca-vxlan0 looks up FDB for destination MAC:
│   │  unicast FDB: {node-2-vxlan-MAC} dev cca-vxlan0 dst 192.168.124.30
│   └── VXLAN encapsulation:
│       ┌─────────────────────────────────────────────────┐
│       │ Outer Ethernet: node-1 MAC → next-hop MAC       │
│       │ Outer IP:       192.168.124.20 → 192.168.124.30 │
│       │ Outer UDP:      srcPort:hash → dstPort:4789      │
│       │ VXLAN Header:   VNI=42                           │
│       │ Inner Ethernet: veth-A MAC → ??? (BUM flood)     │
│       │ Inner IP:       10.100.1.2 → 10.100.3.4          │
│       └─────────────────────────────────────────────────┘
│
├── [5] Physical network: node-1 eth0 → switch → node-2 eth0
│
├── [6] node-2 receives UDP 4789, decapsulates VXLAN header
│   └── inner frame delivered to cca-vxlan0
│
├── [7] Linux routing table on node-2:
│   │  10.100.3.0/24 dev cca0 (local bridge)
│   └── matches → forward to cca0 bridge
│
└── [8] cca0 bridge delivers to vethYYYY → Container-B eth0
```

### Diagram

```
  node-1 (192.168.124.20)               node-2 (192.168.124.30)
  ┌──────────────────────┐               ┌──────────────────────┐
  │ Container-A          │               │ Container-B          │
  │ 10.100.1.2           │               │ 10.100.3.4           │
  │      │               │               │      ▲               │
  │  [vethXXXX]          │               │  [vethYYYY]          │
  │      │               │               │      │               │
  │  [cca0 bridge]       │               │  [cca0 bridge]       │
  │  10.100.1.1/24       │               │  10.100.3.1/24       │
  │      │               │               │      ▲               │
  │  routing table       │               │  routing table       │
  │      │               │               │      │               │
  │  [cca-vxlan0]        │               │  [cca-vxlan0]        │
  │  VNI=42, MTU=1450    │               │  VNI=42, MTU=1450    │
  │      │               │               │      ▲               │
  │  [eth0]              │               │  [eth0]              │
  │  192.168.124.20      │               │  192.168.124.30      │
  └──────┼───────────────┘               └──────┼───────────────┘
         │         UDP 4789 VXLAN tunnel         │
         └───────────────────────────────────────┘
```

---

## VIP Data Plane (Service Load Balancing)

### How VIPs become routable

VIPs are IP addresses (from `--vip-cidr`, default `10.200.0.0/24`) that exist
only as nftables/iptables DNAT targets. They are **not** assigned to any
container. The data plane makes them work:

```
Client (container or external)
│
│ connects to 10.200.0.1:5432 (VIP for "metadata-db")
│
├── [1] Packet hits nftables/iptables PREROUTING or OUTPUT chain
│   └── Lookup in verdict map: {10.200.0.1, tcp, 5432} → chain svc_metadata_db
│
├── [2] svc_metadata_db chain:
│   └── dnat to numgen inc mod N map {
│         0 : 10.100.1.2:5432,   ← container IP (local backend)
│         1 : 192.168.124.30:32001   ← node IP:hostPort (remote backend)
│       }
│   └── Round-robin via numgen counter
│
├── [3] DNAT rewrites dst to chosen backend
│
├── [4] POSTROUTING: masquerade (source NAT for return path)
│
└── [5] Packet reaches backend container
```

### nftables table layout

```
table ip cca {
    chain prerouting {
        type nat hook prerouting priority dstnat; policy accept;
        ip daddr . ip protocol . th dport vmap @vip_dispatch
    }
    chain output {
        type nat hook output priority dstnat; policy accept;
        ip daddr . ip protocol . th dport vmap @vip_dispatch
    }
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        masquerade
    }

    map vip_dispatch {
        type ipv4_addr . inet_proto . inet_service : verdict
        elements = {
            10.200.0.1 . tcp . 5432 : goto svc_metadata_db,
            10.200.0.2 . tcp . 8080 : goto svc_web,
        }
    }

    chain svc_metadata_db {
        dnat to numgen inc mod 2 map {
            0 : 10.100.1.2 . 5432,
            1 : 192.168.124.30 . 32001,
        }
    }

    chain svc_web { ... }
}
```

### VIP dummy interface

```
cca-vip0 (dummy interface)
├── ip addr add 10.200.0.1/32 dev cca-vip0   [one per service VIP]
├── ip addr add 10.200.0.2/32 dev cca-vip0
└── ip link set cca-vip0 up

Purpose: Linux needs a local address to accept packets destined for the VIP.
Without cca-vip0, the kernel would drop VIP-destined packets as "not for us"
before they reach nftables PREROUTING.
```

**Source:** `network/nftables_dataplane.go` (NftablesDataPlane)
**Alternative:** `network/iptables_dataplane.go` (IptablesDataPlane — same dummy, linear chain walk instead of verdict map)
**Alternative:** `network/ebpf_dataplane.go` (EbpfDataPlane — BPF maps, TC programs)

### Backend resolution (local vs remote)

```
DataPlaneReconciler.resolveServiceBackends(ctx, serviceName)
│
├── FOR EACH endpoint fact (endpoint/service/{name}/{instanceID}/{port}):
│   │
│   ├── Read observed/instance/{id}/node → instanceNodeID
│   │
│   ├── IF staleNodeSet[instanceNodeID]:
│   │   └── SKIP (node heartbeat > 30s stale)
│   │
│   ├── IF instanceNodeID == myNodeID:
│   │   └── resolveLocalBackend
│   │       ├── Read observed/instance/{id}/ip → container bridge IP
│   │       └── Backend = {containerIP}:{containerPort}
│   │       (direct via cca0 bridge — no NAT hairpin)
│   │
│   └── IF instanceNodeID != myNodeID:
│       └── resolveRemoteBackend
│           ├── [PREFERRED] Read observed/instance/{id}/ip → overlayIP
│           │   └── IF overlayIP available AND containerPort > 0:
│           │       └── Backend = {overlayIP}:{containerPort}
│           │       (routable cross-node via VXLAN tunnel + proxy ARP)
│           │
│           └── [FALLBACK] No overlay IP available:
│               ├── Read observed/node/{remoteNodeID}/address → nodeIP
│               ├── Read observed/instance/{id}/host_port/{port} → hostPort
│               └── Backend = {nodeIP}:{hostPort}
│               (via LAN to remote node's host port mapping)
```

---

## Port Exposure

### How containers become reachable from outside the cluster

```
Service definition (.cca file):
  service web {
      ports 8080
      expose { port 8080 }
  }

Two paths:

1. VIP (intra-cluster)
   ┌─────────────┐         ┌─────────────┐
   │ Client Pod   │─VIP───→│ nftables    │──DNAT──→ container:8080
   │ 10.100.1.5   │ 10.200 │ cca table   │         or node:hostPort
   └─────────────┘ .0.1:   └─────────────┘
                   8080

2. Host Port (external access)
   ┌─────────────┐         ┌─────────────┐
   │ External     │─────── │ Node LAN IP │──portmap──→ container:8080
   │ client       │  :32001│ 192.168.    │  (CNI
   └─────────────┘        │ 124.20      │  portmap
                          └─────────────┘  plugin)
```

### Host port mapping flow

```
Agent: startInstanceAndPublishState
├── runtime.Start(spec) → CNI portmap plugin assigns host port
├── HostPortsForInstance(instanceID) → map[containerPort]hostPort
├── publishInstanceHostPorts
│   └── factStore.Put(observed/instance/{id}/host_port/{containerPort}, hostPort)
│
EndpointController.Reconcile
├── reads observed/instance/{id}/host_port/*
├── reads observed/node/{nodeID}/address
└── writes endpoint/service/{name}/{instanceID}/{port} = "{nodeIP}:{hostPort}"
    (or "{containerIP}:{containerPort}" if using overlay)
```

### Store keys for port exposure

| Key Pattern | Written By | Read By | Value |
|---|---|---|---|
| `observed/instance/{id}/host_port/{port}` | Agent (publishInstanceHostPorts) | DataPlaneReconciler, EndpointController | `32001` |
| `endpoint/service/{name}/{id}/{port}` | EndpointController | DataPlaneReconciler, StoreBackedResolver | `10.100.1.2:8080` or `192.168.124.20:32001` |
| `observed/node/{nodeID}/address` | DataPlaneReconciler.PublishAdvertiseAddress | EndpointController, VXLANReconciler | `192.168.124.20` |

---

## Complete Data Flow: Deploy to Routable

End-to-end: from `cca apply` to a container being reachable via VIP.

```
User: cca apply service.cca
│
├── [1] desired/service/web/ports = "8080"
│   desired/service/web/image = "nginx:latest"
│   desired/service/web/instances = "2"
│
├── [2] InstanceController writes observed/instance/web-{hash}/state = pending
│
├── [3] NetworkController:
│   ├── allocates VIP: network/vip/service/web = 10.200.0.1
│   ├── writes port:  network/vip/service/web/port = 8080
│   └── writes DNS:   network/dns/web = 10.200.0.1
│
├── [4] Scheduler writes placement/node/{nodeID}/web-{hash}
│
├── [5] Agent on nodeID sees placement, runs reconcileDesiredInstance:
│   ├── pulls image
│   ├── runtime.Start → CNI setup:
│   │   ├── creates netns, veth pair
│   │   ├── attaches veth to cca0 bridge
│   │   ├── host-local IPAM assigns 10.100.N.M
│   │   └── portmap plugin maps containerPort 8080 → hostPort 32001
│   ├── publishes:
│   │   ├── observed/instance/web-{hash}/state = running
│   │   ├── observed/instance/web-{hash}/ip = 10.100.N.M
│   │   ├── observed/instance/web-{hash}/node = {nodeID}
│   │   └── observed/instance/web-{hash}/host_port/8080 = 32001
│   └── injects /etc/resolv.conf → nameserver {agentIP}, search cca.local
│
├── [6] EndpointController:
│   ├── sees running instance with IP and port
│   └── writes endpoint/service/web/web-{hash}/8080 = "10.100.N.M:8080"
│
├── [7] DataPlaneReconciler on EVERY node (1s cycle):
│   ├── reads network/vip/service/web → VIP 10.200.0.1
│   ├── reads endpoint/service/web/* → backends
│   ├── resolves backends:
│   │   ├── local: containerIP:containerPort (direct bridge)
│   │   └── remote: nodeIP:hostPort (LAN)
│   ├── programs nftables:
│   │   ├── ensures cca-vip0 has 10.200.0.1/32
│   │   └── verdict map entry + DNAT chain
│   └── VIP is now routable from any container on any node
│
└── [8] Any container can now:
    ├── dig web.cca.local → 10.200.0.1
    └── connect to 10.200.0.1:8080 → DNAT → backend
```

---

## Route-Based Traffic (Sticky Sessions)

Routes override VIP dispatch for specific services, enabling sticky sessions.

```
Route definition:
  route web-public {
      service web
      sticky source-ip     ← OR sticky cookie { name "session" }
  }

nftables chain override:

map vip_dispatch {
    10.200.0.1 . tcp . 8080 : goto rt_web_public     ← route chain instead of svc_web
}

chain rt_web_public {
    ── source-ip sticky:
       jhash ip saddr mod N map { 0: dnat to backend-0, 1: dnat to backend-1 }

    ── cookie sticky:
       dnat to 127.0.0.1:{proxyPort}
       └── CookieStickyProxy reverse-proxies with Set-Cookie affinity
}
```

**Source:** `network/nftables_dataplane.go` (route chains),
`network/cookie_proxy.go` (CookieStickyProxy)

---

## Network Policy Enforcement

When a DataPlanePolicyReconciler is present (eBPF data plane):

```
DataPlaneReconciler.Reconcile
└── policyReconciler.ReconcilePolicies(ctx)
    └── reads derived/network/rule/* → compiled allow/deny rules
    └── programs BPF maps for per-packet enforcement
```

**Source:** `network/dataplane.go:122` (DataPlanePolicyReconciler interface),
`network/ebpf_dataplane.go` (BPF implementation)

---

## Reverse Index: Interface → Source File

| Interface/Component | Source File | Key Function |
|---|---|---|
| `cca0` bridge | `runtime/containerd.go:560` | `buildCNIConflist()` |
| `cca-vxlan0` | `agent/vxlan.go:152` | `ensureVXLANInterfaceExists()` |
| `cca-vip0` dummy | `network/iptables_dataplane.go:323` / `network/nftables_dataplane.go:444` | `ensureDummyInterface()` |
| veth pairs | CNI bridge plugin (external binary at `/opt/cni/bin/bridge`) | — |
| Container resolv.conf | `runtime/containerd.go:500` | `generateResolvConf()` |
| DNS Forwarder | `network/dns_forwarder.go:92` | `DNSForwarder.Start()` |
| DNS Server | `network/dns.go:56` | `DNSServer.Start()` |
| VIP verdict map | `network/nftables_dataplane.go:81` | `ReconcileVIPDataPlane()` |
| FDB entries | `agent/vxlan.go:560` | `executeBridgeFDBAppend()` |
| Route tables | `agent/vxlan.go:536` | `executeIPRouteReplace()` |
| Store-backed resolver | `network/resolver.go:40` | `StoreBackedResolver` |
| Endpoint facts | `controllers/endpoint.go:54` | `EndpointController.Reconcile()` |
| VIP allocation | `controllers/network.go:88` | `NetworkController.Reconcile()` |
| Stateful DNS | `controllers/stateful_dns.go:49` | `StatefulDNSController.Reconcile()` |
