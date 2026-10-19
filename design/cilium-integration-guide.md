# Cilium-CCattler Integration: Quick Reference

## Key Findings

### 1. **Cilium KVStore Prefixes for CCattler**

| Key Path | Purpose | Lease | Required for CCattler? |
|----------|---------|-------|------------------------|
| `cilium/state/identities/v1/id/<id>` | Master: Identity → Label key mapping | None | **YES** (core) |
| `cilium/state/identities/v1/value/<key>/<node>` | Slave: Per-node identity reference | 15m | **YES** (core) |
| `cilium/state/ip/v1/<cluster>/<ip>` | Endpoint IP → Identity mapping | 15m | **YES** (core) |
| `cilium/state/nodes/v1/<cluster>/<node>` | Node registration & CIDR allocation | 15m | **YES** (node mgmt) |
| `cilium/state/services/v1/...` | Kubernetes-style services | — | No (unless LB proxy needed) |
| `cilium/state/backends/v1/...` | Service backend endpoints | — | No (unless LB proxy needed) |
| `cilium/.heartbeat` | Operator health signal | — | No (monitor for health) |

### 2. **Identity Key Format (Load-Bearing)**

Cilium **requires** labels in this exact format:

```
source:key1=value1;source:key2=value2;...;
```

**Rules:**
- Sorted alphabetically by `source:key`
- Semicolons terminate each label
- No spaces
- Example: `container:app=frontend;container:env=prod;container:tier=api;`

**For CCattler:** Implement a label → sorted key converter that produces this format deterministically.

### 3. **Cilium-Agent Minimal Flags for Standalone Mode**

```bash
cilium-agent \
  --kvstore etcd \
  --kvstore-opt etcd.endpoints=https://etcd1:2379,https://etcd2:2379,https://etcd3:2379 \
  --kvstore-opt etcd.ca=/etc/cilium/etcd-ca.crt \
  --kvstore-opt etcd.cert=/etc/cilium/etcd-client.crt \
  --kvstore-opt etcd.key=/etc/cilium/etcd-client.key \
  --identity-allocation-mode kvstore \
  --enable-k8s=false \
  --cluster-name ccattler \
  --cluster-id 0 \
  --node-name $(hostname) \
  --advertise-address $(hostname -I | awk '{print $1}')
```

**Do NOT set:** `--k8s-api-server` or rely on Kubernetes service discovery.

### 4. **Master / Slave Key Dual Model**

Cilium uses a **distributed reference-counting system:**

**Master Key (Authoritative):**
```
Key:   cilium/state/identities/v1/id/1001
Value: container:app=frontend;container:env=prod;
Lease: None (permanent until GC'd)
```
→ "Identity 1001 exists for this label set"

**Slave Keys (Per-Node Leases):**
```
Key:   cilium/state/identities/v1/value/container:app=frontend;container:env=prod;/node-1
Value: 1001
Lease: 15m (expires if node stops renewing)
```
→ "Node-1 has allocated identity 1001; renew every 15m or delete"

**When a slave key expires (no renewal), cilium-operator's GC collects it. When all slave keys for a master key are gone, the master is marked for deletion on the next GC cycle.**

For CCattler: You must ensure containers renew their endpoint leases continuously, or Cilium will garbage-collect their identities.

### 5. **IPIdentityPair JSON Schema**

When writing to `cilium/state/ip/v1/<cluster>/<container-ip>`:

```json
{
  "IP": "10.100.1.42",                        // Required
  "HostIP": "192.168.1.10",                   // Required: node IP
  "ID": 1001,                                 // Required: numeric identity
  "Key": "container:app=frontend;container:env=prod;",  // Required: must match master key
  "Metadata": "reserved:ingress",             // Optional
  "NamedPorts": [                             // Optional: service port mapping
    {"Name": "http", "Port": 8080, "Protocol": "TCP"},
    {"Name": "https", "Port": 8443, "Protocol": "TCP"}
  ]
}
```

**Error if IP doesn't match key:** Cilium validates the IP in the key path equals the IP in the JSON value.

### 6. **cilium-operator Setup (Required)**

Run on at least one node (typically the control plane):

```bash
cilium-operator \
  --kvstore etcd \
  --kvstore-opt etcd.endpoints=https://etcd1:2379,... \
  --kvstore-opt etcd.ca=/etc/cilium/etcd-ca.crt \
  --kvstore-opt etcd.cert=/etc/cilium/etcd-client.crt \
  --kvstore-opt etcd.key=/etc/cilium/etcd-client.key \
  --identity-allocation-mode kvstore \
  --cluster-name ccattler \
  --enable-k8s=false \
  --skip-crd-creation=true
```

