# Storage in CCattler

## What CCattler Provides

CCattler has a storage layer with three components:

### 1. StorageProvider Interface (`storage/storage.go`)

Eight methods covering the full volume lifecycle:

| Method | Purpose |
|--------|---------|
| `CreateVolume` | Ensure a named volume exists |
| `DeleteVolume` | Remove a volume (fails if attached) |
| `AttachVolume` | Bind a volume to a node, return mount path |
| `DetachVolume` | Unbind a volume from a node |
| `IsAttached` | Report attachment status |
| `SnapshotVolume` | Point-in-time snapshot |
| `VolumeUsage` | Used/total bytes |
| `ResizeVolume` | Expand capacity (shrink not allowed) |

Three implementations:

- **`SimulatorStorageProvider`** (`storage/simulator.go`) — in-memory backend for integration tests.
- **`LocalPathStorageProvider`** (`storage/local.go`) — host-local directories under `--storage-dir`. Volumes survive container restarts on the same node (ReadWriteOnce). Each volume is a subdirectory bind-mounted into the container.
- **`NFSStorageProvider`** (`storage/nfs.go`) — directories on an NFS mount under `--storage-dir`. Supports ReadWriteMany (multiple nodes attach the same volume). Operator must mount NFS before starting the agent.

### 2. StorageController (`controllers/storage.go`)

A reconciliation controller that watches volume facts and manages lifecycle:

- Creates observed volumes when desired volumes are declared
- Force-detaches volumes from unreachable nodes so they can be reattached
- Executes resize and snapshot operations after store transactions commit
- Replicates and migrates volumes when nodes fail

### 3. DSL Volume Blocks

The `.cca` language supports volume declarations and service-level mounts:

```
volume shared-content {
    size 10Gi
    persistent true
}

volume pgdata {
    size 10Gi
    persistent true
    type local
}

service webserver {
    image nginx:1.27
    instances 2
    port 80
    volume shared-content /usr/share/nginx/html
}
```

The optional `type` field selects the storage backend per volume:

- **`local`** — uses `LocalPathStorageProvider` at `/var/lib/cca/volumes`. The volume is pinned to the node where the instance first runs. Use for databases that require POSIX fsync/flock semantics (PostgreSQL, MySQL).
- **`nfs`** — uses `NFSStorageProvider` on the NFS mount. Accessible from all nodes.
- **(empty)** — uses the agent's `--storage-backend` default.

When the agent runs with `--storage-backend nfs`, it creates both an NFS provider (primary) and a local provider (fallback at `/var/lib/cca/volumes`). The agent reads the volume's type fact and routes attach/detach operations to the correct provider.

The `volume` keyword inside a service block binds a named volume to a container path. The compiler produces facts that the StorageController consumes. The node agent tracks volume attach/detach state via the StorageProvider and passes mount paths to the container runtime via `runtime.Spec.VolumeMounts`. Both containerd (OCI bind mounts) and nerdctl (`-v` flags) support volume pass-through.

## How to Use NFS with CCattler

CCattler follows the same model as Docker Compose: the operator mounts shared storage on the host, and the container runtime binds the host path into the container. The agent resolves volume facts to host paths via the StorageProvider and passes them to the runtime as bind mounts.

### Example: Two Webservers Serving Files from NFS

**Step 1: Mount NFS on every node**

On each node that will run the webserver containers:

```bash
# Install NFS client
apt install -y nfs-common

# Create mount point
mkdir -p /mnt/nfs/web-content

# Mount the NFS share
mount -t nfs 192.168.1.100:/exports/web-content /mnt/nfs/web-content

# Make it persistent across reboots
echo "192.168.1.100:/exports/web-content /mnt/nfs/web-content nfs defaults 0 0" >> /etc/fstab
```

**Step 2: Write the workload file**

```
# web-cluster.cca

volume web-content {
    size 10Gi
    persistent true
}

service nginx-frontend {
    image nginx:1.27
    instances 2
    port 80
    volume web-content /usr/share/nginx/html

    resources {
        cpu 200
        memory 134217728
    }
}
```

