# CCattler Project Status

## Current Milestone

M20 — Service Networking & Placement (Phase 23) **COMPLETE**

- All 13/13 items done
- Tagged: v0.20.0, v0.20.1
- Demo: `curl -H "Host: web" http://node:80` round-robins via proxy, DNS resolves VIPs, require/prefer/restrict/accept placement

## Next Milestone

M21 — not yet defined. Candidates:
- Cluster autoscaling (infrastructure provider integration)
- Observability dashboard (Prometheus/Grafana integration)
- CLI improvements (interactive mode, better error messages)
- Multi-cluster federation
- Persistent volume migration on node failure

## Recent Completions

- [2026-09-18] M20 Phase 23 — cross-host endpoints, DNS server, HTTP reverse proxy, placement constraints
- [2026-09-18] v0.20.1 — README and docs updates, VitePress documentation site
- [2026-09-14] M19 Phase 22 — node enrollment via `cca token create` / `cca join`
- [2026-09-14] M18 Phase 21 — VIP data plane with iptables DNAT

## Known Issues

(none currently tracked)

## Metrics

- Source files: 85
- Test files: 48
- Total Go lines: ~40K
- Latest release: v0.20.1
- Milestones complete: M1–M20
