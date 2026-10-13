# Demo: Deploying Airflow on a CCattler Cluster

Recording guide for a 5-7 minute terminal demo showcasing CCattler's
multi-node cluster with a real Airflow deployment.

## Prerequisites

- 3 Linux machines (VMs, bare metal, or cloud instances) with network connectivity
- Container runtime (containerd/nerdctl) installed on each node
- Pre-pull images to avoid download delays on camera:
  ```
  nerdctl pull postgres:16
  nerdctl pull redis:7
  nerdctl pull apache/airflow:2.10.5
  ```

## Recording Setup

Use [asciinema](https://asciinema.org/) for terminal recording or OBS for
screen recording with voiceover. A tmux split-pane layout works well for
showing multiple nodes simultaneously.

Suggested tmux layout:
```
┌─────────────────────────────────┐
│          node-1 (ctrl)          │
├────────────────┬────────────────┤
│    node-2      │    node-3      │
└────────────────┴────────────────┘
```

## Script

### Act 1: Install CCattler (~1 min)

Show the install on all three nodes. Can be pre-staged and just shown on
one node for brevity.

```bash
# On each node:
curl -sSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | sh
cca version
```

### Act 2: Bootstrap the Cluster (~1.5 min)

```bash
# Node 1 — start the control plane
cca server --tls --listen 0.0.0.0:9770

# Node 1 — create join tokens (in a second terminal)
cca token create --node-id node-2 --ttl 15m
cca token create --node-id node-3 --ttl 15m

# Node 2 — join the cluster
cca join 192.168.124.10:9770 <token> --node-id node-2 --ca-cert /etc/cca/ca.pem

# Node 3 — join the cluster
cca join 192.168.124.10:9770 <token> --node-id node-3 --ca-cert /etc/cca/ca.pem

# Verify — show all three nodes healthy
cca get nodes
```

Expected output:
```
NODE     STATE    CPU    MEMORY   ZONE
node-1   ready    4000m  8Gi      -
node-2   ready    4000m  8Gi      -
node-3   ready    4000m  8Gi      -
```

### Act 3: Create Secrets (~30s)

```bash
cca secret set airflow.db-password "airflow-db-pass-2026"
cca secret set airflow.admin-password "admin-pass-2026"
cca secret set fernet-key "$(python3 -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())')"
cca secret set webserver-secret-key "$(openssl rand -hex 32)"

cca get secrets
```

### Act 4: Deploy Airflow (~1 min)

This is the hero moment — one file, one command.

```bash
# Show the CCA file briefly
cat examples/airflow.cca

# Deploy
cca apply examples/airflow.cca
```

### Act 5: Watch Convergence (~1.5 min)

```bash
# Live stream facts being created
cca watch

# (After a few seconds, Ctrl+C and switch to status commands)

# Cluster overview
cca status

# Services created
cca get services

# Instances spreading across nodes
cca get instances

# Resource utilization
cca top workloads
cca top nodes
```

Expected instance distribution:
```
SERVICE      INSTANCE           NODE     STATE    HEALTH
metadata-db  metadata-db-a1b2   node-1   running  healthy
redis        redis-c3d4         node-2   running  healthy
webserver    webserver-e5f6     node-3   running  healthy
scheduler    scheduler-g7h8     node-1   running  healthy
worker       worker-i9j0        node-2   running  healthy
worker       worker-k1l2        node-3   running  healthy
worker       worker-m3n4        node-1   running  healthy
```

### Act 6: Access the Web UI (~30s)

```bash
# Open browser to the webserver node
echo "Open http://$(cca get instances | grep webserver | awk '{print $3}'):8080"
```

- Show the Airflow login page
- Login with admin / the admin password
- Show DAGs loading and the Airflow dashboard

### Act 7: Scale Workers (~30s)

```bash
# Scale workers from 3 to 5
cca scale worker 5

# Watch new workers appear
cca get instances

# Show updated resource utilization
cca top nodes
```

### Act 8: Inspect and Logs (~30s)

```bash
# Aggregate logs from the scheduler
cca logs scheduler --follow

# Check a specific node's runtime state
cca node-inspect node-2
```

## Tips for a Clean Recording

1. **Pre-pull all images** on every node before recording. Image pulls take
   minutes and kill the pacing.

2. **Pre-create secrets** if you want to skip Act 3. You can always mention
   "secrets were created earlier" in the voiceover.

3. **Use a fixed terminal size** (120x35 or similar) so the recording
   renders consistently.

4. **Pause briefly** after each command so viewers can read the output.
   asciinema lets you adjust playback speed in post.

5. **Have a browser ready** with the Airflow URL pre-loaded (just not
   logged in) to avoid typing URLs on camera.

6. **Kill background services cleanly** before recording to avoid stale
   state. `cca` on a fresh boot is ideal.

## Prep Script

Run this on all nodes before recording to ensure a clean slate:

```bash
#!/bin/bash
# demo-prep.sh — run on each node before recording

set -e

# Pre-pull images
echo "Pulling container images..."
nerdctl pull postgres:16
nerdctl pull redis:7
nerdctl pull apache/airflow:2.10.5

echo "Images ready."
nerdctl images | grep -E "postgres|redis|airflow"
```

Run on the control node only:

```bash
#!/bin/bash
# demo-prep-ctrl.sh — run on node-1 before recording

set -e

# Create secrets ahead of time (optional — can do live in demo)
cca secret set airflow.db-password "airflow-db-pass-2026"
cca secret set airflow.admin-password "admin-pass-2026"
cca secret set fernet-key "$(python3 -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())')"
cca secret set webserver-secret-key "$(openssl rand -hex 32)"

echo "Secrets ready."
cca get secrets
```
