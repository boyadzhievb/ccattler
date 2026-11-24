# Tasks

Milestone M279. Design: [design.md](design.md). Specs: [binary-install](specs/binary-install/spec.md),
[host-setup](specs/host-setup/spec.md).

## 1. Tests first (record each failing before section 2)

- [ ] 1.1 install.sh tests in `cmd/cca` (or a `scripts` test package), with fake `sudo`, `install`, `restorecon`
  and `curl` on PATH and a local release file: `TestInstallScriptRestoresSELinuxLabel`,
  `TestInstallScriptWithoutSELinuxTools`, `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcases `missing`,
  `on path`). Record the failures of the first and third.
- [ ] 1.2 setup.sh tests: `TestSetupScriptWritesEtcdUnitWhenNoneExists`, `TestSetupScriptKeepsDistributionEtcdUnit`
  (the drop-in has no `ETCD_DATA_DIR`), `TestSetupScriptFindsEtcdInUsrLocalBin`,
  `TestSetupScriptEtcdMissingHintNamesRealSources` (non-zero exit, no `dnf install etcd`),
  `TestSetupScriptOpensEtcdClientPort` (firewalld and ufw). Where a test runs the script, do it in a temp root with fake
  `systemctl`, `useradd`, `firewall-cmd` and `ufw`. Record the failures.
- [ ] 1.3 `TestContainerdRuntimeCNIPluginDirectories`: the plugin directories passed to the CNI library are
  `/opt/cni/bin`, `/usr/libexec/cni`, `/usr/lib/cni`, in that order. Record the failure.

## 2. Implementation

- [ ] 2.1 D1 and D2 in `scripts/install.sh`: restorecon after install; a `secure_path` warning with the drop-in
  command; the general note when sudo would prompt. 1.1 is green.
- [ ] 2.2 D3, D4 and D5 in `cmd/cca/command_init.go`: the etcd lookup order, the full unit and user when no unit
  exists, the drop-in unchanged otherwise, the real-sources message with `exit 1`, and 2379/tcp in firewalld and ufw.
  Functions stay under 80 lines (split the etcd step out if needed). 1.2 is green.
- [ ] 2.3 D6 in `runtime/containerd.go`: `cniPluginDirectories`. 1.3 is green; NETWORK_GRAPH if it names the
  directory.
- [ ] 2.4 D7 in `website/installation.md`: "Prepare a fresh host" with Debian/Ubuntu and RHEL/AlmaLinux/Rocky blocks
  (containerd from Docker's repo, CNI from AppStream or the tarball, the checksum-verified etcd 3.5 release binary,
  `uname -m`), the sudo variant and the `secure_path` note, an SELinux and firewalld paragraph, and the manual-setup
  anchors and links updated. Check `TestDocCrossCheck*` and the website build.
- [ ] 2.5 milestones.md: the M279 row and Phase 279 section (done with this proposal); check them against the final
  scope.

## 3. Checks

- [ ] 3.1 gofmt, golangci-lint, shellcheck on `scripts/install.sh`, `go test -count=1 ./...`.
- [ ] 3.2 Rocky 9.8 VM (192.168.100.108), reverted to the pre-test snapshot: follow `website/installation.md`
  literally with the new install.sh and `cca` (a release candidate built from this change, copied to the host). No
  workaround. Single-node cluster up; the Getting Started service running; VIP answers 200; `ausearch -m AVC` empty;
  journal free of `203/EXEC`. Record the commands and output (check `install-host:rhel-family`).
- [ ] 3.3 Debian docs host (192.168.100.103): the same guide's Debian path on a fresh install; the cluster up and the
  service running. Record it.
- [ ] 3.4 Independent review: `code-reviewer` (correctness) and a patterns review against `design/Anti-pattern.md`;
  each finding recorded as fixed or rejected.
- [ ] 3.5 One full Deploy Testbed run (job summary `full run`); record the run ID.

## Workflow follow-up

- Archive (sync `binary-install` and `host-setup`) and release with `scripts/release.sh M279 <version>`.
