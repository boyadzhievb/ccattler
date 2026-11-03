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
- [x] e2e honesty: whoami-multiport port 8081 reported as KNOWN GAP (M186) instead of a passing check; nftables check matches real `vip_dispatch` entries and fails if port 80 is missing; step 5.9 no longer claims whoami is pinned

**Deploy, run & ship:**
- [x] Ansible worker template passes `--proxy-tls-listen` (port 443)
- [ ] Green on the deploy cluster 3 consecutive runs
- [ ] One-off run on the bare-metal eBPF cluster
- [ ] `gate-tls` added to deploy-testbed workflow default workloads
- [x] `design/dsl-reference.md` TLS section updated (node :443, service ports stay plain HTTP)

### Security review (2026-11-03) — Phases 171–177

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
- [ ] Deploy cluster run: `cca exec`, `cca logs -f`, `cca images pull` work from the control plane

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
- [ ] Deploy cluster run: `cca role grant user:<name> --role api-reader` takes effect without a restart; caddy-sync still reads routes

### Phase 174 — Secrets Architecture (M174)

- [ ] Design in `design/config-secrets.md`: per-node secret delivery, keyring format, rotation flow
- [ ] Enrollment response no longer contains the master key
- [ ] Agents fetch only secrets granted to instances placed on their node
- [ ] Envelope records key version; keyring persisted before re-wrap; restart test after rotation
- [ ] Rotation lock on the API server key field; replicas reload the keyring
- [ ] Tenant-scoped secret names and capability scope
- [ ] Gate TLS private keys (`desired/route/*/tls/key`, `derived/route/*/tls/key`) stored encrypted, not as plain facts (found during M173)

### Phase 175 — HA Correctness (M175)

- [ ] Leader election on an etcd lease (`GrantLease` exists) instead of wall-clock timestamps
- [ ] Leader key revision compared in every controller transaction (fencing)
- [ ] Multi-prefix controller scans read at one pinned revision
- [ ] Persisted JWT signing keyring shared by API replicas; `token_use` claim separates user and workload tokens
- [ ] Integration test: deposed leader's transaction is rejected

### Phase 176 — Store Access Isolation (M176)

- [ ] Design doc: agent access to state (etcd roles vs API gateway)
- [ ] Agents limited to their node's keys and read-only desired state
- [ ] Short-lived node certificates with rotation; removed nodes rejected by etcd and API
- [ ] Threat model T5 updated with the real residual risk

### Phase 177 — Security Defense in Depth (M177)

- [ ] Volume host path must be under the storage provider root
- [ ] Secret API bound to the bridge gateway; caller IP must match the token's instance
- [ ] Process runtime refused when TLS is enabled unless `--allow-process-runtime`
- [ ] nftables ruleset input validated (IPs, names)
- [ ] `/api/state` returns raw desired config values: apply the describe/status sensitive-key redaction there too (found during M173)
- [ ] Paginated store scans
- [ ] CI check: every `controllers.New*` and API `Set*` has a production caller

### OpenSpec pilot (unscheduled, before M201)

Decision in `design/chats/chat-03nov-2.md`: adopt OpenSpec, pilot on GitOps, migrate no existing documentation.

Roles: `CLAUDE.md` = engineering rules and invariants; `design/` = architecture and implementation details; `design/runtime-invariant-flows.md` = end-to-end chains across components (Flows 11, 24, 25 stay there; specs link to flows and flows link back, nothing copied); `openspec/specs/` = behaviour requirements of maintained capabilities; `openspec/changes/` = work in progress; `milestones.md` = roadmap.

- [ ] Install pinned `@fission-ai/openspec@1.16.0` (needs Node >= 22.12.0; local v25.9.0); record the version
- [ ] `openspec init --tools claude` on a clean tree; review `git status` and `git diff -- .claude/` before committing; keep existing agents, skills and workflows unless a real conflict shows up
- [ ] Workflow ownership: OpenSpec drives proposal → specs → tasks → implementation → archive for OpenSpec-managed capabilities; `.claude/workflows/feature-development.js` does not run a competing pipeline for the same change; project-manager and technical-writer agents stay; `release-pipeline.js` / `scripts/release.sh` keep release execution
- [ ] Minimal `openspec/config.yaml`: `schema: spec-driven`, `context` points at CLAUDE.md and `design/` (no restated invariants), `rules` per artifact, `operations.apply.guidance` (inspect implementation and call graphs first, report real test results)
- [ ] First change `git-backed-configuration-sources` drafted from M201/M202 and existing architecture, resolving: Git outage, invalid revision, source ownership, partial application, concurrent revisions (pin to commit), sync vs health, `cca scale` overrides, manual `cca apply` (`cli` owner, conflict/adoption), etcd transaction limits
- [ ] Review ownership, override, prune and transaction semantics before implementation starts
- [ ] Release order per change: tasks done → tests pass → `openspec archive` → update M201/M202 links to `openspec/specs/...` and the dated `openspec/changes/archive/<date>-<change>/` → `scripts/release.sh`

