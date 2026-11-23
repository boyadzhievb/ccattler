# Tasks: The agent on a `cca init` host reaches its local API server (M248)

Implementation goes to `lead-developer` (Opus, effort medium) via `openspec-apply-change`. Group 3 stays in the main
session. The change is small: one flag in the generated agent unit. Read `buildSetupScript` (cmd/cca/command_init.go)
and `configureAgentSecretDelivery` / `resolveAgentAPIServerAddress` (cmd/cca/command_agent.go) first.

## 1. Failing test first

- [x] 1.1 Write `TestSetupScriptAgentUsesLocalAPIServer` in cmd/cca. Build the script with
  `buildSetupScript("192.0.2.10", ...)`, parse it with `extractDaemonInvocations`, and assert two things: the single
  `agent` invocation has `--api-server 192.0.2.10:<defaultAPIListenPort>`, and the `server` invocation's `--listen`
  ends in the same port. Run `go test ./cmd/cca/ -run TestSetupScriptAgentUsesLocalAPIServer` and record the red
  output here (no `--api-server` in the agent unit).
  - Done in cmd/cca/daemon_flags_test.go. The extracted argument lists go through the real `parseAgentCommandArgs` /
    `parseServerCommandArgs`, so the test reads the parsed `apiServerAddress` and `listenAddress`. Red:
    ```
    --- FAIL: TestSetupScriptAgentUsesLocalAPIServer (0.00s)
        daemon_flags_test.go:193: setup.sh agent --api-server = "", want "192.0.2.10:9770"
    FAIL	github.com/boyadzhievb/ccattler/cmd/cca	2.495s
    ```

## 2. Fix and docs

- [x] 2.1 In `buildSetupScript`, add `--api-server <advertiseAddress>:<defaultAPIListenPort>` to the agent unit. In
  the same function, replace the literal `9770` in the server unit's `--listen` and in the firewall lines with
  `defaultAPIListenPort`, so the two units cannot drift apart. Verify: 1.1 is green, and
  `TestShippedUnitFilesPassOnlyKnownFlags` and `TestSetupScript*` still pass.
  - Done: `--api-server` added after `--advertise-address`; the ufw, firewall-cmd and `--listen` literals now use
    `defaultAPIListenPort` (no `9770` left in cmd/cca/command_init.go). Green:
    `go test ./cmd/cca/ -run 'TestSetupScript|TestShippedUnitFilesPassOnlyKnownFlags' -v` →
    PASS TestShippedUnitFilesPassOnlyKnownFlags, TestSetupScriptAgentUsesLocalAPIServer,
    TestSetupScriptNeverReplacesSecretKeys, TestSetupScriptServerUnitSurvivesEtcdRestart; `ok cmd/cca 2.142s`.
- [x] 2.2 Docs: add one line to Flow 27 in `design/runtime-invariant-flows.md` saying where the agent's API address
  comes from (init host: `--api-server` from `setup.sh`; joined worker: `--api-server` or
  `/etc/ccattler/server-address`; Ansible: template). If any `design/graphs/*_GRAPH.md` names the generated unit's
  flags or the line numbers of `buildSetupScript`, update it. Add an upgrade note to the M248 row in milestones.md:
  hosts set up before M248 add `--api-server <advertise-address>:9770` to `/etc/systemd/system/cca-agent.service`
  or re-run `cca init` and `setup.sh`. Verify: `TestDocCrossCheck*` passes.
  - Done: Flow 27 step 5 names the address source per host kind; the M248 row carries the upgrade note. No
    `design/graphs/*_GRAPH.md` names `buildSetupScript`, cmd/cca/command_init.go or the generated unit's flags, so no
    graph changed. `go test ./... -run TestDocCrossCheck -v` → 6 PASS, 0 FAIL.
- [x] 2.3 Run `gofmt -l .` (empty), `golangci-lint run ./...` (0 issues) and `go test ./...` (all ok), and record
  the commands and their results here.
  - 2026-11-20: `gofmt -l .` → empty; `golangci-lint run ./...` → `0 issues.`; `go test ./...` → exit 0, 20
    packages `ok`, no FAIL; also `go vet ./...` → clean and `go test -race ./cmd/cca/` → `ok` (9.8s).

## 2b. Rework from the code review

- [x] 2b.1 (C1) Build the agent's `--api-server` with `net.JoinHostPort(advertiseAddress, defaultAPIListenPort)` so
  an IPv6 advertise address gets brackets; the rest of the script's IPv6 handling is out of scope (design failure
  row). Add an IPv6 case (`fd00::1` → `[fd00::1]:9770`) to `TestSetupScriptAgentUsesLocalAPIServer`, red first.
  - The test is now table-driven (`192.0.2.10`, `fd00::1` subtests). Red before the fix:
    ```
    --- FAIL: TestSetupScriptAgentUsesLocalAPIServer/fd00::1 (0.00s)
        daemon_flags_test.go:203: setup.sh agent --api-server = "fd00::1:9770", want "[fd00::1]:9770"
    ```
    Green after `net.JoinHostPort`: both subtests PASS.
- [x] 2b.2 (C2) Strengthen the test: the server's listen host must be `0.0.0.0` or the advertise address (not only
  the port), and `buildCertificateSANs(initCommandConfig{advertiseAddress, nodeID: "node-1"})` must contain the
  advertise address.
  - Done with `net.SplitHostPort` and `slices.ContainsFunc(..., net.IP.Equal)`, per subtest. The code was already
    right, so there is no red from the code itself. As a mutation check, `--listen 127.0.0.1:%s` was set for a moment
    and then reverted. The test failed with `setup.sh server --listen = "127.0.0.1:9770", want 0.0.0.0 or 192.0.2.10
    on port 9770` (and the same for fd00::1).
