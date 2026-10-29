# CCattler — updated code review

The repository has advanced substantially since the previous review. The latest commit I found is M121, `ffc20cd7` from October 17, 2026, on `master`.

The earlier findings need to be re-evaluated against these changes: the load-balancer code now iterates over multiple port configurations, connection URL construction imports `net/url`, and the container runtime tracks host-port mappings per instance and container port.

I’m checking whether those changes are fully wired through the controllers and whether the latest networking changes preserve existing CNI and firewall rules.

## CI results

Test (unit, race, lint, audit)

Run

E2E cluster test

Run

CodeQL

Run

Trivy security scan

Run

GitHub Pages deployment

Run

Release workflow

Run

All six listed workflows passed on M121. The earlier duplicate changelog heading issue is resolved, and the Pages deployment is green.

## Remaining concerns

1\. Multi-port cloud load-balancer state needs another pass. The controller now collects all external ports, which addresses the previous map-overwrite issue. However, it looks up the applied configuration hash by service name while computing a separate hash for each port. I would verify that the executor stores and retrieves hashes at the same granularity; otherwise one port's hash could be compared against another port's configuration and cause repeated or skipped updates. See [cloud_loadbalancer.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/cloud_loadbalancer.go?utm_source=chatgpt.com#L80-L110).

2\. Per-port host-port publishing is implemented. The agent now publishes distinct facts per container port, and the data-plane resolver checks those facts before falling back to the legacy key. That is the right direction. I would still add an end-to-end regression test that exercises two exposed ports across different nodes, since the existing green E2E workflow does not by itself prove that exact scenario.

3\. URL encoding and init-network handling are fixed. `buildConnectionURL` now uses `net/url`, and `ExecInit` returns an error if network setup fails instead of silently proceeding. Those two earlier findings are no longer outstanding.

## Networking change worth watching

The newest commit, [M121](https://github.com/boyadzhievb/ccattler/commit/4775647668bd36eca7f16595514202c58505485a?utm_source=chatgpt.com), replaces a broad nftables ruleset flush with deletion scoped to CCattler's own table. That is a meaningful safety improvement: a cleanup operation should not remove rules belonging to CNI or the host firewall. The latest E2E run is green.

Verdict: The earlier findings have largely been addressed, and the latest CI is healthy. I would not block the release based on the issues checked here, but I would verify the per-port load-balancer hash lifecycle and add the explicit multi-port, cross-node regression test before considering that area fully covered.