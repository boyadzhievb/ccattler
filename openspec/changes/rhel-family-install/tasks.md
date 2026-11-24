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
  section intro say "9 and 10" (follows from the EL10 block; not asked for; user decision 2026-11-24: keep it). A2: "No EL9 or EL10 repository ships it". A3: red on
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

- [x] 3o.1 R2-1: reject a prefix covering every address, checked on the unmapped form (`netip.ParsePrefix` +
  `Unmap`). Add the three values to `TestInitRejectsInvalidEtcdClientCIDR`, red first.
  Result: red on 6dfb619: TestInitRejectsInvalidEtcdClientCIDR/{0.0.0.0/0, ::/0, ::ffff:0.0.0.0/96} (exit 0, files written). Fix: `prefixCoversEveryAddress` (netip.ParsePrefix; Is4In6 subtracts the 96 mapped bits; bits <= 0 refused) with an error naming the value and "covers every address; name the cluster's networks". Green.
- [x] 3o.2 R2-2: the guide's and getting-started's build-from-source install line, and `cluster_test.sh:2196`
  (`chown` or `install -o 0 -g 0`).
  Result: installation.md and getting-started.md build-from-source: `sudo install -o root -g root -m 0755 cca /usr/local/bin/` plus `restorecon` when `selinuxenabled` succeeds; cluster_test.sh binary swap: `sudo install -o 0 -g 0 -m 0755 /tmp/cca /usr/local/bin/cca && rm -f /tmp/cca` (bash -n ok; shellcheck warnings 6 before and after). No testbed run (test-only).
- [x] 3o.3 R2-3: the drop-in lists only directories that pass the safety check after resolving symlinks, all parents
  included, plus the install dir. If the install dir fails, there is no drop-in, only the full-path advice. Subcases
  `unsafe sudo path entries`, a symlink into a user-owned dir, and a user-owned parent; red first.
  Result: red on 6dfb619: TestInstallScriptWarnsWhenSudoPathLacksInstallDir subcases unsafe sudo path entries, user-owned parent, symlink into a user-owned dir (drop-in printed), and user-owned/group-writable/world-writable dir and relative path (spec now wants the full-path advice there). Fix: `directory_is_safe_for_sudo` (absolute, plain chars, `readlink -f`, every parent to / root-owned and not g/o-writable); `safe_sudo_path_entries` filters sudo's PATH; unsafe install dir: warning and full path, no drop-in; next steps use the absolute path. Fake stat now per path, real readlink. Green 23/23 under sh and bash; shellcheck sh and dash clean.
