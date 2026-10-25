# Changelog

All notable releases of CCattler are documented here.
## v1.60.1-beta <Badge type="tip" text="latest" /> {#v1-60-1-beta}

**2026-10-25** — HA Etcd Cluster Validation.

- **HA Etcd Cluster Validation** (M136) — First end-to-end HA validation on vmherd: 3 ctrl (4GB, etcd + cca-server) + 1 worker (2GB) on 192.168.125.0/24. HA test script validates 9 areas: VM reachability, etcd 3-member quorum, cca-server API on all 3 ctrl, leader election (one active controller), 4-node registration, whoami/whoami-multiport workload convergence, VIP routing from all nodes, control plane failover (stop leader → new leader in 30s), etcd member failure (2/3 quorum holds). Uses existing `ha-deploy.yml`, `etcd-cluster` and `ha-controlplane` Ansible roles — no Go code changes.

---

## v1.60.0-beta {#v1-60-0-beta}

**2026-10-25** — HA Etcd Cluster Validation.

- **HA Etcd Cluster Validation** (M136) — First end-to-end HA validation on vmherd: 3 ctrl (4GB, etcd + cca-server) + 1 worker (2GB) on 192.168.125.0/24. HA test script validates 9 areas: VM reachability, etcd 3-member quorum, cca-server API on all 3 ctrl, leader election, 4-node registration, whoami/whoami-multiport workload convergence, VIP routing from all nodes, control plane failover, etcd member failure recovery. Uses existing HA Ansible roles — no Go code changes.

---

## v1.59.0-beta {#v1-59-0-beta}

**2026-10-25** — Scheduler Rebalancing & Pressure Detection.

- **Scheduler Rebalancing & Pressure Detection** (M135) — Prevent thundering-herd placement on cluster startup via 10s grace period. Reactive rebalancing detects load imbalance and migrates up to 2 instances per cycle from overloaded to underloaded nodes, respecting architecture/zone/label/restrict constraints. Node allocation pressure detection logs WARNING when CPU or memory allocation exceeds 80%. 9 new tests, design doc at `design/scheduler-rebalancing.md`.

---

## v1.58.0-beta {#v1-58-0-beta}

**2026-10-25** — BPF Binary Consolidation.

- **BPF Binary Consolidation** (M134) — Consolidated three separate BPF ELF objects into a single multi-program ELF (`cca_dataplane.o`) embedded in the Go binary via `go:embed`. All 5 BPF programs (socket LB, tc DNAT, tc SNAT) share maps from one `ebpf.Collection` — no more `MapReplacements`. Eliminated `/usr/lib/cca/*.o` install step, Ansible on-node BPF compilation, and separate load methods. `build-ebpf.sh` produces one file; deploy copies only the binary.

---

## v1.57.3-beta {#v1-57-3-beta}

**2026-10-24** — DSL Gate Keyword.

- **DSL Gate Keyword** (M131) — Rename user-facing DSL keyword from `route` to `gate` for external traffic blocks. `gate` is shorter, unambiguous, and avoids collision with cloud/VPC route terminology already used by `CloudRouteController`. Parser accepts only `gate`; formatter emits `gate`; compiler error messages say "gate". Internal fact store keys (`desired/route/`, `derived/route/`) unchanged — the rename is DSL-surface only. Updated: parser, compiler, formatter, linter, all tests, examples, workloads, design docs, kubernetes migration guide, STATUS.md.

---

## v1.57.2-beta {#v1-57-2-beta}

**2026-10-20** — DSL Gate Keyword.

- **DSL Gate Keyword** (M131) — Rename user-facing DSL keyword from `route` to `gate` for external traffic blocks. `gate` is shorter, unambiguous, and avoids collision with cloud/VPC route terminology already used by `CloudRouteController`. Parser accepts only `gate`; formatter emits `gate`; compiler error messages say "gate". Internal fact store keys (`desired/route/`, `derived/route/`) unchanged — the rename is DSL-surface only. Updated: parser, compiler, formatter, linter, all tests, examples, workloads, design docs, kubernetes migration guide, STATUS.md.

---

## v1.57.1-beta {#v1-57-1-beta}

**2026-10-20** — DSL Gate Keyword.

- **DSL Gate Keyword** (M131) — Rename user-facing DSL keyword from `route` to `gate` for external traffic blocks. `gate` is shorter, unambiguous, and avoids collision with cloud/VPC route terminology already used by `CloudRouteController`. Parser accepts only `gate`; formatter emits `gate`; compiler error messages say "gate". Internal fact store keys (`desired/route/`, `derived/route/`) unchanged — the rename is DSL-surface only. Updated: parser, compiler, formatter, linter, all tests, examples, workloads, design docs, kubernetes migration guide, STATUS.md.

---

## v1.57.0-beta {#v1-57-0-beta}

**2026-10-20** — DSL Gate Keyword.

- **DSL Gate Keyword** (M131) — Rename user-facing DSL keyword from `route` to `gate` for external traffic blocks. `gate` is shorter, unambiguous, and avoids collision with cloud/VPC route terminology already used by `CloudRouteController`. Parser accepts only `gate`; formatter emits `gate`; compiler error messages say "gate". Internal fact store keys (`desired/route/`, `derived/route/`) unchanged — the rename is DSL-surface only. Updated: parser, compiler, formatter, linter, all tests, examples, workloads, design docs, kubernetes migration guide, STATUS.md.

---

## v1.56.1-beta {#v1-56-1-beta}

**2026-10-20** — BPF SNAT (nftables elimination).