### GitOps — Phases 201–202

Proposal: `design/chats/chat-03nov.md`. Git supplies desired-state inputs; policy, scheduling, execution and observation stay with CCattler. Ownership and prune come first because they also fix `cca apply` today. Numbers are out of priority order (added 2026-11-03 at the end of the sequence, after M200); renumber in the next priority pass.

### Phase 201 — Apply Ownership & Prune (M201)

- [ ] Design as OpenSpec change `git-backed-configuration-sources` (see OpenSpec pilot; `design/gitops.md` only if the pilot is dropped): owner model, ownership keys, prune rules, conflict rules; cross-component flow in `design/runtime-invariant-flows.md`
- [ ] Decide how `intent/user/` overrides (`cca scale`) relate to an owner: kept as a separate layer or reverted by the owner
- [ ] Ownership facts per resource (e.g. `source/{owner}/owns/{kind}/{name}`), not per fact key; new root in `controllerOutputPrefixes`, store RBAC roles and `key-ownership-matrix.md`
- [ ] `cca apply` records owner `cli`; applying a resource owned by another owner is refused with the owner's name
- [ ] Prune: resources owned by this owner and missing from the applied set are deleted; generalize `collectRemovedGateFacts` to every resource kind
- [ ] Prune never runs on a partial or failed compile; an empty candidate is refused unless explicitly forced
- [ ] Per-resource atomic commit within the etcd operation limit: either enforce a maximum facts-per-resource or add staged generation and promotion for larger resources; source-wide application resumable and recorded by revision
- [ ] `cca diff` lists additions, changes, removals and ownership conflicts per resource
- [ ] Integration tests: remove a service from a directory → deleted; another owner's service untouched; repeated apply is a no-op

### Phase 202 — Git Configuration Sources (M202)

- [ ] Design in the same OpenSpec change: source facts, lifecycle, status model, Git client choice (`git` binary vs Go library: air-gap and supply-chain impact)
- [ ] `cca source add/list/status/diff/sync/pause/resume/remove`; `design/cli-reference.md`
- [ ] Source reconciler under controller leadership: poll, resolve branch to SHA, fetch only the path, render values, compile, validate, apply through M201 as the source's owner
- [ ] Source identity: one service principal per source, grants scope what it may manage (e.g. one tenant); compiled facts go through the policy gate
- [ ] Safe fetch: allowed URL schemes and hosts, path traversal and symlink protection, size limits
- [ ] Credentials stored as CCattler secrets, never in URLs (depends: M174)
- [ ] Failure handling: fetch or compile failure keeps last good state, reports Failed, never prunes
- [ ] Status API: fetched revision, applied revision, sync status, last error; health reported separately from workload observations
- [ ] Integration tests with a local bare repository; e2e on the deploy cluster

### Phase 178 — eBPF Data Plane Correctness (M178)

Harden the eBPF data plane (M128–M134) to the point where it can fully replace nftables for VIP, SNAT, and basic network policy — running a single data plane instead of two in parallel. Discovered during VXLAN CI investigation: tc SNAT was stateless and broke TCP reply packets; agent restart race deleted BPF pin directory; no TCP-level integration tests existed; packet flow matrix was undocumented.

**tc SNAT conntrack validation:**
- [x] Make `cca_tc_snat` conntrack-aware via `bpf_skb_ct_lookup` — skip SNAT for reply-direction packets
- [x] Add `IP_CT_DIR_ORIGINAL`/`IP_CT_DIR_REPLY` defines to `cca_ct_helpers.h`
- [x] Document packet flow matrix in `NETWORK_GRAPH.md` (all source→dest combinations)
- [x] Update `EBPF_GRAPH.md` — SNAT call tree with conntrack, kfuncs table
- [ ] Validate tc SNAT conntrack fix passes 10+ CI runs without init probe regression
- [ ] Verify container-initiated egress (container → external) still works with SNAT