- [x] 3o.4 R2-5: the M279 row in milestones.md names `--etcd-client-cidr` (etcd's port closed by default) and the
  S1 fix with the remedy `sudo chown root:root /usr/local/bin/cca`.
  Result: M279 row names the S1 fix with `sudo chown root:root /usr/local/bin/cca`, `--etcd-client-cidr` (port closed by default, allow-all refused), selinuxenabled, safe drop-in, enabled etcd unit, EnvironmentFile reset, EL9/10. Not marked BREAKING: the base setup.sh never opened 2379.
- [x] 3o.5 R2-6 and R2-7: the test helper, the comment, the note wording (with and without a running firewall), the
  family in the note; one guide sentence for a group-writable install directory. Run the checks as in 3.1.
  Result: installedTo matches only `sudo install `; install.sh relabel comment corrected; red on 6dfb619 then green: TestSetupScriptLeavesEtcdClientPortClosedWithoutNetworks/{firewalld, ufw, no firewall} (note names the active firewall or says none is active; family placeholder `<ipv4 or ipv6>`); guide sentence on a group-writable install dir (root:staff 2775). Checks: gofmt clean, golangci-lint ./... 0 issues, go vet, go test -count=1 ./... exit 0, -race ok, vitepress build of a copy.
  Round 3 (2026-11-24, on 6dfb619..56068a0):
  - **code-reviewer (Opus high, Codex job review-mwuuhwyz-lsed0n): request changes.** Red on 6dfb619 for the 3o
    tests; green 54/54 under dash and bash.
  - **security-audit (Opus high): needs work.**

  The main session reproduced B1 with the 56068a0 build: `0.0.0.0/00`, `::/000` and `::ffff:0.0.0.0/096` exit 0 and
  write /0 rules; `0.0.0.0/1,128.0.0.0/1` exits 0.

  | # | Finding | Resolution |
  |---|---|---|
  | B1 | Leading-zero prefixes pass the every-address check (`netip` rejects them, so the check returns false; `net.ParseCIDR` accepts them and writes /0). HIGH, both reviewers, reproduced | **fix** 3p.1 (one canonical value, user option ii) |
  | B2 | The drop-in prints the unresolved path; a user-owned symlink to /usr/bin passes and can be repointed (medium, both reviewers, reproduced) | **fix** 3p.2 (structural, user option a: no sudoers text from install.sh at all) |
  | B3 | `0.0.0.0/1,128.0.0.0/1` opens 2379 to all (security, reproduced) | **fix** 3p.1 (union refused) |
  | B4 | The `SELinux disabled` subtest does not assert "prints nothing about SELinux" | **fix** 3p.3 |
  | B5 | `$(cd "$INSTALL_PATH" && pwd)` follows CDPATH and keeps the logical path | **fix** 3p.2 (`CDPATH= cd -P --`, or moot if the path is no longer printed) |
  | — | Runtime warning when the binary is not owned by uid 0 after an install without sudo (security suggestion) | **fix** 3p.2 (one warning line) |
  | Codex | "`<ipv4 or ipv6>` is not runnable"; "`::fffe:0:0/95` covers all IPv4" | **rejected**: the line is a template next to `<your node network>`; that prefix becomes an ipv6 rule, which IPv4 packets never match |

- [x] 3p.1 B1 and B3 (design.md "Structural changes after review round 3"):
  - check the canonical parsed network with one parser (`Mask.Size()` after unmapping);
  - refuse a union covering a family;
  - warn for wider than /8 or /32, or public ranges, naming M280.

  Tests, red on 56068a0: the new subcases of `TestInitRejectsInvalidEtcdClientCIDR`, `TestInitWarnsOnWideEtcdClientCIDR`
  and `TestSetupScriptEtcdRulesNeverCoverEveryAddress` (parse the rules out of the generated setup.sh).
- [x] 3p.2 B2, B5, warning (user option a): `install.sh` drops `safe_sudo_path_entries`,
  `directory_is_safe_for_sudo` and every printed sudoers line.
  - When the install directory is missing from secure_path, it warns, gives the full-path next steps and names the
    guide's `secure_path` step (link).
  - It warns when the installed binary is not owned by uid 0.
  - The `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` subcases assert there is no `Defaults secure_path` and no
    `/etc/sudoers.d` in the output, red first.
  - Remove the dead helpers and keep shellcheck clean.
- [x] 3p.3 B4: the `SELinux disabled` subcase asserts that nothing about SELinux is printed. Run the checks as in 3.1.

  Who did 3p: the lead-developer started it (tests, canonical CIDR form, union check, warnings, the install.sh
  rewrite). It stopped at its usage limit. The main session finished it (user decision 2026-11-24, option b): one
  lint fix, the red and green evidence, the ticks. Round-4 reviewers review it independently.

  Results (2026-11-24):
  - **Red on ca6e9b9.** New test files copied into worktree `scratchpad/m279-red-3p`; the run compiled cleanly. Log:
    `scratchpad/m279-evidence/3p-red.txt`. Failing:
    - TestInitRejectsInvalidEtcdClientCIDR: `0.0.0.0/00`, `::/000`, `::ffff:0.0.0.0/096`, `0.0.0.0/1,128.0.0.0/1`,
      `::/1,8000::/1`, `0.0.0.0/1,128.0.0.0/2,192.0.0.0/2,10.0.0.0/8`;
    - TestInitWarnsOnWideEtcdClientCIDR: `10.0.0.0/7`, `203.0.113.0/24`, `2001::/16`, `0.0.0.0/1`;
    - TestSetupScriptEtcdRulesNeverCoverEveryAddress: 7 subcases, among them the leading-zero forms and the unions;
    - TestInstallScriptWarnsWhenSudoPathLacksInstallDir: `missing`, `unsafe sudo path entries`, `user-owned dir`,
      `user-owned parent`, `symlink into a user-owned dir`, `relative path`;
    - TestInstallScriptWarnsWhenBinaryNotRootOwned: `user-owned` (left out of the first version of this list).

    **Correction, 2026-11-24, after review round 4:** the first version said these install subcases failed with
    "sudoers text printed". That was wrong. On ca6e9b9 none of them printed sudoers text, because the tests' temp
    directory is never "safe". They failed on the missing guide link. The reviewer's mutation (printing
    `Defaults secure_path=x`) shows the new assertion does catch sudoers text.
  - **3p.3:** `SELinux disabled` passes on the base, because the behaviour was already right. A mutation that prints
    "SELinux check skipped" makes it fail ("output mentions SELinux although it is disabled").
  - **Green on the worktree:**
    - `gofmt -l .` empty;
    - `golangci-lint run ./...` 0 issues, after fixing QF1001 at command_init.go:224 (De Morgan);
    - `go vet ./...` clean;
    - `shellcheck -s sh` and `-s dash install.sh` clean;
    - `go test -count=1 ./...` exit 0, 20 packages ok (`scratchpad/m279-evidence/3p-go-test.txt`);
    - `go test -race ./cmd/cca ./runtime` ok.
  - **install.sh:** no `safe_sudo_path_entries`, `directory_is_safe_for_sudo`, `Defaults secure_path` or `sudoers.d`
    is left. The directory lookup is `CDPATH='' cd -P -- … && pwd -P` (B5), and a non-uid-0 owner gives a warning.
  - **Spec:** the union subcases are named by their values (host-setup spec aligned).
  Round 4 (2026-11-24, on ca6e9b9..de03b14; both on Opus high after the user switched models):
  - **code-reviewer (Codex included): request changes, small.** All CIDR checks hold. The checked form equals the
    written form for every probed input. Red reproduced on ca6e9b9 and the SELinux mutation confirmed. Green 66/66
    under dash and bash.
  - **security-audit: adequate, nothing above LOW.** About 30 allow-all attempts, all refused; no injection; no
    sudoers text in any install.sh path.

  The main session checked R4-1, R4-2, R4-3 and R4-6 in the code:
  - `install.sh:189` uses the absolute path unnormalized, where the base used `cd … && pwd`;
  - `install.sh:177-178` print an unquoted path and an incomplete fix;
  - `installation.md:213` still says "prints the fix".

  | # | Finding | Resolution |
  |---|---|---|
  | R4-1 | The uid-0 warning's fix (`chown`) does not remove the risk: the user can still replace the file in their writable directory (both reviewers) | **fix** 3q.1: say the directory must be root-only, as installation.md:216 does |
  | R4-2 | The printed `sudo chown root:root ${INSTALL_PATH}/cca` is unquoted, so a path with a space changes ownership of the wrong file when pasted (security LOW) | **fix** 3q.1 (quote the path in every printed command) |
  | R4-3 | Absolute INSTALL_PATH no longer normalized (`/usr/local/bin/`, `/usr/local//bin`): false warning, `sudo /usr/local/bin//cca` (reproduced by the reviewer) | **fix** 3q.1 (normalize every path with `CDPATH='' cd -P -- … && pwd -P`); test subcases, red first |
  | R4-4 | The 3p evidence claimed "sudoers text printed"; a red subcase was missing | **fixed** by the main session (correction above) |
  | R4-5 | No spec scenario for the uid-0 warning; `init_etcd_client_cidr_cli_test.go:44` no longer checks that the error names the value | **spec fixed** by the main session (scenario added); **fix** 3q.2 (test asserts the value) |
  | R4-6 | `installation.md:213` says install.sh "prints the fix"; it now links to it | **fix** 3q.2 |
  | R4-7 | The warning calls link-local and shared ranges (169.254/16, 100.64/10, fe80::/10) "public" (Codex, confirmed) | **fix** 3q.2: wording "not private or loopback", which matches the spec's definition |
  | — | Only an exact cover of a family is refused; wider sets get warnings | **by design** (design.md "Structural changes after review round 3") |

