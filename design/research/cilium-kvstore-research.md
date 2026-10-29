# Cilium KVStore Mode Integration with External Orchestrators (e.g., CCattler)

## Executive Summary

Cilium **does** support standalone kvstore-backed operation (etcd as the sole state backend) without Kubernetes, making it suitable for integration with alternative orchestrators like CCattler. However, Cilium was historically designed for Kubernetes; modern versions (v1.14+) emphasize CRD-based state and have reduced standalone kvstore support in favor of clustermesh deployments.

**Minimum Recommended Version:** Cilium **v1.14.x** or **v1.15.x** supports kvstore-backed identity allocation and state synchronization. Earlier versions (v1.10–v1.13) had more robust standalone kvstore implementations but are reaching EOL.

---

## 1. Cilium KVStore Architecture & Key Prefixes

### 1.1 Root Prefix Structure

All Cilium state lives under a hierarchical etcd prefix:

| Prefix | Purpose | Lease | Notes |
|--------|---------|-------|-------|
| cilium/ | Root for all Cilium state | — | Base namespace |
| cilium/state/ | Operational state; auto-recreated on loss | — | Agent state rebuilds here |
| cilium/cache/ | Remote cluster data cached locally | — | Used by KVStoreMesh multi-cluster |
| cilium/.initlock | Distributed init lock | 25s (lock lease) | Tests quorum during startup |
| cilium/.heartbeat | Operator heartbeat timestamp | — | Agents use this to detect kvstore health |
| cilium/cluster-config/ | Cluster-wide configuration | — | Multi-cluster settings |
| cilium/synced/ | Synchronization status flags | — | External source sync completion |

### 1.2 Identity Key Layout

Path: cilium/state/identities/v1/...

Cilium uses a dual-key allocator model for distributed identity management:

#### Master Keys (Authoritative Identity to Key Mapping)

- **Path:** cilium/state/identities/v1/id/<numeric-identity>
- **Value:** Identity key string (see "Identity Keys" below)
- **Lease:** NONE — permanent; garbage collected by cilium-operator
- **Example:** cilium/state/identities/v1/id/1001 → k8s:namespace=default;k8s:app=frontend;

Interpretation: Identity 1001 is allocated to endpoints with labels matching the key string.

#### Slave Keys (Per-Node Reference Tracking)

- **Path:** cilium/state/identities/v1/value/<key>/<node-name>
- **Value:** Numeric identity (1001)
- **Lease:** 15 minutes (default), TTL typically 30 minutes — automatically cleaned up if node fails
- **Example:** cilium/state/identities/v1/value/k8s:namespace=default;k8s:app=frontend;/node-1 → 1001

Interpretation: Node-1 has allocated identity 1001 for the given label set. When lease expires, entry vanishes and can be garbage collected.

#### Lock Prefix (Distributed Locks for ID Allocation)

- **Path:** cilium/state/identities/v1/locks/...
- **Lease:** 25 seconds (lock-specific; separate from data leases)
- **Purpose:** Prevent race conditions during concurrent ID allocation across nodes

### 1.3 Identity Key Format (KVStore Label Encoding)

Identity keys use a sorted, semicolon-delimited label format:

```
source:key1=value1;source:key2=value2;source:key3=...;
```

Key Rules:
1. Sorted deterministically by source:key pairs to ensure same label set always produces same key
2. Semicolons terminate each entry — format is load-bearing in kvstore
3. No spaces — all whitespace stripped
4. Label sources (see § 1.4) distinguish origin: k8s, reserved, cidr, fqdn, node, container

Examples:

```
# Kubernetes pod labels
k8s:namespace=default;k8s:app=frontend;k8s:version=v1;

# CIDR-based identity (for external access)
cidr:10.0.0.0/8;

# Host/Node identity
reserved:host;

# Mixed sources (pod + CIDR)
cidr:172.20.0.0/16;k8s:namespace=prod;k8s:tier=db;
```

#### Label Sources

- reserved — Well-known identities: host, world, world-ipv4, world-ipv6, health, init, kube-apiserver, ingress
- k8s — Kubernetes label selectors (pod, namespace, service account)
- cidr — IP CIDR ranges (endpoints outside the cluster)
- fqdn — DNS FQDN-resolved endpoints (for Cilium DNS policy)
- node — Remote node labels in multi-cluster scenarios
- container — Container or process labels (for standalone/non-K8s orchestrators)

### 1.4 Endpoint / IP Identity Mapping

Path: cilium/state/ip/v1/<cluster>/<ip-address>

Maps container/endpoint IPs to their numeric identities and metadata.

Value: IPIdentityPair JSON structure (see § 1.5)