**Agent restart BPF pin race:**
- [ ] Fix `removePinnedMaps()` to only remove pins created by THIS instance (PID-namespaced pin dir or flock)
- [ ] Add integration test: restart agent under load, verify BPF maps survive and service map entries persist
- [ ] Add startup log line: "reusing existing BPF pins" vs "creating fresh BPF pins"

**eBPF TCP integration tests:**
- [ ] Add e2e test: host-ns → remote container TCP via VIP (the init probe path)
- [ ] Add e2e test: container → container TCP cross-node via VIP
- [ ] Add e2e test: container → external host TCP (egress SNAT path)
- [ ] Add e2e test: verify `nc` TCP connectivity, not just ICMP ping, in cluster health check
- [ ] Each test row from NETWORK_GRAPH.md packet flow matrix has a corresponding e2e assertion

Policy and nftables elimination items moved to Phase 192.

### Phase 179 — HA Etcd Cluster Validation (M179)

First end-to-end validation of HA control plane on vmherd: 3-node etcd raft cluster, leader-elected cca-server, control plane failover, and etcd member failure recovery.

- [ ] vmherd Herdfile: 3 ctrl (4GB) + 1 worker (2GB) on 192.168.125.0/24
- [ ] Ansible inventory mapping vmherd VMs to HA groups (etcd, controlplane, workers)
- [ ] Test workload: whoami (3 instances) + whoami-multiport (2 instances)
- [ ] HA validation script: 9 checks (reachability, etcd health, API, leader election, nodes, workload, VIP, failover, etcd failure)
- [ ] Deploy cluster via `ha-deploy.yml` with existing HA Ansible roles
- [ ] Verify etcd 3-member quorum, leader election, workload placement
- [ ] Control plane failover: stop leader cca-server, verify new leader within 30s
- [ ] etcd member failure: stop one member, verify quorum holds (2/3), API works
- [ ] Recovery: restart failed components, verify they rejoin

### Phase 192 — eBPF Policy & nftables Elimination (M192)

Second half of the original Phase 151 checklist. Depends: M178.

**eBPF network policy (basic):**
- [ ] tc ingress program on cca0 for ingress allow/deny by source CIDR (replaces nftables filter FORWARD)
- [ ] BPF map populated from `derived/network/rule/` facts by agent reconciler
- [ ] Default-deny when at least one policy applies to the service
- [ ] Integration test: policy blocks cross-service traffic, allows permitted

**nftables elimination gate:**
- [ ] M178 complete and all items above done
- [ ] Remove nftables VIP chains when `--dataplane ebpf` (currently kept as fallback)
- [ ] Remove nftables POSTROUTING masquerade per-container rules when eBPF SNAT active
- [ ] `--dataplane ebpf` tested on all three targets: bare-metal, Vagrant, Lima
- [ ] Document kernel version requirements (6.6+ for TCX, 5.18+ for conntrack kfuncs)

### Phase 200 — Airflow Triggerer & Observability (M200)

Future placeholder: additional Airflow services for full Helm chart parity.

- [ ] Triggerer service (deferrable operators, exec-based probes)
- [ ] StatsD metrics exporter sidecar
- [ ] Log groomer via existing logging DSL keyword

## Milestones

Ordered by priority. Renumbered 2026-11-03: the security review milestones were inserted as M171–M177, and the former M171–M193 became M178–M200. Earlier plans in `design/plans/` use older numbers.