- **BPF SNAT (nftables elimination)** (M130) — Phase 3 of independent eBPF data plane: replace nftables POSTROUTING masquerade with BPF tc egress SNAT, eliminating the last nftables dependency. New tc BPF program `cca_tc_snat` attached to the physical NIC tc egress via TCX link. For every outbound packet, checks whether the source IP falls in the overlay CIDR range (`cca_config` map stores `overlay_prefix` and `overlay_mask`); if so, rewrites source IP to the node's advertise address (from `cca_config` index 0) and recomputes IP + L4 checksums via `bpf_l3_csum_replace`/`bpf_l4_csum_replace`. Non-overlay-sourced packets pass through unchanged. This covers both DNAT paths: container-to-VIP via tc bridge DNAT (M129 conntrack kfuncs) and host-to-VIP via cgroup socket LB (M128 kernel conntrack). Container-to-container cross-node traffic over VXLAN does NOT need SNAT — VXLAN encapsulation preserves overlay addresses and the tunnel mesh provides symmetric routing, so packets leaving via `cca-vxlan0` are skipped. New `cca_config` BPF array map (8 entries, u32 values: index 0 = node_ip, 2 = overlay_prefix, 3 = overlay_mask) populated by Go control plane from `--advertise-address` and `--cluster-cidr`. Physical interface auto-discovered from advertise address via `detectPhysicalInterfaceForAddress` (same as VXLAN reconciler). Go changes: `EbpfDataPlane` gains `snatCollection`, `snatLink`, `configMap` fields; `loadSNATProgram()` loads `cca_tc_snat.o` with `MapReplacements`, creates `cca_config` map; `attachSNATProgram(physInterface)` attaches to physical NIC egress; `RegisterNode()` extended to discover physical interface and attach SNAT after bridge attachment. On `Cleanup()`, nftables `ip cca` table deleted entirely when `--dataplane ebpf` — `writePostRoutingChain` becomes a no-op. Compiled to separate ELF (`cca_tc_snat.o`). Kernel 6.6+ (TCX). `build-ebpf.sh` and Makefile updated. See `design/ebpf-data-plane-v2.md`.

---

## v1.56.0-beta {#v1-56-0-beta}

**2026-10-20** — BPF SNAT (nftables elimination).

- **BPF SNAT (nftables elimination)** (M130) — Phase 3 of independent eBPF data plane: replace nftables POSTROUTING masquerade with BPF tc egress SNAT, eliminating the last nftables dependency. New tc BPF program `cca_tc_snat` attached to the physical NIC tc egress via TCX link. For every outbound packet, checks whether the source IP falls in the overlay CIDR range (`cca_config` map stores `overlay_prefix` and `overlay_mask`); if so, rewrites source IP to the node's advertise address (from `cca_config` index 0) and recomputes IP + L4 checksums via `bpf_l3_csum_replace`/`bpf_l4_csum_replace`. Non-overlay-sourced packets pass through unchanged. This covers both DNAT paths: container-to-VIP via tc bridge DNAT (M129 conntrack kfuncs) and host-to-VIP via cgroup socket LB (M128 kernel conntrack). Container-to-container cross-node traffic over VXLAN does NOT need SNAT — VXLAN encapsulation preserves overlay addresses and the tunnel mesh provides symmetric routing, so packets leaving via `cca-vxlan0` are skipped. New `cca_config` BPF array map (8 entries, u32 values: index 0 = node_ip, 2 = overlay_prefix, 3 = overlay_mask) populated by Go control plane from `--advertise-address` and `--cluster-cidr`. Physical interface auto-discovered from advertise address via `detectPhysicalInterfaceForAddress` (same as VXLAN reconciler). Go changes: `EbpfDataPlane` gains `snatCollection`, `snatLink`, `configMap` fields; `loadSNATProgram()` loads `cca_tc_snat.o` with `MapReplacements`, creates `cca_config` map; `attachSNATProgram(physInterface)` attaches to physical NIC egress; `RegisterNode()` extended to discover physical interface and attach SNAT after bridge attachment. On `Cleanup()`, nftables `ip cca` table deleted entirely when `--dataplane ebpf` — `writePostRoutingChain` becomes a no-op. Compiled to separate ELF (`cca_tc_snat.o`). Kernel 6.6+ (TCX). `build-ebpf.sh` and Makefile updated. See `design/ebpf-data-plane-v2.md`.

---

## v1.55.0-beta {#v1-55-0-beta}

**2026-10-19** — tc DNAT for Bridge Traffic.

- **tc DNAT for Bridge Traffic** (M129) — Phase 2 of independent eBPF data plane. tc BPF program `cca_tc_dnat` attached to `cca-net` bridge ingress replaces nftables PREROUTING DNAT for container-to-VIP traffic. Uses conntrack kfuncs (`bpf_skb_ct_alloc`, `bpf_ct_set_nat_info`, `bpf_ct_insert_entry`) to create kernel conntrack DNAT entries — the kernel's nf_nat applies actual packet rewriting and handles return-path reverse NAT automatically. Fast path: `bpf_skb_ct_lookup` for established connections (one hash lookup, no backend selection). Shares `cca_services`/`cca_backends`/`cca_affinity` maps with socket LB via `MapReplacements`. Bridge interface auto-discovered from overlay CIDR. Compiled to separate ELF (`cca_tc_dnat.o`), loaded with cilium/ebpf `link.AttachTCX` (kernel 6.6+). After this phase, nftables is only used for POSTROUTING masquerade (SNAT). See `design/tc-dnat-design.md`.

---

## v1.54.0-beta {#v1-54-0-beta}

**2026-10-19** — Independent eBPF Socket LB.

- **Independent eBPF Socket LB** (M128) — Phase 1 of independent eBPF data plane (no Cilium). BPF C programs: `cca_connect4` (cgroup/connect4 for TCP VIP translation), `cca_sendmsg4` (cgroup/sendmsg4 for UDP), `cca_getpeername4` (cgroup/getpeername4 for reverse VIP). Shared BPF maps: `cca_services` hash map ({VIP,port,proto} → service metadata), `cca_backends` hash map ({service_id,slot} → endpoint), `cca_affinity` LRU hash (client IP sticky), `cca_reverse_nat` LRU hash (getpeername reverse). Go `EbpfDataPlane` implements `DataPlaneProvider` via cilium/ebpf library (bpf2go generated bindings). Map reconciliation from `ServiceVIPConfig` facts — same pattern as nftables. `--dataplane ebpf` flag (build tag `ebpf`, kernel 6.1+). nftables PREROUTING+POSTROUTING kept for bridge traffic and SNAT (Phase 2 replaces). `scripts/build-ebpf.sh` compiles C→ELF on Linux. See `design/ebpf-data-plane-v2.md`.

