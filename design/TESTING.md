# CCattler Test Plan

2028 test functions across 19 packages (counted 2026-11-11 with `go test ./... -list '.*'`). Every milestone lists its test scenarios in its checklist in [milestones.md](../milestones.md), and is verified by unit tests plus a deploy-cluster or e2e run.

## How to Run

```bash
go test ./...                          # all tests
go test ./integration/ -v              # integration tests only
go test ./... -run TestVolumeSurvives  # single test by name
go run ./cmd/cca/ demo-storage         # live demo with volume migration
go run ./cmd/cca/ readiness            # test suite across 16 areas, readiness report
./test/e2e/cluster_test.sh --env lan   # e2e on the bridged LAN VMs (from testbed-100.77)
```

## Test Layers

| Layer | Where | Runs in |
|-------|-------|---------|
| Unit | `*_test.go` next to the code | `test.yml` on every push and pull request (`go test -race -short ./...`) |
| Integration (simulated runtime, in-memory store) | `integration/` | `test.yml`; auth and security subsets also in `auth-test.yml`, `security-test.yml` |
| Chaos (failure injection, convergence) | `chaos/`, `cca chaos`, `cca benchmark` | `test.yml` (unit tests); commands run by hand |
| E2E on the LAN VM cluster | `test/e2e/cluster_test.sh --env lan` (includes `gate_tls_checks.sh`) | `e2e.yml` (runner label `e2e`), manual dispatch |
| Extended and upgrade E2E (still the default `--env test`, the retired bare-metal nodes) | `test/e2e/cluster_test.sh --extended`, `--from-release --upgrade` | `e2e-extended.yml`, `upgrade-test.yml` (runner label `e2e`), manual dispatch |
| E2E on the deploy cluster (Airflow and heavy workloads) | `test/e2e/cluster_test.sh --env deploy` | `deploy-testbed.yml` (runner label `deploy`), manual dispatch |
| Placement and tenant quota (12 scenarios, deploy cluster) | `test/e2e/placement_test.sh` | by hand; no workflow runs it |
| HA failover (bare-metal node addresses, not the `vm-ha-lan` set) | `test/e2e/baremetal_ha_test.sh` | `e2e-ha.yml`, manual dispatch |
| Deploy cluster health | `test/e2e/health_check.sh` | `health-check.yml`, hourly |

## Examples

The `examples/` directory contains working `.cca` configs — one per feature area. Each can be applied with `cca apply`, and all are validated by `TestAllExamplesParseAndApply`.

| File | Features Demonstrated |
|------|----------------------|
| `basic.cca` | Single service, 3 instances, exposed port, resources |
| `multi-service.cca` | Two services, different instance counts and resources |
| `health-checks.cca` | HTTP health probe with path, TCP health probe, intervals |
| `distributed.cca` | 6 instances spread across 3 nodes, failure recovery |
| `networking.cca` | VIPs, DNS, load balancing across 2 services |
| `storage.cca` | Persistent volume, service-to-volume binding, migration |
| `web.cca` | Process runtime (sleep command as image) |
| `web-container.cca` | Container runtime with SSI hostname display |
| `autoscaling.cca` | Horizontal/vertical/event-driven scaling policies |
| `zabbix.cca` | Monitoring integration with Zabbix |
| `rolling-update.cca` | Update strategy, disruption budget, health-gated rollout with automatic rollback |
| `auth.cca` | Roles, groups and grants (capabilities with scopes) |
| `airflow.cca` | Apache Airflow CeleryExecutor stack with PostgreSQL and Redis |
| `reference.cca` | **All DSL constructs** — annotated config reference (every block, every field, with comments) |

## Known Gaps

Scenarios not yet tested. Triaged 2026-11-11; the open items are tracked in [milestones.md](../milestones.md) under "Test gaps".