| Milestone | Phases | Demo |
|-----------|--------|------|
| **Tier 0 — Finish & verify what shipped** | | |
| M170 — Gate HTTPS Listener & End-to-End Validation | 170 | Gate TLS moves from the service VIP to a node `:443` listener on the agent gate proxy: SNI selects the gate cert, Host routes to the service, service ports stay plain HTTP for in-cluster callers, multiple gates per service, works on every data plane. Then validate on the deploy cluster (15 e2e cases). Fixes M169 gaps: TLS-only gates not dispatched on nftables, eBPF ignored TLS. Depends: M169. |
| **Tier 0S — Security review fixes (2026-11-03)** | | |
| M171 — Enrollment Identity Hardening | 171 | The CA sets the node certificate identity itself instead of copying the CSR subject, so a join token can no longer produce a `ccattler-server` (cluster-admin) certificate. Node certs are client-auth only with no CSR-chosen SANs. Node IDs are validated and reserved names refused. A join token is consumed atomically. |
| M172 — Agent Debug API Lockdown | 172 | The agent debug API (`/debug/exec`, `/debug/images/pull`) requires an mTLS client certificate from the cluster CA and an admin principal. Ansible binds it to loopback until then. |
| M173 — API Authorization Correctness | 173 | Store grants and ABAC policies reach the API authorizer in every API process (AuthController was never wired). `/api/state` and `/api/watch` use a prefix allowlist instead of the bypassable denylist. Authenticators skip tokens from other issuers instead of aborting the chain. The watch limit is enforced atomically. |
| M174 — Secrets Architecture | 174 | Nodes no longer receive the cluster master key. Agents get only secrets granted to instances placed on them. Versioned keyring persisted before re-wrap, so rotation survives a restart and reaches every replica. Tenant-scoped secrets. Design doc first. |
| M175 — HA Correctness | 175 | Leader election on an etcd lease instead of wall-clock timestamps, and the leader key revision is checked in every controller transaction. Multi-prefix scans read at one revision. Persisted JWT signing keyring shared by replicas, with a token-use claim. Before M179. |
| M176 — Store Access Isolation | 176 | Agents can no longer read or write every etcd key. Near term: etcd roles mapped from certificate CN, or a separate etcd client CA for the control plane only. Target: agents go through an API gateway. Short-lived node certificates with rotation and revocation. Design doc first. |
| M177 — Security Defense in Depth | 177 | Volume host paths checked against the storage root. Secret API bound to the container bridge with a source-IP check. Process runtime refused outside dev mode. nftables ruleset input validated. Scan pagination. A CI check that every controller constructor and API setter has a production caller. |
| M178 — eBPF Data Plane Correctness | 178 | Correctness half of the original M151 (see checklist): tc SNAT conntrack fix validated over 10+ CI runs, egress SNAT verified, agent-restart BPF pin race fixed, TCP e2e tests for every packet-flow-matrix row. eBPF policy and nftables removal moved to M192. Depends: M128–M134. |
| M179 — HA Control Plane Validation | 179 | First end-to-end HA validation on vmherd (see checklist): 3-node etcd quorum, leader-elected cca-server (`controllers/leader.go`, `ha_runner.go` already exist), control plane failover <30s, etcd member failure, recovery. Absorbs the former Control Plane HA milestone: also adds `cca server --join <existing>` if validation shows it is needed. Depends: M175. |
| M180 — Etcd Backup & Restore | 180 | `cca backup create/restore`. Scheduled automatic backups. Retention policy. Point-in-time restore. |
| **Tier 1 — High value for effort** | | |
| M181 — Debug & Explain CLI | 181 | Existing: `cca describe` (service/node/instance/gate), `cca diff`, `cca events`. Remaining: `cca debug <service>` (one-shot health report: instances, probes, recent events, placement failures) and `cca explain <service>` (scheduler placement reasoning per node). |
| M182 — Network Policy Egress Rules | 182 | `network_policy "name" { egress { to_cidr "10.0.0.0/8" port 443 } }`. nftables output chain. Default-deny egress when policy applies. DNS-based egress (`to_fqdn`). Depends: M168. |
| M183 — Prometheus Metrics Gaps | 183 | Existing: `/metrics` in Prometheus format (`metrics/metrics.go`) with API, reconciliation (duration, changes, conflicts), activation, and node/service/instance gauges. Remaining: per-controller labels on reconciliation metrics, autoscaler decision metrics, instance counts by state, scrape config docs. |
| M184 — Quota CLI | 184 | Existing: tenant `quota {}` DSL and `QuotaAdmission` (`tenant/quota.go`) blocking instance/volume admission, usage facts. Remaining: `cca quota list` / `cca quota describe <tenant>` showing limits vs usage. |
| M185 — Persistent Volume Gaps | 185 | Existing: `volume` DSL (size, persistent, type, replicas), service volume mounts, per-ordinal stateful volumes (`controllers/stateful_volume.go`), LocalPath/NFS providers (M164). Remaining: scheduler volume locality (place instances on nodes holding their local volume), size enforcement/quota on local provisioner. |
| M186 — Multi-Port Service VIPs | 186 | Found during M170: `NetworkController` gives each service one VIP port (`servicePorts[svc][0]`), so `expose 8081` on a second port never reaches the data plane, although M124 claimed per-port VIPs. One VIP per service with every exposed port: `network/vip/service/{name}/port/{port}` (legacy single `port` key migrated), `ServiceVIPConfig.Ports`, nftables/iptables/eBPF dispatch per port, `/api/status` networking entry per port, cloud LB per-port config. E2E: whoami-multiport answers on 8081 through the VIP from every node (hard failure, not a warning). Design in `design/networking.md` first. Depends: M178 (eBPF data plane). |
| **Tier 2 — Substantial features** | | |
| M187 — Service Mesh via Groups | 187 | `mesh { mtls "strict" }` — compiler auto-adds proxy to service group. Proxy uses existing SPIFFE identity. Traffic metrics as derived facts. Uses group network sharing, not sidecar injection. Depends: M169. |
| M188 — DSL Admission Policy Rules | 188 | `policy "no-latest" { on apply match { service where image =~ ":latest$" } deny "not allowed" }`. Mutating policies. Compiled into PolicyGate — no webhooks. Escape hatch: `validate_url` for external validation. |
| M189 — Volume Snapshots | 189 | `cca volume snapshot create <service>`. Filesystem copy for local provisioner. Restore to new volume. Retention policy (count/age). Depends: M185. |
| M190 — Distributed Tracing | 190 | OpenTelemetry trace propagation through controllers. OTLP export. Trace correlation: API request → controller → agent. `cca trace <request-id>`. Depends: M183. |
| M191 — DSL Templates & Imports | 191 | `template "web-app" (image, port) { ... }`. `import "lib/common.cca"`. Pure substitution. Standard library at `stdlib/`. Reconsider first: Go templates + `.ccv` values already cover parameterization. |
| M192 — eBPF Policy & nftables Elimination | 192 | Second half of the original M151 checklist: tc ingress policy program on cca0 from `derived/network/rule/` facts, default-deny, then remove nftables VIP/masquerade chains under `--dataplane ebpf`; test on bare-metal, Vagrant, Lima; document kernel requirements. Depends: M178. |
| M201 — Apply Ownership & Prune | 201 | Foundation for GitOps, useful without Git: every resource in desired state has one owner (`cli` for `cca apply`, later a Git source), recorded per resource. `cca apply <dir>` of the same owner deletes resources removed from the files (today only gates are pruned, M170). Applying a resource owned by someone else is refused. Each resource's facts commit atomically; application is resumable and idempotent. `cca diff` shows removals and ownership conflicts in CCattler terms. Proposal: `design/chats/chat-03nov.md` (stage 2). |
| M202 — Git Configuration Sources | 202 | `cca source add/list/status/diff/sync/pause/resume/remove`. A source = repository, revision, path, values files, sync mode, prune flag. Polls (webhook later), pins the branch to a commit SHA, fetches only the path, renders `.ccv` values, compiles and validates before writing; a failed fetch or compile keeps the last good desired state and never prunes. Applies through M201 ownership as the source's own service identity, scoped by grants (M173). Source status (Synced/OutOfSync/Applying/Failed, fetched vs applied revision) kept separate from workload health. Runs under controller leadership. Credentials as CCattler secrets (M174). Depends: M201, M173, M174. |
| **Tier 3 — Later / optional** | | |
| M193 — Multi-Cluster Service Discovery | 193 | `cca cluster register <remote>` links clusters. Services visible as `<svc>.<cluster>.svc.ccattler.global`. Cross-cluster health checking. Depends: M168, M154. |
| M194 — Storage Plugin Interface | 194 | CSI-compatible plugin model. Driver registration and capability discovery. External provisioners (NFS, cloud block). Attach/detach lifecycle. Depends: M185. |
| M195 — Custom Fact Types with Schema Validation | 195 | Schema at `meta/fact-type/{name}`. PolicyGate validates writes to `custom/{type}/`. `cca get <type>` auto-discovers. `cca describe fact-type`. No API group versioning — facts with optional schema. |
| M196 — Controller SDK gRPC Transport | 196 | gRPC streaming for out-of-process controllers. `cca controller register --name X --address host:port`. Health check + auto-deregister. Builds on existing CustomController interface (M81). |
| M197 — Controller Scaffolding CLI | 197 | `cca controller init <name>` scaffolds Go/Python project with gRPC client, reconciler stub, test harness. Working example included. Not an operator lifecycle manager. Depends: M196. |
| M198 — Cluster Upgrade Controller | 198 | `cca upgrade plan/apply`. Rolling control-plane + agent upgrade. Drain → upgrade → re-enable node. Rollback on failure. Depends: M179, M180. |
| M199 — Conformance Test Suite | 199 | 100+ e2e test cases across all phases. `cca conformance run`. Test matrix: single/multi-node, HA, multi-tenant. Depends: all previous. Prefer growing it with each milestone over one big phase at the end. |
| M200 — Airflow Triggerer & Observability | 200 | Placeholder: triggerer service, StatsD exporter, log groomer |
| **Future (unscheduled)** | | |
| *(future)* OpenSpec Adoption (pilot) | — | Adopt OpenSpec 1.16.0 (pinned) for new capabilities, GitOps first; existing docs not migrated. Checklist under "OpenSpec pilot" above. Decision and config: `design/chats/chat-03nov-1.md`, `design/chats/chat-03nov-2.md`. Before M201. |
| *(future)* Production GitOps | — | Stage 3 of the GitOps proposal (`design/chats/chat-03nov.md`): signed commit verification, webhook-triggered fetches (a webhook only triggers a fetch, never carries config), approval policies, pull request previews (`cca source diff` against a branch), rollback by repinning a revision, declarative source definitions. Depends: M202. |
| *(future)* API TLS Rate Limiting | — | Rate-limit failed TLS handshakes per source IP on the `cca server` API listener. After N failed handshakes from the same IP within a sliding window (e.g. 10 failures in 60s), temporarily block that IP for a cooldown period. Prevents a misconfigured client (wrong certs, broken TLS) from flooding the server with handshake errors, which starves goroutines and causes etcd operation timeouts — leading to leadership loss and controller stall. Implementation: wrap `tls.Listener` with a rate-limiting accept loop that tracks per-IP failure counts. Log blocked IPs at WARN level. No configuration needed beyond sensible defaults. Discovered during M130 validation when a caddy-sync daemon with wrong certificates caused leadership loss on the deploy cluster. |
| *(future)* Gate Traffic Splitting | — | Weighted backend routing for `gate` blocks. DSL: `gate canary { host app.example.com; backend api-v1 weight 90; backend api-v2 weight 10 }`. Compiler emits `desired/route/{name}/backend/{service}/weight` facts. Route controller distributes backends proportionally in `derived/route/{name}/backends` (e.g. 9 v1 entries + 1 v2 entry for round-robin). Data plane round-robin naturally approximates weights. For exact splitting, BPF `numgen` mod N with weighted slot assignment. Inspired by Kubernetes Gateway API HTTPRoute `backendRefs[].weight`. Enables canary deployments and A/B testing without a separate proxy. |
| *(future)* WireGuard Node Encryption | — | Transparent encryption of all inter-node traffic using WireGuard tunnels. Agent creates `cca-wg0` WireGuard interface on startup, generates key pair, publishes public key as `observed/node/{id}/wireguard/pubkey` fact. WireGuardReconciler watches node facts, programs peer entries for every other node (`wg set cca-wg0 peer <pubkey> endpoint <ip>:51820 allowed-ips <subnet>`). VXLAN traffic routed over WireGuard by adjusting FDB entries to use `cca-wg0` as the outgoing device. DSL opt-in: `cluster { encryption wireguard }` in the cluster config. No third-party dependencies — uses standard Linux WireGuard kernel module (`ip link add cca-wg0 type wireguard`). Key rotation via periodic regeneration and fact update — peers converge within one reconciliation cycle. |
| *(future)* Gate L7 Metrics | — | Request-level observability for `gate` blocks. The agent HTTP proxy (`network/userspace_proxy.go`) already sees every L7 request via Host header routing. Add per-gate metrics: request count, latency histogram (p50/p95/p99), status code distribution, error rate. Emit as `derived/metric/gate/{name}/{metric}` facts on each proxy reconciliation cycle. `cca top gates` CLI command reads and displays metrics in real-time. `cca metric export` outputs Prometheus exposition format for external scraping. No kernel instrumentation needed — measured at the userspace proxy layer. |
| *(future)* BPF L7 Policy | — | eBPF-based L7 request filtering for HTTP traffic. tc BPF program inspects HTTP request headers (method, path) on `cca-net` bridge and enforces allow/deny rules compiled from DSL `firewall` blocks with L7 matchers: `firewall web-api { allow from backend method GET path "/api/*" }`. Compiler emits `derived/network/rule/{name}/l7/{index}` facts with method/path patterns. BPF program parses TCP payload for HTTP/1.1 request line, matches against BPF hash map of compiled rules, drops non-matching packets. Kernel 6.1+ (BPF packet parsing helpers). Complements existing L3/L4 identity-based policy with application-layer granularity. |
