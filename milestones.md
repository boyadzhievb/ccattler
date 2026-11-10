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

- [ ] Fresh root key `/etc/ccattler/secret-root.key` (`0400`, root), never derived from `secret-master.key`; Ansible generates once and provisions every control-plane replica
- [ ] `--kms-provider` for the root key wires `security.LookupKMSKeyProviderFactory` into the server (no production caller since M174 removed agent decryption)
- [ ] Startup errors: missing, unreadable, wrong size, group/world-readable, identical to the legacy key, `keyring/root_check` fails to open
- [ ] Keyring in etcd (`keyring/kek/{v}`, `keyring/active`, `keyring/root_check`); replicas watch it and report `observed/keyring/replica/{id}`
- [ ] Envelope v2 (magic, KEK version, wrapped DEK, AAD = store key); legacy key opens v1 only and never wraps anything new; writes stay v1 until migration starts
- [ ] Migration gate + `cca secret migrate-keys --confirm-backup` → `intent/keyring/legacy_migration`; leader-elected keyring controller re-encrypts v1 → v2 in bounded CAS batches, resumes after a crash, persists `observed/keyring/migration` (`legacy_v1_envelope_count`)
- [ ] Retirement gate + `cca secret retire-legacy-key` (count 0 by full scan, migration completed, no plaintext gate key facts, every node `legacy_key_file = absent`); replicas stop loading the legacy key and refuse v1; Ansible removes `secret-master.key` from workers, then control plane, and asserts absence
- [ ] Etcd backup/restore runbook in `design/TESTING.md` (referenced by `--confirm-backup`)
- [ ] Tests: legacy key cannot unwrap any KEK, open `root_check` or any v2 envelope; every legacy value unchanged after migration; kill mid-batch and resume; gates refuse with the unmet conditions; v1 refused after retirement; deploy cluster migration under traffic

### Phase 178 — Keyring Rotation (M178)

- [ ] `cca secret rotate` → `intent/keyring/rotation`; refused until migration completed
- [ ] Keyring controller: create-only KEK write, `keyring/active` switch, CAS re-wrap of DEKs in bounded batches (`keyringRewrapBatchSize`), idempotent resume, `retiring` → deleted after `--keyring-retire-grace` (7d); referenced versions never deleted
- [ ] Root key rotation as a separate operator command (re-wraps `keyring/kek/*` and `root_check` only)
- [ ] Tests: kill at each step and restart; every secret readable; CAS conflict retried; replicas read the same versions; rotation during traffic on the deploy cluster

### Phase 179 — Tenant-Scoped Secrets (M179)

- [ ] Canonical names `secrets/tenant/{t}/…`, `secrets/cluster/…`; tenant resolution owner → hierarchical name → reserved `default`
- [ ] CLI: `cca secret set/get/list/delete --tenant/--cluster` (mutually exclusive, default tenant when omitted), value from stdin or `--from-file`; `share`/`unshare` → `desired/secret_share/{owner}/{name}/{consumer}`; write scope `team/{tenant}`, `team/default`, `cluster`
- [ ] Compiler resolution (unqualified, `cluster/…`, `tenant/{t}/…`), apply errors for unresolvable/unshared references, grant facts with canonical names (incl. connection auto-grants); delivery re-checks shares
- [ ] `cca secret migrate-names --plan` / `--apply mapping.ccv`: refuses unmapped secrets; per secret re-encrypts under the canonical AAD, rewrites grants and references, removes the legacy key in one transaction
- [ ] Drop the `route/` secret-name reservation (M176) once user and system secrets have separate key prefixes: user secrets under `secrets/tenant/…` / `secrets/cluster/…`, gate keys under `secrets/route/…`. Only after `migrate-names` has moved every legacy `secrets/{name}` key (a legacy `secrets/route/…` user key would still collide); then `route/…` is an ordinary user name (`IsSystemOwnedSecretName`, `/api/secret` filter, compiler checks, docs)
- [ ] Optional admission policy rejecting services that fall into `default` (off by default)
- [ ] Tests: acceptance A (unowned service gets `default`, refused `payments`); share → delivered, `unshare` → refused at next start; cluster secret needs cluster grant; `--tenant` with `--cluster` rejected

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

