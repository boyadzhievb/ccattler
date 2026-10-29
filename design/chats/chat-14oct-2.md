# CCattler code review — October 14, 2026

Latest master: [`f2d483ea28`](https://github.com/boyadzhievb/ccattler/commit/f2d483ea28ee98a740e71f3dcc5bcffd3a9a63b8) — M104, release bookkeeping for v1.33.0-beta.

The core CI checks are green, but I found three code-level issues worth addressing and one documentation-build failure.

## Findings

1\. Multi-port services are not handled consistently

P1

`extractExternalServicePorts` stores external-port configuration in a map keyed only by service name. If a service declares multiple external ports, each fact overwrites the previous entry, leaving only one port selected for the cloud load balancer. The compiler emits a fact for each declared external port, so this input is accepted rather than rejected.

There is a related cross-node issue: the runtime allocates host ports per container port, but the agent publishes only the first host port for an instance. The endpoint controller then uses that same host port for every exposed port. For example, a service exposing ports 80 and 443 can have cross-node traffic for 443 sent to the host port allocated for 80.

Fix: Either reject multiple external ports explicitly at compile time, if they are unsupported, or make load-balancer and host-port reconciliation port-specific.

Locations: [cloud_loadbalancer.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/cloud_loadbalancer.go#L93-L111) · [compiler.go](https://github.com/boyadzhievb/ccattler/blob/master/lang/compiler.go#L323-L353) · [container.go](https://github.com/boyadzhievb/ccattler/blob/master/runtime/container.go#L294-L324) · [endpoint.go](https://github.com/boyadzhievb/ccattler/blob/master/controllers/endpoint.go#L121-L155)

2\. Connection URLs do not escape credentials or database names

P2

`buildConnectionURL` concatenates the username, password, host and database directly into a URL. Valid passwords containing characters such as `@`, `:`, `/`, `?` or `#` can be interpreted as URL delimiters rather than credential data. The application may receive a malformed connection string and fail to connect even when the secret itself is correct.

Fix: Build the URL using Go’s `net/url` facilities, including `url.UserPassword`, and correctly encode path components. Add tests with reserved characters in passwords and database names.

Location: [agent.go](https://github.com/boyadzhievb/ccattler/blob/master/agent/agent.go#L879-L907)

3\. Init container network setup errors are swallowed

P2

In `ExecInit`, failure from `ensureNetworkExists` simply prevents adding `--network` to the command. The init container is still launched, potentially on the default network. An init command can consequently fail to reach services it needs, or execute without its intended network configuration, while hiding the underlying network setup error.

Fix: Return the network setup error instead of starting the init container without the requested network. Add a test that simulates network setup failure.

Location: [container.go](https://github.com/boyadzhievb/ccattler/blob/master/runtime/container.go#L483-L503)

4\. GitHub Pages build fails on a duplicate changelog anchor

P2

`website/changelog.md` defines the heading ID `v1-33-0-beta` twice, at lines 4 and 12. VitePress rejects the duplicate ID, so the latest Pages deployment fails even though the application test and security workflows pass.

Fix: Remove the duplicate v1.33.0-beta section or correct the release-generation process that produced it.

Locations: [website/changelog.md](https://github.com/boyadzhievb/ccattler/blob/master/website/changelog.md#L4-L16) · [failed Pages run](https://github.com/boyadzhievb/ccattler/actions/runs/38586663947)

## CI status at the latest head

Test workflow

Unit/race tests, lint and audit passed.

Passed

CodeQL and Trivy

Static analysis and security scan passed.

Passed

E2E cluster test and testbed deployment

Both succeeded for the latest master head.

Passed

Deploy to GitHub Pages

VitePress build failed because of the duplicate heading ID.

Failed

References: [Test](https://github.com/boyadzhievb/ccattler/actions/runs/38586664000) · [CodeQL](https://github.com/boyadzhievb/ccattler/actions/runs/38586664059) · [Trivy](https://github.com/boyadzhievb/ccattler/actions/runs/38586663993) · [E2E](https://github.com/boyadzhievb/ccattler/actions/runs/38586663844) · [Testbed deployment](https://github.com/boyadzhievb/ccattler/actions/runs/38587384679)

## Previous findings revisited

The stale cloud-route concern appears to have been addressed: the controller now cancels pending ensure operations for routes that are no longer desired, and the executor checks that the target node is alive before ensuring a route. The earlier instance-controller lint errors are also cleared in the latest Test workflow.

I am not treating event-projector history gaps as a controller bug; as discussed, that remains a documented best-effort-versus-complete-history design decision.

Recommendation: Fix the multi-port behavior and connection URL encoding first, then make init network setup fail explicitly. Remove the duplicate changelog heading to restore the Pages deployment.