---

## v1.53.0-beta {#v1-53-0-beta}

**2026-10-19** — Cilium E2E Integration.

- **Cilium E2E Integration** (M127) — Wire CiliumFactTranslator into agent reconciliation loop: call `WriteEndpointIPIdentity` when containers start, `WriteNodeRegistration` on agent startup. Wire `CiliumPolicyTranslator.ReconcilePolicies` into network controller for identity-based policy. Ansible `cilium` role: install Cilium agent binary (DEB/RPM), configure `--kvstore etcd --kvstore-opt etcd.config=<path>`, kernel BTF/bpf_jit check, systemd unit. E2E test workload: deploy service with `--network-mode cilium --dataplane cilium`, validate cross-node VIP DNAT via Cilium eBPF instead of nftables. Shadow mode: Cilium runs alongside nftables, `cca benchmark dataplane` compares latency/throughput, operator switches when confident. Test on testbed-100.43 bare-metal VMs (kernel 5.15+).

---

## v1.52.0-beta {#v1-52-0-beta}

**2026-10-19** — Cilium eBPF Data Plane.

- **Cilium eBPF Data Plane** (M126) — Replace native VXLAN + nftables + network policy with Cilium's eBPF stack on kernel 5.4+ hosts. Cilium in kvstore mode reads CCattler's etcd directly — no Cilium API server, CCattler remains the single control plane. `CiliumFactTranslator` watches CCattler endpoint/VIP/policy facts, writes Cilium-compatible kvstore keys (`cilium/state/identities/`, `cilium/state/ip/`, `cilium/state/services/`). `CiliumDataPlaneProvider` implements `DataPlaneProvider` — `ReconcileVIPDataPlane` and `ReconcileRouteDataPlane` become no-ops (Cilium handles it), `Cleanup` removes Cilium kvstore keys. VXLAN replacement: Cilium tunnel mode replaces `VXLANReconciler`, manages its own `cilium_vxlan` interface. Network policy replacement: `CiliumPolicyTranslator` maps `DerivedFirewallRule` to Cilium identity-based policy in kvstore. Ansible `cilium` role installs Cilium agent (DEB/RPM), configures kvstore endpoint, kernel BTF check. `--dataplane cilium` agent flag alongside `nftables\

---

## v1.51.0-beta {#v1-51-0-beta}

**2026-10-19** — Multi-Port Cross-Node Regression.

- **Multi-Port Cross-Node Regression** (M124) — End-to-end proof that multi-port services work across nodes, closing external review finding. New `whoami-multiport.cca` test workload exposing ports 80 (HTTP) and 8081 (HTTP alt), each returning the port it was served on. Per-port E2E assertions: curl both ports from every node through VIPs, verify distinct nftables verdict map entries per port. Per-port hostport fact check: verify `observed/instance/{id}/hostport/80` and `observed/instance/{id}/hostport/8081` exist (not just legacy single-port key). Cloud LB hash granularity audit: verify `cloud_loadbalancer.go` stores/retrieves config hashes at per-port granularity (review flagged per-service hash vs per-port config mismatch). Legacy fallback removal: if all multi-port paths work, deprecate the legacy single-port `hostport` key with a TODO for removal.

---

## v1.50.0-beta {#v1-50-0-beta}

**2026-10-19** — Traffic-Level Networking Validation.

- **Traffic-Level Networking Validation** (M123) — Promote E2E networking from rule-existence checks to live traffic proof — CI fails on traffic regression. Harden HTTP-through-VIP (step 5.8) from soft warning to hard CI failure: every exposed HTTP port must return 2xx/3xx from every node. Backend-churn traffic test: `curl` VIP in a loop while scaling service down, verify zero failed requests once remaining backend is healthy. Node-failure traffic test: stop `cca-agent` on one worker, verify VIP traffic continues via backends on surviving nodes within the failure controller's replacement window. Cross-node path assertion: deploy `whoami` on worker-1 only (placement restrict), curl its VIP from worker-2, verify response proves worker-1 container served it. Sticky session validation: deploy a route with `sticky cookie`, send 10 consecutive requests with cookie jar, verify all hit the same backend via `whoami` hostname. DNS-under-churn: scale a service, verify `dig` resolves VIP correctly before and after the scale event.

---

## v1.49.0-beta {#v1-49-0-beta}

**2026-10-18** — VXLAN Overlay Networking.

- **VXLAN Overlay Networking** (M122) — Native VXLAN tunnel mesh for cross-node container connectivity. `--network-mode vxlan` flag on `cca agent`. Agent-managed VXLAN reconciler creates `cca-vxlan0` interface (VNI 42, UDP 4789), programs per-peer routes and FDB entries from node facts in the store. Subnet allocation as `desired/network/subnet/{node-id}` facts written by the server on node registration. Bridge MTU 1450 (VXLAN 50-byte overhead). No external daemon — fully fact-based, idempotent reconciliation. See `design/networking.md`.

---

## v1.48.0-beta {#v1-48-0-beta}

**2026-10-17** — nftables Data Plane.

- **nftables Data Plane** (M121) — Replace iptables linear chain walk with nftables hash set lookups (O(1) per connection). `NftablesDataPlane` implements `DataPlaneProvider` using `google/nftables` pure-Go library. Verdict map dispatches VIP:proto:port to per-service chains, `numgen` round-robin for backend selection, `jhash` for source-IP sticky with kernel-enforced timeouts. PREROUTING + OUTPUT hooks (ingress + host-originated), POSTROUTING masquerade. Atomic nftables transactions (all-or-nothing updates). Add `Protocol` field to `ServiceVIPConfig`. `--dataplane auto\

---

## v1.47.0-beta {#v1-47-0-beta}

**2026-10-17** — DSL Formatter & Linter.

- **DSL Formatter & Linter** (M120) — `cca fmt <file>` parses `.cca` files and re-emits them in canonical format (consistent indentation, block ordering, spacing). `cca lint <file>` validates syntax and semantics without writing to the store (catches errors early, usable in CI). AST→text emitter with configurable indent width, idempotent formatting (fmt∘fmt = fmt), lint checks: undefined service references in routes/groups/network rules, duplicate names, missing required fields, port range validation.