Both instances of `nginx-frontend` mount the same NFS share at `/usr/share/nginx/html`. Any file written to the NFS share is immediately visible to both containers.

**Step 3: Deploy**

```bash
cca apply web-cluster.cca
```

The scheduler places the two instances on available nodes. Each node already has `/mnt/nfs/web-content` mounted, so the container runtime binds it into the container at the declared mount path.

**Step 4: Update content**

Write files to the NFS share from any machine that has the share mounted:

```bash
echo "<h1>Hello from NFS</h1>" > /mnt/nfs/web-content/index.html
```

Both containers serve the updated file immediately — no redeploy needed.

### NFS with Ansible

CCattler ships an `nfs-client` role at `deploy/ansible/roles/nfs-client/`. It is wired into `site.yml` and `demo-deploy.yml` with a guard (`when: nfs_server is defined`), so clusters without NFS vars skip it.

Add these variables to your inventory:

```ini
[all:vars]
nfs_server=192.168.124.1
nfs_export_path=/mnt/disk2/exports/cca-volumes
nfs_mount_path=/mnt/nfs/cca-volumes
storage_backend=nfs
storage_directory=/mnt/nfs/cca-volumes
```

The role installs `nfs-common`, creates the mount point, mounts via fstab, and verifies access. The `storage_backend` and `storage_directory` vars are passed to `cca agent` via the systemd unit template.

## Why No Plugin System

Kubernetes needs CSI (Container Storage Interface) because it manages storage lifecycle end-to-end: provisioning cloud disks, attaching them to VMs, formatting filesystems, and mounting into pods. This requires a plugin protocol so cloud providers (AWS EBS, GCE PD, Azure Disk) and storage vendors (Ceph, NetApp, Portworx) can implement their own drivers.

CCattler does not need this because:

1. **CCattler runs on bare metal or known VMs.** There is no cloud provider API to abstract. The operator knows their storage infrastructure.

2. **NFS already solves shared storage.** Multiple containers across multiple nodes can read and write the same files with no orchestrator involvement. The operator mounts NFS once per node, and every container that needs it gets a bind mount.

3. **The operator owns the mount.** CCattler does not provision, format, or attach storage. It passes host paths to `nerdctl --volume`. This is the same model Docker Compose uses, and it works because the operator is responsible for making storage available on the host.

4. **The StorageProvider interface is an internal Go adapter.** The controller uses it to track volume state in etcd (which node has what attached), enforce exclusive access, and handle cleanup when nodes fail. Three backends exist: SimulatorStorageProvider (tests), LocalPathStorageProvider (host directories), NFSStorageProvider (shared NFS mount). Adding a new backend is one Go file implementing eight methods, compiled into the binary.

5. **Adding a CSI-like protocol would add complexity without value.** It requires a gRPC sidecar per node, a registration mechanism, health checking, and versioned protocol negotiation. For bare-metal NFS this is pure overhead.

### When You Might Want More

If your deployment needs block storage (local SSDs with replication), the path forward is implementing `StorageProvider` for that specific backend — not building a generic plugin protocol. One Go file, eight methods, compiled into the binary. No sidecars, no gRPC, no discovery protocol.

## Comparison with Kubernetes CSI

| Aspect | Kubernetes | CCattler |
|--------|-----------|----------|
| Storage model | Orchestrator provisions and mounts | Operator mounts, orchestrator passes through |
| Plugin protocol | CSI (gRPC, sidecars) | None — internal Go interface |
| NFS usage | Requires PV/PVC/StorageClass + CSI driver | Mount on host, reference in workload |
| Cloud disks | CSI driver per cloud provider | Not applicable (bare metal) |
| Complexity | High (CSI spec, node plugin, controller plugin) | Low (operator manages mounts) |
| Shared volumes | ReadWriteMany requires CSI driver support | NFS provides this natively |
