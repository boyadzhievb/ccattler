# Rolling updates and rollback

This guide covers how CCattler handles image updates, controls rollout speed, and automatically rolls back when new instances fail.

## How rolling updates work

When you change a service's image, the rollout controller detects a mismatch between the desired image and running instances. It gradually stops old-image instances so the instance controller creates replacements with the new image.

```
desired: 5 × myapp:v2
actual:  5 × myapp:v1

→ stop one v1 instance
→ instance controller creates one v2 instance
→ wait until v2 instance is healthy
→ stop the next v1 instance
→ repeat until all 5 run v2
```

At no point does the system stop all instances at once. The rollout controller respects `max_unavailable` to limit how many instances can be down simultaneously.

## Configuring the update strategy

Add an `update` block inside your service:

```hcl
service api {
    image myapp:v2
    instances 5
    expose 8080

    health {
        http /health
        every 10s
    }

    update {
        max_unavailable 1
        max_extra 1
    }
}
```

| Field | Default | Meaning |
|-------|---------|---------|
| `max_unavailable` | 1 | Maximum old-image instances to stop before replacements are healthy |
| `max_extra` | 1 | Maximum extra instances allowed above the desired count during rollout |

With `max_unavailable 1`, the rollout stops one old instance at a time. If you want faster rollouts, increase it:

```hcl
update {
    max_unavailable 3
    max_extra 3
}
```

This allows 3 old instances to be stopped simultaneously, with up to 3 extra instances created before old ones are fully removed.

## Triggering a rolling update

Change the image in your `.cca` file and re-apply:

```hcl
service api {
    image myapp:v3    # was myapp:v2
    instances 5
    expose 8080
    update {
        max_unavailable 1
        max_extra 1
    }
}
```

```bash
cca apply myapp.cca
```

## Monitoring rollout progress

### Watch rollout state

```bash
cca watch derived/service/api/rollout/
```

Output during a rollout:

```
PUT derived/service/api/rollout/state = rolling
PUT derived/service/api/rollout/previous_image = myapp:v2
```

When the rollout finishes:

```
PUT derived/service/api/rollout/state = complete
```

### Check service detail

```bash
cca get services api --describe
```

Shows the rollout section during an active update:

```
Rollout:
  State:  rolling
  Previous image:  myapp:v2
```

### Watch instance transitions

```bash
cca watch observed/instance/
```

Shows individual instance state changes as the rollout progresses:

```
PUT observed/instance/api-abc12/state = stopped      # old v2 instance
PUT observed/instance/api-def34/state = pending       # new v3 instance
PUT observed/instance/api-def34/state = running       # new v3 healthy
```

## Automatic rollback

The rollout controller monitors new-image instances. If **3 or more** new-image instances enter the `failed` state, the controller automatically reverts the desired image to the previous version.

```
desired: myapp:v3
instances: 3 × myapp:v3 (all failed), 2 × myapp:v2 (still running)

→ rollout controller detects 3 failed new-image instances
→ reverts desired image: myapp:v3 → myapp:v2
→ rollout state: rollback
→ instance controller converges: removes failed v3, creates v2 replacements
```

In the fact store:

```bash
cca watch derived/service/api/rollout/
```

```
PUT derived/service/api/rollout/state = rollback
```

```bash
cca watch desired/service/api/
```

```
PUT desired/service/api/image = myapp:v2    # reverted automatically
```

### Why threshold 3?

A threshold of 3 prevents rollback from triggering on transient failures (a single node hiccup or a slow pull). If one instance fails and the next two succeed, the rollout continues. Three simultaneous failures strongly indicate the new image is broken.

## Manual rollback

There is no dedicated `cca rollback` command. Manual rollback is just re-applying the previous image — the same mechanism as any other update:

```hcl
service api {
    image myapp:v2    # revert from v3 back to v2
    instances 5
    expose 8080
    update {
        max_unavailable 1
        max_extra 1
    }
}
```

```bash
cca apply myapp.cca
```

Or use `cca scale` to quickly change just the image without editing the file:

```bash
# Not yet implemented — change the .cca file and re-apply
```

Because CCattler operates on desired state, reverting the image fact is all that's needed. The rollout controller handles the rest.

## Disruption budgets

Disruption budgets constrain how many instances can be unavailable during any voluntary operation — rolling updates and node drains both respect them:

```hcl
service api {
    image myapp:v3
    instances 10
    expose 8080

    disruption {
        min_available 8
    }

    update {
        max_unavailable 3
        max_extra 2
    }
}
```

The effective `max_unavailable` is the **minimum** of the update policy and the disruption budget:

- Update policy says `max_unavailable 3`
- Disruption budget says `min_available 8` out of 10, so at most 2 unavailable
- Effective: `max_unavailable 2`

The disruption budget always wins when it's more restrictive.

## Complete example

A production service with health checks, update strategy, and disruption budget:

```hcl
service api {
    image myapp:v2
    instances 10
    expose 8080

    resources {
        cpu 500m
        memory 512Mi
    }

    health {
        http /health
        every 10s
    }

    update {
        max_unavailable 2
        max_extra 2
    }

    disruption {
        min_available 8
    }
}
```

### Deploying v3

Edit the image line:

```hcl
    image myapp:v3
```

```bash
cca apply myapp.cca
```

### What happens

1. Rollout controller detects desired `myapp:v3`, actual `myapp:v2`
2. Sets rollout state to `rolling`, records previous image `myapp:v2`
3. Stops up to 2 old instances (respecting disruption budget)
4. Instance controller creates 2 new `myapp:v3` instances
5. Waits for new instances to pass health checks
6. Stops the next batch of old instances
7. Repeats until all 10 run `myapp:v3`
8. Sets rollout state to `complete`

### If v3 is broken

If the new `myapp:v3` image crashes (fails health checks, exits immediately):

1. First 2 new instances fail — rollout pauses, old instances keep serving
2. Third new instance fails — threshold reached
3. Rollout controller reverts desired image to `myapp:v2`
4. Rollout state becomes `rollback`
5. Instance controller removes failed v3 instances, creates v2 replacements
6. Service converges back to 10 healthy `myapp:v2` instances

No operator intervention required.