---

## v1.46.0-beta {#v1-46-0-beta}

**2026-10-17** — Service Tags & Cross-Service Queries.

- **Service Tags & Cross-Service Queries** (M119) — `tags` block inside service declarations (key=value pairs stored as `desired/service/{name}/tag/{key}` facts), `cca get services --tag team=payments` filters by tag, `cca logs --tag team=payments` aggregates logs across all matching services, `cca top workloads --tag tier=frontend` scoped resource view. Tags are user metadata for grouping and filtering — not used for scheduling (that's `placement`) or identity (that's `owner`/tenant).

---

## v1.45.0-beta {#v1-45-0-beta}

**2026-10-17** — Route Block & Sticky Sessions.

- **Route Block & Sticky Sessions** (M118) — Top-level `route` DSL block (host matching, service+port target, sticky cookie with name/secure/http-only/same-site), parser→compiler→`route()` facts, endpoint controller resolves route→backend mapping, agent data plane programs iptables/eBPF DNAT with cookie-based session affinity (Set-Cookie on first request, hash cookie→backend on subsequent), cookie name defaults to `__CCA_STICKY_{service}`, stale mapping cleanup on backend failure, source-IP affinity as alternative strategy. Inspired by Traefik IngressRoute / nginx upstream sticky — CCattler equivalent is the `route` block.

---

## v1.44.0-beta {#v1-44-0-beta}

**2026-10-16** — E2E Streamline & CI Fixes.

- **E2E Streamline & CI Fixes** (M117) — Default e2e workload from `airflow,zabbix` to `airflow`-only across `cluster_test.sh`, `e2e.yml`, `deploy-testbed.yml`, and `upgrade-test.yml` — reduces e2e time by skipping 3 extra image pulls and container starts. Fix flaky `TestAgentReconcileDesiredInstanceStartsMissing` (bare assertion after `waitFor` → move into predicate). Fix release workflow: non-overlapping asset globs (`cca-v*-*` vs `cca-cloud-v*-*`) prevent duplicate delete/upload cycles causing GitHub API "Not Found" errors.

---

## v1.43.0-beta {#v1-43-0-beta}

**2026-10-16** — Join Token TLS Chain Fix.

- **Join Token TLS Chain Fix** (M116) — Fix `cca join` CA fingerprint verification failure: server TLS handshake now includes CA certificate in the presented chain (append to `tls.Certificate.Certificate` slice) so fingerprint-based join tokens work. `setup.sh` writes `/etc/ccattler/endpoints` with `https://127.0.0.1:2379` so CLI commands auto-detect etcd TLS instead of falling back to plaintext. Change `cca-agent` systemd unit from `Requires=containerd.service` to `Wants=containerd.service` so the agent starts without containerd installed.

---

## v1.42.0-beta {#v1-42-0-beta}

**2026-10-16** — Code Quality & RHEL Compat.

- **Code Quality & RHEL Compat** (M115) — Enforce 80-line function limit: refactor 8 functions (`executeTokenCommand`, `executeInitCommand`, `runControllerLoop`, `attemptSingleReconciliation`, `failure.Reconcile`, `cloud_routes.Reconcile`, `handleApply`, `handleWatch`) into smaller helpers. Add exemption comments for 2 flat declarative functions (`buildSetupScript`, `readinessCoreAreaDefinitions`). Fix silent CA loading failure in `cca token create` — warn when CA unavailable for join token fingerprinting. Fix RHEL etcd startup: override ExecStart in systemd drop-in to prevent etcd 3.5+ flag/env variable conflict. Remove redundant etcd dir creation from e2e cleanup.

---

## v1.41.0-beta {#v1-41-0-beta}

**2026-10-16** — Deploy Enrollment & E2E Cleanup.

- **Deploy Enrollment & E2E Cleanup** (M114) — Fix `cca token create` to embed CA fingerprint so `cca join` can verify server identity without pre-distributed `--ca-cert` (loads CA from `/etc/ccattler/pki/`). Fix etcd data dir not recreated after cleanup (setup.sh creates `/var/lib/etcd` with `etcd:etcd` 0700). Remove 200+ lines of manual e2e provisioning shims (cert scp, inline service files, endpoint writes) — both paths now delegate to Ansible for `cca init` + `cca join`. Merge deploy-env and e2e cleanup into unified ALREADY_PROVISIONED block.

---

## v1.40.0-beta {#v1-40-0-beta}

**2026-10-16** — Cloud Provider Build-Tag Split.

- **Cloud Provider Build-Tag Split** (M113) — Gate AWS/GCP SDK imports behind `//go:build cloudprovider` tag — default binary drops from 62MB to ~25MB. Factory-registration pattern (`cloud/registry.go`, `security/kms_registry.go`) with `init()` in tagged files. Extract `AWSKMSKeyProvider`/`GCPKMSKeyProvider` into `security/kms_cloud.go`, `AWSSTSAdapter` into `security/cloud_credentials_aws.go`. Refactor `createCloudProvider` and `CreateAgentKeyProvider` to registry lookup. CI builds and tests both slim and cloud-enabled binaries. Release includes `cca` (slim) and `cca-cloud` (full) artifacts.

---

## v1.39.0-beta {#v1-39-0-beta}

**2026-10-16** — Node Bootstrap & Install Flow.

- **Node Bootstrap & Install Flow** (M112) — `cca init` command generates PKI + setup.sh for one-command node bootstrapping (CA, node cert, secret master key, etcd TLS drop-in, systemd units), `cca server` pre-flight checks (cert existence, key permissions 0600, etcd TCP connectivity) with actionable error messages, CLI auto-detects TLS via `/etc/ccattler/ca.pem`, remove plaintext localhost admin listener (port 9771) and `.ccattler/` fallback paths, etcd fail-fast connectivity check, Ansible migration to `cca init` (remove certificates/etcd roles, etcd from distro packages), demo doc updated to `cca init` flow

---

## v1.38.0-beta {#v1-38-0-beta}

**2026-10-16** — Node Bootstrap & Install Flow.