- [x] 2b.3 (Q6) Fold `defaultStatusAPIPort` (cmd/cca/helpers.go) into `defaultAPIListenPort`; replace the literal
  9770 in cmd/cca/command_token.go if it is code (help text stays).
  - The duplicate constant is deleted, and `resolveStatusAPIListenAddress` uses `defaultAPIListenPort`. The
    command_token.go line is the runtime output of `cca token create` (the printed join command), not usage text,
    so it now prints `defaultAPIListenPort`. The usage text in main.go is unchanged. The deletion moved
    cmd/cca/helpers.go by 3 lines, so the graph citations of `coreControllers` and `NewAuditLogGCController` were
    set to their current lines: HA_GRAPH and EXTENSIBILITY_GRAPH 286→294, AUDIT_GRAPH 303→312. Those citations were
    already 11–12 lines off at HEAD.
  - Checks: `gofmt -l .` → empty; `golangci-lint run ./...` → `0 issues.`; `go test ./...` → exit 0, 20 packages
    ok; `go test -race ./cmd/cca/` → ok (9.8s).

## 3. Testbed and review (main session)

- [x] 3.1 Independent review: `code-reviewer` (Opus, effort high) on the milestone diff against this change. This is
  not a network-facing or auth change, so there is no `security-audit` (the agent already used this client path on
  every joined worker). Record each finding as fixed or rejected, with one line of reasoning.
  - 2026-11-20, verdict APPROVE (no bugs). The reviewer confirmed that delivery really turns on: the URL host is the
    advertise IP, which is in the node-certificate SANs, and the CN `node:<node-id>` is enrolled by the bootstrap. A
    server that is not up yet is retried (`api_unreachable`, 2–30 s back-off), so delivery is never turned off.

    | # | Finding | Resolution |
    |---|---|---|
    | S1 | The spec said gate-key delivery is enabled, but it only starts with `--proxy` (command_agent.go:270), which the unit does not pass | **fixed** (main session): spec, design Goals and proposal Why now say secret delivery; gate-key delivery uses the same address once `--proxy` is on |
    | S2 | "The certificate covers the advertise address" holds only for an IP | **fixed**: design failure row for a hostname or IPv6 advertise address (pre-existing; etcd URLs already refuse a hostname) |
    | C1 | `--api-server` built with `%s:%s` gives `fd00::1:9770` | **fixed** in 2b.1 (`net.JoinHostPort`) |
    | C2 | The test checked only the listen port suffix and not the certificate SAN | **fixed** in 2b.2 |
    | Q6 | `defaultStatusAPIPort` duplicates `defaultAPIListenPort` | **fixed** in 2b.3; the token join hint also uses the constant |
    | Q3 | The Lima demo writes its own agent unit with no `--api-server`, and its certificate CN `cca-ctrl` does not match `--node-id node-1` | **out of scope**: not the `cca init` path; separate bug, decision pending |
- [x] 3.2 By hand on a fresh host (`install-host:secret-delivery`): follow the install docs with the new binary
  (`cca init`, `setup.sh`), then apply a service that mounts a secret. Record three things: the agent log line
  `secret delivery from <advertise>:9770`, the instance reaching `running`, and the secret file inside the container
  (`cca exec` or `nerdctl exec`). Host: the Debian 13 install-docs host 192.168.100.99 or any fresh VM; it is not
  the deploy cluster, because Ansible overwrites this unit there.
  - 2026-11-23 on 192.168.100.99 (Debian 13), binary built from 1024e46. The host ran v1.93 from an earlier
    `cca init`; `/etc/ccattler`, both units, the binary and `~/cca-init` were backed up first
    (`/etc/ccattler.bak-v193`, `/usr/local/bin/cca.v193`, `~/cca-init.v193`).
  - Steps as in website/installation.md: `cca init --advertise-address 192.168.100.99 --node-id debian -o ~/cca-init`,
    `sudo ~/cca-init/setup.sh`, `sudo systemctl enable --now cca-server cca-agent` (+ restart). The written unit has
    `--api-server 192.168.100.99:9770` (cca-agent.service:11).
  - Agent log: `Agent debian: secret delivery from 192.168.100.99:9770 (material on /run/ccattler/secrets)`.
  - `sudo cca secret set m248.greeting hello-m248`, then `sudo cca apply ~/secret-check.cca` (nginx 1.28,
    `secret m248.greeting "/run/secrets/greeting"`): `secretcheck-c905614e2e state=running`.
  - Inside the container (`ctr -n ccattler tasks exec … cat /run/secrets/greeting`): `hello-m248`; the file is
    `-r--r--r-- root root 10`. (`cca exec` fails there: the setup.sh unit has no `--debug-listen`; nerdctl is not
    installed on this host.)
  - No code change, so the closing run 45132907087 (3.3) still covers the final diff.
- [x] 3.3 Closing full Deploy Testbed run (CLAUDE.md, M231). Its job summary must say `full run`. Record the run
  ID. It shows no regression elsewhere, although it does not run the `setup.sh` agent unit.
  - Run 45132907087 on 1024e46 (2026-11-23): `run_kind=full`, success; skipped check groups: none; node failure,
    scale-down and certificate rotation checks passed; adversarial replay 32 passed, 0 failed, 0 skipped.
  - Recorded before 3.2. If 3.2 leads to a code change, this run no longer covers the final diff and must be repeated.

## Workflow follow-up

- Release with `scripts/release.sh M248 <version>`, then archive this change (syncing `host-setup` into
  `openspec/specs/`).
- Possible follow-up milestone: a CI job that runs the plain install path (`cca init` + `setup.sh` only) on one VM,
  so the generated units are exercised end to end.
