# Milestones — Active & Upcoming

Open phase checklists and the upcoming milestone table. Completed phases and
milestones (M1–M169) live in [milestones-archive.md](milestones-archive.md).
When a milestone is released, move its checklist and row to the archive.

## Open Phase Checklists

### Phase 170 — Gate HTTPS Listener & End-to-End Validation (M170)

Move gate TLS from the service VIP to a node HTTPS listener on the agent gate proxy, then validate on the deploy cluster. Plan: [design/milestones/m170-gate-tls-e2e.md](design/milestones/m170-gate-tls-e2e.md).

**Design:**
- [x] Node HTTPS listener design in `design/gate-tls-termination.md`
- [x] Flow 13 updated in `design/runtime-invariant-flows.md`

**Listener:**
- [x] Host → certificate map in `UserSpaceProxy` (desired over derived, 5 s refresh) + unit tests
- [x] `--proxy-tls-listen` HTTPS listener: SNI cert selection, no default cert, 421 on SNI/Host mismatch
- [x] 308 HTTP → HTTPS redirect for TLS gate hosts on the plain listener
- [x] Cookie sticky over HTTPS via shared cookie helpers (`Secure` cookie)
- [x] Remove VIP TLS: nftables/iptables `hasTLS` branches, `CookieStickyProxy` TLS, `RouteDataPlaneConfig` TLS fields, agent TLS reads

**Workload & harness:**
- [x] `test/e2e/workloads/gate-tls.cca` + `.workload` (tls-auto with two gates, tls-manual, tls-sticky)
- [x] Manual cert generated with openssl at test time (SAN = gate host), not committed
- [x] T1–T15 checks in `cluster_test.sh`, each inside a retry/wait loop