- **Node Bootstrap & Install Flow** (M112) — `cca init` command generates PKI + setup.sh for one-command node bootstrapping (CA, node cert, secret master key, etcd TLS drop-in, systemd units), `cca server` pre-flight checks (cert existence, key permissions 0600, etcd TCP connectivity) with actionable error messages, CLI auto-detects TLS via `/etc/ccattler/ca.pem`, remove plaintext localhost admin listener (port 9771) and `.ccattler/` fallback paths, etcd fail-fast connectivity check, Ansible migration to `cca init` (remove certificates/etcd roles, etcd from distro packages), demo doc updated to `cca init` flow

---

## v1.37.0-beta {#v1-37-0-beta}

**2026-10-15** — VIP Hairpin Fix & Install Hardening.

- **VIP Hairpin Fix & Install Hardening** (M111) — Fix cross-node VIP DNAT (per-port hostport key resolution), fix local VIP hairpin NAT (agent publishes container bridge IP from nerdctl inspect, dataplane uses container IP for local backends), POSIX sh install scripts (dash/bash/zsh), install script CI on Debian/RHEL/macOS, E2E iptables debug dumps + cross-node VIP/DNS checks, test speedup (build tags for loadtest/chaos, t.Parallel for integration, ~8min→~30s)

---

## v1.36.0-beta {#v1-36-0-beta}

**2026-10-15** — Security Hardening (All Open Threats).

- **Security Hardening (All Open Threats)** (M110) — Close all open threat-model items: (1) T1: etcd mTLS deployment — Ansible/Lima templates with `--client-cert-auth`, `https://` endpoints, etcd TLS flags on cca-server/agent; (2) T4: `--ca-cert` required for `cca join`, `--insecure` escape hatch with warnings, audit log enrollment source IP; (3) T8: CSR-based enrollment — node generates keypair locally, sends CSR, server signs and returns cert only, private key never transits network; (4) T7: store enrollment token hashes not plaintext; (5) T3: node cert permissions 0644→0600, redact sensitive config patterns in describe output; (6) E2E test etcdctl calls updated for TLS. Backward-compatible: legacy enrollment still works during rolling upgrade.

---

## v1.35.0-beta {#v1-35-0-beta}

**2026-10-15** — Node Principal Tenant Visibility Fix.

- **Node Principal Tenant Visibility Fix** (M109) — Fix `IsPlatformPrincipal` to recognize `PrincipalKindNode` as infrastructure — node principals (mTLS-authenticated agents/server) were treated as tenantless users by `ScopedScan`, returning empty results from `/api/state`. Latent since M55, exposed when M106 switched E2E tests from etcdctl to API. 4 unit tests, 1 integration test.

---

## v1.34.0-beta {#v1-34-0-beta}

**2026-10-15** — External Review Fixes.

- **External Review Fixes** (M108) — Fix 3 findings from external code review: (1) P1: multi-port service consistency — container runtime, agent, endpoint controller, and cloud LB all assume single port per service; (2) P2: connection URL credential escaping — `buildConnectionURL` uses `net/url` + `url.UserPassword` instead of raw string concat; (3) P2: init container network error swallowing — `ExecInit` returns network setup errors instead of silently skipping

---

## v1.33.0-beta {#v1-33-0-beta}

**2026-10-14** — Connection Block Auto-Grant.

- **Connection Block Auto-Grant** (M104) — Compiler auto-generates `desired/service/{name}/secret/{secret}` grant facts when a service references a connection that uses a secret, eliminating the need for redundant explicit `secret` declarations. Explicit secret mounts take precedence over auto-grants. Low-level connection URL assembly flow documented in `design/runtime-invariant-flows.md`. 3 new compiler tests.

---

## v1.32.0-beta {#v1-32-0-beta}

**2026-10-14** — Airflow Connection Block Fix.

- **Airflow Connection Block Fix** (M103) — Replace `bash -c 'echo ...'` `_CMD` env vars with CCattler connection blocks + `cat /run/connections/...`. Fixes trailing newline in DB URL (`database "airflow\n"`) that prevented Airflow webserver from connecting to PostgreSQL. Both E2E and examples updated.

---

## v1.31.0-beta {#v1-31-0-beta}

**2026-10-14** — Cross-Node VIP Routing Fix.

- **Cross-Node VIP Routing Fix** (M102) — Agent re-publishes host port facts on every steady-state cycle (not just initial start), fixing endpoint controller missing `hostport` for cross-node address resolution. Remote backend resolution no longer falls back to non-routable container-bridge IPs — skips unreachable backends instead of creating broken DNAT rules. E2E validates iptables `CCA_SERVICES` chain has DNAT rules after deployment. Init container `--entrypoint bash` bypass, rsync+build-on-testbed workflow, host-side image caching, `GOTOOLCHAIN=auto`, per-environment `VAGRANT_DOTFILE_PATH`.

---

## v1.30.0-beta {#v1-30-0-beta}

**2026-10-13** — Bearer Auth Chain Fix.

- **Bearer Auth Chain Fix** (M101) — `BearerTokenAuthenticator` now skips non-JWT tokens (hex local-tokens) instead of rejecting them, preventing authenticator chain short-circuit that blocked local-token auth when InternalJWT authenticator was active. Remove redundant secret master key SCP from E2E deploy script — `cca join` enrollment already distributes it.

---

## v1.29.0-beta {#v1-29-0-beta}

**2026-10-13** — User Authentication & `cca login`.

- **User Authentication & `cca login`** (M100) — `UserTokenIssuer` mints ES256 JWTs for human users (wraps WorkloadTokenIssuer signing key), `OIDCDiscoveryClient` fetches and caches external IdP JWKS for external OIDC auth, server authenticator chain: mTLS → ExternalOIDC (--oidc-issuer) → InternalJWT → LocalToken, new server flags (--oidc-issuer, --oidc-client-id, --oidc-claim-team), user management API (POST/GET/DELETE /api/user, POST /api/user/token, GET /api/auth/whoami, GET /api/auth/oidc-config), admin CLI (cca user create/token/list/delete), login CLI (cca login/logout/whoami), credential loading priority ($CCA_TOKEN → ~/.ccattler/credentials.json → local-token), 8-hour default TTL