| Area | Scenario | Priority |
|------|----------|----------|
| DSL | `cca apply` accepts an undefined volume reference and duplicate service/volume names; only `cca lint` reports them | Should |
| Storage | Two services mounting the same exclusive volume (only the provider-level attach conflict is tested) | Should |
| Storage | Delete desired volume while attached (the provider refuses it in `TestDeleteVolumeWhileAttachedReturnsError`; the controller's cleanup is tested only for an available volume) | Could |
| Networking | VIP pool exhaustion: allocation is highest octet + 1 with no upper bound and no reuse of freed addresses | Should |
| Store | Watch ordering under concurrent writers (`TestWatchEventOrdering` uses one writer) | Could |
| End-to-end | Full lifecycle in one test: deploy → scale → update image → rollback (each step is tested separately) | Could |

## E2E Test Infrastructure

Two separate clusters, on separate hosts to avoid resource contention:

### E2E cluster (bridged LAN VMs on testbed-100.77)

Three vmherd VMs (Debian cloud image) on testbed-100.77, attached to the host bridge `br1` with vmherd bridge mode (vmherd >= 0.4.0), so they sit directly on the LAN like bare-metal nodes. Herdfile: `test/e2e/vm-lan/Herdfile`; inventory: `deploy/ansible/lan-e2e-inventory.ini`; values: `test/e2e/e2e-lan.ccv`. Layout matches the old bare-metal e2e cluster: the control-plane node runs no agent.

| VM | IP | Role | Resources |
|----|-----|------|-----------|
| cca-lan-ctrl | 192.168.100.50 | etcd + controlplane | 4096 MB / 2 vCPU |
| cca-lan-worker-1 | 192.168.100.51 | worker | 3072 MB / 2 vCPU |
| cca-lan-worker-2 | 192.168.100.52 | worker | 3072 MB / 2 vCPU |

- Addresses are static (cloud-init), from the range 192.168.100.50–.60 reserved for e2e VMs; keep that range outside the router's DHCP pool. Gateway and DNS are the LAN router (192.168.100.1), the host's upstream resolver — the host's own resolv.conf points at a 127.0.0.1 local resolver the VMs cannot use.
- Images come from the `registry:2` container on testbed-100.77 (`192.168.100.77:5000`).
- VMs stay running after a run (failed runs keep them for inspection); destroy with `cd test/e2e/vm-lan && vmherd destroy`.

Run: `./test/e2e/cluster_test.sh --env lan` (workflow `e2e.yml`, runner label `e2e` on the testbed-100.77 runner; manual dispatch only while the HA set occupies the host, see "HA cluster on testbed-100.77")

### Deploy cluster (libvirt VMs on m4800)

Long-lived 3-node cluster on m4800 (192.168.100.71, 31 GB), managed by vmherd. Runs Airflow and other heavy workloads. Host setup, requirements and a manual step-by-step deployment: [deploy-host.md](deploy-host.md).

| VM | IP | Role | RAM |
|----|-----|------|-----|
| cca-deploy-ctrl | 192.168.124.10 | etcd + controlplane + worker | 4 GB |
| cca-deploy-worker-1 | 192.168.124.20 | worker | 4 GB |
| cca-deploy-worker-2 | 192.168.124.30 | worker | 4 GB |

VM state: `test/e2e/vm-deploy/.vmherd/`. Health check: `./test/e2e/health_check.sh` (hourly cron via `health-check.yml`).

### Resource requirements for workloads

The Airflow stack (postgres + redis + webserver + scheduler + 2 workers) needs at minimum 4 GB per node. On 2 GB nodes the Celery workers alone consume 500 MB–1 GB each, pushing the system deep into swap and causing multi-minute startup times. Lightweight workloads (whoami) run fine on 2 GB.

| Workload | Minimum per node | Recommended per node |
|----------|-----------------|---------------------|
| whoami, whoami-multiport | 1 GB | 2 GB |
| Airflow full stack | 4 GB | 8 GB |

### Two clusters, two hosts

The E2E VM cluster runs on testbed-100.77 (16 GB) and the deploy cluster on m4800. They use separate Herdfiles (`test/e2e/vm-lan/` and `test/e2e/vm-deploy/`): E2E VMs are bridged onto the LAN (192.168.100.50–.60), deploy VMs sit on a private libvirt NAT network (124.x). VM lifecycle managed by vmherd.

## etcd backup and restore (runbook)

Required before `cca secret migrate-keys --confirm-backup` (M177, Flow 29 step 5): the migration is the rollback boundary, and the only way back is this restore **together with the previous `cca` binaries**. Tested on the e2e control plane (cca-lan-ctrl, etcd 3.5.16, single member) on 2026-11-10: a marker written after the snapshot was gone after the restore, and `cca status` was healthy.

Run on the control-plane node as root. The client certificate is the node certificate from `cca init`; the etcd data directory and member name come from the `etcd` unit (`ETCD_DATA_DIR=/var/lib/etcd/default`, `ETCD_NAME=%H`).

```sh
ETCD="--endpoints=https://127.0.0.1:2379 --cacert /etc/ccattler/pki/ca.pem \
  --cert /etc/ccattler/pki/node.pem --key /etc/ccattler/pki/node-key.pem"

# 1. Back up and verify (keep the file off the node as well).
mkdir -p /var/backups/ccattler && chmod 700 /var/backups/ccattler
SNAPSHOT=/var/backups/ccattler/etcd-$(date +%Y%m%d-%H%M%S).db
etcdctl $ETCD snapshot save "$SNAPSHOT"
etcdutl snapshot status "$SNAPSHOT" -w table      # revision and key count must be non-zero

# 2. Restore (single member): stop writers, restore into a new directory,
#    keep the old one, swap, start.
systemctl stop cca-server etcd
etcdutl snapshot restore "$SNAPSHOT" --name "$(hostname)" \
  --data-dir /var/lib/etcd/default.restored \
  --initial-cluster "$(hostname)=http://localhost:2380" \
  --initial-advertise-peer-urls http://localhost:2380
chown -R etcd:etcd /var/lib/etcd/default.restored
mv /var/lib/etcd/default /var/lib/etcd/default.pre-restore
mv /var/lib/etcd/default.restored /var/lib/etcd/default
systemctl start etcd && systemctl start cca-server
cca status
```

- The snapshot holds every secret envelope and the keyring (`keyring/kek/*` wrapped by the root key). Protect it like the root key; it is useless without `secret-root.key`. **Before retirement it also holds every v1 envelope, which anyone with a worker's old `secret-master.key` can open:** store off-node copies encrypted (e.g. `age`/`gpg` to an operator key), and delete pre-migration snapshots once the legacy key is retired and a fresh snapshot exists.
- Restoring a pre-migration snapshot after `migrate-keys` brings back v1 envelopes and `migration = not_started`: install the previous binaries before starting `cca-server`, and keep `secret-master.key` until the migration runs again.
- After `retire-legacy-key` and the deletion of `secret-master.key`, a pre-migration snapshot can no longer be decrypted. Take a fresh snapshot after retirement.
- HA (3 members): restore the same snapshot on every member with `--initial-cluster` listing all three peer URLs, then start etcd on all of them; see the etcd disaster-recovery guide.

### Legacy key migration and retirement notes (M177)

- **Order:** upgrade every control-plane replica (`cca secret generate-root-key` or the Ansible roles) → back up etcd (above) → `cca secret migrate-keys --confirm-backup` → wait until `observed/keyring/migration` reports `completed` → workers lose `secret-master.key` (worker Ansible role) → `cca secret retire-legacy-key` → `ansible-playbook … -e retire_legacy_secret_key=true` → take a fresh snapshot.
- **`unreadable_count > 0`:** a v1 secret the legacy key cannot open, typically written through an HA replica that had generated its own `secret-master.key` before M177 (Ansible keeps that key as `/etc/ccattler/secret-master.key.<timestamp>~` on the replica). Recover the value from its source and re-create it with `cca secret set <name>`: the new value is written v2, the migration completes, and retirement can pass. Retirement stays refused while any unreadable envelope exists.
- **Node IDs:** the retirement gate excuses API replicas from `legacy_key_file = absent` by matching the server's `--node-id` with the agent's node ID on the same host (Ansible sets both to `inventory_hostname`). Keep them equal on hosts that run both.
- **Disabled nodes** are not checked by either gate and may still hold the old key file; remove it before re-enabling such a node.
- **`cca init` output** (e.g. `/tmp/cca-init`) holds `secret-root.key` and `ca-key.pem`: it is control-plane-only material; delete it after `setup.sh` ran, never copy it to workers.

## HA cluster on testbed-100.77

Bridged 3 control-plane + 1 worker set for HA checks (`test/e2e/vm-ha-lan/Herdfile`, 192.168.100.53–.56, 11 GB). The host has 16 GB, so it never runs next to the e2e set (`vm-lan`, 10 GB): stop the ccattler runner and the e2e VMs first, and bring both back afterwards.

```sh
# on testbed-100.77
sudo systemctl stop actions.runner.boyadzhievb-ccattler.testbed-100-43.service
(cd ~/actions-runner/_work/ccattler/ccattler/test/e2e/vm-lan && vmherd down)
cd <repo copy>/test/e2e/vm-ha-lan && vmherd up
cd ../../../deploy/ansible
ansible-playbook -i lan-ha-inventory.ini ha-deploy.yml -e vmherd_ssh_key=$(cd ../../test/e2e/vm-ha-lan && vmherd info ssh-key)
# afterwards
(cd <repo copy>/test/e2e/vm-ha-lan && vmherd down)
(cd ~/actions-runner/_work/ccattler/ccattler/test/e2e/vm-lan && vmherd up)
sudo systemctl start actions.runner.boyadzhievb-ccattler.testbed-100-43.service
```

The HA servers listen on the host IP only: on a control-plane host use `CCA_API=<host ip>:9770 sudo -E cca …`.

While the HA set occupies the host (since 2026-11-11), `e2e.yml`, `e2e-extended.yml` and `upgrade-test.yml` run only on manual dispatch (their push, schedule and release triggers are removed); restore them when the e2e set returns.

## Root key rotation (runbook, M178)

Flow 30 part B. Offline: the API is down for about a minute; agents and running containers are not touched. File root key only (a KMS root key is refused).

```sh
cd deploy/ansible
ansible-playbook -i <inventory> rotate-root-key.yml                    # default: deletes .old, then rotates the KEKs
ansible-playbook -i <inventory> rotate-root-key.yml -e keep_old_root_key=true   # archive the old key as secret-root.key.retired-<time>
ansible-playbook -i <inventory> rotate-root-key.yml -e resume_root_key_rotation=true   # a run failed after the commit
```

- **Before:** every control-plane host on the same `cca` version; no `secret-root.key.old` left from an earlier rotation (archive it with the snapshots it opens, or delete it); for the KEK rotation step, the legacy key must be retired (otherwise that step reports 409 and the root key rotation still stands).
- **Re-run after any failure.** The playbook reads each host's stage (`cca secret root-key-status`: `idle | staged | committed | invalid`) and only goes forward once the keyring is committed to the new key. Before the commit a failure restarts the servers on the old key; after it, the servers stay down until the re-run. If the run failed after every host was already swapped (all `idle`, `secret-root.key.old` present), the playbook cannot tell that from a deliberately kept key: re-run with `-e resume_root_key_rotation=true`.
- **Status:** `cca secret keyring-status` prints the migration and KEK rotation status (including `unreadable_keys`) and every replica's report.
- **Manual inspection:** `sudo cca secret root-key-status` on each control-plane host. `committed` on any host means the keyring is already under `secret-root.key.next`: never delete or regenerate that file; run `sudo cca secret rotate-root-key --finish` on each host and start `cca-server`.
- **Back up the new key** off-node (encrypted) after the run; the playbook prints its fingerprint (first 16 hex characters of its SHA-256) so copies can be compared. Old off-node key backups hold the old key and open only pre-rotation snapshots.
- **Old key leaked:** re-wraps never change a secret's DEK, so the old root key plus any etcd snapshot or copy from before the rotation still decrypts every secret value that existed then. Rotate (root key, then the default KEK step, which the play waits for), **re-set every secret value** (`cca secret set …`, and rotate the credentials they stand for), then delete or re-encrypt the pre-rotation snapshots.
- **Unreadable envelopes** (`unreadable_keys` in `cca secret keyring-status`): the KEK rotation completes anyway and keeps their KEK version; re-set those secrets, and the version retires on its own.
- **Snapshots** under `/var/backups/ccattler` are never expired by the playbook; they hold the keyring under the root key of their time — move them off-node (encrypted) and expire them yourself.
- **Rollback** (only before step 8 shredded `.old`): stop every `cca-server`, restore the `etcd-before-root-key-rotation-*.db` snapshot (restore steps above), put `secret-root.key.old` back as `secret-root.key` on every replica, start the servers.

## Demo Commands

Each milestone has a corresponding demo command that exercises its headline feature interactively:

| Command | Milestone | What It Shows |
|---------|-----------|---------------|
| `cca demo` | M2 | Single node, 3 instances, simulated runtime |
| `cca demo-distributed` | M3 | 3 nodes, kills one, shows rescheduling |
| `cca demo-network` | M4 | VIPs, DNS, round-robin load balancing |
| `cca demo-storage` | M5 | Persistent volume migrates when node dies |

## Acceptance Testing Methodology

Based on [IEEE/ISTQB acceptance testing](https://en.wikipedia.org/wiki/Acceptance_testing) definitions. Acceptance criteria are the conditions a system must satisfy to be accepted by a user or customer.

### Types and How CCattler Implements Them

| Type | Definition | CCattler Implementation |
|------|-----------|------------------------|
| **UAT** (User Acceptance Testing) | Verify the solution works for the user — test scenarios, not click-by-click steps | M214 adversarial testing: real user scenarios against a live cluster; each milestone's deploy-cluster check verifies the user story works end-to-end |
| **OAT** (Operational Acceptance Testing) | Operational readiness: backup, disaster recovery, maintenance, security procedures | Deploy Testbed workflow: deploy → converge → health check → data plane verification → adversarial replay; `cca health` and `health-check.yml` (hourly) |
| **ATDD** (Acceptance Test-Driven Development) | Acceptance criteria drive development — tests written before code | Every milestone: design spec with acceptance criteria in `milestones.md` → failing tests → implementation → deploy check. A milestone is not complete until its acceptance tests pass |
| **Field Testing** | Real-world customer environment with real workloads | Airflow (DAGs, scheduler, workers, metadata-db, redis) running continuously on the deploy cluster under load |
| **Alpha Testing** | Internal pre-release at developer's site | Integration tests (`integration/`), chaos benchmarks (`cca benchmark`), demos |
| **Contract/Regulation** | Verify contractual or regulatory requirements | Not applicable (no external customers yet) — but the milestone checklist and independent review (code-reviewer + security-audit) serve the same role for internal gates |

### Acceptance Criteria in Practice

Every milestone in `milestones.md` is a set of acceptance criteria:

1. **Before implementation**: design spec lists Must/Should/Could test scenarios (ATDD)
2. **During implementation**: integration tests fail first, then pass (test-driven)
3. **Before release**: deploy-cluster check with real workloads (OAT + field testing)
4. **Independent review**: code-reviewer + security-audit verify the diff against the spec (from Phase 174)
5. **Adversarial replay**: deterministic cases from manual testing confirm security properties hold (UAT, `test/e2e/adversarial/`)

### Test Execution Flow

```
Unit tests (go test)
  → Integration tests (simulated runtime, auth, security)
    → E2E on LAN VMs (cluster_test.sh --env lan)
      → Deploy Testbed (real workloads, eBPF dataplane)
        → Adversarial replay (auth enforcement, role escalation)
          → Manual adversarial testing (M214 — human + AI testers)
```

Each layer adds realism. A milestone must pass all layers before release.

## Adding Tests for a New Milestone

1. List the milestone's **Must/Should/Could** test scenarios in its checklist in `milestones.md` before starting implementation.
2. Write the **Must** tests as integration tests in `integration/`.
3. Write **Should** tests as unit tests in the relevant package.
4. Add an example `.cca` file to `examples/` — it is automatically validated by `TestAllExamplesParseAndApply`.
5. After completing the milestone, review the **Known Gaps** section and promote any that became relevant.
