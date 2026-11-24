# Tasks

Milestone M279. Design: [design.md](design.md). Specs: [binary-install](specs/binary-install/spec.md),
[host-setup](specs/host-setup/spec.md).

## 1. Tests first (record each failing before section 2)

- [x] 1.1 install.sh tests in `cmd/cca` (or a `scripts` test package), with fake `sudo`, `install`, `restorecon`
  and `curl` on PATH and a local release file: `TestInstallScriptRestoresSELinuxLabel`,
  `TestInstallScriptWithoutSELinuxTools`, `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcases `missing`,
  `on path`). Record the failures of the first and third.
  Result: tests run a copy of the root install.sh (CCATTLER_VERSION, INSTALL_PATH, checksums.txt, sudo mv) with fake curl/sudo/restorecon. Red on base: RestoresSELinuxLabel (relabels after install, restorecon fails), WarnsWhenSudoPathLacksInstallDir (missing, sudo prompts), added TestInstallScriptCNIPrerequisiteCheck (usr libexec cni, usr lib cni, none: anchor). WithoutSELinuxTools, `on path`, `opt cni bin` pass on base.
- [x] 1.2 setup.sh tests: `TestSetupScriptWritesEtcdUnitWhenNoneExists`, `TestSetupScriptKeepsDistributionEtcdUnit`
  (the drop-in has no `ETCD_DATA_DIR`), `TestSetupScriptFindsEtcdInUsrLocalBin`,
  `TestSetupScriptEtcdMissingHintNamesRealSources` (non-zero exit, no `dnf install etcd`),
  `TestSetupScriptOpensEtcdClientPort` (firewalld and ufw). Where a test runs the script, do it in a temp root with fake
  `systemctl`, `useradd`, `firewall-cmd` and `ufw`. Record the failures.
  Result: red on 84d2dd1 + extracted steps: WritesEtcdUnitWhenNoneExists, FindsEtcdInUsrLocalBin (2 of 3 subcases), EtcdMissingHintNamesRealSources (wording; base already exit 1), OpensEtcdClientPort (both), and the added EtcdUserWithoutUseradd (adduser fallback, neither); KeepsDistributionEtcdUnit passes on base (guard). Runs the steps in a temp root with fake systemctl/useradd/adduser/id/chown/firewall-cmd/ufw.
- [x] 1.3 `TestContainerdRuntimeCNIPluginDirectories`: the plugin directories passed to the CNI library are
  `/opt/cni/bin`, `/usr/libexec/cni`, `/usr/lib/cni`, in that order. Record the failure.
  Result: red: `CNI plugin directories = [/opt/cni/bin], want [/opt/cni/bin /usr/libexec/cni /usr/lib/cni]` (checks go-cni GetConfig().PluginDirs via newContainerdCNINetwork).

## 2. Implementation

- [x] 2.1 D1 and D2 in the root `install.sh` (corrected 2026-11-24, see design.md; `scripts/install.sh` edits reverted), plus its CNI check (D6 directories) and guide anchor (D7): restorecon after install; a `secure_path` warning with the drop-in
  command; the general note when sudo would prompt. 1.1 is green.
  Result: green: all four install tests under /bin/sh (dash) and /bin/bash; restorecon after mv (sudo when mv used), warns only on failure; secure_path warning with drop-in or general note; CNI check over the three D6 dirs; anchor #prepare-a-fresh-host. `shellcheck -s sh install.sh` clean; scripts/install.sh unchanged from base.
- [x] 2.2 D3, D4 and D5 in `cmd/cca/command_init.go`: the etcd lookup order, the full unit and user when no unit
  exists, the drop-in unchanged otherwise, the real-sources message with `exit 1`, and 2379/tcp in firewalld and ufw.
  Functions stay under 80 lines (split the etcd step out if needed). 1.2 is green.
  Result: green: all 1.2 tests; etcd step = setupScriptEtcdLookupStep + setupScriptEtcdUnitStep + drop-in (no ETCD_DATA_DIR/ETCD_NAME); useradd gets `--user-group` so the etcd:etcd chown works; buildSetupScript 178 -> 147 lines (keeps its documented template exemption); new functions under 80.
- [x] 2.3 D6 in `runtime/containerd.go`: `cniPluginDirectories`. 1.3 is green; NETWORK_GRAPH if it names the
  directory.
  Result: green: TestContainerdRuntimeCNIPluginDirectories; NETWORK_GRAPH container-bridge tree names newContainerdCNINetwork and the three directories.
- [x] 2.4 D7 in `website/installation.md`: "Prepare a fresh host" with Debian/Ubuntu and RHEL/AlmaLinux/Rocky blocks
  (containerd from Docker's repo, CNI from AppStream or the tarball, the checksum-verified etcd 3.5 release binary,
  `uname -m`), the sudo variant and the `secure_path` note, an SELinux and firewalld paragraph, and the manual-setup
  anchors and links updated. Check `TestDocCrossCheck*` and the website build.
  Result: "Prepare a fresh host" with Debian/Ubuntu and RHEL/AlmaLinux/Rocky blocks (Docker repo containerd.io, AppStream CNI or tarball, etcd 3.5.36 release with SHA256SUMS + restorecon, ARCH from uname -m), sudo-holder passwordless variant, secure_path note, SELinux/firewalld paragraph, EPEL+CRB fallback; anchors updated in installation.md and getting-started.md; Quick install notes the label and warning. TestDocCrossCheck* and TestShippedUnitFilesPassOnlyKnownFlags pass; vitepress build of a copy succeeded, page has id="prepare-a-fresh-host". Not run on a host (3.2).
- [x] 2.5 milestones.md: the M279 row and Phase 279 section (done with this proposal); check them against the final
  scope.
  Result: row still matches; Phase 279 gets the scope-correction paragraph (root install.sh, CNI check, anchor).

## 3. Checks

- [x] 3.1 gofmt, golangci-lint, shellcheck on `install.sh`, `go test -count=1 ./...`.
  Result: gofmt clean; golangci-lint ./... 0 issues; go vet clean; shellcheck -s sh install.sh clean; go test -count=1 ./... exit 0 (no FAIL); go test -race cmd/cca runtime ok.
- [ ] 3.2 Rocky 9.8 VM (192.168.100.108), reverted to the pre-test snapshot: follow `website/installation.md`
  literally with the published M279 release (order per design.md, user decision 2026-11-24 option c: after 3.4,
  3.5 and the release; a defect found here becomes a follow-up fix). No workaround. Single-node cluster up; the Getting Started service running; VIP answers 200; `ausearch -m AVC` empty;
  journal free of `203/EXEC`. Record the commands and output (check `install-host:rhel-family`).
- [ ] 3.3 Debian docs host (192.168.100.103): the same guide's Debian path on a fresh install; the cluster up and the
  service running. Record it.
- [x] 3.2a Extra check (user request, 2026-11-24): AlmaLinux 10.3 VM (192.168.100.109, fresh, snapshot taken;
  SELinux enforcing, firewalld on, kernel 6.12). The branch guide was followed literally from step 1, with two
  recorded deviations:
  - the quick install ran the branch's `install.sh` (scp'd), because the published one is the unfixed v1.97;
  - `cca` was then replaced by a branch build (d0657df, built like release.yml), because v1.97 lacks the setup.sh
    fixes.

  Results:
  - **Step 1 sudo:** the passwordless and secure_path snippets parse OK and work (`sudo cca version`).
  - **Step 2:** OK.
  - **Step 3:** the Docker repo resolves to rhel/10; containerd.io and nftables install, containerd is active.
    **`dnf install containernetworking-plugins` fails: `Unable to find a match`.** EL10 packages no CNI in BaseOS,
    AppStream, CRB, extras or EPEL 10 (checked with repoquery). The guide's tarball fallback works.
  - **etcd:** the pinned SHA-256 gives `OK`; the binaries are root:root and `bin_t`.
  - **install.sh:** `cca` is uid 0, mode 0755, `bin_t`. The secure_path warning comes with the checked drop-in. The
    prerequisite check finds everything.
  - **setup.sh** (exit 0):
    - "No etcd.service found: creating the etcd user and unit";
    - etcd is healthy over TLS and `enabled`;
    - 2379 is NOT opened, and the NOTE with both commands is printed; firewalld ports are 9770/tcp and 4789/udp.
  - **Cluster:** cca-server and cca-agent active, node `ctrl` alive. The Getting Started example (3 instances) runs
    3/3, the VIP answers 200, `ausearch` shows 0 AVC, the journal has 0 `203/EXEC` and 0 warnings.

  Findings, fixed in 3n:
  - A1: CNI on EL10 has no package;
  - A2: the "No EL9 repository" wording;
  - A3: install.sh's next steps say `sudo cca status` right after the secure_path warning.
- [x] 3n A1–A3:
  - the guide's RHEL CNI lines split per release: EL9 uses the AppStream package, EL10 the tarball into
    `/opt/cni/bin`, and the Verify line matches;
  - the etcd comment says EL9 and EL10;
  - install.sh prints `sudo /usr/local/bin/cca status` in its next steps when it warned about secure_path, with a
    test subcase, red first.
  Result: A1: the RHEL block is split into separate code blocks: EL9 `dnf install -y containernetworking-plugins`
  (/usr/libexec/cni), EL10 the Debian tarball lines into /opt/cni/bin; the fallback sentence removed; Verify says
  `/opt/cni/bin/bridge` on Debian/Ubuntu and EL10, `/usr/libexec/cni/bridge` on EL9. Also the OS row and the
  section intro say "9 and 10" (follows from the EL10 block). A2: "No EL9 or EL10 repository ships it". A3: red on
  73ae6ee: TestInstallScriptWarnsWhenSudoPathLacksInstallDir/missing (next steps lacked `4. sudo <dir>/cca status`;
  the other subcases assert `4. sudo cca status`); fix `sudo_cca`, set to `${INSTALL_PATH}/cca` with the warning;
  green under sh and bash; shellcheck sh and dash clean. Checks: gofmt, golangci-lint ./... 0 issues, go vet,
  go test -count=1 ./... exit 0, -race ok, bash -n on the guide blocks, vitepress build of a copy.
- [ ] 3.4 Independent review: `code-reviewer` (correctness) and a patterns review against `design/Anti-pattern.md`;
  each finding recorded as fixed or rejected.
  Round 1 (2026-11-24, on e330546..4a93ca2):
  - **code-reviewer (Opus high, Codex job review-mwurjj9s-qlcte1): approve.** No correctness bug, no Debian regression
    (the generated setup.sh diffed base vs head). Red on e330546 for every new test except the documented guards;
    green under dash and bash.
  - **security-audit (Opus high): needs work.**

  The main session checked S1, S2, S3 and S7 in the code. S1 is confirmed by the released `install.sh:145-147`
  (`sudo mv` keeps the user's owner, and the unprivileged `chmod +x` only succeeds because the user owns the file)
  and by the Rocky quick install getting past that line.

  | # | Finding | Resolution |
  |---|---|---|
  | S1 | install.sh leaves `/usr/local/bin/cca` owned by the installing user; systemd and `sudo cca` run it as root (local escalation; in every release so far). Codex flagged the chmod; the reviewer rejected it on the same mechanism | **fix** 3m.1 (user decision 2026-11-24, option a: in M279); release notes tell existing users `sudo chown root:root /usr/local/bin/cca` |
  | S2 | setup.sh opens 2379/tcp to every source; every cluster-CA leaf with ClientAuth is an etcd root client, gate certificates included (route_tls.go:162 → ca.go:211) | **fix** 3m.5 (user decision 2026-11-24, option b: `--etcd-client-cidr`, closed by default); the CA design issue → milestone M280 |
  | S3 | An existing unit's `EnvironmentFile` can override the TLS drop-in | **fix** 3m.2 (empty `EnvironmentFile=` in the drop-in) |
  | S4 | The guide's etcd checksum does not stop the install | **fix** 3m.3 (`&&` chain; pinned SHA-256 in the doc) |
  | S5 | The printed secure_path drop-in can add user-writable dirs (also code-reviewer 3) | **fix** 3m.1 (only for an absolute, root-owned, not group/world-writable INSTALL_PATH) |
  | S6 | Sudoers snippets write before `visudo -cf`; a `.` in the user name makes sudo ignore the file | **fix** 3m.3 (check a temp file, then `install -m 0440`; name note) |
  | S7 | Our etcd unit is never enabled (also code-reviewer 1) | **fix** 3m.2 (`systemctl enable etcd` when we wrote the unit) |
  | S8 | Docker GPG fingerprint in the doc; etcd unit hardening | **rejected** (user decision 2026-11-24: not in M279) |
  | R2 | restorecon runs where SELinux is installed but disabled | **fix** 3m.1 (also require `selinuxenabled`) |
  | R4 | macOS without cached sudo prints the Linux secure_path note | **fix** 3m.1 (Linux only) |
  | R5 | Receiver `stepRun` (setup_script_etcd_test.go:71) breaks the naming rule | **fix** 3m.4 |
  | R6 | 2379 hardcoded at command_init.go:299, :319, :378 beside the new constant; CNI dirs listed in Go and install.sh | **fix** 3m.4 (use the constant); the CNI list is kept in both with a cross-reference test (one place per language) |
  | — | Advisory for S1 | **yes** (user decision 2026-11-24): a GitHub Security Advisory published with the M279 release, with the remedy `sudo chown root:root /usr/local/bin/cca` (Workflow follow-up) |

- [x] 3m.1 S1, S5, R2, R4 in `install.sh`: install as root:root 0755 when sudo is used (`sudo install -o root -g root
  -m 0755` or chown+chmod after mv), then relabel when SELinux is enabled; the secure_path advice only for a safe
  INSTALL_PATH, Linux only. Red first: a test that the installed file's owner/mode are root:root 0755 (fake sudo
  records the calls), then green.
  Result: red on 4a93ca2: new TestInstallScriptInstallsRootOwnedBinary (sudo mv, no `sudo install -o 0 -g 0 -m 0755`), RestoresSELinuxLabel/SELinux_disabled, WarnsWhenSudoPathLacksInstallDir/{user-owned,group-writable,world-writable dir, relative path, macOS sudo prompts}. Fix: `sudo install -o 0 -g 0 -m 0755` (numeric ids also work with BSD install on macOS), `install -m 0755` when writable; restorecon only when `selinuxenabled` succeeds, on both paths (subcase "writable dir, no sudo"); secure_path advice only on Linux for an absolute, plain-character, root-owned, not group/world-writable INSTALL_PATH; printed drop-in uses a temp file, visudo -cf, sudo install -m 0440. Green (21 subtests) under /bin/sh and /bin/bash; shellcheck -s sh and -s dash clean.
- [x] 3m.2 S3 and S7 in `cmd/cca/command_init.go`: the empty `EnvironmentFile=` in the drop-in, and `systemctl enable
  etcd` when the unit was written. Red first, then green. S2 (which sources may reach 2379/tcp) waits for a user
  decision; it is recorded in design.md when made.
  Result: red on 4a93ca2: WritesEtcdUnitWhenNoneExists (no `systemctl enable etcd.service`), KeepsDistributionEtcdUnit (no empty `EnvironmentFile=`; also asserts no enable for a distribution unit). Fix: drop-in starts with `EnvironmentFile=`; the unit step runs `systemctl daemon-reload` and `systemctl enable etcd.service` after writing the unit. Green. S2 untouched.
- [x] 3m.3 S4, S6 in `website/installation.md` and the printed snippets: `&&` chains, pinned etcd SHA-256 for amd64
  and arm64 (from the release's SHA256SUMS), sudoers via a checked temp file.
  Result: guide: etcd download, checksum, tar, install (root:root 0755) and restorecon chained with `&&`; SHA-256 pinned per ARCH (amd64 2733753e…, arm64 f1b93e50…) from v3.5.36 SHA256SUMS, source stated, amd64 tarball checked locally `OK`; all three sudoers snippets write a temp file, `visudo -cf`, then `install -m 0440`; dot-in-name sentence. bash -n on every changed block; TestDocCrossCheck*, TestShippedUnitFilesPassOnlyKnownFlags, vitepress build of a copy pass.
- [x] 3m.4 R5, R6: the receiver rename, the etcd port constant everywhere, and a test that the CNI directory list in
  `install.sh` matches `cniPluginDirectories`.
  Result: receiver `setupScriptStepRun`; `setupScriptEtcdClientPort` at the endpoints file, `--endpoints` and etcdctl health lines (no literal 2379 left in command_init.go); TestInstallScriptCNIDirectoriesMatchAgent (runtime) passes and fails on a temporary drift edit (reverted). Checks: gofmt clean, golangci-lint ./... 0 issues, go vet clean, go test -count=1 ./... exit 0, -race cmd/cca runtime ok.
- [x] 3m.5 S2 (user decision 2026-11-24, option b; design.md D5 and the host-setup requirement updated):
  - `cca init --etcd-client-cidr` (repeatable or comma-separated, validated, error before any file is written);
  - setup.sh opens 2379/tcp only from those CIDRs (firewalld rich rules, `ufw allow from`) and otherwise prints the
    commands and names the flag;
  - the guide's worker section and `design/cli-reference.md` updated (`TestDocCrossCheckCLICommandsExist`).

  Tests first, red on 71e24ac: `TestSetupScriptOpensEtcdClientPortOnlyToGivenNetworks`,
  `TestSetupScriptLeavesEtcdClientPortClosedWithoutNetworks`, `TestInitRejectsInvalidEtcdClientCIDR`. Replace the
  old `TestSetupScriptOpensEtcdClientPort` (it asserted the open-to-all rule the decision removed), recorded here
  with that reason.
  Result: plumbing first (field `etcdClientNetworks`, new unused parameter), then red on 71e24ac logic:
  TestSetupScriptOpensEtcdClientPortOnlyToGivenNetworks (firewalld, ufw; three CIDRs incl. IPv6),
  TestSetupScriptLeavesEtcdClientPortClosedWithoutNetworks (firewalld, ufw), TestInitRejectsInvalidEtcdClientCIDR
  (cmd/cca main_test via the binary: 192.0.2.0/33, 192.0.2.1, not-a-network, trailing comma; error names flag and
  value, output dir not created), TestInitParsesEtcdClientCIDRs (repeat + comma, host bits cleared). The old
  TestSetupScriptOpensEtcdClientPort is replaced: it asserted the open-to-all 2379 rule that the S2 decision removed.
  Fix: `parseEtcdClientCIDRs` (net.ParseCIDR per item, error from the flag set before any file is written); one
  firewalld rich rule (ipv4/ipv6) or `ufw allow from` per network; without networks no 2379 rule and a printed note
  with both commands, `<your node network>` and the flag. Guide: step 1 paragraph, worker-section note with the
  firewalld command, SELinux/firewalld paragraph corrected; cli-reference lists the flag. Green; TestDocCrossCheck*,
  vitepress build, gofmt, golangci-lint ./... 0 issues, go vet, go test -count=1 ./... exit 0, -race ok.
  Round 2 (2026-11-24, on 4a93ca2..194c66e):
  - **code-reviewer (Opus high, Codex job review-mwutw74q-kvvebi): request changes.** No correctness bug in setup.sh
    or the CIDR flag. Red on 4a93ca2 for every new test; green 21/21 install and 21/21 setup-step tests under dash and
    bash.
  - **security-audit (Opus high): needs work.** S3, S4, S6 and S7 are closed; no injection path (rules are built from
    canonical `IPNet.String()`).

  The main session reproduced R2-1 (`--etcd-client-cidr 0.0.0.0/0` exits 0 and writes an allow-all ufw and firewalld
  rule) and checked R2-2 in the docs (`installation.md:239`, `getting-started.md:41`).

  | # | Finding | Resolution |
  |---|---|---|
  | R2-1 | `0.0.0.0/0`, `::/0`, `::ffff:0.0.0.0/96` accepted: etcd opened to all (security, reproduced) | **fix** 3o.1; spec scenario added |
  | R2-2 | Build-from-source snippets still `sudo mv cca /usr/local/bin/` (the S1 mechanism, no relabel) | **fix** 3o.2 (`sudo install -o root -g root -m 0755` + `restorecon` when SELinux is enabled); `test/e2e/cluster_test.sh:2196` too |
  | R2-3 | The secure_path drop-in copies sudo's PATH unchecked (`~/.local/bin`, `.`); `stat -L` follows symlinks and parents are not checked (both reviewers, reproduced) | **fix** 3o.3 (spec requirement split) |
  | R2-4 | binary-install spec stale (no S1 requirement, secure_path rule, selinuxenabled) | **fixed** by the main session (design.md note) |
  | R2-5 | The milestones.md M279 row (changelog text) lacks `--etcd-client-cidr` and the S1 remedy | **fix** 3o.4 |
  | R2-6 | Suggestions: the `installedTo` test helper matches any sudo call; install.sh:160 comment wrong about the label; the setup.sh note says "not opened in the host firewall" when no firewall runs; the note always prints `ipv4` | **fix** 3o.5 |
  | R2-7 | Group-writable install dir (old Debian root:staff 2775) leaves a user-owned binary | **document** in the guide (writing there is already near root) |
  | R2-8 | "No EL10 repository ships etcd" not backed by 3.2a | **fixed** by evidence: repoquery on Alma 10.3 found etcd in no enabled repo (BaseOS, AppStream, CRB, extras), and neither in EPEL 10 nor in Docker rhel/10 (main session, 2026-11-24) |
  | Codex | "Host bits silently masked widens access" | **rejected**: the operator writes the prefix; D5 and `TestInitParsesEtcdClientCIDRs` clear host bits on purpose |

- [ ] 3o.1 R2-1: reject a prefix covering every address, checked on the unmapped form (`netip.ParsePrefix` +
  `Unmap`). Add the three values to `TestInitRejectsInvalidEtcdClientCIDR`, red first.
- [ ] 3o.2 R2-2: the guide's and getting-started's build-from-source install line, and `cluster_test.sh:2196`
  (`chown` or `install -o 0 -g 0`).
- [ ] 3o.3 R2-3: the drop-in lists only directories that pass the safety check after resolving symlinks, all parents
  included, plus the install dir. If the install dir fails, there is no drop-in, only the full-path advice. Subcases
  `unsafe sudo path entries`, a symlink into a user-owned dir, and a user-owned parent; red first.
- [ ] 3o.4 R2-5: the M279 row in milestones.md names `--etcd-client-cidr` (etcd's port closed by default) and the
  S1 fix with the remedy `sudo chown root:root /usr/local/bin/cca`.
- [ ] 3o.5 R2-6 and R2-7: the test helper, the comment, the note wording (with and without a running firewall), the
  family in the note; one guide sentence for a group-writable install directory. Run the checks as in 3.1.
- [ ] 3.5 One full Deploy Testbed run (job summary `full run`); record the run ID.

## Workflow follow-up

- GitHub Security Advisory for S1 (user decision 2026-11-24), published with the release: affected versions are every release whose install.sh used `sudo mv`; remedy `sudo chown root:root /usr/local/bin/cca`; fixed in the M279 release.
- Order (design.md, option c): 3.4 and 3.5, then `scripts/release.sh M279 <version>`, the user pushes the tags, then
  3.2 and 3.3 on the published release, then the archive (sync `binary-install` and `host-setup`).