---

## v1.28.0-beta {#v1-28-0-beta}

**2026-10-13** — JSON API Error Responses.

- **JSON API Error Responses** (M99) — Replace all `http.Error()` (plain text) with `writeJSONError()` across API server (49), agent debug API (15), agent secret API (8), rate limiter (1), server auth (2) — every error response returns `Content-Type: application/json` with `{"error":"message"}`. CLI `checkAPIResponseStatus()` helper validates HTTP status before `json.Decode` at all 10 call sites, preventing `invalid character` decode crashes on auth/rate-limit errors.

---

## v1.27.0-beta {#v1-27-0-beta}

**2026-10-13** — Connection Block & Credential Probe.

- **Connection Block & Credential Probe** (M98) — Top-level `connection` DSL block (driver, user, secret, host, port, database) compiles to JSON facts, service-level `connection` reference mounts assembled URL as secret file, credential probe init step TCP-dials named connection before container start (catches password mismatches vs stale persistent volumes), agent `materializeConnectionFiles` + `assembleConnectionURL` + `buildConnectionURL`, `parseConnectionJSON` for fact-to-struct, Airflow `_CMD` fix (wrap in `bash -c` for `shlex.split` compatibility), design doc, demo recording guide, 13 new parser/compiler tests

---

## v1.26.0-beta {#v1-26-0-beta}

**2026-10-12** — dnsmasq Domain-Scoped Forwarding.

- **dnsmasq Domain-Scoped Forwarding** (M97) — Fix dnsmasq config to use domain-scoped forwarding (`server=/cca.local/...`) instead of unscoped `server=...#15353` — prevents CCattler DNS from racing the gateway on external queries like Docker Hub, fixing image pull DNS failures on deploy. Kill nerdctl process group on timeout via `Setpgid` + `Kill(-pid, SIGKILL)` + `WaitDelay` to fix agent livelock from stuck containerd I/O. Disable IPv6 via sysctl instead of gai.conf (Go DNS resolver ignores gai.conf).

---

## v1.22.0-beta {#v1-22-0-beta}

**2026-10-12** — K8s Comparison & Deploy Fixes.

- **K8s Comparison & Deploy Fixes** (M95) — Kubernetes production tooling comparison page (K9s, Argo CD, KEDA, Karpenter, Istio, Secrets Store CSI, Kyverno, Prometheus, Jaeger mapped to CCattler equivalents), Caddy deploy-only (skip test env), IPv4 preference for image pulls, secret master key path fix, deploy workflow workload selector

---

## v1.21.0-beta {#v1-21-0-beta}

**2026-10-12** — E2E Workload Selection & Caddy Proxy.

- **E2E Workload Selection & Caddy Proxy** (M94) — Manifest-driven workload deployment (`.workload` files), `--workload` flag for `cluster_test.sh` (comma-separated or `all`), `--list-workloads`, `--clear-workloads` for agent-reconciled cleanup, Caddy reverse proxy auto-config on testbed host (192.168.100.43), GitHub workflow dispatch with workload/binary selector

---

## v1.20.0-beta {#v1-20-0-beta}

**2026-10-12** — Rolling Updates & Rollback Docs.

- **Rolling Updates & Rollback Docs** (M93) — Rolling updates guide (`website/guides/rolling-updates.md`), rollback documentation (automatic threshold-3 + manual re-apply), disruption budget interaction, `rolling-update.cca` example, website examples/guides index updated

---

## v1.19.0 {#v1-19-0}

**2026-10-11** — Localhost admin listener, secret bind-mounts, local token auth.

- **Localhost admin listener** (M91) — When the server binds to an external IP with TLS, a plain-HTTP admin listener on `127.0.0.1:<port>` allows local CLI commands (`cca secret`, `cca status`) without mTLS certificates
- **Secret bind-mounts** (M91) — Secrets materialized by the agent are now bind-mounted read-only (`-v`) into containers; previously they were written to the host but invisible inside containers, breaking `POSTGRES_PASSWORD_FILE` and similar patterns
- **Local token authentication** (M91) — File-based bearer token at `/etc/ccattler/local-token` for system-wide CLI access in TLS mode; replaces the old header-based authenticator
- **Secret master key distribution** (M91) — E2E test distributes the encryption key from the controller to worker nodes for multi-node secret decryption
- **Networking docs & eBPF data plane plan** (M91) — Document networking architecture with honest trade-offs (iptables linear scaling, comparison table vs kube-proxy/IPVS/Cilium), plan eBPF `DataPlaneProvider` — BPF hash map VIP→backend lookups (O(1)), XDP/TC attachment, `cilium/ebpf` Go loader, kernel 5.10+ requirement

---

## v1.18.0-beta {#v1-18-0-beta}

**2026-10-11** — Deploy Testbed & Doc Cleanup.

- **Deploy Testbed & Doc Cleanup** (M90) — Replace Java with Airflow across all deploy targets (E2E, Ansible, Lima), rename `.ccattler` → `.cca` in deploy/test files, website docs updated to `.cca` extension, release bookkeeping script (`scripts/release.sh`), e2e Airflow workload

---


## v1.17.0-beta {#v1-17-0-beta}

**2026-10-11** — Template conventions, command directive, Airflow deployment.

- **Template conventions** (M89) — `.ccv` extension for values files, `<name>/values.ccv` auto-loaded as defaults for `<name>.cca` templates, directory convention with same-name subdirectory for environment values, precedence chain: auto-loaded < `--values` < `--set` < `--set-from-env`
- **`command` directive** (M88) — container CMD override as shell string, always passed through `sh -c`, full pipeline from parser → compiler → fact store → agent → container runtime
- **Airflow deployment** (M88) — CeleryExecutor stack example (postgres, redis, webserver, scheduler, worker) with health checks, init steps, secrets, autoscaling, and HTTPS on port 443 with self-signed TLS (`airflow.home.arpa`)
- **Fair allocation** (M87) — arithmetic base+remainder distribution in InstanceController eliminates alphabetical starvation across nodes
- **Event projector gap markers** (M87) — compaction and watch overflow emit resync markers instead of silent data loss
- **DeltaController** (M87) — watch-based controller interface with delta buffer coalescing and overflow fallback to full reconciliation

