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
  | S2 | setup.sh opens 2379/tcp to every source; every cluster-CA leaf with ClientAuth is an etcd root client, gate certificates included (route_tls.go:162 → ca.go:211) | **fix** 3m.2 (rule limited to cluster sources); the CA design issue → milestone M280 |
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
- [ ] 3.5 One full Deploy Testbed run (job summary `full run`); record the run ID.

## Workflow follow-up

- GitHub Security Advisory for S1 (user decision 2026-11-24), published with the release: affected versions are every release whose install.sh used `sudo mv`; remedy `sudo chown root:root /usr/local/bin/cca`; fixed in the M279 release.
- Order (design.md, option c): 3.4 and 3.5, then `scripts/release.sh M279 <version>`, the user pushes the tags, then
  3.2 and 3.3 on the published release, then the archive (sync `binary-install` and `host-setup`).