- [x] 3q.1 R4-1, R4-2, R4-3 in install.sh:
  - the ownership warning names the root-only directory fix and quotes the path;
  - every printed command quotes the path;
  - every INSTALL_PATH is normalized with `CDPATH='' cd -P -- … && pwd -P`.

  Red first: subcases for a trailing slash, a double slash and a path with a space. Keep shellcheck clean under sh and
  dash.
  Result: red on cdaccc9: TestInstallScriptWarnsWhenSudoPathLacksInstallDir new subcases `trailing slash on path`
  (false warning), `double slash` (`//` path), `path with a space` (unquoted), plus the quoted next step in the other
  warning subcases; TestInstallScriptWarnsWhenBinaryNotRootOwned `user-owned` and `user-owned, path with a space`
  (scenario: owner or directory writer, "writable only by root", quoted paths). Fix: INSTALL_PATH normalized once
  after the directory exists (`CDPATH='' cd -P -- … && pwd -P`, error if it fails); `shell_quoted` (single quotes,
  `'` escaped; round-trip checked under dash and bash) for every printed command (restorecon, ownership, secure_path
  call, next steps); the ownership warning gives `sudo chown root:root '<dir>' && sudo chmod 0755 '<dir>'` and "run
  this install again". Test fakes: stat table tab-separated (paths with spaces), `sed` linked; the `restorecon fails`
  subcase now expects the quoted command. Green 29/29 under sh and bash; shellcheck sh and dash clean.