### What's new since v1.14.1

| Version | Phase | Summary |
|---|---|---|
| v1.17.0 | 89 | Template conventions: `.ccv` values, auto-loading defaults, directory convention |
| v1.16.0 | 87–88 | Command directive, Airflow deployment, fair allocation, DeltaController |
| v1.15.0 | 80–86 | MemoryStore rewrite, per-node index, etcd cleanup, cloud deletion, lint v2 |

---

## v1.14.1-beta {#v1-14-1-beta}

**2026-10-09** — Phases 72–78 complete. Five correctness review passes, durable capacity state machine.

- **Durable capacity request state machine** (M78) — autoscaler uses post-commit pattern with explicit `pending → launching → ready | failed` lifecycle; scheduler emits structured unplaced-demand facts distinguishing `insufficient_capacity` from `unsatisfiable_constraint`
- **Correctness V** (M77) — storage operation retry after provider failure, duplicate pending-operation key collision fix, event projector watch gap on reconnect, cloud controllers converted to post-commit
- **Correctness IV** (M76) — CAS liveness race fix, storage pre-commit side effects eliminated, heartbeat guard, projector reconnect resilience
- **Correctness III** (M75) — VIP deletion atomicity, agent node resurrection race, oversized group hard error, independent heartbeat goroutine
- **Atomic change groups** (M74) — budget truncation respects multi-key atomic operations, stale derived marker poisoning fixed for stateful ordinals
- **Derived failure markers** (M73) — failure-path writes moved to `derived/instance/*`, CAS decoupling eliminates controller/agent shared-key contention (root cause of 50+ node livelock)
- **Correctness II** (M72) — failure controller recovery cap, node failure controller single budget, HA fencing, agent resilience
- **Race condition fix** — `SimulatorRuntime.InjectStartFailure` for concurrent-safe test failure injection

### What's new since v1.8.3

| Version | Phase | Summary |
|---|---|---|
| v1.14.0 | 78 | Durable capacity request state machine, scheduler unplaced-demand signaling |
| v1.13.0 | 77 | Correctness V: storage retry, operation isolation, watch continuity |
| v1.12.0 | 76 | Correctness IV: atomic liveness, storage idempotency, projector resilience |
| v1.11.0 | 75 | Correctness III: VIP atomicity, agent resurrection, heartbeat decoupling |
| v1.10.1 | 74 | Atomic change groups, stateful marker lifecycle, API consistency |
| v1.10.0 | 73 | Derived failure markers, CAS decoupling, node recovery |
| v1.9.9 | 72 | Budget alignment, HA fencing, agent resilience |
| v1.9.7 | 71 | Post-review correctness fixes |
| v1.9.1 | 69 | Node runtime inspection: agent debug API, `cca node-inspect`, `cca exec` |
| v1.9.0 | 68–69 | Scheduler scale (NodeCapacityCache min-heap), agent debug API |

---

## v1.8.3-beta {#v1-8-3-beta}

**2026-10-04** — Phase 67 complete plus CI hardening.

- **Production readiness validation** (M67) — controller failure isolation, HA resilience, chaos recovery metrics, runtime evidence, observability audit trail
- **`cca readiness` CLI** — pre-flight cluster health check
- **DSL enhancements** — triple-quote strings, `from` file inclusion, `equals` syntax
- **CI hardening** — PR security checks, license headers, CodeQL alerts resolved, E2E Vagrant parallel race fix
- **Bugfixes** — container memory double-conversion, lease timeout, agent TLS loading regression

---

## v1.6.0-beta {#v1-6-0-beta}

**2026-10-03** — Phase 67c: chaos benchmark with recovery metrics.

- **Chaos benchmark CLI** — `cca benchmark --json` for structured recovery reporting with P50/P95/P99 convergence times
- **RecoveryReport** — structured metrics (mean/P50/P95/P99/max convergence, recovery success rate) at 100/1K/5K workload scales
- **Node recovery scenario** — new chaos injection type: restart previously killed nodes
- **Autoscaler oscillation test** — validates stabilization windows prevent rapid scale-up/down thrashing

---

## v1.5.0-beta {#v1-5-0-beta}

**2026-10-03** — Phase 67b: HA and control plane resilience tests.

- **9 resilience tests** — etcd unavailable (workloads survive), control plane restart (no data loss), leader election fencing, disaster recovery from snapshot, split-brain healing, endpoint staleness during partition, volume disappearance recovery, slow store latency, rolling control plane upgrade
- **slowStore wrapper** — configurable per-operation latency injection for store operations

---

## v1.4.0-beta {#v1-4-0-beta}

**2026-10-03** — Phase 67a: controller failure isolation tests.

- **5 isolation tests** — scheduler crash, network controller crash, instance controller restart, all controllers restart, concurrent controller recovery
- **Per-controller lifecycle** — independent kill/restart of individual controller groups via separate Runner instances

---

## v1.3.0-beta {#v1-3-0-beta}

**2026-10-03** — Phase 66: stateful workloads.

- **Stateful workloads** — ordinal instance IDs, ordered startup/teardown, per-ordinal persistent volumes, stable DNS names per ordinal
- **Disruption budgets** — `max_unavailable` enforcement during voluntary operations (drain, rolling update)
- **Security audit remediation** — 10 findings from external audit addressed: local token auth, default listen localhost, request body limits, SSE connection caps, secret capability split, cross-tenant tests, GitHub Actions SHA pinning
- **Node drain and disable/enable** — `cca drain` and `cca disable-node`/`enable-node` commands

### What's new since v1.2.0

| Milestone | Phase | Summary |
|---|---|---|
| M64 | 64 | Node drain and disable/enable |
| M65 | 65 | Disruption budgets + security audit remediation |
| M66 | 66 | Stateful workloads: ordinal IDs, ordered startup, per-ordinal volumes |

---

## v1.2.0-beta {#v1-2-0-beta}

**2026-10-02** — Phases 58–63 complete. Full authorization stack, cloud SDK integration, and production hardening.

