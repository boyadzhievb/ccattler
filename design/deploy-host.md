# Deploy Host Setup

The deploy cluster is three vmherd VMs (`cca-deploy-ctrl` 192.168.124.10,
`cca-deploy-worker-1` .20, `cca-deploy-worker-2` .30) on one Linux host. That
host also runs the GitHub runner with the `deploy` label, the local image
registry, the NFS export for volumes, and Caddy for external gate access.

Current host: **m4800** (192.168.100.71, 8 threads, 31 GB RAM, Debian 13).
The previous host, testbed-100.49 (16 GB), had host-specific settings (LAN
registry address, `/mnt/disk2` NFS path) that are now replaced by the
host-independent values below.

This document is the result of a manual step-by-step deployment on m4800
(2026-11-09). Every requirement found there is listed, together with the gaps
it closed.

## One-time host setup

Requirements: Debian/Ubuntu, KVM-capable CPU, passwordless sudo for the runner
user. Then:

```bash
scripts/setup-deploy-host.sh
```

It is idempotent and does the following:

| Step | What | Why |
|---|---|---|
| Packages | `qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients virt-install dnsmasq-base genisoimage xxd` | vmherd: KVM VMs from cloud images, cloud-init seed ISO (`genisoimage`), `xxd` used by vmherd |
| | `ansible golang git jq curl rsync openssl` | Ansible deployment, source build in the workflow, scripts |
| | `docker.io` | local image registry (`registry:2`) |
| | `nfs-kernel-server` | NFS export for persistent volumes |
| | `caddy` | external access to gates (`caddy-sync`, cluster_test.sh step 4.5) |
| Groups | runner user in `libvirt` and `docker` | vmherd and the Ansible registry task run without sudo. **Log in again and restart the runner service** so the groups apply |
| vmherd | latest release into `/usr/local/bin` | the workflow also installs it on each run |
| Registry | `registry:2` on port 5000, all interfaces | VMs pull from `192.168.124.1:5000` (libvirt gateway) |
| Images | all images referenced by `test/e2e/workloads/*.cca` | the registry is not a pull-through cache: an image missing here makes instances sit in `image-pull-backoff` |
| NFS | export `/srv/exports/cca-volumes` to `192.168.124.0/24` (`rw,sync,no_subtree_check,no_root_squash`) | matches `nfs_export_path` in `deploy/ansible/deploy-inventory.ini` |

Reseed images after changing a workload image: `scripts/setup-deploy-host.sh --images`.

## Runner

Register the GitHub Actions runner on the host with the label `deploy`
(`.github/workflows/deploy-testbed.yml` runs on `[self-hosted, deploy]`).
Remove the label from any old deploy host so jobs do not split between hosts.

## Host-independent addresses

| Setting | Value | File |
|---|---|---|
| Registry seen by the VMs | `192.168.124.1:5000` | `test/e2e/deploy.ccv` |
| NFS server seen by the VMs | `192.168.124.1` | `deploy/ansible/deploy-inventory.ini` |
| NFS export path | `/srv/exports/cca-volumes` | `deploy/ansible/deploy-inventory.ini`, `scripts/setup-deploy-host.sh` |

`192.168.124.1` is the host's address on the `cca-deploy` libvirt network,
which vmherd creates from `test/e2e/vm-deploy/Herdfile`. Using it instead of the
host's LAN address keeps the VMs working when the LAN address changes or the
LAN link is down (the cause of the failed deploy runs 42656702467 and
42659257147 on testbed-100.49: every Airflow instance stuck in
`image-pull-backoff` because `192.168.100.49:5000` was unreachable).

## Manual deployment (what CI does, step by step)

Run on the deploy host from a repo checkout:

```bash
# 1. Binary (CI builds it the same way)
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags ebpf -o deploy/ansible/cca-linux-amd64 ./cmd/cca/

# 2. VMs (about 30 s once the cloud image is cached)
(cd test/e2e/vm-deploy && vmherd up && vmherd status)

# 3. Cluster
cd deploy/ansible
ansible-playbook -i deploy-inventory.ini baremetal-deploy.yml --skip-tags testapp,cleanup \
  -e vmherd_ssh_key=$(cd ../../test/e2e/vm-deploy && vmherd info ssh-key) \
  -e insecure_registry=192.168.124.1:5000 -e dataplane_backend=ebpf

# 4. Workloads (or: test/e2e/cluster_test.sh --env deploy --skip-build --workload ...)
cd ../../test/e2e/vm-deploy
vmherd scp ../deploy.ccv cca-deploy-ctrl:/tmp/e2e-values.ccv
vmherd scp ../workloads/airflow.cca cca-deploy-ctrl:/tmp/airflow.cca
vmherd ssh cca-deploy-ctrl -- "sudo cca secret set airflow.db-password ...; sudo cca apply --values /tmp/e2e-values.ccv /tmp/airflow.cca"
vmherd ssh cca-deploy-ctrl -- sudo cca get instances
```

Results on m4800: the playbook finished with no failures, the Airflow stack
reached 6/6 running in about 2.5 minutes, and all four `gate-tls` gates served
their own certificate by SNI on the nodes' `:443`.

Things to know when checking by hand:

- The gate listeners bind the node IP (`192.168.124.20:443`), not loopback:
  `curl --resolve host:443:192.168.124.20 https://host/`.
- The playbook's CNI chain cleanup used to print `fatal: ... ignoring`
  (`[ERROR]` with the Ansible on Debian 13) on fresh VMs; it now only touches
  chains that exist and succeeds.
- The CLI has no command to read a single fact; use the state API on the
  control node: `curl --cacert /etc/ccattler/pki/ca.pem -H "Authorization: Bearer $(sudo cat /etc/ccattler/local-token)" "https://127.0.0.1:9770/api/state?key=observed/gate-keys/migration"`.

## Gaps found during the manual deployment

| Gap | Resolution |
|---|---|
| vmherd needs host packages that were installed by hand (`libvirt-*`, `virt-install`, `genisoimage`, `xxd`) | listed above, installed by the setup script |
| No Docker, registry, NFS server or Caddy on a new host; the registry was seeded by hand on the old host and nothing in the repo did it | setup script installs them and seeds every workload image |
| Registry address was the old host's LAN IP | `192.168.124.1:5000` |
| NFS export path was specific to the old host's disk layout | `/srv/exports/cca-volumes` |
| New group membership does not reach a running GitHub runner | restart the runner service after setup |
| A separate `m4800.yml` workflow duplicated `deploy-testbed.yml` | removed; the host gets the `deploy` label instead |
| No CLI to read one fact | documented the state API call above (candidate for a `cca` command) |
| CNI chain cleanup "failed and was ignored" on every fresh VM (`[ERROR]` on newer Ansible) | task only touches existing chains |