- [ ] `DeltaController` / `deltaBuffer` / `ReconcileDelta` plumbing has no implementer (dead code since added): implement for the hot controllers (endpoint, instance) or delete
- [ ] A controller loop returning nil makes `Runner.Run` cancel every controller while the node still holds leadership (`controllers/runner.go` `Run`, `runSingleController`): treat a nil exit as an error and restart
- [ ] Controller restart backoff never resets (`runSingleController` `attemptIndex`): reset after a healthy period
- [ ] A cycle abandoned after CAS retries waits for the 30 s resync: re-arm the reconcile trigger
- [ ] Repo hygiene: ~180 MB of stale build binaries in the repo root (`cca`, `cca-linux*`) → gitignored `dist/`; split `agent/agent.go` (~2,000 lines)

### Phase 181 — Store Access Isolation (M181)

- [ ] Design doc: agent access to state (etcd roles vs API gateway)
- [ ] Agents limited to their node's keys and read-only desired state
- [ ] Secret delivery checks binding (found during M174): agents can no longer write placements, grants or `observed/instance/{id}/service`; instance → service mapping from a control-plane-only fact
- [ ] Short-lived node certificates with rotation; removed nodes rejected by etcd and API
- [ ] Threat model T5 updated with the real residual risk
- [ ] Store-layer audit writes (found during M175): `AuthorizedStore` persists an audit entry to etcd (`StoreBackedAuditLog.Log` → `persistEntry` → Put) for every allowed Get/Scan/Watch, so reads become etcd writes (an uncached status call wrote ~615 before M175, still one per scan); persist denies and writes, keep allowed reads in memory or aggregated per request; measure etcd puts/s before and after

### Phase 182 — Security Defense in Depth (M182)

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

### OpenSpec pilot (unscheduled, before M206)

Decision in `design/chats/chat-03nov-2.md`: adopt OpenSpec, pilot on GitOps, migrate no existing documentation.

Roles: `CLAUDE.md` = engineering rules and invariants; `design/` = architecture and implementation details; `design/runtime-invariant-flows.md` = end-to-end chains across components (Flows 11, 24, 25 stay there; specs link to flows and flows link back, nothing copied); `openspec/specs/` = behaviour requirements of maintained capabilities; `openspec/changes/` = work in progress; `milestones.md` = roadmap.

- [ ] Install pinned `@fission-ai/openspec@1.16.0` (needs Node >= 22.12.0; local v25.9.0); record the version
- [ ] `openspec init --tools claude` on a clean tree; review `git status` and `git diff -- .claude/` before committing; keep existing agents, skills and workflows unless a real conflict shows up
- [ ] Workflow ownership: OpenSpec drives proposal → specs → tasks → implementation → archive for OpenSpec-managed capabilities; `.claude/workflows/feature-development.js` does not run a competing pipeline for the same change; project-manager and technical-writer agents stay; `release-pipeline.js` / `scripts/release.sh` keep release execution
- [ ] Minimal `openspec/config.yaml`: `schema: spec-driven`, `context` points at CLAUDE.md and `design/` (no restated invariants), `rules` per artifact, `operations.apply.guidance` (inspect implementation and call graphs first, report real test results)
- [ ] First change `git-backed-configuration-sources` drafted from M206/M207 and existing architecture, resolving: Git outage, invalid revision, source ownership, partial application, concurrent revisions (pin to commit), sync vs health, `cca scale` overrides, manual `cca apply` (`cli` owner, conflict/adoption), etcd transaction limits
- [ ] Review ownership, override, prune and transaction semantics before implementation starts
- [ ] Release order per change: tasks done → tests pass → `openspec archive` → update M206/M207 links to `openspec/specs/...` and the dated `openspec/changes/archive/<date>-<change>/` → `scripts/release.sh`

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
| M179 — Tenant-Scoped Secrets | 179 | Secrets belong to a tenant (`secrets/tenant/{t}/…`, `secrets/cluster/…`, reserved `default`); `cca secret set --tenant/--cluster`, `share`/`unshare`; compiler resolution and delivery re-check shares; `cca secret migrate-names` re-encrypts legacy names. Depends: M177. |
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