Example:
```
Key: cilium/state/ip/v1/default/10.100.1.42
Value: {
  "IP": "10.100.1.42",
  "HostIP": "192.168.1.10",
  "ID": 1001,
  "Key": "k8s:namespace=default;k8s:app=frontend;",
  "Metadata": "reserved:ingress",
  "NamedPorts": [
    {"Name": "http", "Port": 8080, "Protocol": "TCP"}
  ]
}
```

Lease: 15 minutes (default) — expires if endpoint stops heartbeating

### 1.5 Node Registration

Path: cilium/state/nodes/v1/<cluster>/<node-name>

Each agent registers itself with runtime information.

Value: JSON node metadata
```json
{
  "name": "node-1",
  "cluster": "default",
  "addresses": [
    {"type": "InternalIP", "address": "192.168.1.10"},
    {"type": "CiliumInternalIP", "address": "10.100.0.1"}
  ],
  "health_check_ip": "10.100.0.1",
  "pod_cidr": "10.100.1.0/24",
  "ipv6_pod_cidr": "fd00:100:1::/64"
}
```

Lease: 15 minutes — node must renew periodically

### 1.6 Endpoint Details (Optional, Advanced)

Path: cilium/state/endpoints/v1/<cluster>/<namespace>/<endpoint-id>

Detailed per-endpoint state (labels, policy revision, realized policies). Less commonly stored in etcd in modern Cilium; mostly cached locally on agents.

---

## 2. Service & Load Balancing Keys (Optional for CCattler)

Cilium's service/backend model is strongly Kubernetes-centric in modern versions. Standalone orchestrators typically don't need to populate these keys unless integrating with Cilium's service proxy features.

### 2.1 Service Definition

Path: cilium/state/services/v1/<cluster>/<namespace>/<service-name>

Mirrors Kubernetes service objects into etcd (populated by cilium-operator).

Value: JSON service definition
```json
{
  "name": "frontend",
  "namespace": "default",
  "cluster": "default",
  "frontend": {
    "address": "10.200.0.100",
    "port": 80,
    "protocol": "TCP"
  },
  "backends": [
    {"address": "10.100.1.10", "port": 8080},
    {"address": "10.100.2.20", "port": 8080}
  ],
  "traffic_policy": "Cluster",
  "type": "ClusterIP"
}
```

### 2.2 Backend State

Path: cilium/state/backends/v1/<cluster>/<backend-id>

Tracks backend endpoint status (Active, Terminating, Quarantined, etc.) for graceful shutdowns and health checks.

Value: Backend state JSON
```json
{
  "id": "<hash>",
  "service": "frontend",
  "address": "10.100.1.10:8080",
  "state": "Active",
  "failures": 0
}
```

---

## 3. Cilium Standalone Mode: Configuration

### 3.1 cilium-agent CLI Flags for KVStore Mode

```bash
cilium-agent \
  --kvstore etcd \
  --kvstore-opt etcd.endpoints=https://10.0.0.1:2379,https://10.0.0.2:2379 \
  --kvstore-opt etcd.ca=/path/to/ca.crt \
  --kvstore-opt etcd.key=/path/to/client.key \
  --kvstore-opt etcd.cert=/path/to/client.crt \
  --identity-allocation-mode kvstore \
  --enable-k8s=false \
  --k8s-api-server="" \
  --cluster-name default \
  --cluster-id 0 \
  --node-name $(hostname) \
  --advertise-address 192.168.1.10
```

#### Key Flags Explained

| Flag | Value | Purpose |
|------|-------|---------|
| --kvstore | etcd | Use etcd as backing store |
| --kvstore-opt | etcd.endpoints=<urls> | etcd server endpoints (comma-separated) |
| --kvstore-opt | etcd.ca=<path> | CA certificate for TLS verification |
| --kvstore-opt | etcd.key=<path> | Client key for mTLS |
| --kvstore-opt | etcd.cert=<path> | Client certificate for mTLS |
| --identity-allocation-mode | kvstore | Use etcd (not CRDs) for identity allocation |
| --enable-k8s | false | Disable Kubernetes API communication |
| --k8s-api-server | "" | Disable K8s discovery |
| --cluster-name | <name> | Cluster identifier (default: default) |
| --cluster-id | <0-255> | Numeric cluster ID for multi-cluster (default: 0) |
| --node-name | <hostname> | Unique node identifier |
| --advertise-address | <ip> | IP for inter-node communication |

#### etcd Connection Options

All options use prefix --kvstore-opt etcd.:

- endpoints — Comma-separated etcd URLs (required)
- ca — Path to CA certificate file
- key — Path to client private key
- cert — Path to client certificate
- dial-timeout — Connection timeout (default: 30s)
- dial-keepalive — Keep-alive interval (default: 5m)
- command-timeout — Operation timeout (default: 5s)

### 3.2 IPAM Configuration for Non-Kubernetes Mode

In standalone mode, configure IPAM without Kubernetes node discovery:

```bash
cilium-agent \
  --ipam kubernetes \
  --ipam-cilium-crd=false \
  --enable-local-node-route=true \
  --node-address 10.100.1.0/24
```

Alternatively, use delegated IPAM if separate IPAM controller manages allocations:

```bash
cilium-agent \
  --ipam plugin
```

### 3.3 cilium-operator Configuration

cilium-operator watches identities and performs garbage collection. For standalone:

```bash
cilium-operator \
  --kvstore etcd \
  --kvstore-opt etcd.endpoints=https://10.0.0.1:2379 \
  --kvstore-opt etcd.ca=/path/to/ca.crt \
  --kvstore-opt etcd.key=/path/to/client.key \
  --kvstore-opt etcd.cert=/path/to/client.crt \
  --cluster-name default \
  --identity-allocation-mode kvstore \
  --enable-k8s=false \
  --skip-crd-creation=true
```

---

## 4. Cilium Identity Model for Non-Kubernetes Orchestrators

### 4.1 Identity Allocation Without Kubernetes

Without Kubernetes API and pod labels, Cilium derives identities from explicitly assigned labels. For CCattler, you must:

1. Assign labels to each container/endpoint via CCattler/Cilium integration API or configuration
2. Include label source prefix (e.g., container: or custom source)
3. Ensure sorted label strings match Cilium kvstore key format

### 4.2 Label Assignment Flow

```
CCattler → Cilium kvstore

1. User defines workload with labels

2. Translate labels to Cilium format:
   container:env=prod;container:tier=frontend;container:version=v1;

3. Call Cilium identity allocator:
   AllocateIdentity(key=...) → Returns ID 1042

4. Cilium writes master key:
   cilium/state/identities/v1/id/1042 = label_string

5. Cilium writes slave key:
   cilium/state/identities/v1/value/label_string/node-1 = 1042

6. Update endpoint IP mapping:
   cilium/state/ip/v1/default/10.100.1.42 = JSON_with_id
```

### 4.3 Fixed Identity (Optional)

For system endpoints that always need specific identity:

```json
{
  "name": "dns-resolver",
  "labels": {
    "io.cilium.fixed-identity": "64",
    "component": "coredns"
  }
}
```

This directly assigns identity 64, bypassing the allocator.

---

## 5. Version Compatibility

### Cilium Version Support Matrix

| Version | kvstore Support | K8s-Free | Notes |
|---------|-----------------|----------|-------|
| v1.10–v1.12 | Full | Yes | Robust standalone; reaching EOL |
| v1.13 | Good | Partial | Some K8s APIs still expected |
| v1.14–v1.15 | Good | Yes | Recommended for new deployments |
| v1.16+ | Good | Yes | Multi-cluster emphasis |

**Minimum etcd version:** etcd v3.5+ (Cilium v1.14+ uses etcd client v3.7.2)

---

## 6. Integration Checklist for CCattler

- Deploy etcd cluster (minimum 1 node for testing; 3+ for HA)
- Deploy Cilium agents with --kvstore etcd --enable-k8s=false
- Implement label translation (CCattler metadata → Cilium format)
- Write IP-to-Identity mappings to cilium/state/ip/v1/...
- Manage identity lifecycle via allocator
- Monitor cilium/.heartbeat for kvstore health
- Handle label changes at runtime
- Configure TLS for Cilium-to-etcd communication
- Test policy enforcement via Cilium's eBPF dataplane

---

## 7. Known Limitations & Caveats

1. **No multi-cluster isolation by default** — Without clustermesh operator, identity conflicts may arise if multiple CCattler clusters use same etcd. Use --cluster-id and separate prefixes to isolate.

2. **Service object sync minimal** — Cilium's service kvstore keys assume Kubernetes API input. CCattler must either write service entries manually or skip service kvstore sync if CCattler handles load balancing independently.

3. **Node agent coupling** — cilium-agent expects to run on each node. CCattler must ensure each node runs a cilium-agent instance.

4. **IPSec integration complexity** — If IPSec encryption enabled, manual kvstore state reconstruction can break tunnel connectivity. Test carefully.

5. **Label source confusion** — If containers have both k8s: and container: labels, Cilium treats them as separate identities. Use single, consistent label source per orchestrator.

6. **Operator Garbage Collection** — cilium-operator periodically collects stale identities. Ensure operator runs and can access etcd. Master keys without slave-key backing marked for deletion after 2 GC cycles.

---

## 8. References

- Cilium kvstore.rst Documentation: https://github.com/cilium/cilium/blob/main/Documentation/kvstore.rst
- Identity Model: https://github.com/cilium/cilium/blob/main/pkg/identity/identity.go
- Allocator Internals: https://github.com/cilium/cilium/blob/main/pkg/kvstore/allocator/allocator.go
- Labels & KVStore Format: https://github.com/cilium/cilium/blob/main/pkg/labels/labels.go