- **ABAC condition engine** — attribute-based access control with `when` conditions
- **Multi-tenant visibility** — tenant-scoped filtering across all API endpoints
- **Network policy enforcement** — identity-based allow/deny with iptables rule generation
- **Service groups** — co-scheduled process groups sharing network and volumes
- **Vertical autoscaling** — P95 sliding window with asymmetric stabilization
- **Real cloud SDKs** — AWS EC2/ELB/VPC and GCP Compute/LB/Routes integration
- **Secret encryption** — envelope encryption with KMS (AWS KMS, GCP KMS)
- **3-VM E2E tests** — Vagrant/libvirt cluster tests with real container workloads

| Milestone | Phase | Summary |
|---|---|---|
| M56 | 58 | ABAC condition engine |
| M57 | 59 | Multi-tenant visibility filtering |
| M58 | 60 | Network policy enforcement |
| M59 | 61 | Service groups with co-scheduling |
| M60 | 62 | Vertical autoscaling controller |
| M61 | 63 | Cloud SDK wiring (AWS, GCP) + secret encryption |
| M62 | 64 | Production hardening: E2E tests, install scripts |
| M63 | 65 | Placement policy and tenant isolation tests |

---

## v1.1.0-beta {#v1-1-0-beta}

**2026-09-28** — Phases 48–57 complete. Authorization architecture, DSL templating, anti-pattern remediation.

- **Anti-pattern remediation** — dead code removal, magic number extraction, function length enforcement, typed enums
- **Authorization wiring** — capability-based permissions, per-controller least privilege, store prefix RBAC
- **Authentication** — local token auth, mTLS cert auth, OIDC infrastructure
- **DSL templating** — `.ccv` values files with auto-loading, `--set`, `--set-from-env`, `cca render` command
- **CLI acceptance tests** — end-to-end CLI command validation

| Milestone | Phase | Summary |
|---|---|---|
| M48 | 50a | Split god-object main.go into per-command files |
| M49 | 49b | CI fix: skip container tests in short mode |
| M52 | 50b–54 | Anti-pattern remediation, authorization, authentication |
| M55 | 55–57 | Auth DSL, templating engine, CLI acceptance tests |

---

## v1.0.0-beta {#v1-0-0-beta}

**2026-09-25** — Gate F complete. All correctness, security, and performance gates resolved.

### Highlights

- **Synthetic cluster load test** — 5-phase test exercising 50 nodes and 1,000-1,500 workloads: deploy convergence, placement verification, node failure recovery, scale-up, and store verification
- **Controller runner hardening** — exponential backoff with jitter for optimistic concurrency retries, configurable input-key guard limit to reduce transaction conflicts under high contention
- **Performance validated at scale** — 1,000 instances converge in ~24s, node failure recovery in ~90s, scale-up to 1,500 in ~2m40s

### What's new since v0.9.1

| Milestone | Phase | Summary |
|---|---|---|
| M44 | 47 | Synthetic cluster load test (50 nodes, 1K-1.5K workloads) |

---

## v0.9.1

**2026-09-24** — Controller algorithm complexity audit.

- Audited all 17 controllers + runner for O(n^2) or worse patterns
- Removed dead O(n^2) code in InstanceController
- Replaced 11 full fact-store scans with `FactsWithPrefix` binary search across 6 controllers + SDK
- Merged redundant prefix scans in credential broker and cloud controllers
- Runner early-exit on input-key guard loop when etcd 128-op transaction cap reached

---

## v0.9.0

**2026-09-22** — MCP server, container runtime default, auto-logging hook.

- Container runtime as default mode
- MCP server for remote host management with auth, read-only mode, and audit log
- Auto-logging hook for command history

---

## v0.8.0

**2026-09-21** — Store correctness, truth & reconciliation, agent decomposition, security audit.

| Milestone | Phase | Summary |
|---|---|---|
| M39 | 42 | Watch loss-tolerance, compaction resync, 12 conformance tests |
| M40 | 43 | Deterministic plans, write domain enforcement, key ownership matrix |
| M41 | 44 | Agent sub-component extraction, runtime conformance suite, failure matrix tests |
| M42 | 45 | Formal threat model (10 categories), command/secret/etcd audits with fixes |

---

## v0.7.0 — v0.7.1

**2026-09-20** — System design patterns and algorithm improvements.

- Circuit breaker, rate limiter, least-connections LB, bulkhead, tracing, response cache, DLQ, CDC stream
- Min-heap scheduler, binary search extraction, trie prefix scan, topological sort, cycle detection
- CI test workflow, fuzz tests, race detector, benchmarks

---

## v0.6.0 — v0.6.1

**2026-09-19** — Warm-zero scale-to-zero with HTTP activation proxy, project hardening (CI, benchmarks, structured errors).

---

## v0.5.0 — v0.5.5

**2026-09-18** — Correctness passes, cloud controller manager, API horizontal scalability, identity RBAC.

- Transaction snapshot CAS, derived/ prefix, init dual authority
- Cloud provider interface, node lifecycle, load balancers, VPC routes
- Credential broker, agent credential materialization, OIDC infrastructure

---

## v0.4.0

**2026-09-17** — Storage resilience, observability, CLI improvements.

- Storage migration, snapshots, monitoring, resize, replication
- Prometheus /metrics, structured JSON logging, event streaming, Grafana dashboards
- `cca logs`, `cca describe`, `cca diff`, `cca events`, shell completions

---

## v0.3.0

**2026-09-16** — Service networking, placement, multi-host deployment.

- DNS, proxy, VIP data plane with iptables DNAT
- Multi-host demo across physical servers
- Ansible deployment automation with Vagrant test clusters
- Node enrollment (`cca join` with bootstrap tokens)

---

## v0.2.0

**2026-09-15** — Foundation through single-machine runtime.

- Fact store (memory + etcd), domain language parser, reconciliation engine
- Instance, endpoint, failure, scheduler controllers
- Process and container runtime adapters
- mTLS, RBAC + ABAC, per-controller least privilege
- Unified autoscaling engine, multi-tenancy, rolling deployments
