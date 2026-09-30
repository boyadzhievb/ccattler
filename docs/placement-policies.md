# Placement Policies

CCattler's placement system controls where instances get scheduled across cluster nodes. It uses four mechanisms that map to familiar Kubernetes concepts but with cleaner DSL syntax.

## CCattler vs Kubernetes

| Kubernetes | CCattler | Behavior |
|-----------|---------|----------|
| `nodeSelector` / `requiredDuringScheduling` | `require label = value` | Hard filter — node MUST have the label |
| `preferredDuringScheduling` | `prefer label = value` | Soft score — matching nodes ranked higher |
| Taint (`NoSchedule`) | `restrict label` on node | Blocks scheduling unless service tolerates |
| Toleration | `accept label` on service | Permits scheduling on restricted nodes |
| Pod anti-affinity (topology) | `zone spread` | Distributes instances across zones |
| Architecture constraint | `architecture amd64` | Hard filter — node CPU architecture must match |

---

## `require` — Hard Label Filter

The `require` constraint is the equivalent of Kubernetes `requiredDuringSchedulingIgnoredDuringExecution` node affinity. A node must have every required label with the exact value, or it is excluded from scheduling.

### DSL Syntax

```
service gpu-worker {
    image nvidia/cuda:12.0
    instances 2
    placement {
        require gpu = true
    }
}
```

Multiple require constraints are AND'd — the node must satisfy all of them:

```
placement {
    require gpu = true
    require region = us-east
}
```

### How It Works

```
  node-1              node-2              node-3
  labels: gpu=true    labels: ssd=true    labels: gpu=true
  ┌──────────┐        ┌──────────┐        ┌──────────┐
  │ OK CAN   │        │ X CANNOT │        │ OK CAN   │
  │ schedule │        │ schedule │        │ schedule │
  │ here     │        │ here     │        │ here     │
  └──────────┘        └──────────┘        └──────────┘
       ^                                       ^
       └────── gpu-worker instance(s) ─────────┘
```

Only nodes with `gpu=true` are eligible. The scheduler then picks the least-loaded eligible node.

### Fallback Behavior

If no node satisfies the require constraint, the scheduler falls back to all available nodes rather than leaving instances unscheduled. This prevents deadlocks but means the constraint is "best effort" — the service will run, just not on ideal hardware. Monitor `cca status` to detect constraint violations.

---

## `prefer` — Soft Scoring

The `prefer` constraint is the equivalent of Kubernetes `preferredDuringSchedulingIgnoredDuringExecution`. Nodes matching the preference are ranked higher, but all nodes remain eligible.

### DSL Syntax

```
service analytics {
    image analytics:v3
    instances 3
    placement {
        prefer ssd = true
    }
}
```

Multiple prefer constraints stack — a node matching more preferences scores higher:

```
placement {
    prefer ssd = true
    prefer region = us-east
}
```

### How It Works

```
  node-1              node-2              node-3
  labels: ssd=true    labels: (none)      labels: (none)
  score: 1            score: 0            score: 0
  ┌──────────┐        ┌──────────┐        ┌──────────┐
  │ * FIRST  │        │ OK also  │        │ OK also  │
  │ choice   │        │ eligible │        │ eligible │
  └──────────┘        └──────────┘        └──────────┘
       ^                   ^                   ^
       │                   │                   │
  instance-1          instance-2          instance-3
```

All nodes are eligible. node-1 is ranked first because it matches the `ssd=true` preference. The remaining instances go to least-loaded nodes among the lower-scored candidates.

### Difference from `require`

| | `require` | `prefer` |
|---|---|---|
| Mismatched node | Excluded | Still eligible (lower rank) |
| No matching nodes | Falls back to all | All nodes equally ranked |
| Multiple labels | Must match ALL | Score by match count |

---

## `restrict` + `accept` — Taint/Toleration

CCattler's `restrict` and `accept` are the equivalent of Kubernetes taints and tolerations. A restriction on a node blocks all services unless they explicitly accept that restriction.

### DSL Syntax

Restrictions are set on nodes (via `etcdctl` or `cca label` in future):

```bash
# Mark node-1 as dedicated compute — only tolerating services can use it
etcdctl put /ccattler/observed/node/node-1/restrict/dedicated-compute true
```

Services declare which restrictions they tolerate:

```
service ml-training {
    image tensorflow:latest
    instances 1
    placement {
        accept dedicated-compute
    }
}
```

### How It Works

**Service WITHOUT accept:**

```
  service web-app { instances 1 }    (no accept declaration)

  node-1 (restricted)   node-2              node-3
  ┌──────────────┐      ┌──────────┐        ┌──────────┐
  │  X BLOCKED   │      │ OK CAN   │        │ OK CAN   │
  │  (restricted │      │ schedule │        │ schedule │
  │   node)      │      │ here     │        │ here     │
  └──────────────┘      └──────────┘        └──────────┘
```

**Service WITH accept:**

```
  service ml-job { placement { accept dedicated-compute } }

  node-1 (restricted)   node-2              node-3
  ┌──────────────┐      ┌──────────┐        ┌──────────┐
  │ OK ALLOWED   │      │ OK CAN   │        │ OK CAN   │
  │ (service     │      │ schedule │        │ schedule │
  │  tolerates)  │      │ here     │        │ here     │
  └──────────────┘      └──────────┘        └──────────┘
```

### Important: `accept` is Permission, Not a Directive

Like Kubernetes tolerations, CCattler's `accept` grants permission to schedule on a restricted node — it does not force the scheduler to place there. The scheduler still considers load balancing and other constraints. To guarantee placement on a specific node, combine `accept` with `require`.

### Multiple Restrictions

A node can have multiple restrictions. A service must accept ALL of them to be eligible:

```bash
etcdctl put /ccattler/observed/node/node-1/restrict/gpu-pool true
etcdctl put /ccattler/observed/node/node-1/restrict/high-memory true
```

```
service big-model {
    placement {
        accept gpu-pool
        accept high-memory
    }
}
```

---

## Combining Constraints — Reserving a Node

Neither `require` alone nor `restrict` alone can fully reserve a node for a specific service:

- **`require` alone** attracts the right service but does not block other services
- **`restrict` alone** blocks other services but does not attract the right one

The combination of both provides full scheduling control:

```
Goal: Only ml-job can run on node-1 (the GPU node)

Setup:
  node-1: labels gpu=true, restrict dedicated-gpu
  node-2: labels (none)
  node-3: labels (none)
```

```
service ml-job {
    placement {
        require gpu = true        # attracts to node-1
        accept dedicated-gpu      # tolerates restriction
    }
}

service web-app { instances 3 }   # no require, no accept
```

```
  node-1                   node-2              node-3
  gpu=true                 (no labels)         (no labels)
  restrict: dedicated-gpu
  ┌─────────────────┐      ┌──────────┐        ┌──────────┐
  │ ml-job OK       │      │ web-app  │        │ web-app  │
  │ (has require +  │      │ OK       │        │ OK       │
  │  accept)        │      │          │        │          │
  │                 │      │          │        │          │
  │ web-app X       │      │          │        │          │
  │ (blocked by     │      │          │        │          │
  │  restriction)   │      │          │        │          │
  └─────────────────┘      └──────────┘        └──────────┘

  restrict repels web-app (no accept) --- REPULSION
  require attracts ml-job (gpu=true)  --- ATTRACTION
  accept lets ml-job through          --- PERMISSION
  = node-1 is effectively reserved for ml-job
```

### The Scheduling Matrix

| Mechanism | Attracts service to node | Repels others from node | Reserves node? |
|-----------|:---:|:---:|:---:|
| `require` alone | Yes | No | No |
| `restrict` alone | No | Yes | No |
| `require` + `restrict` + `accept` | Yes | Yes | **Yes** |

---

## `zone spread` — Anti-Affinity

The `zone spread` constraint is the equivalent of Kubernetes pod topology spread constraints. It distributes instances across failure domains (zones) for high availability.

### DSL Syntax

```
service web {
    image nginx:1.27
    instances 3
    placement {
        zone spread
    }
}
```

Nodes must have a zone assigned:

```bash
etcdctl put /ccattler/observed/node/node-1/zone zone-a
etcdctl put /ccattler/observed/node/node-2/zone zone-b
etcdctl put /ccattler/observed/node/node-3/zone zone-c
```

### How It Works

```
  zone-a              zone-b              zone-c
  ┌──────────┐        ┌──────────┐        ┌──────────┐
  │ node-1   │        │ node-2   │        │ node-3   │
  │          │        │          │        │          │
  │ web-1 OK │        │ web-2 OK │        │ web-3 OK │
  └──────────┘        └──────────┘        └──────────┘

  3 instances across 3 zones = 1 per zone
```

The scheduler always picks the zone with the fewest existing instances for the service. This guarantees maximum spread without complex topology constraints.

### Unbalanced Example

If zone-a already has 2 instances of the service and zone-b has 0:

```
  zone-a (2 existing)   zone-b (0 existing)   zone-c (1 existing)
  ┌──────────┐           ┌──────────┐           ┌──────────┐
  │ web-1    │           │          │           │ web-3    │
  │ web-2    │           │ web-4 *  │           │          │
  └──────────┘           │ (new)    │           └──────────┘
                         └──────────┘
  Next instance goes to zone-b (least populated)
```

---

## `architecture` — CPU Architecture Filter

Hard filter that matches the node's CPU architecture. Only nodes with the matching architecture are eligible.

### DSL Syntax

```
service arm-native {
    image myapp:arm64
    instances 2
    placement {
        architecture arm64
    }
}
```

### How It Works

```
  node-1 (amd64)      node-2 (arm64)      node-3 (amd64)
  ┌──────────┐        ┌──────────┐        ┌──────────┐
  │ X CANNOT │        │ OK CAN   │        │ X CANNOT │
  │ schedule │        │ schedule │        │ schedule │
  └──────────┘        └──────────┘        └──────────┘
```

If a node has no architecture reported, it passes any architecture constraint (permissive default).

---

## Complete Example

A service using all placement constraints together:

```
service ml-training {
    image tensorflow:2.15
    instances 2
    placement {
        architecture amd64
        zone spread
        require gpu = true
        prefer nvme = true
        accept dedicated-compute
    }
}
```

The scheduler evaluates in order:

1. **Architecture** — exclude non-amd64 nodes
2. **Require** — exclude nodes without `gpu=true`
3. **Restrict/Accept** — exclude restricted nodes unless `dedicated-compute` is accepted
4. **Zone spread** — among remaining candidates, pick the zone with fewest existing instances
5. **Prefer** — among zone-spread candidates, rank by `nvme=true` match count
6. **Load balance** — among equally-ranked candidates, pick least loaded

---

## Setting Node Properties

Node labels, zones, architecture, and restrictions are stored as observed facts in the store.

### Via etcdctl (current)

```bash
# Labels
etcdctl put /ccattler/observed/node/node-1/label/gpu true
etcdctl put /ccattler/observed/node/node-1/label/region us-east

# Zone
etcdctl put /ccattler/observed/node/node-1/zone us-east-1a

# Architecture
etcdctl put /ccattler/observed/node/node-1/architecture amd64

# Restrictions (taint analog)
etcdctl put /ccattler/observed/node/node-1/restrict/dedicated-compute true
```

### Viewing Node Properties

```bash
cca status    # shows all node labels, zones, and restrictions
```
