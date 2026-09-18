# Placement constraints

Placement constraints control where instances run. CCattler uses four human-readable keywords instead of Kubernetes' affinity/anti-affinity/taints/tolerations:

| Keyword | Type | Effect |
|---|---|---|
| `require` | Hard constraint | Instance **must** run on nodes with this label |
| `prefer` | Soft preference | Scheduler **prefers** nodes with this label (not required) |
| `restrict` | Node restriction | Node **rejects** all workloads unless they `accept` the label |
| `accept` | Toleration | Instance **accepts** restricted nodes |

## Require: hard constraints

Only schedule on nodes with a specific label:

```hcl
service ml-training {
    image training:v2
    instances 4
    placement {
        require gpu = true
    }
}
```

If no nodes have `gpu=true`, the instances remain unscheduled (the scheduler reports unsatisfied demand).

## Prefer: soft preferences

Prefer nodes in a specific region, but fall back if unavailable:

```hcl
service api {
    image myapp:v3
    instances 6
    placement {
        prefer region = us-east
    }
}
```

The scheduler scores nodes with the preferred label higher, but will place instances on other nodes if `us-east` nodes are full.

## Restrict and accept: node restrictions

Mark a node as restricted (only workloads that explicitly accept it will be scheduled there):

Nodes publish restriction labels:
```
observed/node/gpu-node-1/restrict/dedicated-compute
```

Services that need those nodes declare acceptance:

```hcl
service ml-training {
    image training:v2
    instances 4
    placement {
        accept dedicated-compute
        require gpu = true
    }
}
```

Regular services without `accept dedicated-compute` will never be scheduled on `gpu-node-1`.

## Built-in placement options

### Architecture

```hcl
placement {
    architecture amd64
}
```

Filters nodes by CPU architecture. Nodes publish their architecture as `observed/node/{id}/architecture`.

### Zone spread

```hcl
placement {
    zone spread
}
```

Distributes instances evenly across availability zones for fault tolerance.

## Combining constraints

Constraints compose naturally:

```hcl
service web {
    image nginx:1.28
    instances 10
    placement {
        architecture amd64
        zone spread
        require region = us-east
        prefer ssd = true
        accept dedicated-web
    }
}
```

Evaluation order:
1. Filter by `architecture` (must be amd64)
2. Filter by `require` labels (must have `region=us-east`)
3. Filter by `restrict` / `accept` (skip restricted nodes unless accepted)
4. Score by `prefer` labels (prefer nodes with `ssd=true`)
5. Score by `zone spread` (prefer zones with fewer existing instances)
6. Score by resource availability (prefer nodes with more free resources)

## Comparison with Kubernetes

| Kubernetes | CCattler | Difference |
|---|---|---|
| `nodeSelector` | `require label = value` | Same concept, clearer name |
| `nodeAffinity.required` | `require label = value` | Same semantics |
| `nodeAffinity.preferred` | `prefer label = value` | Same semantics |
| `tolerations` | `accept label` | No "effect" field needed |
| `taints` | `restrict label` | Applied to node, not to a "taint" object |
| Pod anti-affinity | `zone spread` | Built-in, no expression language |

The naming is self-documenting: `require gpu = true` reads as a sentence. No `matchExpressions`, `operator: In`, or `effect: NoSchedule`.