**Purpose:** Identity GC, heartbeat updates, leadership election.

### 7. **Identity Allocation Without Kubernetes**

Cilium's identity model **requires labels on each endpoint**. For CCattler:

1. **When creating a container:**
   - Assign labels: `{"tier": "frontend", "app": "nginx", "env": "prod"}`
   - Convert to Cilium format: `container:app=nginx;container:env=prod;container:tier=frontend;`
   - Call allocator or write master/slave keys directly

2. **Fixed Identity (for infrastructure):**
   - Use label `io.cilium.fixed-identity: "64"` to pin identity 64
   - No allocator call needed

3. **CIDR-based identity (for external endpoints):**
   - Use label source `cidr:` for IP ranges
   - Example: `cidr:10.0.0.0/8;reserved:world;` for "any external traffic"

### 8. **Minimum Cilium Version**

- **Recommended:** v1.14.x or v1.15.x
- **Minimum etcd version:** v3.5+
- Cilium's current builds (v1.14+) depend on etcd client library v3.7.2

Earlier versions (v1.10–v1.13) had more robust standalone support but are EOL; avoid unless forced by constraints.

### 9. **Integration Workflow (CCattler → Cilium)**

**On container start:**
```
1. CCattler creates container: {labels: {tier: frontend, app: nginx}}
2. Allocate Cilium identity:
   - Format labels: "container:app=nginx;container:tier=frontend;"
   - Write master key: cilium/state/identities/v1/id/1042 = label_string
   - Write slave key:  cilium/state/identities/v1/value/label_string/node-1 = 1042
3. Get container IP (10.100.1.42) from overlay network
4. Write IP→ID mapping:
   cilium/state/ip/v1/ccattler/10.100.1.42 = {IP, HostIP, ID: 1042, Key: label_string}
5. Cilium-agent picks up mapping from etcd → programs eBPF for this endpoint
```

**On label/pod change at runtime:**
```
1. Update labels in CCattler
2. Derive new Cilium label key
3. Allocate new identity (if key changed) or reuse (if same)
4. Update cilium/state/ip/v1/ccattler/10.100.1.42 with new Key and ID
```

**On container stop:**
```
1. Release identity: call allocator.Release(id) or delete slave key
2. Delete endpoint mapping: rm cilium/state/ip/v1/ccattler/10.100.1.42
3. Cilium-agent sees deletion → removes eBPF rules for this endpoint
```

### 10. **Known Gotchas**

1. **Lease renewal required:** Endpoint IPs expire in 15m if not renewed. CCattler must either:
   - Periodically update the IP entry (refresh TTL), OR
   - Use a long-lived lease, OR
   - Ensure Cilium agents run locally on each node and manage leases

2. **Label sorting is critical:** If `container:tier=frontend;container:app=nginx;` and `container:app=nginx;container:tier=frontend;` are both used, Cilium treats them as different identities. Implement deterministic sorting.

3. **No K8s fallback:** With `--enable-k8s=false`, Cilium will NOT fall back to Kubernetes APIs. All identity and node state must come from kvstore.

4. **Cluster ID isolation:** If multiple CCattler clusters share one etcd, use `--cluster-id 0`, `--cluster-id 1`, etc. to isolate identities.

5. **Service features limited:** Cilium's L4 service proxy and load balancer features assume Kubernetes Services. CCattler must either:
   - Manually write `cilium/state/services/v1/...` entries (complex), OR
   - Implement its own service proxy and skip Cilium's

### 11. **Cilium CLI for Debugging**

On any agent node with Cilium socket access:

```bash
cilium endpoint list                       # Show endpoints and identities
cilium identity list                       # Show allocated identities
cilium kvstore get --recursive cilium/state/identities   # Raw kvstore dump
cilium status                              # Agent and kvstore health
cilium identity info <id>                  # Inspect identity labels
```

---

## File Locations

**Research outputs:**
- `/tmp/cilium_kvstore_research.md` — Full technical reference
- `/tmp/cilium_ccattler_integration_guide.md` — This file (quick reference for CCattler)

---

## Next Steps for CCattler

1. **Write a label → kvstore key converter** that deterministically sorts labels and formats them as `source:key=value;...`
2. **Implement identity allocator wrapper** around Cilium's `pkg/kvstore/allocator` or manual etcd operations
3. **On container lifecycle:**
   - Create: allocate identity, write IP→ID mapping, renew endpoint lease periodically
   - Update labels: reallocate identity if key changes
   - Delete: release identity, delete endpoint mapping
4. **Deploy Cilium agents** on all nodes with the flags above
5. **Deploy cilium-operator** for GC and heartbeat
6. **Test policy enforcement** by applying Cilium network policies and verifying eBPF enforcement