**Found during the first deploy run:**
- [x] Shared (ReadWriteMany) volume attach: whoami's 2 instances sharing one NFS volume no longer wait forever on different nodes (Flow 14, `StorageProvider.SupportsSharedAttach`)
- [x] Shared volume attachments per instance: `observed/volume/{name}/attachment/{node}/{instance}`, state derived only by StorageController, unreachable-node attachments dropped without migration, ACCESS/NODES/INSTANCES in `cca get volumes`
- [x] RouteController deleted RouteTLSController's `derived/route/{name}/tls/*` as stale every cycle → certificates re-issued continuously, brief "no gate certificate" handshake failures (T8). Staleness now limited to `/backends` keys
- [x] e2e: probe captures in `gate_tls_checks.sh` no longer exit silently under `set -e`; `cluster_test.sh` ERR trap reports file:line of any silent exit; T5 requires the background request loop to have run
- [x] `cca apply` never deleted desired facts, so removing `tls` from a gate kept HTTPS on (T14). Apply now replaces each declared gate (`collectRemovedGateFacts`); `cca diff` lists the deletions. General removal for other resource kinds is not covered
- [x] Scheduler soft service spread: placement ranks nodes by same-service instance count before total load, rebalance never moves an instance onto a node with as many siblings. whoami ended up with both instances on worker-1 after churn, skipping the node-failure test
- [x] e2e node-failure timing (runs 41619119743, 41620626862, first runs where the test was not skipped): failover deadline was 30s with ~3 attempts while Flow 13 drops a stopped node's backends only after the 30s staleness threshold, so passing depended on random backend choice. Now 75s, polled every second, and passes only on 5 consecutive answers after the threshold. Recovery now waits until worker-1 reaches the VIP 3 times in a row (agent restart rebuilds `cca-vxlan0` about 7s after start), and the multi-port HTTP check retries for 30s
- [x] VXLAN interface left down by an agent stopped mid-creation (run 41662762813: Ansible restarted the agent within a second, `ip link set up` was cancelled, the next agent trusted the existing `cca-vxlan0` and every route failed with "Nexthop device is not up"). The reconciler now checks up/MTU every pass and recreates a missing interface
- [x] Real node capacity and right-sized Airflow (run 41673229739 measurements: Celery workers 1.9 GiB each at default concurrency 16, webserver 894 MiB, scheduler 547 MiB, while agents registered a fixed 4000m / 8192Mi on 3.9 GB VMs and Airflow requested 4096Mi per worker). Agents now register all CPUs and MemTotal minus max(768Mi, 10%); Airflow e2e workload sets worker concurrency 4 and 2 webserver workers, limits 1536/1536/1024Mi
- [x] Investigate run 41703846796 (eBPF, all four workloads): every functional check passed, then 5 Airflow instances regressed. metadata-db (Postgres on cca-deploy-ctrl) crashed with `runtime_crash` around 19:46, near the node-failure test's agent restart on worker-1; its replacement failed with `create container: snapshot "metadata-db-69c..."` (snapshot left behind, so the new instance cannot start), and the Airflow init step `airflow db check-migrations` then failed in a loop. No OOM kill seen. At 19:48:57 all controller watch channels closed on ctrl (etcd or ctrl under pressure?). Find the Postgres crash cause and fix the snapshot leak on instance replacement Explained by the later items: the rebalancer moved metadata-db, its replacement hit the snapshot leak, and its `type local` pgdata was a different (stale) directory on the new node. Confirmed green 3 runs in a row on 23070d5: 41860670204, 41879580109, 41882395686
- [x] Rebalancer moved a volume-backed single-instance database (run 41716329604, 8193df1: at 21:03:58 `scheduler: rebalancing instance service=metadata-db from=cca-deploy-ctrl to=cca-deploy-worker-1` to even out instance counts 8/5; the node-failure test then stopped worker-1's agent, metadata-db was replaced again and was still `pulling` at the stability check, with Airflow worker and scheduler `pending`). `findMovableInstances` skipped groups and placement hints but not stateful services, which preemption already skips. Rebalance now also skips services marked `stateful` and services that mount a volume not observed as shared (`observed/volume/{name}/access`); the scheduler watches `observed/volume/`. Confirmed green 3 runs in a row on 23070d5: 41860670204, 41879580109, 41882395686
- [x] Snapshot leak on in-place restart (run 41809905081, 12653cf: T13 killed `tls-auto-33a7460200`; the agent restarted it and `create container: snapshot "tls-auto-33a7460200-snap": already exists`, the server replaced it, and the replacement was still `pending` at the stability check; the next run 41809919327 passed). `ContainerdRuntime.Start` for a tracked, exited container only cleared in-memory state and left the containerd container and snapshot behind. It now calls `Stop` first, which deletes the task, CNI state, container and snapshot. Confirmed green 3 runs in a row on 23070d5: 41860670204, 41879580109, 41882395686
- [x] Airflow scheduler never passed its startup probe under load (run 41819704929, acc0914: `airflow jobs check` hit the 20s timeout or exit 120 twelve times in 11 min on worker-2 while etcd timed out; the rebalancer then moved it to worker-1, restarting its startup; after T12's server/agent restart a replacement was still starting at the stability check). Rebalance now skips instances whose startup probe has not succeeded (`observed/instance/{id}/probe/startup`); e2e Airflow scheduler startup probe timeout 20s → 60s. Run 41825857428 (0b40cf2): the scheduler was still moved 2s after init, during `initial_delay` before any probe result; an instance of a service that declares a startup probe now counts as starting until it records `succeeded`. Same run: startup probe failed with `exited with code 120` three times, while the same command run by hand passed (exit 0, 23s); a failing exec probe now logs the tail of its output in the agent log, never in etcd (agent `runExecProbe`); exec output copy waits at most 2s after exit. Run 41829957009 (56fca36) showed the cause: Airflow probe commands print success (`Found one alive job.`, `1 node online.`) but exit after the 20–30s timeout (by hand 23s idle), so liveness failures restarted scheduler and worker. All Airflow exec probe timeouts in the e2e workload are now 60s. Confirmed green 3 runs in a row on 23070d5: 41860670204, 41879580109, 41882395686
- [x] Node-local volumes were not pinned to their node (run 41838979669, da026b6: metadata-db crash-looped on three nodes in turn; Postgres log `last known up at 2026-11-03 19:46:05 UTC ... PANIC: could not locate a valid checkpoint record`. `type local` pgdata is a per-node directory; "migration" and "replication" only change facts, so a moved instance attached the target node's own directory, either empty (silent data loss) or a stale copy from an earlier run there). Fix (Flow 26): `StorageProvider.NodeLocal()`; the agent records `observed/volume/{name}/home_node` on the first node-local attach and refuses to attach that volume on any other node; the scheduler places instances of services mounting it only on the home node (hard constraint, unplaced reason `volume_home_node_unavailable` while that node is down); `design/storage.md` no longer claims data replication/migration for local volumes. Trigger in the deploy test: `cluster_test.sh` wipes etcd every run (fresh cluster) but kept `/var/lib/cca/volumes`, so each new cluster reused the previous clusters' per-node pgdata directories; it now removes them in the same non-`--keep-data` cleanup (as the Ansible cleanup role already did) Confirmed green 3 runs in a row on 23070d5: 41860670204, 41879580109, 41882395686
- [x] Rebalancing is opt-in, like Kubernetes (run 41851808529, e0c7bfe: the healthy single-instance Airflow scheduler was moved to even out instance counts; a move is a restart, a full outage for one instance, and Airflow needs minutes to start, so a replacement was still starting at the stability check). The scheduler now moves only services that declare `placement { rebalance true }` (`desired/service/{name}/placement/rebalance`); all other running instances stay where they were placed. e2e whoami opts in so rebalancing stays exercised Confirmed green 3 runs in a row on 23070d5: 41860670204, 41879580109, 41882395686
- [x] e2e honesty: whoami-multiport port 8081 reported as KNOWN GAP (M191) instead of a passing check; nftables check matches real `vip_dispatch` entries and fails if port 80 is missing; step 5.9 no longer claims whoami is pinned

**Deploy, run & ship:**
- [x] Ansible worker template passes `--proxy-tls-listen` (port 443)
- [x] Green on the deploy cluster 3 consecutive runs (beea1ae: 41652626638, 41655543117, 41656173560, workloads whoami, whoami-multiport, gate-tls)
- [x] One-off run on the bare-metal eBPF cluster: run 42054575640 (3e080ff, eBPF, whoami + whoami-multiport + gate-tls) green, T1–T15 passed, 10/10 instances stable. Needed first: `e2e.yml` `dataplane` input and a gate-tls workload option; `cluster_test.sh` builds with `-tags ebpf` for `--dataplane ebpf`; bare-metal node-1 is control plane only (no agent), so the multi-port cross-node sender and the gate TLS checks now target agent nodes (`vm_has_active_agent`, `GATE_TLS_NODE`); the multi-port VIP backend bug (M183) fixed
- [x] `gate-tls` added to deploy-testbed workflow default workloads (`airflow,whoami,whoami-multiport,gate-tls`)
- [x] `design/dsl-reference.md` TLS section updated (node :443, service ports stay plain HTTP)

### Security review (2026-11-03) — Phases 171–174, 176–182

An architecture review found holes that the threat model did not list, plus features marked done that never ran in production (AuthController from M156/Phase 58, store RBAC for agents from Phase 52c). Ordered by severity.

### Phase 171 — Enrollment Identity Hardening (M171)

A join-token holder could submit a CSR with CN `ccattler-server` and authenticate as the server's cluster-admin principal. CSR SANs were copied into a server-auth certificate.

- [x] Flow 11 in `design/runtime-invariant-flows.md` (enrollment identity rules)
- [x] `SignCSR` ignores the CSR subject and SANs: CN = node ID, SANs = `<id>`, `<id>.cca.local`, request IPs
- [x] Enrolled node certificates carry client-auth only
- [x] Node ID validated (`types.ValidateNodeID`, lowercase DNS name); `ccattler-server` and node IDs holding `auth/grant/node/<id>/` grants refused
- [x] Join token consumed and node record created in one transaction (no double use)
- [x] Re-enrolling an already-enrolled node ID requires a token bound to that node ID
- [x] Unit tests for each rule (`security/enrollment_test.go`, concurrent single-use test); `AUTH_GRAPH.md` and threat model updated
- [x] Deploy cluster run: workers join and agents reach etcd and the API with client-auth-only certificates (run 41616667630: both workers re-enrolled, all nodes alive, agent debug mTLS works with the enrolled certificates)

### Phase 172 — Agent Debug API Lockdown (M172)

Design: Flow 24 in `design/runtime-invariant-flows.md`. Users reach the debug API only through an authenticated API server proxy; the agent accepts only the control plane over mTLS. The loopback-only Ansible step was skipped (it would break remote `cca exec`).

- [x] Flow 24 in `design/runtime-invariant-flows.md`
- [x] Agent debug API serves TLS 1.3 with a required cluster-CA client certificate; the client principal must hold a cluster-admin grant
- [x] Agent refuses a non-loopback `--debug-listen` without TLS credentials; published address carries its scheme
- [x] API proxy `/api/node/debug/{node}/{endpoint}`: per-endpoint capability (`node.read`, `workload.read`, new `workload.exec`, `node.manage`), agent cert CN must equal the node ID, caller's `Authorization` not forwarded, log streaming
- [x] `cca exec`, `cca logs`, `cca node-inspect`, `cca images` go through the API proxy
- [x] e2e (`cluster_test.sh` Phase 69 checks, `health_check.sh`) use the proxy; new check that the debug port rejects requests without a client certificate
- [x] Unit tests: agent rejects missing/non-admin certs and plain non-loopback listen; proxy capability checks, node identity check, header stripping
- [x] Threat model T12, `CALL_GRAPH.md`, `AUTH_GRAPH.md`, security-model capability list updated
- [x] `cca exec` runs its arguments directly, without an implicit `sh -c` (found in run 41634411854: every e2e image is shell-less whoami, so exec could never work there). Debug API and proxy take `args` (JSON array) instead of a `command` string; a shell only when the user writes `-- sh -c '…'`. Probes, lifecycle hooks and init commands keep their DSL shell strings
- [x] e2e exec checks require an empty `error` (the old curl check passed on any response with `instance_id`) and run a binary present in the whoami image
- [x] Deploy cluster run: `cca node-inspect`, `cca exec`, `cca logs`, `cca images pull` work from the control plane (run 41644990156; `cca logs` tested with `--tail`, follow mode not covered by e2e)

### Phase 173 — API Authorization Correctness (M173)

Design: Flow 25 in `design/runtime-invariant-flows.md`.

- [x] Grant/policy sync: `AuthController` runner in every API process (`startAPIAuthorizationSync`), not leader-only
- [x] `GrantRole` bootstrap grants (local-admin, server principal) survive `ReplaceGrants`
- [x] Builtin role grants without capability facts (`cca role grant --role api-reader`) use the builtin role
- [x] ABAC policies without conditions ignored (would otherwise grant to every principal); `APIAuthorizer.SetAuditLogger` wired
- [x] `/api/state`, `/api/watch` readable-root allowlist; `?prefix=secret`/`s`/`enroll` bypass tests
- [x] Gate TLS private keys (`*/tls/key`) dropped from state scans, single-key reads and watch events (found during M173)
- [x] Chain order mTLS → local token → internal JWT → external OIDC → webhook; each claims only its own token shape or issuer; the webhook never sees local or internal tokens; client sees a generic 401 message
- [x] Watch connection limit incremented before the check (`reserveWatchConnection`)
- [x] `AUTH_GRAPH.md` chain, grant and caller sections; threat model T7 correction
- [x] Users authorized only at the API layer (found in run 41637959333: a granted user got 500 "has no role bindings" because `AuthorizedStore` checked store-layer RBAC with the user principal). The API server uses the store as `service:api-server` with the new `api-server` store role (no access to secrets/, credentials/, enrollment/, bootstrap/, audit/); store audit records the user in `on_behalf_of`
- [x] Apply authorizes every compiled fact by capability (`RequiredCapabilityForFactWrite`): `auth/`, `policy/network/` and cloud identities need `cluster.admin`, secret bindings need `secret.use`. Without this, removing the store check would let a `workload.create` user apply `grant cluster-admin to user ...`
- [x] `/api/status` and `/metrics` return the store error instead of an empty cluster
- [x] Visibility from grant scope (decision 2026-11-03, option a): `workload.read` at `cluster` sees every team, at `team/<name>` only that team; membership (`team` attribute, `platform` group) and principal kind add nothing; deeper scopes give no team visibility. Same rule for `/api/state` (scan, single key answers 404 outside the caller's teams), `/api/watch` and `/api/status`; team-scoped readers are no longer refused by a cluster-only check. `cca user create` does not require a team. The audit endpoint keeps its membership filter and `/api/events/stream` stays cluster-only (follow-up)
- [x] Deploy cluster run: `cca role grant user:<name> --role api-reader` takes effect without a restart; caddy-sync still reads routes (run 41644990156: 403 → 200 → 403, all 6 route hosts in the Caddyfile)

### Phase 175 — Status Snapshot & Bulk Reads (M175)

Found 2026-11-06 on the deploy cluster: uncached `GET /api/status` takes 17–21s (`cca top`, `cca get`, `cca status` all decode the full `ClusterStatus`); a cached hit within the 2s TTL takes 0.02s. `buildStatusFromStore` (`api/status.go`) issues ~330 sequential store reads for 16 instances / 3 nodes / ~10 services: 14 `Get`s per instance, a placement `Get` per instance × node, `ReadService` + tag scan + config scan per service, a grant `Get` per secret × service. etcd logged 41k "apply request took too long" (~110ms single-key reads) in 30 min on the memory-starved testbed host.

- [x] Benchmark first: status build against a store with injected 110ms per-call latency; count store round-trips; assert the count is constant in instance, node and service count — baseline 615 reads / 68s (16 instances, 3 nodes, 8 services); `TestStatusBuildRoundTripsConstantInClusterSize` (cluster and team scope)
- [x] Status fact loader: a fixed set of prefix scans (observed instance/node, desired service, placements, derived instance, crash loops, network, volumes, secrets, endpoints) indexed into maps; per-item `Get`s removed from every `collect*StatusFromStore`
- [x] Error semantics: a failed required scan returns an API error, never a partial status that looks like an empty cluster; a missing optional fact stays a blank field (today several scan errors are dropped with `_`)
- [x] Authorization unchanged: loads go through the same API store; team visibility filtering applied as today; no new prefixes or sensitive values exposed (secret values and redacted config stay redacted)
- [x] Consistency stays eventually consistent: no claim of a single store revision (pinned-revision multi-prefix reads are M180)
- [x] Call graph `design/graphs/OBSERVABILITY_GRAPH.md` updated
- [x] Result: 15 (now 14) concurrent scans per build, 0.116s with 110ms injected latency (was 615 reads / 68s); verified through the api-server store role
- [x] Independent review 2026-11-07 (`code-reviewer` + `security-audit` on 3690fd8; approve, no regression, race-clean). Outcome:
  - fixed — `buildStatusFromStore` doc comment over 4 lines → trimmed
  - fixed — failed optional `secrets/` scan dropped silently → debug log with prefix and error
  - fixed — round-trip test only compared small vs large → also asserts at most one read per scan prefix
  - fixed — no end-to-end test for parent/child service names (`payments`, `payments/checkout`) → definition, tags, config and owner visibility kept apart
  - rejected — re-sorting already-sorted per-prefix scans: negligible at status sizes
  - rejected — `requireCoveredKey` panic in a handler path: only reachable by a coding error (keys come from fixed `types.Key*` builders), covered by tests, `net/http` recovers
  - moved to M182 — pre-existing: a `?all=true` status body is written to the 2s cache that plain requests read (`api/server.go` `handleStatus`), so `cca get` can briefly show stopped instances
  - moved to M182 — pre-existing: `/metrics` is unauthenticated, not rate-limited and builds the full status per scrape uncached; Networking, Volumes, CloudIdentities and Endpoints are not team-filtered; empty-valued keys such as `{svc}/resources/cpu` are discovered as services
- [x] Found 2026-11-07 measuring etcd writes on the deploy cluster (60s window): ~89 changes/s, of which ~83/s were 6 `network/dns/hostname/*` keys deleted and re-created (put ≈ delete ≈ 49/s, 35 WAL fsyncs/s at 15ms, 645 reads/s, etcd never compacted, revision 174k for 744 keys). Cause: since M168 the network controller treated `network/dns/hostname/` (hostname-dns) and `network/dns/instance/` (stateful-dns) records as stale service DNS and deleted them; the owners re-created them → write livelock. Fixed: network controller skips those sub-namespaces; `TestDNSControllersConvergeWithoutDeletingEachOther` fails on the old code. Draft PR #13 (same issue, not compiling, and skipped hierarchical service DNS) superseded
- [ ] Open from the same investigation: service DNS keys share `network/dns/` with the sub-namespaces, so a service named `hostname/x` collides (needs a `network/dns/service/` key migration); hierarchical services (`payments/checkout`) get no VIP or service DNS (`extractServiceExposedPorts` only parses `{name}/expose/{port}`); etcd has no auto-compaction; deploy VM disks live on the HDD
- [x] Independent review of e0f0647 (`code-reviewer`; approve, livelock fix correct and sufficient). Outcome:
  - fixed — status snapshot scanned `network/dns/` (required) but no section uses it → dropped (14 scans; it also pulled every hostname/instance DNS key)
  - fixed — `collectNetworkingStatusEntries` (`cmd/cca/command_server.go`) built an unused `dnsMapping` from `network/dns/` that would misread hostname/instance keys → removed
  - fixed — code comments claimed hierarchical service DNS stays safe and embedded measurements → corrected (hierarchical names still map to their first segment in `collectDefinedServiceNamesFromFacts`)
  - follow-up — `hostname-dns` and `stateful-dns` have no write domain in `controllerOutputPrefixes`; network's domain `network/dns/` still covers their keys, so its CAS input guards include churning hostname keys and every hostname write triggers a no-op network reconcile; resolve with the `network/dns/service/` migration
  - not audited — `storage.go` (22 deletes) and `clusterscale.go` for other cross-controller stale-deletion loops
- [x] Deploy cluster: uncached `cca get instances` well below 1s on the same environment — run 42370890022 (58fce68, fresh VMs) green: 16/16 stable, gate TLS T1–T15, 0 etcd timeouts; uncached `cca get instances` 0.02–0.06s (16 instances; was 17–21s). etcd over 60s after the livelock fix: 7 revisions/s (was 21), 7 puts/s (was 49), 10 deletes/s (was 50; remainder outside watchable prefixes, likely audit trim), no `network/dns/` churn (was 83/s), 25 WAL fsyncs/s (was 35), 496 reads/s (was 645 — controller polling, see the read audit below)
- [ ] Follow-up audit (separate, no mechanical changes): controller read patterns with sequential `Get`s inside per-instance/per-node loops; any change keeps revision guards and CAS
- [x] Found while benchmarking (moved: audit writes → M181, `/metrics` and secret list → M182): every allowed read through the API's `AuthorizedStore` persists an audit entry (`StoreBackedAuditLog.Log` → `go persistEntry` → etcd Put), so each uncached status call wrote ~615 audit facts; `/metrics` builds the full status on every scrape; the status secret list is always empty in production because the api-server role has no `secrets/` rule (each call logs a denied scan)
- [ ] Separately: measure etcd revision growth and telemetry write rate before attributing the 260 MB etcd DB size; compaction/defrag as operations, not this fix

### Secrets architecture — Phases 174, 176–179

Specification: `design/config-secrets.md` "M174 design" (decisions 1–7, A–C, R1–R8), Flow 27 and Flows 14/22 in `design/runtime-invariant-flows.md`, CLI in `design/cli-reference.md` "Secrets (M174)". Split 2026-11-06 into five releases. Order: delivery first (agents stop decrypting), then gate keys, then the fresh root key and key migration (needs every agent on delivery), then rotation, then tenant names (needs v2 envelopes for re-encryption).

- [x] Design direction reviewed 2026-11-05: points 1–4, 6, 7 approved; connection secrets as files only (point 5); control plane decrypts, agents receive authorized plaintext over mTLS
- [x] Open questions decided 2026-11-05: A built-in `default` tenant for unowned services; B every gate-serving agent gets the gate keys (residual risk documented); C `CCA_SECRET_TOKEN` one release as per-service opt-in via control-plane delivery
- [x] Second review 2026-11-06 (R1–R8): fresh root key, old master key only as control-plane legacy key; ownership and shares via CLI; `team/default` write scope; gate keys cached in tmpfs; event-driven retry with 5-minute fallback; delivery before `pulling`; measurable migration and retirement gates; v2 under legacy AAD first, canonical AAD after `migrate-names`

### Phase 174 — Secret Delivery to Agents (M174)

Control plane decrypts with the existing key (unchanged envelope format); agents stop decrypting.

- [x] `POST /api/node/secrets {instance_id}`: mTLS `node:` principals only; placed on caller, enrolled, not disabled, every grant present; all-or-nothing; audited (names, never values); refusal audit rate-limited per (node, instance, reason)
- [x] `service:secret-delivery` store principal and role (reads `secrets/`, placement, instance service, desired service/connection, node state, enrollment; writes nothing through the store; `credentials/` not needed: agents never used cloud credentials); `api-server` role still has no `secrets/`; `node-agent` role loses `credentials/`
- [x] Agent: no `secrets/` or `credentials/` reads, no decryption (agent KMS provider and `--kms-provider`/`--kms-key` removed); tmpfs materialization `/run/ccattler/secrets/{instance}/` (dir `0700`, files `0444` so non-root containers can read them), read-only bind mounts via `Spec.SecretMounts` (runtimes no longer copy secrets under the logs directory or `/tmp`), connection URLs as files only; `--api-server` flag
- [x] `waiting-for-secrets` typed state with `observed/instance/{id}/secret-wait` (reason incl. `delivery_not_configured`, secrets, next_retry); before `pulling` also with a cached image (Flows 14/22: pull credential from delivered material); not a failure (no replacement, no crash-loop count, ignored by node-failure controller); shown by `cca get instances`
- [x] Retry: API unreachable 2s…30s backoff; refused → on placement/grant change and every 5 min fallback; agent watches metadata only
- [x] Agent restart keeps material of running placed instances, removes orphans, leaves containers alone; node reboot re-fetches; cleanup after stop/re-placement
- [x] Legacy secret API only for `legacy_secret_api true`, served from tmpfs, per-instance token revoked at stop, deprecation warnings (agent + `cca apply`)
- [x] Agents report `observed/node/{id}/secret_delivery = v2` and `legacy_key_file = present | absent`
- [x] Enrollment response without `SecretMasterKey`; `cca join` writes no key; agent ignores the file (logs once); Ansible join task `creates: /etc/ccattler/pki/node.pem`; agent unit passes `--api-server`
- [x] Unit tests: delivery refusals (wrong node, unplaced, missing grant, disabled node, unenrolled, user/service/non-mTLS principal); availability cases (unreachable, refused, restart reuse, sweep, cached image waits); secrets only as read-only mounts; legacy API from tmpfs material only; enrollment response without key material
- [x] Deploy run 42240496869 found: the control-plane node's `cca init` certificate has no enrollment record, so its agent was refused → server records its own node as enrolled at start (create-only, 60eee55)
- [x] Independent review 2026-11-06 (`code-reviewer` + `security-audit` on bf54400..60eee55; no critical/high). Outcome:
  - fixed — store read errors on the agent counted as "needs no secrets" → instance could start without them; now not ready, retried
  - fixed — material reuse checked grants only → now exactly the granted set and every connection/pull secret, so a removed grant or ungranted reference forces a new delivery
  - fixed — config-change restart reused old values → material dropped on config restart (value rotation without a config change remains M178)
  - fixed — server store/decrypt errors reported as `not_authorized`/`secret_missing` (5-min wait) → plain 500, agent backs off 2s…30s; not audited as refusals
  - fixed — `secret_missing` waited 5 min after `cca secret set` → fixed 30s retry (agent cannot watch `secrets/`)
  - fixed — map-order mount collisions and unclean/unsafe explicit paths → sorted names, cleaned paths, `/proc` `/sys` `/dev` `/run/cca-secrets` refused
  - fixed — hierarchical names (`payments/db-password`) silently undeliverable → validated with `ValidateSecretName`, `/` encoded as `%2F` on disk
  - fixed — no tmpfs check → `cca agent` refuses to write material unless `/run/ccattler/secrets` is tmpfs/ramfs; base dir forced to `0700`
  - fixed — legacy API served revoked grants → checks the current grant on every read
  - fixed — refusal-audit map unbounded / pruned on every request → instance IDs validated (`ValidateResourceName`), map capped at 10000, pruned once per minute
  - fixed — delivery client followed redirects with the node certificate → redirects refused
  - fixed — control-plane enrollment skipped with a rotating server certificate → uses `GetCertificate`
  - rejected — legacy API read racing a material rewrite: rewrite happens only before the container (and its token) starts; transient 403 at worst
  - rejected — audit is fire-and-forget: same for every audit path in the system, out of M174 scope
  - rejected — `RootCAs` nil falls back to system pool: not insecure, server configs always carry the cluster CA
  - rejected — `nerdctl --creds` on the command line: pre-existing (Flow 22), unchanged by M174; noted for M182
- [x] Deploy cluster run 42268165296 (98f639b) green: Airflow (uid 50000) reads its delivered connection and secret files (`0444` in `/run`), all nodes report `secret_delivery = v2`, workers have no `secret-master.key`, no `cca-config-*` copies in `/tmp`, no secret values under the logs directory, 0 refused deliveries; gate TLS T1–T15 and 16/16 stability passed; E2E Cluster Test green on 98f639b

### Phase 176 — Gate Key Sealing & Delivery (M176)

Design: Flow 28 in `design/runtime-invariant-flows.md`; decisions 2026-11-07 in `design/config-secrets.md` "Gate TLS private keys" (inline keys sealed through `PUT /api/gate-key`; plaintext deleted only after every agent reports `gate_key_delivery = v1`).

- [x] Design flow written (Flow 28) and Flow 13 updated

- [x] `RouteTLSController` seals generated keys at `secrets/route/{gate}/tls/key`; cert and expiry stay plain facts (one change group; watches `secrets/route/` so renewal passes the CAS guard; pre-M176 plaintext key kept for the migration controller)
- [x] `tls { key secret "…" }` in the DSL (`desired/route/{gate}/tls/key_secret`); inline PEM kept out of the facts and sent by `cca apply` to `PUT /api/gate-key` (`secret.write`, cluster scope; key must match `tls/cert`) after the gate facts; failure exits non-zero naming the gate; warning for inline keys from Git sources (when M207 lands) — done except the Git-source warning (waits for M207). `POST /api/apply` and the policy gate reject inline keys (they cannot seal a client-side file); `cca fmt` prints `key secret`
- [x] `gate-key-migration` controller: existing plaintext `desired/route/*/tls/key` and `derived/route/*/tls/key` facts sealed at once, deleted only when every enrolled, non-disabled node reports `gate_key_delivery = v1`; persists `observed/gate-keys/migration` (nodes counted: enrolled or registered, not disabled; Flow 28 updated)
- [x] RouteTLSController owns `secrets/route/{gate}/tls/key`: deleted when the gate is gone, has no tls, or uses `key_secret`. Also: system-owned `route/…` secrets hidden from and refused by `/api/secret`, never delivered to instances (a service granting one is refused — found during M176: otherwise any service could receive a gate key), rejected as service secrets and as `key secret` by the compiler
- [x] `POST /api/node/gate-keys`: enrolled, non-disabled node reporting `gate_proxy = enabled`; returns certificate and key together per gate (pair validated), pending gates listed; audited separately, refusals rate-limited (`GateKeyDeliveryService`, store principal `secret-delivery` now also reads `desired/route/`, `derived/route/`; refusal rate limiter shared with Flow 27)
- [x] Agent cache `/run/ccattler/gate-keys/{gate}/` (tmpfs, private key `0400`), reload on agent restart, keep serving while the API is down, reconcile on gate removal/key rotation; `observed/node/{id}/gate_keys = ready | waiting` (`GateKeyCache` + `GateKeySyncer`; refetch on `/tls/` watch events, 5 min fallback, 2–30 s backoff; pending gates keep their cached pair; gate proxy reads pairs only from the cache, never key facts)
- [x] Tests: no plaintext key fact after reconcile and after inline apply; acceptance B (node without a local backend terminates TLS); agent restart with API down keeps TLS; gate TLS e2e T1–T15 unchanged
  - [x] Unit/integration: route-tls seals and never writes a key fact; apply never stores the inline key and deletes the pre-M176 plaintext fact; migration seals then deletes; delivery, cache, syncer, handlers, system-owned secrets
  - [x] Manual on m4800 (2026-11-09, fa8c0a4): 4 gates by SNI, cache survives agent restart with cca-server down, migration completed (design/deploy-host.md)
  - [x] e2e T16 (keys delivered, migration completed), T17 (acceptance B), T18 (agent restart with API down) added to `gate_tls_checks.sh`; Deploy Testbed run 42689347389 on m4800 (e12cf27, eBPF, airflow + whoami + whoami-multiport + gate-tls) green: 16 containers, T1–T18 passed. Known gap: on a fresh cluster T16 cannot exercise sealing/cutover of pre-M176 plaintext keys (covered by unit tests and the manual run)
- [x] Independent review 2026-11-09 (`code-reviewer` + `security-audit`, 444f87c..e12cf27), each finding verified in the code:
  - security 1 (HIGH, fixed): `/api/diff` returned a stored pre-M176 plaintext key as `OldValue` — `collectRemovedGateFacts` no longer touches `desired/route/*/tls/key` (migration owns it), so neither apply nor diff reads it
  - security 2 (HIGH, fixed): `tls/key_secret` was writable with `workload.create` — now needs `secret.use`; `GateKeySecretName` ignores `route/` names
  - security 3 (HIGH, fixed; existed since M169): server-side compile read client file paths (`tls cert`, config `from`) → arbitrary server file read via `/api/diff`/apply — `ValidateNoLocalFileReferences` on `/api/apply`, `/api/diff` and the policy gate
  - security 4 (MEDIUM, fixed): controllers kept the startup key provider after `/api/secret/rotate` — they seal with the shared `SecretStore`'s current provider (`KeyProviderSource`); rotation persistence stays M178
  - security 5 (MEDIUM, fixed): `PutGateKey` could send the key over plain HTTP — refused unless https or loopback
  - security 6 (MEDIUM, fixed): `key_secret` honoured in auto mode — shared rule `types.GateUsesKeySecret` (manual only) in delivery, route-tls and migration
  - security 7 (rejected): purge cache on 403 — Flow 28 decided "no remote wipe"; a transient refusal would cut TLS on an honest node
  - security 8 (fixed/recorded): `/api/status` listed `route/` names → filtered; migration/route-tls flapping on key_secret gates → migration seals only system-key gates; etcd history → compaction note in Flow 28; 4 MiB delivery cap → documented known limit; delivery role reading legacy facts → harmless, no change
  - review 1 (MEDIUM, fixed): pending gates waited 5 min → retried with 2–30 s backoff
  - review 2 (MEDIUM, fixed): capability publish not retried → `PublishGateProxyCapabilityUntilDone`; `gate_keys` rewritten every 30 min
  - review 3 (MEDIUM, fixed): re-apply deleted the legacy plaintext key during a rolling upgrade → same fix as security 1
  - review 4 (fixed): empty key file accepted → compile error
  - review 5 (fixed): refused inline key left a partial apply → checked before writing facts
  - review 6 (fixed): write domains declared for `route-tls` and `gate-key-migration` (the dependency sort that would cycle has no production caller)
  - review 7 (fixed): lost sealed key never repaired → route-tls re-issues when sealed key and plaintext key are both absent
  - review 8 (fixed): cache loaded inside the syncer goroutine → loaded before the proxy starts
  - review 9 (accepted 2026-11-10, documented): `route/` prefix reserved for gate keys; existing user secrets named `route/…` become unreachable — documented in config-secrets.md and cli-reference.md with the rename-before-upgrade step
  - review 10 (fixed): staging suffix length → `stagingSuffixRandomBytes`
  - review 11 (fixed): T18 could exit with cca-server stopped → `gate_tls_fail_with_server_started`
  - review 12 (fixed): T17 could pass on an empty status query → fails instead
  - review 13 (recorded): T16 cannot exercise the cutover on a fresh cluster (known gap above)
- [x] Deploy Testbed green on the review fixes: run 42850695267 on m4800 (5679403, eBPF; includes HARunner post-commit fix f2600df and data plane startup check 5679403), T1–T18 passed

### Phase 177 — Root Key, Keyring & Key Migration (M177)

Design: Flow 29 in `design/runtime-invariant-flows.md`; decisions 2026-11-10 in `design/config-secrets.md` (fresh clusters start on v2 with no legacy key; four ordered steps, one release).

- [x] Design flow written (Flow 29)

**Step 1 — Root key and startup checks** (done 2026-11-10: unit tests; manual `--env lan` e2e on .77 passed with root key 0400 on the control plane only; `cca secret generate-root-key` for clusters that predate M177; the server still seals with the legacy key until step 2)
- [x] Fresh root key `/etc/ccattler/secret-root.key` (`0400`, root), never derived from `secret-master.key`; `cca init` writes it for new clusters (no `secret-master.key`); the HA role copies it to every replica like the CA files (fixes replicas with different keys)
- [x] `--kms-provider` for the root key wires `security.LookupKMSKeyProviderFactory` into the server (no production caller since M174 removed agent decryption)
- [x] Startup errors: missing, unreadable, wrong size, group/world-readable, identical to the legacy key; `loadOrGenerateSecretMasterKey` removed (no silent generation); in-memory store uses an ephemeral in-memory root key

**Step 2 — Keyring and envelope v2** (done 2026-11-10: `KeyringEnvelopeCodec` shared by secrets, credentials and controllers; `cca init` writes no `secret-master.key`, so new clusters start on v2; `POST /api/secret/rotate` now 409 — the old in-memory rotation lost every secret on restart; unit tests incl. race, AAD binding, R1 legacy-key proofs, concurrent bootstrap; manual `--env lan` e2e on .77 with whoami + gate-tls passed (T1–T18): no `secret-master.key`, migration `completed`, legacy `none`, replica report present, sealed gate keys in v2 format)
- [x] Keyring bootstrap in one create-only transaction (`keyring/kek/1`, `keyring/active`, `keyring/root_check`, `keyring/legacy`, migration state: `completed` on a fresh cluster, `not_started` on an upgrade); `root_check` failure → exit; replicas watch `keyring/` and report `observed/keyring/replica/{id}` with a lease
- [x] One envelope codec for `SecretStore`, `CredentialStore`, route-tls, gate-key migration and `PUT /api/gate-key`: v2 (magic, KEK version, wrapped DEK, AAD = store key) once migration is in progress or completed, v1 before; legacy key opens v1 only and never wraps anything new

**Step 3 — Migration gate and controller** (done 2026-11-10 except the role decision below: unit tests incl. 120-secret run through the runner, crash resume, unreadable envelopes, API 409/200; `--env lan --from-release --upgrade` e2e on .77 runs `migrate-keys` after the upgrade: 4 envelopes migrated, count 0, gate keys v2 and re-fetched by every agent with its cache cleared)
- [x] Migration gate + `cca secret migrate-keys --confirm-backup` → `intent/keyring/legacy_migration` (409 lists unmet conditions: backup confirmation, live replica reports, every node `secret_delivery = v2`)
- [x] Keyring controller boundary: runner-enforced write domain `keyring-migration: secrets/, credentials/, observed/keyring/migration` (`controllerOutputPrefixes`) instead of a separate store role. **Decided 2026-11-10 (user):** every controller runs on the runner's raw store, so a role would never be bound (dead code); the process holds the root key and full etcd credentials anyway, so a role would not stop an attacker, and the write domain already drops out-of-domain writes from bugs. `api-server` keeps no rule for `secrets/`, `credentials/`, `keyring/` (asserted by `TestSecretDeliveryRoleIsReadOnlyAndAPIServerRoleHasNoSecrets`). Store-layer isolation for controllers belongs to M181
- [x] Leader-elected keyring controller re-encrypts v1 → v2 under `secrets/` and `credentials/` in bounded CAS batches, resumes after a crash, persists `observed/keyring/migration` (`legacy_v1_envelope_count`, `unreadable_count`)

**Step 4 — Retirement, Ansible, runbook** (done 2026-11-10: unit + API tests; `--env lan --from-release --upgrade` e2e on .77 runs the whole lifecycle — v1.75.0-beta → upgrade → migrate (4 envelopes) → retire → server restart with and without the old file → gate keys re-fetched after each stage)
- [x] Retirement gate + `cca secret retire-legacy-key` (count 0 by full scan, migration completed, no plaintext gate key facts, every node except API replicas `legacy_key_file = absent` — replicas keep the file until retirement because their server opens v1 with it); replicas drop the legacy key and refuse v1; Ansible: worker role removes `secret-master.key` from non-control-plane hosts and asserts it, control-plane roles only with `-e retire_legacy_secret_key=true` after retirement
- [x] Etcd backup/restore runbook in `design/TESTING.md` (referenced by `--confirm-backup`); backup + restore executed on the e2e control plane (marker written after the snapshot gone after restore, `cca status` healthy)
- [x] Tests: legacy key cannot unwrap any KEK, open `root_check` or any v2 envelope; every legacy value unchanged after migration; kill mid-batch and resume; gates refuse with the unmet conditions; v1 refused after retirement; deploy cluster migration under traffic (2026-11-10, d8b3c3e on m4800: `cluster_test.sh --env deploy --from-release --upgrade --workload airflow,gate-tls` — v1.75.0-beta → d8b3c3e with Airflow + gate-tls running, migrate-keys moved 8 envelopes, 0 v1/unreadable left, gate keys v2 re-fetched, legacy key retired and file removed; 12/12 instances running with 0 restarts throughout)
- [x] Independent review 2026-11-10 (`code-reviewer` + `security-audit`, d3f6bb1^..2c0fcfe); each finding verified in the code:
  - security F3 + review 4 + review 5 (fixed): write mode and v1 opening came from `observed/keyring/migration` (writable by any controller-role component; a late v1 write after completion stranded retirement; two concurrent `migrate-keys` could both start). `keyring/legacy` is now the only authority: none | present → migrating → retired, forward only, each step one transaction (`advanceLegacyKeyState`) guarded by the revisions of `keyring/legacy` and the migration fact after re-checking the value; the controller runs while migrating and re-opens a completed migration when v1 appears; retirement requires every live replica report to act on migrating
  - review 1 (HIGH, fixed): an agentless control plane is enrolled at server start, never reports, and blocked both gates forever (and the M176 gate-key cutover). `NodesNotReporting` counts registered nodes only (agent wrote `observed/node/{id}/state`); Flow 28 updated
  - review 2 (documented): replica exclusion matches server `--node-id` with the agent node ID; runbook says to keep them equal (Ansible does)
  - security F2 (fixed): bootstrap compared only `keyring/active`; a deleted `keyring/active` would overwrite KEK 1 and lose every v2 secret. Every bootstrap key is compared absent, and any other `keyring/` key or any v2 envelope refuses bootstrap
  - review 3 (fixed): concurrent Refresh could store an older snapshot; Refresh is serialized; a closed keyring watch is logged and re-subscribed (watch on `keyring/` only, the sole authority)
  - review 10 (fixed): replica report rewritten on every renewal (now also carries `legacy_key_state`); renewal errors logged
  - security F5 (fixed): retire transaction also guards the migration fact's revision
  - security F1 + F6 + review 6 + review 7 + review 8 (fixed): HA role staged root/legacy/CA keys in a fixed, world-readable, never-cleaned `/tmp/ccattler-ha-pki` (a stale legacy key could be redistributed after retirement): per-run 0700 `tempfile` directory deleted after use; root-key fetch tolerant like the legacy fetch; legacy never fetched with `retire_legacy_secret_key`; retirement removes `secret-master.key*` backups too and asserts none is left; runbook covers unreadable-envelope recovery
  - security F7 (fixed): root key loader opens with `O_NOFOLLOW` and checks mode and owner (= process uid) on the opened file
  - security F9 (fixed): migrate-keys and retire-legacy-key require `cluster.admin` (irreversible)
  - security F12 + F8 + review 11 (documented): encrypted off-node snapshots, delete pre-migration snapshots after retirement, `cca init` output is control-plane-only, disabled nodes are not checked
  - style (fixed): typed constants for node report names/values and the migrate response; marshal error handled in the CLI; AUTH_GRAPH entries corrected (`executeServerCommand`, codec passed directly, line numbers)
  - security F4 (M181 dependency): a legacy-key holder with etcd write access can plant v1 data that the migration then re-encrypts as v2; provenance needs agents without `secrets/` write access (M181). Recorded under M181
  - review 9 (rejected): the batch planner decrypts every v1 envelope per cycle to count unreadable ones (O(N²/50)); one-time migration, acceptable below tens of thousands of v1 secrets
  - security F10 (rejected): migration counts, replica versions and `requested_by` in `observed/`/`intent/` are low-value operational status
  - security F11 (info): wrapped DEK without AAD and unauthenticated KEK version only allow denial of service; covered by the nested AEAD

### Phase 178 — Keyring Rotation (M178)

Design: [Flow 30](design/runtime-invariant-flows.md#flow-30-keyring-rotation--root-key-rotation-m178), decisions in [config-secrets.md](design/config-secrets.md#rotation-crash-safe) (2026-11-11).

- [x] Design: Flow 30 + M178 decisions (KEK rotation only after retirement; new `keyring-rotation` controller; offline root key rotation automated by one playbook)
- [x] Step 1 — rotation gate: `cca secret rotate` → `POST /api/secret/rotate` writes `intent/keyring/rotation` + `observed/keyring/rotation`; 409 listing unmet conditions (`keyring/legacy` not `none|retired`, rotation running, replica reports)
- [x] Step 2 — `keyring-rotation` controller: create-only `pending` KEK, `keyring/active` switch, CAS re-wrap in bounded batches (`keyringRewrapBatchSize`), `draining → retiring` when unreferenced and every replica reports the new version, delete after `--keyring-retire-grace` (7d) and a full scan; write domain declared; referenced versions never deleted
- [x] Step 3 — `cca secret rotate-root-key` (local, file root key only): refuses while a replica report is alive, one guarded transaction over every `keyring/kek/*` + `root_check`, `.next`/`.old` files, resume after a crash; a running replica that can no longer unwrap the keyring exits
- [x] Step 3b — rework after the design review below: `--commit|--finish`, `root-key-status`, staged `.next` on every replica before the commit, stale-replica fence
- [x] Step 4 — `deploy/ansible/rotate-root-key.yml` (+ `rotate-root-key-distribute.yml`, `rotate-root-key-start.yml`) and runbook in TESTING.md. **Run 2026-11-11 on the 3+1 HA set on testbed-100.77** (`test/e2e/vm-ha-lan`, `lan-ha-inventory.ini`): 32 s, exit 0; before: all three hosts `idle`, fingerprint b3d19686…; after: all three `idle` on 961560a3…, no `.next`/`.old`, servers active, both test secrets read through each of the three replicas, before/after snapshots written, follow-up KEK rotation 1→2 completed (2 re-wrapped, 0 unreadable), every replica report `active_kek_version: 2`
- [x] HA deploy had been broken since M177 (found by the run above, fixed separately): staging dir per `serial: 1` batch, servers started before the etcd cluster formed, etcd drop-in shadowed by `cca init`'s `tls.conf` (+ data dir owner, unreadable key), no `etcdctl` on Debian, no `--ca-key` in the HA server unit (no enrollment), no `--dns` in the HA server unit
- [x] `keyring replica report: write failed … requested lease not found` after HA start: the role's restart handlers started the servers against the standalone pre-cluster etcds (lease granted on one, put on another). Handlers removed; a fresh HA deploy shows no warning, DNS listening on all three hosts
- Design review of the root key rotation (`security-audit`, 2026-11-11), verified against the code:
  - F1 high, fixed: a replica left running could add a KEK under the old root key around the commit → the re-wrap transaction also guards absence of `intent/keyring/rotation` and `keyring/kek/{max+1}`(`/state`); `CreateWrappedKEK` and `StartKeyringRotation` re-prove the root key (Refresh) first
  - F2 high, fixed: re-running the command on a finished host started a second rotation → `--commit` never generates and refuses without a staged `.next`; `--finish` never writes etcd
  - F3 high, fixed: the new key existed only as one `.next` → staged and checksum-verified on every replica before the commit; `generate-root-key` fsyncs the directory; nothing deletes or regenerates `.next`
  - F4 medium, fixed: rescue decided by playbook position → decided by `root-key-status` (committed → never roll back)
  - F5 medium, fixed: expired reports do not prove no server runs → playbook requires `systemctl is-active` = inactive and one cca version on every host; the command re-reads reports right before the transaction; residual window closed by F1
  - F6 medium, fixed: a second rotation overwrote `.old` → refused before anything changes
  - F7 medium, fixed (design + playbook): root rotation keeps the KEK values → playbook rotates the KEKs afterwards (`rotate_kek_after`, default on); runbook says to delete or re-encrypt pre-rotation snapshots after an old-key leak
  - F8 medium, fixed: key copy hygiene → staging dir deleted in `always:`, `no_log`, `shred -u` for `.old`, fingerprint printed, off-node backup step in the runbook
  - F9 low, rejected: a stale report cannot confirm a start, because the replica reports are verified gone before the commit and the start check is the API port, which opens only after the keyring
  - F10 low, fixed: dry-run unwrap of every re-wrapped value before the commit; resume message corrected (fingerprint output). Not fixed: >~60 KEK versions exceed etcd's 128-op transaction (the re-wrap then fails without change)
  - F11 low, fixed: a staged key equal to the current or the legacy key is refused
- [x] KEK rotation during traffic on the deploy cluster (m4800, 2026-11-11, cluster from Deploy Testbed 43018952938 on 606e0ef, eBPF): `cca secret rotate` → completed in 9 s, KEK 1→2, 8 envelopes re-wrapped (4 Airflow secrets + 4 sealed gate keys), 0 unreadable; 960/960 HTTP 200 to whoami and the Airflow webserver from worker-1 during 120 s around the rotation; 16/16 instances running, 0 restarts; every Airflow secret readable; replica report on KEK 2. (That deploy run failed only on a data plane check misled by a stale nftables table from an older deployment — fixed in 7954ffb, not M178.)
- [ ] Tests: kill at each step and restart; every secret readable; re-wrap leaves DEK/nonce/ciphertext/AAD byte-identical; CAS conflict retried; replicas read the same versions; old root key opens neither `root_check` nor any KEK (unit tests done); `cluster_test.sh` root key rotation (single node + HA) — HA run done by playbook (step 4), not yet automated in cluster_test.sh
- [x] Independent review of the milestone diff 1c5e42e..606e0ef (`code-reviewer` + `security-audit`, 2026-11-11), each finding verified in the code:
  - CR1 medium, fixed (docs): the old-root-key-leak mitigation was overclaimed — re-wraps never change a DEK, so old root key + a pre-rotation snapshot still decrypts every value that existed then; Flow 30, config-secrets and the runbook now say to re-set those values
  - CR2 medium, fixed: one envelope that cannot be re-wrapped wedged the rotation (intent never deleted → every later KEK and root key rotation refused) → the rotation completes when nothing re-wrappable is left, the version stays draining, the status lists up to 10 `unreadable_keys`; new `cca secret keyring-status`
  - CR3 medium, fixed: steps before the rescue block left servers stopped on failure → everything after the stop is inside the block
  - CR4 medium, fixed: a failure after every host swapped re-entered the pre-commit path and restarted "on the old key" → an `.old` with nothing committed now stops the play as ambiguous; `-e resume_root_key_rotation=true` continues; a finished run never leaves `.old` (shredded, or archived as `.retired-<time>` with `keep_old_root_key`)
  - CR5 low-medium, fixed: the follow-up KEK rotation's failure was swallowed → the play fails on it and waits for `completed`
  - CR6 low, fixed (docs): "a replica that stopped reporting cannot be sealing" was too strong; the retire grace (now ≥ 1 h) is the protection; the delete also requires every replica report on the active KEK
  - CR7 low, fixed: warn on an unparseable `keyring/active`; StartKeyringRotation conflicts are typed (409), other errors 500; replica list message. Not fixed: a non-active KEK without a `/state` fact is never retired (only reachable by manual edits)
  - CR8 low, fixed: the watch test no longer sleeps (it re-writes `keyring/legacy` until the watch has seen an event)
  - CR9 low, fixed: doc comment of TestKeyringRotationNeverDeletesReferencedKEK matched to what it asserts; new tests for forged intents, unreadable completion, delete waiting for reports, the version cap, typed conflicts
  - CR10 low, rejected: no test at full batch size against etcd — 40 re-wraps + status = 41 operations, compares capped at 87, within etcd's 128; the runner enforces the budget
  - CR11/12 low, fixed: AUTH_GRAPH line references, `keyringRewrapBatchSize` = 40 in config-secrets, `ansible_host` fallback in the playbook; CI trigger restore tracked below
  - SA1 medium, fixed: `cca init` left the root key and CA key in `/tmp/cca-init` → both control-plane roles shred the key copies after setup.sh; the rotation playbook shreds `/tmp/cca-init/secret-root.key`
  - SA2 medium, fixed: unlimited rotations could outgrow the single root key re-wrap transaction (F10) → the gate refuses at 8 retained KEK versions; grace minimum 1 h
  - SA3 medium, fixed: the controller trusted a forgeable intent → acts only if target = active + 1 and the legacy key is none or retired, and switches only to a target KEK that unwraps (ProveKEKVersion). Removing `intent/` from the api-server role is left to M181
  - SA4 low-medium, partly fixed: StartKeyringRotation re-proves the root key and guards `keyring/root_check`'s revision. Residual (accepted): between CreateWrappedKEK's Refresh and the runner commit a replica left running during an offline root rotation could add a KEK under the old root key — milliseconds wide, needs a server the playbook verified stopped and whose report expired; the commit side is fully guarded
  - SA5 low, fixed: delete re-checks replica reports; grace minimum 1 h
  - SA6 low, fixed: idle cycles skip the scan when there is no intent and no non-active version. Not fixed: orphan `pending` KEK after a manual intent removal (no production path creates one)
  - SA7 low, rejected for M178 → M182: conditional policies pass without a scope check (pre-existing authorizer behaviour)
  - SA8 low-medium, rejected for M178 → M182: `--ca-key` on every HA replica keeps the CA key in three servers' memory (it was already on every control-plane disk; enrollment needs it); documented
  - SA9 low, rejected → M181: etcd trusts every cluster-CA certificate (unchanged by M178)
  - SA10 low, fixed: `no_log` on the HA role's key fetch/copy tasks; KEK step failure fails the play; snapshot expiry and off-node copies documented in the runbook. Not fixed: `ha-deploy.yml` re-run during an interrupted rotation copies the first host's key (do not run it during a rotation — runbook)
- [x] Review fixes executed on the 3+1 HA set (testbed-100.77, 2026-11-11, build c61c5a3):
  - A, failure before the commit (stray `.old` on ctrl-2): play stopped inside the rescue block, every server restarted on the unchanged key, secret readable
  - B, success: 42 s, all three hosts on the new key, no leftover key files, follow-up KEK rotation awaited until `completed`, both test secrets read through each replica
  - C, failure after the commit (commit + swaps done, servers down, `.old` everywhere): plain re-run refused as ambiguous; `-e resume_root_key_rotation=true` finished it (servers up, `.old` shredded, KEK rotation completed), secrets readable through each replica
  - Found while doing so, fixed: the cleanup role never removed `/tmp/cca-init`, so every "clean" HA redeploy skipped `cca init` and reinstalled the previous CA and root key (a fresh redeploy now gets a new root key; checked)
- [ ] Restore the CI triggers removed in 606e0ef (`e2e.yml` push, `e2e-extended.yml` schedule, `upgrade-test.yml` release) when the e2e VM set returns to testbed-100.77

### Phase 210 — Job Secret Isolation Fix (M210)

Found by the M179 round-3 security audit (2026-11-12, SA H2), present in released code since M174: `controllers/job.go:177` writes `observed/instance/{id}/service = {job name}`, and delivery (`security/secret_delivery.go` `authorizeNodeForInstance`) trusted it, so a job named like a service received that service's secrets without `secret.use` (job names are free strings). Scheduled before M179 as a separate fix (user decision 2026-11-12).

- [x] Failing test first: `TestSecretDeliveryRefusesWrongNodeUnplacedDisabledAndUnenrolled` cases "job instance named like a service" and "service not declared" failed (delivery returned the secrets)
- [x] Delivery refuses instances with `derived/instance/{id}/job_owner` and services without the `desired/service/{svc}` marker (`checkInstanceIsServiceInstance`); `secret-delivery` role reads `derived/instance/`
- [x] Docs: Flow 27 step 6, AUTH_GRAPH delivery tree, config-secrets.md delivery role
- [x] `gofmt`, `golangci-lint` (security, api), `go test ./...` green (one api fixture lacked the service marker real applies always write)
- [x] Independent review: `code-reviewer` + `security-audit` on the diff (2026-11-12). Each verified in code:
  - CR 1 (legacy agent secret API still serves a job named like a `legacy_secret_api` service) — **rejected**: the API serves only material that delivery wrote for that instance (`agent/secret_api.go:190` `ReadInstanceSecret(instanceID, …)`), and delivery now refuses job instances, so the job gets "forbidden"; the security audit reached the same result
  - CR 3 / SA 3 (job creation changes ungrouped, `job_owner` written last) — **fixed**: one `instance-create/{id}` group with `job_owner` first (`controllers/job.go` `buildJobInstanceCreationChanges`)
  - SA 1 (job and stateful instance IDs share `{name}-{n}`: a job named like a stateful service overwrites its instances' facts and, with this fix, a stray `job_owner` cuts their secrets) and CR 2 (a job named like a service with grants now waits in `waiting-for-secrets` forever and holds a parallelism slot) — **scheduled as M211** (root cause: shared ID namespace, no create-only instance creation, untyped workload reference)
  - CR 1 proof: `agent/TestLegacySecretAPIRefusesJobNamedLikeService` runs the agent against the production `SecretDeliveryService`; with the M210 check disabled it fails ("job instance received secret material", the released bug), with it the job gets no material and every read is 403
  - CR 4 (stale `job_owner` on a reused stateful ID) — follows from SA 1; closed by distinct names
  - SA 2 (node-writable `observed/instance/{id}/service`) — already recorded below as M181
  - SA 5 / CR 5 (marker check is not a security gate) — **accepted**: it only rejects undeclared services; the docs say no more than that
  - Found while verifying: agents never read `desired/job/{name}/image` (only `KeyDesiredServiceImage`, `agent/agent.go:1093`), so a job instance on a real agent runs only with a same-named service's image — jobs effectively do not run outside the simulator. Pre-existing; **scheduled as M212**
- [x] Deploy-cluster run: Deploy Testbed 43212961434 green on ed24a28 (M210 + M215): 16/16 instances incl. Airflow with secrets, gate TLS T1–T18. A job named like a service is covered by `agent/TestLegacySecretAPIRefusesJobNamedLikeService` and is re-checked live in M214 session 1
- [ ] Not in scope (recorded): the service identity fact stays node-writable (`observed/instance/{id}/service`, compromised node) until M181

### Phase 215 — Keyring Watch Startup Gap (M215)

Found 2026-11-12: CI "Authentication Tests" failed on 21c77db with `TestRunKeyringWatchExitsAfterRootKeyRotation` timing out (reproduced locally ~1/30 with `-race -cover`). Cause: `RunKeyringWatch` (`security/envelope_codec.go`) refreshed only on events or the 60 s fallback tick, so a keyring change between the codec load and the watch subscription (a replica starting during a root key rotation) was missed for up to 60 s; the test's nudge re-wrote `keyring/legacy` with its own value, which `MemoryStore.Put` ignores (no event), so it never covered that window.

- [x] Failing test first: `TestRunKeyringWatchSeesRotationBeforeSubscribe` (rotation before subscribe, no further writes) timed out with the watch still running
- [x] Fix: one `Refresh` right after subscribing; root-check failure returns at once
- [x] Old test no longer relies on no-op writes; both tests pass 100× under `-race -cover`
- [x] AUTH_GRAPH updated
- [x] Independent review (2026-11-12): `code-reviewer` APPROVE, `security-audit` "strong". Verified in code:
  - CR doc comment 5 lines (limit 4), duplicated refresh handling, stale graph line — **fixed** (`refreshKeyringFromWatch` helper, comment trimmed, AUTH_GRAPH)
  - CR/SA: `EtcdStore.Put` also skips identical values (`store/etcd.go:164-188`, `TestEtcdIdempotentPut`), so the old nudge never worked on either backend — confirms the cause
  - SA 1 (initial refresh runs in the watch goroutine, a stale replica may serve for milliseconds) — **rejected**: a root key rotation re-wraps KEKs without changing their values, so a stale snapshot still seals and opens correctly; KEK creation refreshes first and the re-wrap transaction is guarded
  - SA 2 (any `UnwrapKey` error, e.g. a KMS timeout, is reported as `ErrKeyringRootCheckFailed` and exits the replica, `security/keyring.go:218-221`) — pre-existing, KMS providers only; **scheduled in M182**
  - SA 3 (no backoff when a watch keeps closing: hot loop of re-subscribe + refresh) — pre-existing; **added to Runner hardening**
  - SA 4/5 (initial-refresh retry hint, report lease after `os.Exit`) — accepted, within the existing 45 s `--finish` wait
- [x] CI green on ed24a28 (Test, Authentication, Security, CodeQL, Trivy); Deploy Testbed 43212961434 green

### Phase 214 — Adversarial User Testing (M214)

Priority: right after M210 (user decision 2026-11-12). Unit tests, reviews and the scripted deploy run check what we expected; this milestone has someone act as a real user (and as a careless or hostile one) on a deployed cluster, the way M210's job hole and the M211–M213 bugs would have been found earlier. Manual first, then automate what was confirmed.

- [x] Plan written: [design/adversarial-testing.md](design/adversarial-testing.md) — two `haiku` subagent testers in parallel (A: `adv-dev`, `api-writer`, team `payments`, sessions 1+4; B: `adv-reader`, `api-reader`, team `frontend`, session 2), own short-TTL tokens, forced-command SSH keys that can only run `cca` as that user, budget 150 commands / 60 min each; orchestrator keeps admin and sessions 3+5, verifies every finding
- [ ] Run it (preconditions: M210 pushed, Deploy Testbed green, runner idle)
- [ ] Charter `design/adversarial-testing.md`: rules (deploy cluster on m4800 only, run from the runner checkout, never the e2e or HA sets during CI, no `rm -rf`, restore with `vmherd destroy` + Deploy Testbed), roles used (cluster admin, `api-writer`, `api-reader`, team-scoped users, node identity), how a finding is recorded (command, expected, actual, evidence, severity)
- [ ] Session 1 — names and identity: the same name for a service, job, cronjob, volume, gate, connection; hierarchical names with attribute segments (`x/secret/y`, `x/owner`, `x/instances`); uppercase, `_`, `.`, `:`, unicode, 253+ characters; re-apply with changed or omitted fields; delete and re-create with the same name
- [ ] Session 2 — authorization as a non-admin: every `cca` command with `api-reader`, `api-writer` and team-scoped tokens; secrets via apply, status, describe, `/api/state`, watch, legacy APIs; claiming another team's service, gate or connection; expired, revoked and malformed tokens
- [ ] Session 3 — lifecycle under failure: kill/restart server, agent and etcd during apply, rollout, scale, job runs and secret rotation; disable a node mid-rollout; fill the disk; clock skew on one node; network partition between a worker and the control plane
- [ ] Session 3b — broken releases under traffic (2026-11-12, after a K8s "deploy a non-existent tag on purpose" write-up): a load loop against a service VIP and a gate counts failed requests while rolling out (1) a non-existent image tag, (2) an image that exits at start, (3) an image that runs but never passes readiness, (4) each of these on a 1-replica service; then roll back by re-applying the old config and time the recovery. Expected: 0 failed requests, old instances keep serving, a clear rollout status. Hypotheses from reading `controllers/rollout.go`, to confirm or refute here: auto-rollback counts only `failed` new instances, and `image-pull-backoff` never becomes `failed`, so a bad tag never rolls back by itself; a new instance counts as healthy when `running`, not when ready, so case (3) may stop old instances; with no new instance yet, `buildOldInstanceStopChanges` may stop one old instance before a replacement exists (case 4 = outage). Then automate as a Deploy Testbed check
- [ ] Session 4 — limits: 300 services created and deleted (VIP pool), configs that exceed one etcd transaction, many shares or grants, huge secret values, many concurrent `cca apply`, watch connection limits
- [ ] Session 5 — data plane: VIP and DNS after churn, gates and TLS with wrong or missing keys, network policy bypass attempts from a container, volume host paths
- [ ] Every confirmed finding: a failing automated test first (unit/integration where possible), then a milestone item placed by severity (security and data loss before features); record in this phase
- [ ] `test/e2e/adversarial/`: confirmed misuse cases as a repeatable script run by the Deploy Testbed workflow (expected refusals asserted, not just "no crash")
- [ ] Repeat one short session after each security milestone (M179, M181) — add the item to their checklists

**Run 1 (2026-11-12, deploy cluster ed24a28 / v1.78.0-beta).** Two haiku testers in parallel: A `adv-dev` (`api-writer`, team payments, ~55 calls), B `adv-reader` (`api-reader`, team frontend, ~90 calls). Transcripts on m4800: `~/adv/adv-dev.md`, `~/adv/adv-reader.md`, `~/adv/orchestrator.md` (setup), `~/adv/watch.log`. Every finding below verified by the orchestrator in code, transcript or a re-run as admin:

| # | Finding | Verified | Resolution |
|---|---|---|---|
| A1 / B-F1 | `cca apply` from a remote client (token context) silently runs a local 3-node simulation and prints a fake cluster status; nothing reaches the cluster. The CLI has no API apply path (`cmd/cca/command_apply.go`: local simulation or direct etcd only) | code + transcripts | **M218** |
| B-F2 | `POST /api/gc` has no capability check (`api/handler_gc.go:27`): any authenticated user (here `api-reader`) makes the server delete instance facts | code + transcript (`gc instances` ran for the reader) | **M216** |
| A2 | `scale` has no upper bound: `scale a-web 100000` was accepted and 436 instances were created before the tester scaled back | code (`api/server.go:869`) + transcript | **M217** |
| A3 | `scale` on an undeclared service creates orphan instances with no service, VIP or endpoints (`scale nosuch 2`, 30× `a-loop-*`) | code (no existence check) + transcript | **M217** |
| — | `/api/scale` ignores store write errors and still answers `ok: true` (`api/server.go:881-886`, found while verifying A2) | code | **M217** |
| B-F7 | Reader's `cca audit` returned "No audit entries found" while admin sees entries | `resolveAuditTenantFilter` in `handleAudit` | **fixed in M216**: filter leaked (teamless non-admin/non-platform readers got no filter, saw everything; substring match `pay` hit `payments`); now fail-closed 403 for teamless readers, exact-segment tenant match (`resolveAuditTenantFilter`, `auditEntryInTenant`) |
| A4 / A5 / A7 | Service names with `/` overwrite another service's fields (`checkout/owner` → `desired/service/checkout/owner`, `ledger/instances`); owner taken from the first name segment unchecked; names with uppercase, `:`, `.`, unicode, 254 chars accepted | diff output in transcript; code (M179 round 2/3 SA HIGH-1) | **M179 step 1** (name validation, owner checks) — confirmed live |
| A6 | A job named like an existing service passes lint/diff with no warning; `describe job` is not a resource type | transcript | **M211** (cross-kind names), **M212** (job visibility) |
| — | Stateful `ledger` (`stateful` in the config) got random instance IDs, not `ledger-0/1`; `describe` shows no stateful marker | admin `describe service ledger` | investigate first in **M211** (its premise is the `{name}-{n}` collision) |
| A8 / B-F3 | `cca token …`, `root-key-status`, `rotate-root-key`, `generate-root-key` open etcd / local files directly; from a remote client they fail with raw etcd errors (`localhost:2379`) | code (`cmd/cca/command_misc.go:359`) | **rejected as security finding** (needs etcd client certs, i.e. a control-plane host); confusing error → **M218** |
| A10 / B-F8 | `cca whoami` reads only the legacy credential file, ignores contexts and tokens: "not logged in" while every command works | code (`cmd/cca/command_login.go:235`) | **M218** |
| B-F9 | `get secrets` → "No secrets found." while `secret list` returns 5 names (also as admin) | admin re-run | **M218** |
| B-F10 / F11 / F12 / F13, A11 | inconsistent `*_CMD` redaction in describe; `--team` filter possibly ignored; `readiness` requires `go.mod` in the cwd; `images pull` 403 shown as "FAILED (HTTP 403)"; `audit --denied` unsupported by the CLI though the API filters on it | transcripts | **M218** (each to reproduce first) |
| B-F4 | `logs webserver-…` flagged by the local secret scanner | admin check: none of the 4 Airflow secret values occurs in the 364 KB log | **rejected** (scanner false positive on the word "Password") |
| B-F6, A9 | Readers see other teams' services, logs, secret names | — | **accepted**: cluster-wide reads are today's model; tenant scoping is M179/M181 |
| B-F5 | `put-config` accepted for the reader | — | **rejected**: test-harness wrapper feature, not product |
| clean-up | `cca user delete` leaves role grants and does not invalidate the user's tokens (deleted `adv-reader` token still authenticated) | admin re-run | **M216** (top item) |
| B-F14 | Events show `nosuch-*` pending instances and an `observed/instance/` history gap | explained by tester A's `scale nosuch` and the 436-instance flood | covered by A2/A3 |

Tester A's self-escalation probe (`role grant adv-dev cluster-admin`) was blocked by the local auto-mode classifier before reaching the cluster; repeat it in run 2 (B's equivalent was refused with `cluster.admin`).
- [ ] CI, gating (every Deploy Testbed run, after the cluster checks pass): replay of the tester transcripts without any AI — `scripts/transcript-to-cases` turns `~/adv/*.md` into draft cases in `test/e2e/adversarial/cases.yml` (identity, setup config, `cca` args, expected exit code, `must_contain` / `must_not_contain` patterns — never exact output, IDs and times vary); the expectations are reviewed by hand (a transcript records what happened, not what should happen); `deploy/ansible/adversarial.yml` creates the per-run users and 1 h tokens, runs each case on the control node with that user's context, asserts, writes the same Markdown transcript, deletes the users; runs against any inventory (deploy, lan, HA). Transcripts uploaded as a workflow artifact
- [ ] CI, exploratory, non-gating, **manual only** — to set up (wanted, 2026-11-12); exact setup steps in [design/plans/plan-M214-ai-testers-api-key.md](design/plans/plan-M214-ai-testers-api-key.md) (user decision 2026-11-12: `workflow_dispatch` started and watched by a person; never on every green deployment — green deployments run the Ansible replay above): the two AI testers run headless (`claude -p`, model `haiku`, fixed prompts from `design/adversarial-testing.md`, 150-call / 60-min budget) through the same forced-command wrapper; output = transcripts + findings report as artifacts, never a red build by itself. Needs: an Anthropic API key as a repository secret available only to the `deploy` runner job, a spend cap, and the wrapper/keys provisioned by `scripts/setup-deploy-host.sh` instead of by hand. A confirmed finding still goes through the manual verify → failing test → milestone path

### Phase 216 — API Authorization Gaps (M216)

Found in M214 run 1 (2026-11-12). Priority: first after M214's setup, before M211.

- [x] **Deleting a user revokes nothing** (found at M214 run 1 clean-up, 2026-11-12): after `cca user delete adv-dev`, `cca role list` still showed `user:adv-dev api-writer`, and the deleted `adv-reader`'s token still authenticated (403 "no workload.read grant" after the grants were revoked by hand, not 401). Fixed:
  - user deletion removes the record, every grant under `auth/grant/user/{name}/` and the inline role `auth/role/inline-user-{name}/` in one transaction (`collectUserDeleteOperations`, `api/server.go`); test `api/TestUserDeleteRemovesGrantsAndInlineRole` failed first (4 leftover facts)
  - user tokens are accepted only while the user record exists and was created no later than the token's `iat` (`security.LocalUserRecordAuthenticator` around the internal user-token authenticator; store errors refuse); tests `security/TestLocalUserTokenRefusedAfterUserDeleted`, `TestLocalUserRecordCheckFailsClosedAndPassesOtherCredentials` failed first (did not compile, no check existed)
  - group memberships (`auth/group/{g}/member/{user}`) are configuration-owned and stay; the token check already refuses a deleted user. Edge: a user re-created within the same second as an old token's `iat` accepts that token (second precision)
- [x] `/api/gc` requires `cluster.admin` (it deletes facts); `api/TestGCRequiresClusterAdmin`; the sweep below failed first on it
- [x] `/api/audit` stays `workload.read` with its tenant filter (requiring `cluster.admin` was tried and reverted: the filter is the intended design), but the filter **leaked** (M216 security review, verified): a reader without a team and not platform got no filter and saw every entry, and the team match was a substring (`pay` matched `paynter`, `payments`). Fixed: such readers get 403, cluster admins and platform principals see all, tenant match is by whole path segment (`resolveAuditTenantFilter`, `auditEntryInTenant`); `api/TestAuditTenantFilterFailsClosedAndMatchesWholeSegments` failed first on both leaks. Note: `Server.SetTenantAuditView` / `tenantAuditView` have no production caller (dead code) — remove in M218
- [ ] Open (security review): open watch, events and audit-follow streams keep running after their user is deleted or the token expires (authentication is checked once when the stream opens)
- [x] Document config-owned group memberships (user decision 2026-11-12): user delete does not remove `auth/group/*/member/{name}`; re-creating the name reattaches configured groups; covered by `integration/TestUserLifecycleWithConfigOwnedGroupMembership`. Documented in `design/security-model.md` "Auth Groups and User Deletion (M216)"
- [x] Sweep `api/TestReadOnlyPrincipalCannotWriteThroughAnyRoute`: every route registered in `api/server.go` gets POST/PUT/DELETE as a built-in `api-reader`; any 2xx fails, except a reviewed allowlist (`/healthz`, `/metrics`: read-only for any method). Found only `/api/gc`
- [x] Independent review (`code-reviewer` + `security-audit`) of the M216 diff (ea93889..HEAD); findings below

| # | Source | Finding | Verified at | Resolution |
|---|---|---|---|---|
| CR-S1 | code-reviewer | `resolveAuditTenantFilter` returns `("", true)` for empty principal name — fail-open for an unreachable case | `api/server.go:1399` | **fixed**: empty name now returns `("", false)` |
| SA-H1 | security-audit | TOCTOU race in user deletion: grants added between scan and transaction survive as orphans | `api/server.go:2101` collectUserDeleteOperations | **accepted**: orphaned grants are harmless — `LocalUserRecordAuthenticator` refuses tokens for deleted users; only cluster.admin can grant roles, making this an admin-vs-admin race in a millisecond window |
| SA-H2 | security-audit | Pre-M216 tokens (no `cca_user_rev` claim) rejected immediately on upgrade | `security/authn_local_user.go:55` | **accepted**: intentional breaking change (beta); users re-authenticate after upgrade; document in release notes |
| SA-M1 | security-audit | SSE streams (watch/events/audit-follow) survive user deletion | `api/server.go:1340` | **already tracked** (open item above, line 409) |
| SA-M3 | security-audit | Error message says "earlier version" for tokens missing the revision claim | `security/authn_local_user.go:55` | **fixed**: split into "missing revision claim" vs "minted for an earlier version" |
| SA-L1 | security-audit | Dead code `SetTenantAuditView` / `tenantAuditView` | `api/server.go` | **already tracked** for M218 removal |
| CR-Style | code-reviewer | Missing struct field comments on test harness | — | **rejected**: all test struct fields already have inline comments |

- [x] Deploy-cluster check (2026-11-14, m4800 on 15f3156): created m216-test, minted token → 403 (authenticated, no grant); deleted user → 401; re-created same name → 401 (old token's revision refused)

### Phase 217 — Scale API Safety (M217)

Found in M214 run 1 (2026-11-12): `scale a-web 100000` created 436 real instances; `scale nosuch 2` created orphan instances.

- [ ] Failing tests first: scale above the limit → 400; scale of an undeclared service → 404; store write error → 500, not `ok: true`
- [ ] Upper bound per service (named constant, overridable by `cluster.admin` only) — also on `instances` in apply
- [ ] `/api/scale` requires the `desired/service/{svc}` marker
- [ ] Store write errors returned to the client (`api/server.go` `handleScale`)
- [ ] Clean-up path for orphan instances already created by earlier scale calls (gc or controller)

### Phase 218 — CLI Remote Correctness (M218)

Found in M214 run 1 (2026-11-12). The CLI behaves as if it ran on the control plane.

- [ ] `cca apply` with a remote context uses `POST /api/apply` (authz, policy gate) instead of a silent local simulation; the local simulation only with an explicit `--simulate`
- [ ] `cca whoami` reads the current context (server, user, token expiry)
- [ ] `get secrets` and `secret list` agree
- [ ] Local-only commands (`token`, `root-key-status`, `rotate-root-key`, `generate-root-key`) say "run on a control-plane node" instead of raw etcd errors
- [ ] Reproduce and fix or reject: `--team` filter, `readiness` needing `go.mod`, `images pull` 403 message, `*_CMD` redaction rule, `audit --denied`

### Phase 211 — Workload Identity & Safe Instance Creation (M211)

Found during the M210 review (2026-11-12). Root cause, verified in code: (1) jobs (`controllers/job.go:139`) and stateful services (`controllers/instance.go:438`) both build instance IDs as `{name}-{n}` from user-chosen names, but services and jobs live in separate desired namespaces, so nothing ever compares their names; (2) controller `Change` has only put and delete (`controllers/controller.go:25`), so creating an instance overwrites an existing one silently — no create-only option like the keyring's transactions; (3) an instance's workload is the bare string `observed/instance/{id}/service` (a job writes its own name there), not a typed reference. Effect: job `victim` writes `victim-0` over stateful service `victim`'s instance (state back to `pending`, plus `job_owner`, which since M210 cuts its secrets); a job named like a service with secrets waits in `waiting-for-secrets` forever and holds a parallelism slot.

- [ ] Failing tests first: job named like a stateful service does not touch the service's instances; job named like a service is rejected at apply
- [ ] Apply rejects a name used by more than one workload kind (service, job, cronjob), checked against the configuration and the store; cronjob-generated job names cannot equal a service name
- [ ] Controller `Change` gains a create-only put (transaction compare: key absent); instance and job instance creation use it; a conflict is reported, never overwritten
- [ ] Typed workload reference for instances (`kind` + `name`) written by controllers only; delivery, agent and failure handling use it instead of the bare service string (also removes the node-writable identity used by delivery — coordinate with M181)
- [ ] Startup/lint listing of existing clusters that already have a cross-kind name collision

### Phase 212 — Jobs Run on Real Agents (M212)

Found during the M210 review (2026-11-12): agents read an instance's image only through `lookupServiceImageFromStore` (`agent/agent.go:1093`, `desired/service/{name}/image`); nothing in `agent/` reads `desired/job/{name}/image` or the job command. A job instance on a real agent therefore starts only with a same-named service's image and command, otherwise not at all. Simulator-only tests hid it.

- [ ] Failing test first: a job with no same-named service runs to completion on an agent (integration with the process or container runtime, not only the simulator)
- [ ] Agent resolves job instances (typed reference from M211) from `desired/job/{name}/…`: image, command, pull secret, completion reporting
- [ ] Deploy-cluster check: a one-shot job and a cronjob complete; add to the Deploy Testbed workloads

### Phase 213 — VIP Pool Allocation (M213)

Found in the `design/TESTING.md` triage (2026-11-11), scheduled 2026-11-12.

- [ ] Failing tests first: exhaustion and reuse after 254 create/delete cycles
- [ ] Allocate the lowest free address inside `--vip-cidr` (`controllers/network.go` `determineNextVIPFourthOctet`), report exhaustion explicitly as an observed error, never emit `.255` or an address outside the CIDR

### Phase 179 — Tenant-Scoped Secrets (M179)

Spec, decisions and tasks: OpenSpec change [`openspec/changes/tenant-scoped-secrets/`](openspec/changes/tenant-scoped-secrets/) (design.md, specs/…/spec.md, tasks.md). Legacy name migration split off to M209 on 2026-11-12 (user decision after review round 2).

- [ ] Canonical names `secrets/tenant/{t}/…`, `secrets/cluster/…` via `/api/secret/v2/…`; tenants for services, connections (`owner`) and gates (target service's tenant); name validation (forged grant keys), reserved tenant names and secret first segments
- [ ] CLI: `cca secret set/get/list/delete/share/unshare --tenant/--cluster` (default tenant when omitted), value from stdin or `--from-file`; shares `desired/secret_share/{owner}/{consumer}/{name}`, also for cluster secrets
- [ ] Apply authorized at the owning tenant's scope (one resolution stage, both apply paths); conditional policies no longer widen scope (moved from M182)
- [ ] Resolution and delivery: grant value `{source, reference, mount_path}`, delivery re-checks tenant/grant/share, every container start fetches, legacy grants stay pinned, no restarts from the format change
- [ ] Mixed-version guard over replicas and agents; conflict records + `delete-conflict` for canonical-looking legacy keys
- [x] ~~Drop the `route/` secret-name reservation~~ — decided 2026-11-12 (draft review): the reservation stays permanently; `tenant`/`cluster`/`route` reserved as secret first segments and, with `default`, as tenant names
- [ ] Optional admission policy rejecting services that fall into `default` (off by default)
- [ ] Tests: every scenario in the change's spec.md (acceptance A; share → delivered; `unshare` → a crash restart is refused; cluster secret needs a share; `--tenant` with `--cluster` rejected; upgrade without restarts)

**Draft review (2026-11-12, code-reviewer + security-audit on the OpenSpec draft).** Each finding verified in the code; all accepted into the revised draft except SA-L3/L5 (deferred, out of scope):

| # | Finding | Verified at | Resolution |
|---|---|---|---|
| CR-C1 / SA-H1 | Apply and secret handlers authorize at `ScopeCluster` only; `owner payments` claims a tenant | `api/server.go:718`, `security/fact_authorizer.go:33`, `api/server.go:1839` | Fixed in design: scope of the resolved tenant; owner change needs `cluster.admin` (tasks 2.4, 2.5) |
| CR-C2 | `lang.Compile` is pure, cannot check shares | `api/server.go` `lang.Compile(file)` | Fixed: resolution stage before commit on both apply paths (task 4.1) |
| CR-C3 / SA-M9 | Delivery, agent, connections, pull secrets, gate `key secret` use raw names; connections are global | `security/secret_delivery.go:192-207`, `agent/agent.go:1128`, `lang/compiler.go:890-905` | Fixed: canonical grant key + `{source, reference}` value, per-consumer resolution (tasks 4.2–4.5) |
| CR-C4 / SA-H3 | Legacy names may look canonical (`tenant/x/y`) | `types/validate.go:63` | Fixed: reserved first segments; plan/startup conflict check (tasks 2.2, 3.2) |
| SA-H2 / CR-H6 | Old replica ignores `?tenant=`, writes flat key; guard misses no-report and default-tenant writes | `api/server.go:1810`, `security/keyring_migration.go:60-93` | Fixed: `/api/secret/v2` (old replicas 404), separate guard helper, default writes gated, 409 without reporter (task 3.1) |
| SA-H4 / CR-H3 | Lifting `route/` reservation is racy and stateless | `types/validate.go:105` `IsSystemOwnedSecretName` | Fixed: reservation kept permanently |
| SA-H5 | Crash restart reuses tmpfs after unshare | `design/config-secrets.md` failure table | Fixed: agent revocation (task 4.6) |
| CR-H1 | Re-apply before migration breaks existing services | design | Fixed: legacy fallback for existing grants (task 4.2) |
| CR-H2 / SA-M6 | One txn per secret exceeds 128 ops; no guards; no keyring precondition; mapping untrusted | `store/memory.go:35` | Fixed: two-phase resumable migration, preconditions, mapping validation, foreign-tenant refusal (tasks 5.x) |
| CR-H4 / SA-M3 | `secretDeliveryRole` cannot read shares | `security/secret_delivery.go` (no `secret_share`) | Fixed: task 4.3 |
| CR-H5 / SA-M7 | Listing uses `secret.metadata.read`, not `secret.read`; status/describe leak | `api/server.go:1839`, `api/status.go:463`, `api/describe.go:572` | Fixed: keep `secret.metadata.read` per scope; filter status/describe (task 4.7) |
| SA-M4 / CR-M1 | Share key prefix ambiguity; shares survive delete | design | Fixed: `{owner}/{consumer}/{name}`, exact Gets, delete cascade (task 2.7) |
| SA-M1 | Fallback to `default` on store error | `tenant/tenant.go:53-63` | Fixed: only absent owner → `default` (task 2.3) |
| SA-M2 | Error text is an existence oracle | design | Fixed: one error text |
| SA-M5 | Share facts writable through apply with `workload.create`; ABAC skips scope | `security/fact_authorizer.go:48-56`, `security/api_authorizer.go:122-135` | Fixed: `desired/secret_share/` → `cluster.admin`; ABAC scope stays M182 |
| CR-M2/M3/M6, SA-M8 | `default` rules, name length, missing scenarios | spec | Fixed in spec and task 2.2 |
| CR-M7 | "Flow 9" does not exist; Flows 27/28 change | `design/runtime-invariant-flows.md` | Fixed: task 1.1 |
| SA-L1 | No audit records specified | spec | Fixed: audit requirement (task 6.2) |
| SA-L2 | `TenantSecretStore` unwired | `tenant/secret_isolation.go` (no production caller) | Fixed: delete (task 2.8) |
| SA-L3/L5 | Quotas, tenant deletion | — | Out of scope (proposal non-goals); tenant deletion leaves secrets owned by the name, noted for multi-tenancy work |

**Review round 2 (2026-11-12, revised draft).** Each finding verified in the code; user decisions 2026-11-12: gate tenant = target service's tenant, connections get `owner`, cluster secrets shareable, migration by controller (M209), fetch on every start, conditional-policy fix in M179, jobs out of scope, split migration to M209.

| # | Finding | Verified at | Resolution |
|---|---|---|---|
| SA HIGH-1 | Service name `x/secret/y` forges grant key of `x`; names not validated in `lang/` | `lang/parser.go:1397` `expectHierarchicalName`, no `ValidateResourceName` in `lang/` | Fixed: segment validation, reserved attribute segments, grant facts only from declared references (tasks 2.2, 3.1) |
| SA HIGH-2 | Global connections: any tenant redefines `db`, receives others' secrets | `desired/connection/` → `workload.create` | Fixed: connection `owner`; secret resolves in the connection's tenant; change needs owner scope (tasks 2.4, 5.2) |
| CR N1 / SA HIGH-3 | Grant value drops explicit mount path | `lang/compiler.go:870` | Fixed: `{source, reference, mount_path}`, old value parsed (tasks 5.3, 5.6) |
| CR N2 | Grant changes restart every instance (config hash) | `agent/agent.go:1388` | Fixed: hash over `(reference, mount_path)` (task 5.7) |
| CR N3 | Phase 2 exceeds 128 ops | `store/memory.go:35` | Fixed in M209: phases 2a/2b |
| CR N4 / SA HIGH-5 | Gates and jobs have no owner | `lang/ast.go:497` `RouteDecl` | Fixed: gate = target service's tenant (task 5.5); jobs out of scope (pull secret key has no production reader) |
| CR N5 | Per-fact authorizer has no tenant; two principal types | `security/fact_authorizer.go:26-30`, `tenant/policy_gate.go:121` | Fixed: one resolution stage for both paths (task 3.1) |
| CR N6 | Share-revision guard on grant write impossible (sequential Puts) | `api/server.go:702` | Fixed: guard dropped, delivery is the enforcement point |
| CR N7 | Old agents not guarded | `security/keyring_migration.go:51` pattern | Fixed: node report `secret_name_format` (task 4.2) |
| CR N8 | Canonical-looking legacy keys unreachable, block forever | design | Fixed: conflict records + `delete-conflict` (task 4.3) |
| CR N9 / SA MEDIUM-11 | Cluster share contradiction; no delivery check for cluster secrets | `security/secret_delivery.go:192-207` | Fixed: cluster secrets reach tenants via shares; delivery checks them |
| SA HIGH-4 | Foreign grant blocks owner deleting a leaked secret | design | Fixed: only own-tenant grants block; `--force` (task 4.5) |
| SA MEDIUM-6 | Conditional policies skip scope | `security/api_authorizer.go:122-130` | Fixed in M179 (task 3.4); removed from M182 |
| SA MEDIUM-7 | `intent/secret_names/` writable with `workload.create` | `security/fact_authorizer.go` `workloadFactPrefixes` | Fixed in M209 (`cluster.admin`, re-validation, hash) |
| SA MEDIUM-8 | v2 path ambiguity (`share`, `migrate-names` as names) | design | Fixed: fixed endpoints with query/body (task 4.1) |
| SA MEDIUM-9 | Same-named canonical secret silently replaces legacy value | design | Fixed: legacy grants stay pinned (task 5.2) |
| SA MEDIUM-10 | Revocation races; legacy agent API checks grant only | `agent/secret_api.go:253` | Fixed: fetch on every start, generation counter, share check (tasks 5.6, 5.8) |
| SA MEDIUM-12 | Parser cannot express qualified references; invalid grants skipped silently | `lang/parser.go:1548`, `security/secret_delivery.go:199` | Fixed: tasks 5.1, 5.4 |
| SA MEDIUM-13 | Hierarchical name squatting | `lang/compiler.go:312` | Fixed: first segment must equal owner (task 2.3) |
| SA MEDIUM-14 | Compromised node claims instances | M181 notes | Accepted until M181; stated in threat model (task 6.3) |
| CR M1 | Reserved tenant names break existing services; unknown tenants | `lang/compiler.go:312-318` | Fixed: lint/startup listing; `--tenant` requires a declared tenant (tasks 2.5, 4.1) |
| CR M2 | Checklist contradicted design | milestones.md | Fixed: checklist rewritten |
| CR M3 | Migration as long HTTP request | — | Fixed in M209: intent + controller |
| CR M4 | Dual grants during migration | — | Fixed in M209: de-duplicate by reference |
| CR M5/M6, SA LOW-15/16/18/19 | Missing scenarios; mount collisions; share to `default`; listing via `GrantedScopes`; audit fields | spec | Fixed in spec/design |
| SA LOW-17 | Refusal reasons in observed state are an existence oracle | `security/secret_delivery.go:108-116` | Rejected: operator-visible, low; recorded as accepted risk |
| SA LOW-20 | Old replica `/api/secret` accepts `tenant/x/y` | — | Covered: conflict records at first guard pass |
| SA LOW-21 | Owner-change TOCTOU | — | Rejected: owner change needs `cluster.admin`; apply has no transaction (residual, low) |
| CR L3 | Guard passes while a stopped replica's lease expired | — | Accepted: upgrade rule in release notes; returning replica blocks further writes |

**Review round 3 (2026-11-12).** Each verified in code; user decisions 2026-11-12 (all recommendations accepted): job hole fixed first as M210; legacy grants byte-identical; conditional policies cluster-only; stored owner/target checked; decode table with cluster default; ownerless connections need `cluster.admin`; guard only on set/share; share/connection visibility; `keyring/legacy` none|retired for set/share; one release in four steps. All other findings fixed in design.md/spec.md/tasks.md:

| # | Finding | Verified at | Resolution |
|---|---|---|---|
| CR H1 | New hash format restarts every instance on agent upgrade | `agent/agent.go:1388`, stored hash `:1004` | Legacy bytes unchanged + golden test (task 3.7) |
| CR H2 / SA M4 | Conditional policies have no scope field | `security/api_authorizer.go:21-25` | Cluster scope only, `ResourceContext{Team}` on scoped checks, upgrade note (task 1.9) |
| CR H3 | One `source` cannot hold several roles; pull secrets have no grant | `lang/compiler.go:358`, `security/secret_delivery.go` | `roles` list; per-service canonical connection/pull facts; explicit pull grant kept (task 3.2–3.3) |
| CR H4 | Pin order contradictory; no unpin; legacy values not rotatable | design | Order fixed; pin ends with the legacy key; `--legacy` update (task 2.3) |
| CR H5 / SA M5 | `keyring/name_*` writer, trigger, roles undefined | `security/rbac.go:240-275` | `KeyringEnvelopeCodec` reporter loop; canonical paths wait for the marker; roles (tasks 2.5, 2.6) |
| SA H1 | Re-apply without `owner` or with a new gate target takes over another tenant's object | `api/server.go:702` (Put only) | Stored owner/target checked (task 1.6) |
| SA H2 | Job named like a service gets its secrets (released code) | `controllers/job.go:177`, `security/secret_delivery.go:152` | Fixed in M210 |
| SA H3 / CR M3 | Facts without a tenant would open to team scopes | `security/fact_authorizer.go:33` | Decode table; everything else `ScopeCluster`; classification test (task 1.6) |
| CR M1 / SA M1 | Reserved segment list open-ended; route `a/service`; lexer-valid names | `lang/lexer.go:276`, `tenant/scoped_query.go:118` | Generated-list test; single-segment routes/connections; lint of failing names (tasks 1.1–1.3) |
| CR M2 / SA M9 | Gate overwrite, missing target, `/api/gate-key` | `security/gate_key_delivery.go`, `api/node_gate_key_delivery.go:63` | Stored target, unknown target rejected, gate-key stays cluster-only (tasks 1.6, 3.5) |
| CR M4 | v1 envelopes do not bind the key | `security/envelope_codec.go` | set/share need `keyring/legacy` none|retired (task 2.4) |
| CR M5 / SA M3 | Connection owner storage; ownerless connections | `lang/compiler.go:890-905` | Owner in the JSON value; ownerless need `cluster.admin` (tasks 1.5, 1.6) |
| CR M6 / SA M8 | Tmpfs reuse paths remain | `agent/secret_delivery.go:150`, `agent/init.go:325` | Wipe at stop, init uses current material, generation counter (task 3.8) |
| CR M7 / SA M2 | Guard node set wrong; blocks revocation; forged reports | `security/node_reports.go:19-37` | Nodes from `enrollment/node/`; set/share only; forged report accepted until M181 (task 2.4) |
| CR M8 / SA L1 | Docs contradict (cluster grant, share key, mount paths) | `design/cli-reference.md:96`, `lang/parser.go:1553` | Fixed in config-secrets.md, cli-reference.md, design.md |
| SA M6 | `/api/state` shows all shares and connections | `tenant/scoped_query.go:99-110` | Visibility rule (task 2.8) |
| SA M7 | Compromised-node mechanism mis-described | `agent/agent.go:1872` | Corrected; accepted until M181 (task 4.1) |
| SA M10 / M11 | Orphan tenants; cascade over 128 ops | `store/memory.go:35` | Declaration only for set/share; share cap 100 (tasks 2.2, 2.7) |
| SA L2–L8 | Audit request ID, CLI fallback, agent validation, tenant-state errors, tenant change at restart, stale-replica plant, grants never deleted | various | Fixed in tasks or documented (L6, L7, L8 accepted) |

### Phase 209 — Secret Name Migration (M209)

Split from M179 on 2026-11-12; scheduled right after M179 (number chosen so nothing is renumbered). Draft: OpenSpec change [`openspec/changes/secret-name-migration/`](openspec/changes/secret-name-migration/), not yet reviewed.

- [ ] Independent review of the draft (`code-reviewer` + `security-audit`); findings recorded here
- [ ] `--plan` (cluster `secret.metadata.read`) and `--apply` writing `intent/secret_names/` (`cluster.admin` through apply) with preconditions and mapping validation
- [ ] `secret-name-migration` controller: phases 1, 2a, 2b; shares for other-tenant consumers; resume; audit
- [ ] Tests: every scenario in the change's spec.md; deploy cluster migration under traffic with 0 restarts

### Phase 180 — HA Correctness (M180)

- [ ] Leader election on an etcd lease (`GrantLease` exists) instead of wall-clock timestamps
- [ ] Leader key revision compared in every controller transaction (fencing)
- [ ] Multi-prefix controller scans read at one pinned revision
- [ ] Persisted JWT signing keyring shared by API replicas; `token_use` claim separates user and workload tokens
- [ ] Integration test: deposed leader's transaction is rejected
- [ ] Node heartbeats on etcd leases (architecture review 2026-11-10): agents write `time.Now()` (`agent/telemetry.go` `WriteHeartbeat`) and `NodeFailureController` compares it with the control plane's clock (30 s timeout), so clock skew > 30 s marks a healthy node unreachable and triggers mass rescheduling. Use `PutWithLease` + TTL (key expiry = failure, etcd is the only clock)
- [ ] Fencing covers the `HARunner.stopControllers` timeout path: after 10 s it only logs "forcing stop" and clears its fields while the old runner's goroutines may still commit (architecture review 2026-11-10)
- [ ] Absence guards: a decision based on a missing key is CAS-protected only when the controller also writes that key; decide how transactions guard scanned prefixes (e.g. per-prefix revision compare) alongside the pinned-revision item; `maxInputKeyGuards` weakens guarding further (architecture review 2026-11-10)

### Runner hardening (unscheduled; architecture review 2026-11-10)

- [ ] Keyring watch has no backoff when `Watch` keeps returning a closed channel (M215 review SA3): `RunKeyringWatch` re-subscribes and refreshes in a hot loop; add a short delay before re-subscribing
- [ ] Flaky `TestControlPlaneRestart_NoDataLoss` (integration/ha_resilience_test.go:230): failed once in CI on 089a4ff ("running instances during outage: got 3, want 9") after killing controller groups and a fixed 200 ms sleep; passes 8/8 alone and 3/3 as a full package under -race locally. Replace the sleep-then-assert with a `waitFor` on the condition the test means
- [ ] A production controller missing from `controllerOutputPrefixes` passes every write unchecked (`enforceWriteDomain` returns the changes unchanged for an undeclared controller): add a test that fails when a controller in the production list has no declared write domain (found during M177 step 3)
- [ ] `DeltaController` / `deltaBuffer` / `ReconcileDelta` plumbing has no implementer (dead code since added): implement for the hot controllers (endpoint, instance) or delete
- [ ] A controller loop returning nil makes `Runner.Run` cancel every controller while the node still holds leadership (`controllers/runner.go` `Run`, `runSingleController`): treat a nil exit as an error and restart
- [ ] Controller restart backoff never resets (`runSingleController` `attemptIndex`): reset after a healthy period
- [ ] A cycle abandoned after CAS retries waits for the 30 s resync: re-arm the reconcile trigger
- [ ] Repo hygiene: ~180 MB of stale build binaries in the repo root (`cca`, `cca-linux*`) → gitignored `dist/`; split `agent/agent.go` (~2,000 lines)

### Test gaps (unscheduled; `design/TESTING.md` triage 2026-11-11)

- [x] ~~VIP pool has no bound and no reuse~~ — scheduled as M213 (2026-11-12, found bugs are prioritized as milestones)
- [ ] `cca apply` does not run the linter's reference checks: an undefined volume reference and duplicate service/volume names are reported only by `cca lint` (`lang/linter.go`). Decide whether apply rejects them; test on the apply path
- [ ] Two services mounting the same exclusive volume: only `TestExclusiveAttachEnforced` (provider level) exists; add a scheduler/agent-level test for the second service's outcome
- [ ] Delete a desired volume while attached: the provider refuses it (`TestDeleteVolumeWhileAttachedReturnsError`), but `TestStorageControllerCleansUpRemovedVolume` covers only an available volume at controller level
- [ ] Watch ordering with concurrent writers (`TestWatchEventOrdering` uses one writer)
- [ ] One integration test for the full lifecycle: deploy → scale → update image → rollback

### Phase 181 — Store Access Isolation (M181)

- [ ] Design doc: agent access to state (etcd roles vs API gateway)
- [ ] Agents limited to their node's keys and read-only desired state
- [ ] Secret provenance (M177 security review F4): with direct etcd write access an agent holding the old legacy key can plant v1 envelopes under `secrets/`/`credentials/` (and roll back v2 values or `keyring/`); the M177 migration re-encrypts whatever v1 it finds. Closing agent write access to `secrets/`, `credentials/`, `keyring/` and `observed/keyring/` removes this; also decide on version binding for v2 values
- [ ] Secret delivery checks binding (found during M174): agents can no longer write placements, grants or `observed/instance/{id}/service`; instance → service mapping from a control-plane-only fact
- [ ] Short-lived node certificates with rotation; removed nodes rejected by etcd and API
- [ ] Threat model T5 updated with the real residual risk
- [ ] Keyring facts (M178 review SA3/SA9): remove `intent/keyring/` from the api-server and controller store roles' write access; etcd trusts every cluster-CA certificate (a worker's node cert can read and write all of etcd, add members, take snapshots) — separate etcd CA or client-cert hostname restrictions
- [ ] Store-layer audit writes (found during M175): `AuthorizedStore` persists an audit entry to etcd (`StoreBackedAuditLog.Log` → `persistEntry` → Put) for every allowed Get/Scan/Watch, so reads become etcd writes (an uncached status call wrote ~615 before M175, still one per scan); persist denies and writes, keep allowed reads in memory or aggregated per request; measure etcd puts/s before and after

### Phase 182 — Security Defense in Depth (M182)

- [x] ~~Conditional policies (M178 review SA7)~~ — moved to M179 (task 3.4) on 2026-11-12: tenant scoping is meaningless while a conditional policy grants every scope
- [ ] Transient KMS errors exit the replica (M215 review SA2): `verifyKeyringRootCheck` (`security/keyring.go:218-221`) maps any `UnwrapKey` error to `ErrKeyringRootCheckFailed`; only an authentication/decrypt failure should, other errors take the non-fatal refresh path. Failing test with a KMS provider that times out
- [ ] CA key exposure (M178 review SA8): every HA replica loads `ca-key.pem` for enrollment; only the leader or a dedicated signer should hold it
- [ ] Volume host path must be under the storage provider root
- [ ] Secret API bound to the bridge gateway; caller IP must match the token's instance
- [ ] Process runtime refused when TLS is enabled unless `--allow-process-runtime`
- [ ] nftables ruleset input validated (IPs, names)
- [ ] `/api/state` returns raw desired config values: apply the describe/status sensitive-key redaction there too (found during M173)
- [ ] Paginated store scans
- [ ] CI check: every `controllers.New*` and API `Set*` has a production caller
- [ ] `/metrics`: requires authentication (or a dedicated scrape token) and goes through the rate limiter; builds counts from a cached or counts-only path instead of a full status build per scrape (found during M175)
- [ ] Team-scoped status: filter Networking, Volumes, CloudIdentities and Endpoints by visible services, as Services/Instances/Gates already are; filter secret names, not only grants (found during M175)
- [ ] Status discovery treats empty-valued keys such as `{svc}/resources/cpu` as services: discover only root markers (found during M175)
- [ ] Status cache (found during M175): `handleStatus` writes `?all=true` responses (stopped instances included) to the 2s cluster-scope cache that plain requests read, so `cca get` can briefly show stopped instances; cache only the plain view or key the cache by the `all` flag

### OpenSpec pilot (before M179)

Decision in `design/chats/chat-03nov-2.md`: adopt OpenSpec, pilot on GitOps, migrate no existing documentation. Changed 2026-11-11: the pilot moves forward to M179 (Tenant-Scoped Secrets), the next milestone, so that requirement scenarios serve as its acceptance criteria; GitOps (M206/M207) becomes the second change.

Roles: `CLAUDE.md` = engineering rules and invariants; `design/` = architecture and implementation details; `design/runtime-invariant-flows.md` = end-to-end chains across components (Flows 11, 24, 25 stay there; specs link to flows and flows link back, nothing copied); `openspec/specs/` = behaviour requirements of maintained capabilities; `openspec/changes/` = work in progress; `milestones.md` = roadmap.

- [x] Install pinned `@fission-ai/openspec@1.16.0` (needs Node >= 22.12.0; local v25.9.0); record the version. Done 2026-11-11: run as `npx @fission-ai/openspec@1.16.0 …` (a global `npm install -g` needs root on this machine, so there is no `openspec` on PATH); telemetry turned off (`config set telemetry.enabled false`)
- [x] `openspec init --tools claude` on a clean tree; review `git status` and `git diff -- .claude/` before committing; keep existing agents, skills and workflows unless a real conflict shows up. Done 2026-11-11 with only `design/TESTING.md` and `milestones.md` modified: it added `openspec/`, `.claude/commands/opsx/` (6 commands) and 6 `.claude/skills/openspec-*` skills, and changed no existing file
- [ ] Workflow ownership: OpenSpec drives proposal → specs → tasks → implementation → archive for OpenSpec-managed capabilities; `.claude/workflows/feature-development.js` does not run a competing pipeline for the same change; project-manager and technical-writer agents stay; `release-pipeline.js` / `scripts/release.sh` keep release execution
- [x] Minimal `openspec/config.yaml`: `schema: spec-driven`, `context` points at CLAUDE.md and `design/` (no restated invariants), `rules` per artifact, `operations.apply.guidance` (inspect implementation and call graphs first, report real test results). The `specs` and `tasks` rules add: every scenario names its test, and those tests are written and seen failing first
- [x] (drafted 2026-11-11 in `openspec/changes/tenant-scoped-secrets/`, `openspec validate` passes; not yet reviewed, two open questions in its `design.md`) First change `tenant-scoped-secrets` drafted from the M179 checklist and `design/config-secrets.md`; every requirement scenario names the test that proves it, and those tests are written and seen failing before implementation
- [ ] Second change `git-backed-configuration-sources` drafted from M206/M207 and existing architecture, resolving: Git outage, invalid revision, source ownership, partial application, concurrent revisions (pin to commit), sync vs health, `cca scale` overrides, manual `cca apply` (`cli` owner, conflict/adoption), etcd transaction limits
- [ ] Review ownership, override, prune and transaction semantics before implementation starts
- [ ] Release order per change: tasks done → tests pass → `openspec archive` → update the milestone's links (M179, later M206/M207) to `openspec/specs/...` and the dated `openspec/changes/archive/<date>-<change>/` → `scripts/release.sh`

### GitOps — Phases 206–207

Proposal: `design/chats/chat-03nov.md`. Git supplies desired-state inputs; policy, scheduling, execution and observation stay with CCattler. Ownership and prune come first because they also fix `cca apply` today. Numbers are out of priority order (added 2026-11-03 at the end of the sequence, after M205); renumber in the next priority pass.

### Phase 206 — Apply Ownership & Prune (M206)

- [ ] Design as OpenSpec change `git-backed-configuration-sources` (see OpenSpec pilot; `design/gitops.md` only if the pilot is dropped): owner model, ownership keys, prune rules, conflict rules; cross-component flow in `design/runtime-invariant-flows.md`
- [ ] Decide how `intent/user/` overrides (`cca scale`) relate to an owner: kept as a separate layer or reverted by the owner
- [ ] Ownership facts per resource (e.g. `source/{owner}/owns/{kind}/{name}`), not per fact key; new root in `controllerOutputPrefixes`, store RBAC roles and `key-ownership-matrix.md`
- [ ] `cca apply` records owner `cli`; applying a resource owned by another owner is refused with the owner's name
- [ ] Prune: resources owned by this owner and missing from the applied set are deleted; generalize `collectRemovedGateFacts` to every resource kind
- [ ] Prune never runs on a partial or failed compile; an empty candidate is refused unless explicitly forced
- [ ] Per-resource atomic commit within the etcd operation limit: either enforce a maximum facts-per-resource or add staged generation and promotion for larger resources; source-wide application resumable and recorded by revision
- [ ] `cca diff` lists additions, changes, removals and ownership conflicts per resource
- [ ] Integration tests: remove a service from a directory → deleted; another owner's service untouched; repeated apply is a no-op

### Phase 207 — Git Configuration Sources (M207)

- [ ] Design in the same OpenSpec change: source facts, lifecycle, status model, Git client choice (`git` binary vs Go library: air-gap and supply-chain impact)
- [ ] `cca source add/list/status/diff/sync/pause/resume/remove`; `design/cli-reference.md`
- [ ] Source reconciler under controller leadership: poll, resolve branch to SHA, fetch only the path, render values, compile, validate, apply through M206 as the source's owner
- [ ] Source identity: one service principal per source, grants scope what it may manage (e.g. one tenant); compiled facts go through the policy gate
- [ ] Safe fetch: allowed URL schemes and hosts, path traversal and symlink protection, size limits
- [ ] Credentials stored as CCattler secrets, never in URLs (depends: M174)
- [ ] Failure handling: fetch or compile failure keeps last good state, reports Failed, never prunes
- [ ] Status API: fetched revision, applied revision, sync status, last error; health reported separately from workload observations
- [ ] Integration tests with a local bare repository; e2e on the deploy cluster

### Phase 183 — eBPF Data Plane Correctness (M183)

Harden the eBPF data plane (M128–M134) to the point where it can fully replace nftables for VIP, SNAT, and basic network policy — running a single data plane instead of two in parallel. Discovered during VXLAN CI investigation: tc SNAT was stateless and broke TCP reply packets; agent restart race deleted BPF pin directory; no TCP-level integration tests existed; packet flow matrix was undocumented.

- [x] Fresh nodes (deploy run 42258226406, kernel 7.0.0-38): `bpf_skb_ct_lookup` "not supported" because `nf_conntrack`/`nf_nat` were not loaded; the agent now loads both before creating the BPF collection

**tc SNAT conntrack validation:**
- [x] Make `cca_tc_snat` conntrack-aware via `bpf_skb_ct_lookup` — skip SNAT for reply-direction packets
- [x] Add `IP_CT_DIR_ORIGINAL`/`IP_CT_DIR_REPLY` defines to `cca_ct_helpers.h`
- [x] Document packet flow matrix in `NETWORK_GRAPH.md` (all source→dest combinations)
- [x] Update `EBPF_GRAPH.md` — SNAT call tree with conntrack, kfuncs table
- [ ] Validate tc SNAT conntrack fix passes 10+ CI runs without init probe regression
- [ ] Verify container-initiated egress (container → external) still works with SNAT

**Agent restart BPF pin race:**
- [x] Intermittent multi-port VIP failure on eBPF (run 41851786175, e0c7bfe): right after the node-failure test, `curl` from worker-2 to whoami-multiport port 80 failed for 30s (exit 7/28) while worker-1 got 200; both backends answered on their pod IPs. Reproduced on bare metal (run 42043261538, 479a717): `cca_backends` on node-2 held 4 slots for the port-80 VIP, `:80` and `:8081` of both instances; whoami only listens on 80, so ~half the connections failed (live curl `000 200 000`). Cause: endpoints are one per instance per exposed port and `resolveServiceBackends` took all of them regardless of the VIP port; nftables had the same backend list. Fix: only endpoints whose container port equals the VIP port become backends (`TestDataPlaneMultiPortVIPUsesOnlyVIPPortEndpoints`). Port 8081 still has no VIP (M191)
- [ ] Investigate slow recovery after an agent restart on eBPF under load: run 41673229739 (eBPF, Airflow + whoami + gate-tls) took 64s until worker-1 reached the whoami VIP again, vs 9-11s on nftables without Airflow. eBPF programs attached and the whoami instance started within 2s of the restart; the delay is between endpoint publication, readiness and backend map programming. Collect eBPF map dumps and agent logs beyond the 200-line journal cap. Next run 41682266710 (same load, eBPF, real capacity, right-sized Airflow) recovered in 23s, so it varies with load or timing
- [ ] `cca top nodes` shows 0% CPU/memory used on nodes running 5-6 workloads (run 41682266710): the usage column is not populated
- [ ] Fix `removePinnedMaps()` to only remove pins created by THIS instance (PID-namespaced pin dir or flock)
- [ ] Add integration test: restart agent under load, verify BPF maps survive and service map entries persist
- [ ] Add startup log line: "reusing existing BPF pins" vs "creating fresh BPF pins"

**eBPF TCP integration tests:**
- [ ] Add e2e test: host-ns → remote container TCP via VIP (the init probe path)
- [ ] Add e2e test: container → container TCP cross-node via VIP
- [ ] Add e2e test: container → external host TCP (egress SNAT path)
- [ ] Add e2e test: verify `nc` TCP connectivity, not just ICMP ping, in cluster health check
- [ ] Each test row from NETWORK_GRAPH.md packet flow matrix has a corresponding e2e assertion

Policy and nftables elimination items moved to Phase 197.

### Phase 184 — HA Etcd Cluster Validation (M184)

First end-to-end validation of HA control plane on vmherd: 3-node etcd raft cluster, leader-elected cca-server, control plane failover, and etcd member failure recovery.

- [ ] vmherd Herdfile: 3 ctrl (4GB) + 1 worker (2GB) on 192.168.125.0/24
- [ ] Ansible inventory mapping vmherd VMs to HA groups (etcd, controlplane, workers)
- [ ] Test workload: whoami (3 instances) + whoami-multiport (2 instances)
- [x] `cca health`: control-plane health from the API server's `/healthz` (store reachable, controller leader lease), non-zero exit when unhealthy or unreachable; replaces `etcdctl endpoint health` in scripts and agents (no etcdctl)
- [ ] HA validation script: 9 checks (reachability, etcd health, API, leader election, nodes, workload, VIP, failover, etcd failure)
- [ ] Deploy cluster via `ha-deploy.yml` with existing HA Ansible roles
- [ ] Verify etcd 3-member quorum, leader election, workload placement
- [ ] Control plane failover: stop leader cca-server, verify new leader within 30s
- [ ] etcd member failure: stop one member, verify quorum holds (2/3), API works
- [ ] Recovery: restart failed components, verify they rejoin

### Phase 197 — eBPF Policy & nftables Elimination (M197)

Second half of the original Phase 151 checklist. Depends: M183.

**eBPF network policy (basic):**
- [ ] Review identity-based design draft [design/network-policy-identity.md](design/network-policy-identity.md) (identity maps instead of per-node IP rules; default-deny derived from team/export/gate declarations); the items below assume the CIDR model and change if the draft is accepted
- [ ] tc ingress program on cca0 for ingress allow/deny by source CIDR (replaces nftables filter FORWARD)
- [ ] BPF map populated from `derived/network/rule/` facts by agent reconciler
- [ ] Default-deny when at least one policy applies to the service
- [ ] Integration test: policy blocks cross-service traffic, allows permitted

**nftables elimination gate:**
- [ ] M183 complete and all items above done
- [ ] Remove nftables VIP chains when `--dataplane ebpf` (currently kept as fallback)
- [ ] Remove nftables POSTROUTING masquerade per-container rules when eBPF SNAT active
- [ ] `--dataplane ebpf` tested on all three targets: bare-metal, Vagrant, Lima
- [ ] Document kernel version requirements (6.6+ for TCX, 5.18+ for conntrack kfuncs)

### Phase 205 — Airflow Triggerer & Observability (M205)

Future placeholder: additional Airflow services for full Helm chart parity.

- [ ] Triggerer service (deferrable operators, exec-based probes)
- [ ] StatsD metrics exporter sidecar
- [ ] Log groomer via existing logging DSL keyword

## Milestones

Ordered by priority. Renumbered 2026-11-03: the security review milestones were inserted as M171–M177, and the former M171–M193 became M178–M200. Renumbered 2026-11-06: M174 was split into M174–M178, and the former M175–M203 became M179–M207. Renumbered again 2026-11-06: the status bulk-read milestone was inserted as M175 (blocker), and the former M175–M207 became M176–M208. Earlier plans in `design/plans/` use older numbers.

| Milestone | Phases | Demo |
|-----------|--------|------|
| **Tier 0 — Finish & verify what shipped** | | |
| M170 — Gate HTTPS Listener & End-to-End Validation | 170 | Gate TLS moves from the service VIP to a node `:443` listener on the agent gate proxy: SNI selects the gate cert, Host routes to the service, service ports stay plain HTTP for in-cluster callers, multiple gates per service, works on every data plane. Then validate on the deploy cluster (15 e2e cases). Fixes M169 gaps: TLS-only gates not dispatched on nftables, eBPF ignored TLS. Depends: M169. |
| **Tier 0S — Security review fixes (2026-11-03)** | | |
| M171 — Enrollment Identity Hardening | 171 | The CA sets the node certificate identity itself instead of copying the CSR subject, so a join token can no longer produce a `ccattler-server` (cluster-admin) certificate. Node certs are client-auth only with no CSR-chosen SANs. Node IDs are validated and reserved names refused. A join token is consumed atomically. |
| M172 — Agent Debug API Lockdown | 172 | The agent debug API (`/debug/exec`, `/debug/images/pull`) requires an mTLS client certificate from the cluster CA and an admin principal. Ansible binds it to loopback until then. |
| M173 — API Authorization Correctness | 173 | Store grants and ABAC policies reach the API authorizer in every API process (AuthController was never wired). `/api/state` and `/api/watch` use a prefix allowlist instead of the bypassable denylist. Authenticators skip tokens from other issuers instead of aborting the chain. The watch limit is enforced atomically. |
| M174 — Secret Delivery to Agents | 174 | Agents stop decrypting: `POST /api/node/secrets` delivers only the secrets granted to instances placed on the calling node, through a narrow `secret-delivery` store principal, audited. Material lives in tmpfs and is bind-mounted read-only. New `waiting-for-secrets` state before `pulling` (not a failure). Enrollment no longer sends the master key. Legacy secret API only as a deprecated opt-in. |
| M175 — Status Snapshot & Bulk Reads | 175 | Blocker for the next milestones (every `cca top`, `cca get`, `cca status` on the deploy cluster stalls). Uncached `GET /api/status` (behind `cca top`, `cca get`, `cca status`) takes 17–21s on the deploy cluster: ~330 sequential store reads per call. Build status from a fixed set of prefix scans indexed into maps, so round-trips do not grow with instances, nodes or services. Failed required scans return an error, not an empty-looking cluster; authorization and visibility unchanged; still eventually consistent (one-revision reads are M180). Benchmark with injected 110ms latency first. Target: well below 1s uncached. Controller read audit is a separate follow-up. |
| M176 — Gate Key Sealing & Delivery | 176 | Gate TLS private keys sealed under `secrets/route/…` instead of plain facts; `tls { key secret "…" }`; agents fetch them through `POST /api/node/gate-keys` and cache them in tmpfs so TLS survives an agent restart while the API is down. Upgrade note: secret names starting with `route/` are now reserved for gate keys — rename any user secret under `route/` before upgrading. Depends: M174. |
| M177 — Root Key, Keyring & Key Migration | 177 | Fresh control-plane root key (the old master key was on every worker), keyring in etcd with `root_check`, envelope v2 bound to its store key. Gated `cca secret migrate-keys` re-encrypts every v1 envelope with persisted progress; gated `retire-legacy-key` ends use of the old key. Depends: M174, M176. |
| M178 — Keyring Rotation | 178 | `cca secret rotate` as a fact; leader-elected keyring controller with create-only KEKs, CAS re-wrap in bounded batches, crash-safe resume, grace-period retirement. Depends: M177. |
| M210 — Job Secret Isolation Fix | 210 | Secret delivery refuses job instances and undeclared services: a job named like a service no longer receives that service's secrets (hole since M174). Also ships M215: the keyring watch refreshes once after subscribing, so a root key rotation during a replica's startup is not missed for up to 60 s. Upgrade note: none. |
| M215 — Keyring Watch Startup Gap | 215 | Bug fix: the keyring watch refreshes once after subscribing, so a root key rotation during a replica's startup is not missed for up to 60 s; fixes the flaky CI test that exposed it. Shipped with M210. |
| M214 — Adversarial User Testing | 214 | Exploratory testing on the deploy cluster as a real (non-admin and admin) user trying to misuse or break the system; every confirmed finding becomes a failing automated test and a prioritized milestone item; confirmed attacks become a repeatable `test/e2e/adversarial/` suite in the deploy run. Scheduled right after M210 (user decision 2026-11-12: found bugs first). |
| M216 — API Authorization Gaps | 216 | Bug fix (M214 run 1): deleting a user keeps its grants and tokens valid; `/api/gc` has no capability check (a read-only user deletes facts); a route sweep test so no writing handler lacks a capability check. |
| M217 — Scale API Safety | 217 | Bug fix (M214 run 1): scale has no upper bound (100000 accepted, 436 instances created), accepts undeclared services (orphan instances), and reports success on store errors. |
| M218 — CLI Remote Correctness | 218 | Bug fix (M214 run 1): remote `cca apply` silently simulates instead of applying; `whoami` ignores contexts; `get secrets` vs `secret list`; local-only commands fail with raw etcd errors. |
| M211 — Workload Identity & Safe Instance Creation | 211 | Bug fix: jobs and stateful services share instance IDs `{name}-{n}`, controller puts overwrite existing instances, and an instance's workload is a bare name string. Typed owner reference, create-only instance creation, cross-kind name uniqueness at apply. |
| M212 — Jobs Run on Real Agents | 212 | Bug fix: agents read the image only from `desired/service/{name}/image`, so job instances run only with a same-named service's image. Agents resolve job instances from `desired/job/…`. |
| M213 — VIP Pool Allocation | 213 | Bug fix: VIP allocation takes highest octet + 1 with no bound or reuse; after 254 allocations it emits `.255`, then invalid addresses. Lowest free address in `--vip-cidr`, explicit exhaustion. |
| M179 — Tenant-Scoped Secrets | 179 | Secrets belong to a tenant (`secrets/tenant/{t}/…`, `secrets/cluster/…`, reserved `default`); `cca secret set --tenant/--cluster`, `share`/`unshare`; apply authorized at the owning tenant's scope; connections and gates get a tenant; delivery re-checks shares and every container start fetches. Existing secrets keep working for their existing grants. Upgrade: every replica and agent before the first scoped write. Depends: M177. |
| M209 — Secret Name Migration | 209 | `cca secret migrate-names --plan` / `--apply`: a controller re-encrypts legacy `secrets/{name}` keys under tenant or cluster names, rewrites grants and creates shares, without restarting instances. Split from M179 on 2026-11-12; scheduled right after it. Depends: M179. |
| M180 — HA Correctness | 180 | Leader election on an etcd lease instead of wall-clock timestamps, and the leader key revision is checked in every controller transaction. Multi-prefix scans read at one revision. Persisted JWT signing keyring shared by replicas, with a token-use claim. Before M184. |
| M181 — Store Access Isolation | 181 | Agents can no longer read or write every etcd key. Near term: etcd roles mapped from certificate CN, or a separate etcd client CA for the control plane only. Target: agents go through an API gateway. Short-lived node certificates with rotation and revocation. Design doc first. |
| M182 — Security Defense in Depth | 182 | Volume host paths checked against the storage root. Secret API bound to the container bridge with a source-IP check. Process runtime refused outside dev mode. nftables ruleset input validated. Scan pagination. A CI check that every controller constructor and API setter has a production caller. |
| M183 — eBPF Data Plane Correctness | 183 | Correctness half of the original M151 (see checklist): tc SNAT conntrack fix validated over 10+ CI runs, egress SNAT verified, agent-restart BPF pin race fixed, TCP e2e tests for every packet-flow-matrix row. eBPF policy and nftables removal moved to M197. Depends: M128–M134. |
| M184 — HA Control Plane Validation | 184 | First end-to-end HA validation on vmherd (see checklist): 3-node etcd quorum, leader-elected cca-server (`controllers/leader.go`, `ha_runner.go` already exist), control plane failover <30s, etcd member failure, recovery. Absorbs the former Control Plane HA milestone: also adds `cca server --join <existing>` if validation shows it is needed. Depends: M180. |
| M185 — Etcd Backup & Restore | 185 | `cca backup create/restore`. Scheduled automatic backups. Retention policy. Point-in-time restore. |
| **Tier 1 — High value for effort** | | |
| M186 — Debug & Explain CLI | 186 | Existing: `cca describe` (service/node/instance/gate), `cca diff`, `cca events`. Remaining: `cca debug <service>` (one-shot health report: instances, probes, recent events, placement failures) and `cca explain <service>` (scheduler placement reasoning per node). |
| M187 — Network Policy Egress Rules | 187 | `network_policy "name" { egress { to_cidr "10.0.0.0/8" port 443 } }`. nftables output chain. Default-deny egress when policy applies. DNS-based egress (`to_fqdn`). Depends: M168. |
| M188 — Prometheus Metrics Gaps | 188 | Existing: `/metrics` in Prometheus format (`metrics/metrics.go`) with API, reconciliation (duration, changes, conflicts), activation, and node/service/instance gauges. Remaining: per-controller labels on reconciliation metrics, autoscaler decision metrics, instance counts by state, scrape config docs. |
| M189 — Quota CLI | 189 | Existing: tenant `quota {}` DSL and `QuotaAdmission` (`tenant/quota.go`) blocking instance/volume admission, usage facts. Remaining: `cca quota list` / `cca quota describe <tenant>` showing limits vs usage. |
| M190 — Persistent Volume Gaps | 190 | Existing: `volume` DSL (size, persistent, type, replicas), service volume mounts, per-ordinal stateful volumes (`controllers/stateful_volume.go`), LocalPath/NFS providers (M164). Remaining: scheduler volume locality (place instances on nodes holding their local volume), size enforcement/quota on local provisioner. Found 2026-11-06 (deploy runs 42254717859, 42263460802): the StorageController wrote `observed/volume/{name}/type` only when it created a volume, and `cca apply` writes facts one by one, so `pgdata` was created before its `type local` arrived and the agent used NFS; two Postgres instances shared one NFS data directory and corrupted it. Fixed: a missing observed type is now recorded from the desired type, and the agent falls back to the desired type until then. Still open: A desired/observed type mismatch must put the volume in an error state with the reason (data is not migrated), visible in `cca get volumes`. |
| M191 — Multi-Port Service VIPs | 191 | Found during M170: `NetworkController` gives each service one VIP port (`servicePorts[svc][0]`), so `expose 8081` on a second port never reaches the data plane, although M124 claimed per-port VIPs. One VIP per service with every exposed port: `network/vip/service/{name}/port/{port}` (legacy single `port` key migrated), `ServiceVIPConfig.Ports`, nftables/iptables/eBPF dispatch per port, `/api/status` networking entry per port, cloud LB per-port config. E2E: whoami-multiport answers on 8081 through the VIP from every node (hard failure, not a warning). Design in `design/networking.md` first. Depends: M183 (eBPF data plane). |
| **Tier 2 — Substantial features** | | |
| M192 — Service Mesh via Groups | 192 | `mesh { mtls "strict" }` — compiler auto-adds proxy to service group. Proxy uses existing SPIFFE identity. Traffic metrics as derived facts. Uses group network sharing, not sidecar injection. Depends: M169. |
| M193 — DSL Admission Policy Rules | 193 | `policy "no-latest" { on apply match { service where image =~ ":latest$" } deny "not allowed" }`. Mutating policies. Compiled into PolicyGate — no webhooks. Escape hatch: `validate_url` for external validation. |
| M194 — Volume Snapshots | 194 | `cca volume snapshot create <service>`. Filesystem copy for local provisioner. Restore to new volume. Retention policy (count/age). Depends: M190. |
| M195 — Distributed Tracing | 195 | OpenTelemetry trace propagation through controllers. OTLP export. Trace correlation: API request → controller → agent. `cca trace <request-id>`. Depends: M188. |
| M196 — DSL Templates & Imports | 196 | `template "web-app" (image, port) { ... }`. `import "lib/common.cca"`. Pure substitution. Standard library at `stdlib/`. Reconsider first: Go templates + `.ccv` values already cover parameterization. |
| M197 — eBPF Policy & nftables Elimination | 197 | Second half of the original M151 checklist: tc ingress policy program on cca0 from `derived/network/rule/` facts, default-deny, then remove nftables VIP/masquerade chains under `--dataplane ebpf`; test on bare-metal, Vagrant, Lima; document kernel requirements. Depends: M183. |
| M206 — Apply Ownership & Prune | 206 | Foundation for GitOps, useful without Git: every resource in desired state has one owner (`cli` for `cca apply`, later a Git source), recorded per resource. `cca apply <dir>` of the same owner deletes resources removed from the files (today only gates are pruned, M170). Applying a resource owned by someone else is refused. Each resource's facts commit atomically; application is resumable and idempotent. `cca diff` shows removals and ownership conflicts in CCattler terms. Proposal: `design/chats/chat-03nov.md` (stage 2). |
| M207 — Git Configuration Sources | 207 | `cca source add/list/status/diff/sync/pause/resume/remove`. A source = repository, revision, path, values files, sync mode, prune flag. Polls (webhook later), pins the branch to a commit SHA, fetches only the path, renders `.ccv` values, compiles and validates before writing; a failed fetch or compile keeps the last good desired state and never prunes. Applies through M206 ownership as the source's own service identity, scoped by grants (M173). Source status (Synced/OutOfSync/Applying/Failed, fetched vs applied revision) kept separate from workload health. Runs under controller leadership. Credentials as CCattler secrets (M174). Depends: M206, M173, M174. |
| M208 — Cloud-Backed Cluster Autoscaling | 208 | Found in the 2026-11-03 external review: `ClusterAutoscaleController` exists but is wired only into `cca apply`/`cca run`, always with `infra.NewSimulatorInfraProvider`, and not into `cca server`. The real AWS/GCP/Azure adapters in `cloud/` implement `CloudProvider` (`CreateInstance`, `ListInstances`, ...), not the autoscaler's `infra.InfrastructureProvider` (`RequestNodeWithRequirements`, `RemoveNode`, `NodeCount`). Scope: adapter from `CloudProvider` to `InfrastructureProvider` (idempotent by request ID, instance template with join token and labels), wiring under controller leadership in `cca server` when `--cloud-provider` is set, scale-down only through drain, min/max node limits and cost guard, provider error and quota handling, simulator kept for tests. E2E against one real provider account. Depends: M171 (join token per node), M180 (leader fencing). |
| **Tier 3 — Later / optional** | | |
| M198 — Multi-Cluster Service Discovery | 198 | `cca cluster register <remote>` links clusters. Services visible as `<svc>.<cluster>.svc.ccattler.global`. Cross-cluster health checking. Depends: M168, M154. |
| M199 — Storage Plugin Interface | 199 | CSI-compatible plugin model. Driver registration and capability discovery. External provisioners (NFS, cloud block). Attach/detach lifecycle. Depends: M190. |
| M200 — Custom Fact Types with Schema Validation | 200 | Schema at `meta/fact-type/{name}`. PolicyGate validates writes to `custom/{type}/`. `cca get <type>` auto-discovers. `cca describe fact-type`. No API group versioning — facts with optional schema. |
| M201 — Controller SDK gRPC Transport | 201 | gRPC streaming for out-of-process controllers. `cca controller register --name X --address host:port`. Health check + auto-deregister. Builds on existing CustomController interface (M81). |
| M202 — Controller Scaffolding CLI | 202 | `cca controller init <name>` scaffolds Go/Python project with gRPC client, reconciler stub, test harness. Working example included. Not an operator lifecycle manager. Depends: M201. |
| M203 — Cluster Upgrade Controller | 203 | `cca upgrade plan/apply`. Rolling control-plane + agent upgrade. Drain → upgrade → re-enable node. Rollback on failure. Depends: M184, M185. |
| M204 — Conformance Test Suite | 204 | 100+ e2e test cases across all phases. `cca conformance run`. Test matrix: single/multi-node, HA, multi-tenant. Depends: all previous. Prefer growing it with each milestone over one big phase at the end. |
| M205 — Airflow Triggerer & Observability | 205 | Placeholder: triggerer service, StatsD exporter, log groomer |
| **Future (unscheduled)** | | |
| *(future)* OpenSpec Adoption (pilot) | — | Adopt OpenSpec 1.16.0 (pinned) for new capabilities, GitOps first; existing docs not migrated. Checklist under "OpenSpec pilot" above. Decision and config: `design/chats/chat-03nov-1.md`, `design/chats/chat-03nov-2.md`. Before M206. |
| *(future)* Agent Stop & Restart Traffic Continuity | — | Found in the M170 deploy runs. (1) A deliberate agent stop (`systemctl stop`, upgrade) stops its containers but leaves their endpoints until the 30s heartbeat staleness threshold, so VIP requests reach dead backends for ~30s; the agent should withdraw its endpoints (observed instance state) before exiting, as `cca node drain` would. A crash keeps the Flow 13 fast path. (2) On restart the agent starts instances before re-creating `cca-vxlan0`, leaving cross-node traffic from that node broken for ~7s; reconcile the overlay and data plane before the first instance start. Flow 13 update and integration tests. |
| *(future)* Production GitOps | — | Stage 3 of the GitOps proposal (`design/chats/chat-03nov.md`): signed commit verification, webhook-triggered fetches (a webhook only triggers a fetch, never carries config), approval policies, pull request previews (`cca source diff` against a branch), rollback by repinning a revision, declarative source definitions. Depends: M207. |
| *(future)* API TLS Rate Limiting | — | Rate-limit failed TLS handshakes per source IP on the `cca server` API listener. After N failed handshakes from the same IP within a sliding window (e.g. 10 failures in 60s), temporarily block that IP for a cooldown period. Prevents a misconfigured client (wrong certs, broken TLS) from flooding the server with handshake errors, which starves goroutines and causes etcd operation timeouts — leading to leadership loss and controller stall. Implementation: wrap `tls.Listener` with a rate-limiting accept loop that tracks per-IP failure counts. Log blocked IPs at WARN level. No configuration needed beyond sensible defaults. Discovered during M130 validation when a caddy-sync daemon with wrong certificates caused leadership loss on the deploy cluster. |
| *(future)* Gate Traffic Splitting | — | Weighted backend routing for `gate` blocks. DSL: `gate canary { host app.example.com; backend api-v1 weight 90; backend api-v2 weight 10 }`. Compiler emits `desired/route/{name}/backend/{service}/weight` facts. Route controller distributes backends proportionally in `derived/route/{name}/backends` (e.g. 9 v1 entries + 1 v2 entry for round-robin). Data plane round-robin naturally approximates weights. For exact splitting, BPF `numgen` mod N with weighted slot assignment. Inspired by Kubernetes Gateway API HTTPRoute `backendRefs[].weight`. Enables canary deployments and A/B testing without a separate proxy. |
| *(future)* WireGuard Node Encryption | — | Transparent encryption of all inter-node traffic using WireGuard tunnels. Agent creates `cca-wg0` WireGuard interface on startup, generates key pair, publishes public key as `observed/node/{id}/wireguard/pubkey` fact. WireGuardReconciler watches node facts, programs peer entries for every other node (`wg set cca-wg0 peer <pubkey> endpoint <ip>:51820 allowed-ips <subnet>`). VXLAN traffic routed over WireGuard by adjusting FDB entries to use `cca-wg0` as the outgoing device. DSL opt-in: `cluster { encryption wireguard }` in the cluster config. No third-party dependencies — uses standard Linux WireGuard kernel module (`ip link add cca-wg0 type wireguard`). Key rotation via periodic regeneration and fact update — peers converge within one reconciliation cycle. |
| *(future)* Gate L7 Metrics | — | Request-level observability for `gate` blocks. The agent HTTP proxy (`network/userspace_proxy.go`) already sees every L7 request via Host header routing. Add per-gate metrics: request count, latency histogram (p50/p95/p99), status code distribution, error rate. Emit as `derived/metric/gate/{name}/{metric}` facts on each proxy reconciliation cycle. `cca top gates` CLI command reads and displays metrics in real-time. `cca metric export` outputs Prometheus exposition format for external scraping. No kernel instrumentation needed — measured at the userspace proxy layer. |
| *(future)* BPF L7 Policy | — | eBPF-based L7 request filtering for HTTP traffic. tc BPF program inspects HTTP request headers (method, path) on `cca-net` bridge and enforces allow/deny rules compiled from DSL `firewall` blocks with L7 matchers: `firewall web-api { allow from backend method GET path "/api/*" }`. Compiler emits `derived/network/rule/{name}/l7/{index}` facts with method/path patterns. BPF program parses TCP payload for HTTP/1.1 request line, matches against BPF hash map of compiled rules, drops non-matching packets. Kernel 6.1+ (BPF packet parsing helpers). Complements existing L3/L4 identity-based policy with application-layer granularity. |