- [x] 3q.2 R4-5 to R4-7:
  - `TestInitRejectsInvalidEtcdClientCIDR` asserts the error names the value;
  - `installation.md:213` wording;
  - the warning says "not private or loopback".

  Then run the checks as in 3.1.
  Result: red on cdaccc9: TestInitRejectsInvalidEtcdClientCIDR union subcases (error printed `[a b]`, not the value);
  TestInitWarnsOnWideEtcdClientCIDR asserts "is not private or loopback" (new `169.254.0.0/16` subcase). Fix: the
  union error names the networks joined by commas; reason text "is not private or loopback"; installation.md "links
  to the fix". Checks: gofmt clean, golangci-lint ./... 0 issues, go vet, go test -count=1 ./... exit 0, -race ok,
  vitepress build of a copy.
- [x] 3q.3 R4-8 (Codex stop-time check, 2026-11-24, confirmed by the main session at install.sh:188, which warns
  only when the binary's uid is not 0): a root-owned binary in a directory that is group- or world-writable, or not
  root-owned (old Debian `/usr/local/bin` root:staff 2775), gets no warning, although group members can replace it
  and run code as root. Same rule as R4-1, so it is in scope.
  - Fix: the Linux ownership check also warns when the install directory (after normalization) is not owned by uid 0
    or is group- or world-writable, with the same root-only advice.
  - Red first: subcases `root-owned binary, group-writable dir` and `root-owned binary, user-owned dir`.
  - The binary-install scenario "Installed without sudo into a directory the user can write" covers the advice; the
    main session extends its GIVEN to any directory others can write.
  Result: red on ae1976c: TestInstallScriptWarnsWhenBinaryNotRootOwned/{root-owned binary, group-writable dir (0 2775);
  root-owned binary, user-owned dir} (no warning). Fix: the Linux ownership check also reads the normalized
  directory's owner and mode (`stat -c '%u %a'`) and warns when it is not uid 0 or has 022 bits, with the same
  root-only advice and quoted paths; the binary check comes first. Green 31/31 under sh and bash; shellcheck sh and
  dash clean; next-step quoting unchanged. Checks: gofmt clean, golangci-lint ./... 0 issues, go vet, go test
  -count=1 ./... exit 0, -race ok.
  Round 5 (2026-11-24, on cdaccc9..25581cc; both on Opus high):
  - **code-reviewer (Codex included): request changes.** R4-1 to R4-8 fixed otherwise. Red reproduced on cdaccc9;
    green 54/54 under dash and 31/31 under bash.
  - **security-audit: adequate, one MEDIUM.** Quoting holds (`'`, spaces, `$`, backticks, inner newlines); every
    unsafe ownership case warns.

  The main session checked R5-1 in the code (`install.sh:206` always prints `sudo chown root:root '<dir>' && sudo
  chmod 0755 '<dir>'`).

  | # | Finding | Resolution |
  |---|---|---|
  | R5-1 | The printed `chmod 0755` advice removes the sticky bit of `/tmp` (1777) and chowns a home directory to root (security MEDIUM, reproduced on GNU chmod) | **fix** 3r.1, structural (user decision A2: no privileged command printed) |
  | R5-2 | Parent directories not checked: a root-owned `~/bin` under a user-owned home can be swapped (security LOW-MEDIUM, code-review suggestion) | **fix** 3r.1 (A2: detection walks every parent) |
  | R5-3 | The secure_path check compares only the resolved path: false warning when secure_path lists the symlink spelling (rpm-ostree `/usr/local` → `/var/usrlocal`); both reviewers and Codex, reproduced | **fix** 3r.2 (compare both spellings); spec scenario extended |
  | R5-4 | Normalization failure prints an empty path ("Cannot enter the install directory .") | **fix** 3r.2; new requirement "install.sh refuses an install path it cannot use" |
  | R5-5 | `INSTALL_PATH=-` (cd to `$OLDPWD`) and paths with a newline (security LOW) | **fix** 3r.2 (refused, same requirement) |
  | R5-6 | Test gaps: no subcase for a path containing `'`, none for a failed normalization | **fix** 3r.3 |
  | R5-7 | `$(shell_quoted "${INSTALL_PATH}/cca")` built in 4 places; the raw-string `installPathForm`; the no-op `installed_owner=${installed_owner%% *}` | **fix** 3r.3 |
  | Codex | "Single-value leading-zero subcases fail the test" | **rejected**: they are refused earlier by `parseEtcdClientCIDRs` (command_init.go:128), whose error quotes the raw value. The union error printing canonical forms is cosmetic |

- [ ] 3r.1 R5-1 and R5-2 (design.md "No privileged commands printed by install.sh"):
  - the ownership check walks the install directory and each parent up to `/` (resolved path; a sticky parent is
    safe only if root owns its child);
  - it warns with the first unsafe directory and the root-only advice plus the guide link;
  - it prints no `chown`, `chmod` or sudoers command.

  Red first: the `TestInstallScriptWarnsWhenBinaryNotRootOwned` subcases from the spec, each asserting no
  `chown`/`chmod`, among them a sticky world-writable dir like `/tmp` and a user-owned parent.
- [ ] 3r.2 R5-3 to R5-5:
  - the secure_path check accepts the given or the resolved spelling (subcase `symlinked dir on path`);
  - `TestInstallScriptRefusesUnusableInstallPath` covers `-`, a newline and a directory that cannot be entered (the
    error names the given path).

  Red first.
- [ ] 3r.3 R5-6 and R5-7: a subcase for a path with `'`; one variable for the quoted binary path; a typed constant
  for `installPathForm`; drop the no-op. Run the checks as in 3.1.
- [ ] 3.5 One full Deploy Testbed run (job summary `full run`); record the run ID.

## Workflow follow-up

- GitHub Security Advisory for S1 (user decision 2026-11-24), published with the release: affected versions are every release whose install.sh used `sudo mv`; remedy `sudo chown root:root /usr/local/bin/cca`; fixed in the M279 release.
- Order (design.md, option c): 3.4 and 3.5, then `scripts/release.sh M279 <version>`, the user pushes the tags, then
  3.2 and 3.3 on the published release, then the archive (sync `binary-install` and `host-setup`).
