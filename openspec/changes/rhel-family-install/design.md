# Design

## Context

See proposal.md (Why) for the Rocky 9.8 findings. The code paths involved, checked on 2026-11-24:

- **`scripts/install.sh:69`** installs `cca` from a `mktemp -d` directory with `sudo install -m 755`. On the Rocky VM
  the file kept `user_tmp_t`; `restorecon -v` relabelled it to `bin_t`, and the units started. The install directory
  defaults to `/usr/local/bin` (`CCA_INSTALL_DIR`).
- **`cmd/cca/command_init.go`** builds `setup.sh` as a string.
  - The etcd step (:316-340) clears `/etc/default/etcd`, finds `ETCD_BIN=$(command -v etcd)` and writes only the
    drop-in `etcd.service.d/tls.conf` (`ExecStart=` override plus TLS and listen environment).
  - The firewall step (:276-290) opens 4789/udp and the API port in ufw or firewalld.
  - The data directory variable (:263) is used only for `mkdir`, `chown` and `chmod` (:411-415). It is never passed
    to etcd.
- **Debian's `etcd-server` unit** (deploy controller, 2026-11-24) sets `ETCD_DATA_DIR=/var/lib/etcd/default`,
  `ETCD_NAME=%H`, `User=etcd` and `EnvironmentFile=-/etc/default/%p`. The existing drop-in works there only because
  of these settings.
- **`runtime/containerd.go:54,685`:** `cniPluginDirectory = "/opt/cni/bin"`, passed as a one-element list to
  `gocni.WithPluginDir`, which accepts several directories.
- **The documentation sources:** `website/installation.md`, "Prepare a fresh Debian or Ubuntu host", steps 1–3, and
  "Set up a cluster by hand".

No flow in `design/runtime-invariant-flows.md` changes: host setup and binary install are not runtime flows. No store
key is read or written differently, so there are no writers to fence and no change budget to state.

## Goals / Non-Goals

**Goals:**
- `website/installation.md`, followed literally, works on a fresh RHEL-family host with SELinux enforcing and firewalld
  on, and on Debian/Ubuntu as before.
- Each fix is in the product where the product is wrong, not only worked around in the doc.

**Non-Goals:**
- An RPM, a dnf repository for CCattler, CI on RHEL (M276 part A covers packaging later).
- Multi-node RHEL tests.
- Changing the default install directory.

## Decisions

**install.sh installs the binary only, 2026-11-25 (user decision, option 1; supersedes D1/D2 and the install.sh parts
of rounds 1–7).** After 7 rounds on host-computed logic (sudoers text, chown/chmod advice, path walks), the user
pointed at kind (two lines per CPU), Homebrew and Pi-hole. install.sh is now the kind/Homebrew model in 91 lines:
- **Platform:** one `case` each for OS and CPU, and `/etc/os-release` (`ID`, `ID_LIKE`) for the Debian or RHEL path.
- **Download:** the release archive and `checksums.txt`, matched by exact name; a mismatch or a missing entry stops
  with nothing installed.
- **Install:** each privileged command is printed, then run through sudo (`run_as_root`): `install -o 0 -g 0 -m
  0755`, and on the RHEL path `restorecon` when `selinuxenabled`.
- **Output:** fixed next steps, plus one fixed sudo note on RHEL.
- **Dropped:** no prerequisite checks (they stay in the guide), no secure_path or ownership detection, no computed
  advice.

Kept from M279: the setup.sh fixes (etcd unit, `--etcd-client-cidr`, firewall), the agent's CNI directories, the
guide's Debian and RHEL blocks.

Review cap (user, 2026-11-25): one review round on this change; only HIGH or MEDIUM findings are fixed, lows are
listed, then release.

**No privileged commands printed by install.sh, 2026-11-24 (user decision on review round 5, option A2).**
- **Why:** computed privileged advice went wrong again. Rounds 1–3 found it in the sudoers text, rounds 4–5 in the
  `chown`/`chmod` advice: the printed `sudo chmod 0755 '<dir>'` would remove `/tmp`'s sticky bit, and would hand a
  home directory to root.
- **What install.sh does now:**
  - it detects an unsafe path: a binary not owned by uid 0, or the install directory or any parent up to `/` that is
    not root-owned or is group- or world-writable (a sticky parent is safe only when root owns its child);
  - it warns, naming the first unsafe directory, and says to use a directory writable only by root along its whole
    path, such as `/usr/local/bin`, with the guide's link;
  - it prints no `chown`, `chmod` or sudoers command. Detection can only add or miss a warning, never cause harm.
- **Rejected:**
  - (A1) checking only the install directory: it misses a root-owned `~/bin` under a user-owned home;
  - (B) patching the printed commands a third time.

**Structural changes after review round 3, 2026-11-24 (user decisions, options a and ii).** This was the third round
with findings of the same class (the openspec/config.yaml rework limit).
- **The secure_path advice** (rounds 1, 2 and 3):
  - `install.sh` no longer prints any sudoers text. It warns, prints the full-path command and names the guide's fixed
    `secure_path` snippet, which lists only the standard root directories and `/usr/local/bin`. Nothing computed from
    the host reaches sudoers.
  - Rejected: patching a third time (print the resolved path).
- **etcd client networks** (rounds 2 and 3). One canonical value is used for both the check and the rule: the parsed
  `*net.IPNet` is checked through `Mask.Size()` after unmapping, so the text that is checked is the text written into
  `setup.sh`.
  - **Refused:** a set of networks whose union covers a whole family. The deduplicated sizes are summed in the family's
    address space; IPv4-mapped IPv6 counts as IPv4.
  - **Warned, not refused:** networks wider than /8 (IPv4) or /32 (IPv6), and public ranges, because clusters on public
    IPs exist.
  - **A test that holds it:** `TestSetupScriptEtcdRulesNeverCoverEveryAddress` reads the rules back out of the generated
    script.
  - Rejected: refusing only /0, which leaves the two-halves case; allowing private ranges only, which breaks
    public-IP clusters.

**Spec changes during apply, 2026-11-24 (main session, from review round 2).** These follow the user's decisions
(S1 option a, S2 option b) and the security findings:
- **`binary-install`:**
  - a root-ownership requirement (S1);
  - the SELinux requirement now needs `selinuxenabled` (R2);
  - the secure_path rule is split in two: the full-path advice, and a drop-in that may list only safe directories
    (S5 and the round-2 sudo-path finding).
- **`host-setup`:** a scenario refusing networks that cover every address (`0.0.0.0/0`, `::/0`,
  `::ffff:0.0.0.0/96`). It was reproduced opening etcd to all.

No requirement was weakened.

**Order of the proof, 2026-11-24 (user decision, option c).**
- **The problem:** `install.sh` always downloads from a GitHub release (`install.sh:94`), so the guide can be
  followed literally only with a release that contains this change.
- **The order:** the code review and the full Deploy Testbed run first; then the release (`scripts/release.sh M279`);
  then the Rocky 9.8 and Debian runs of the guide, against the published release (tasks 3.2 and 3.3); the archive
  last.
- **If those runs find a defect:** it becomes a follow-up fix (bugs first). It is never left as a doc workaround.
- **Rejected:** a public pre-release tag, and a download-URL override in `install.sh` (out of scope).

**Correction during apply, 2026-11-24 (user decision, option a): D1 and D2 apply to the root `install.sh`, not
`scripts/install.sh`.**
- **Why:** the design named the wrong script (a main-session error found by the implementer).
- **Which script is live:** `release.yml:141` publishes the root `install.sh`, and that is what the guide's `curl … | bash`
  runs. Its `sudo mv` (`install.sh:145`) keeps the temp file's label, which is the `user_tmp_t` seen on Rocky.
  `scripts/install.sh` (M7) installs nothing for users; CI only sources its `detect_os`/`detect_arch`. It stays as it
  is, and the edits made to it are reverted.
- **Also in scope** for the root script:
  - its CNI prerequisite check (`install.sh:168`) accepts the D6 directories, not only `/opt/cni/bin/bridge`;
  - its link to the guide's host-preparation anchor (`install.sh:173`) follows the D7 rename.
- **Variables:** the root script's `CCATTLER_VERSION` and `INSTALL_PATH` are the ones the guide documents.

### D1. Relabel after install, don't change how install.sh copies
`install.sh` runs `sudo restorecon "${INSTALL_DIR}/cca"` (or without sudo when it installed without sudo) when
`command -v restorecon` succeeds, after the copy. `restorecon` applies the policy's label for the path, so it is
right for any `CCA_INSTALL_DIR`.

Alternatives:
- `install -Z`: needs a coreutils built with SELinux support, which not every distribution has.
- Downloading straight into the install directory: leaves a half-written binary on failure.

### D2. Warn about secure_path; don't edit sudoers
`install.sh` reads sudo's effective `secure_path` (`sudo -l` prints it, without a password once sudo was used, or from
`sudo sh -c 'echo $PATH'`). If the install directory is missing from it, it prints one warning and the drop-in
command from the doc. It does not write `/etc/sudoers.d` itself: changing sudo policy is the administrator's
decision, and a broken sudoers file locks them out.

Alternatives:
- Installing into `/usr/bin` on RHEL: that directory belongs to the package manager.
- A symlink in `/usr/bin`: the same objection.

Kubernetes' kubeadm guide sidesteps this with packages that install to `/usr/bin`. CCattler has no packages yet (out
of scope).

### D3. setup.sh writes a full etcd unit only when none exists
- **Finding etcd:** the etcd step looks in `$(command -v etcd)`, then `/usr/local/bin/etcd`, then `/usr/bin/etcd`.
- **When `systemctl cat etcd.service` fails (no unit):**
  - it creates the system user `etcd` (`useradd --system --home-dir /var/lib/etcd --shell /sbin/nologin`, or the
    `/usr/sbin/nologin` that exists);
  - it writes `/etc/systemd/system/etcd.service` with `Type=notify`, `User=etcd`, `ExecStart=<found binary>`,
    `Environment=ETCD_DATA_DIR=/var/lib/etcd/default`, `Environment=ETCD_NAME=%H`, `Restart=on-failure` and
    `LimitNOFILE=65536`.
- **When a unit exists:** unchanged (the drop-in only).
- **In both cases:** the TLS drop-in is written as today. It never sets `ETCD_DATA_DIR`, so an existing Debian
  cluster keeps `/var/lib/etcd/default` (a review finding during this proposal: forcing `/var/lib/etcd` would have
  started etcd on an empty directory).
- **The `mkdir`/`chown` lines** create `/var/lib/etcd` as today; etcd creates `default` itself.

Alternative: always write our own unit. Rejected: it would replace Debian's unit on existing clusters, a larger
change than this bug needs.

### D4. The etcd-missing message names real sources and exits non-zero
- Debian/Ubuntu: `sudo apt install etcd-server`.
- RHEL/AlmaLinux/Rocky: a pointer to the doc section that downloads the checksum-verified release binary.
- `setup.sh` already runs with `set -eu`. The message path ends with `exit 1`; the old message appeared to exit 0
  (seen through a pipe on the VM, so this is checked in the test).

### D5. Open 2379/tcp only to the networks the operator names
Changed during apply, 2026-11-24, by user decision on security finding S2, option b. The first version opened
2379/tcp to every source.
- **Why:** until M280 is fixed, every certificate the cluster CA issues with ClientAuth, gate certificates included,
  is a full etcd client. So the port must not be reachable from hosts nobody chose.
- **The flag:** `cca init --etcd-client-cidr` takes one or more CIDRs (repeatable or comma-separated), each checked
  with `net.ParseCIDR`. An invalid value stops `cca init` before any file is written.
- **firewalld:** one rich rule per CIDR: `firewall-cmd --permanent --add-rich-rule='rule family="ipv4|ipv6" source
  address="<cidr>" port port="2379" protocol="tcp" accept'`.
- **ufw:** `ufw allow from <cidr> to any port 2379 proto tcp comment 'CCattler etcd clients'`.
- **Without the flag:** no rule. `setup.sh` prints both commands with a `<your node network>` placeholder and names the
  flag. The guide's worker section tells multi-node users to pass the flag.
- **Rejected:**
  - the advertise address's own subnet, because it guesses which hosts are trusted;
  - never opening the port, because RHEL multi-node would then fail until a reader finds the doc.

  For comparison, kubeadm and k3s document the ports and touch no firewall.
- **Peer port 2380** is not opened: there is no HA etcd setup in `cca init` (see M184).

### D6. CNI search path
`cniPluginDirectories = []string{"/opt/cni/bin", "/usr/libexec/cni", "/usr/lib/cni"}`, passed to
`gocni.WithPluginDir`. The CNI library takes the first directory that has the plugin, so `/opt/cni/bin` keeps
priority and existing hosts behave the same.

### D7. Documentation layout
- The section becomes "Prepare a fresh host".
- Steps 2 and 3 get two blocks each: **Debian/Ubuntu** (as today) and **RHEL, AlmaLinux, Rocky** (`dnf`).
- RHEL containerd: Docker's repository with `dnf config-manager --add-repo
  https://download.docker.com/linux/rhel/docker-ce.repo` and `containerd.io` (user decision 2026-11-24). It brings
  `container-selinux`.
- RHEL CNI: `dnf install containernetworking-plugins` (AppStream), possible after D6; the tarball stays as the
  fallback.
- etcd on RHEL: the release binary with `SHA256SUMS` verification, version pinned to the 3.5 line the deploy cluster
  runs.
- Architecture from `uname -m` (x86_64→amd64, aarch64→arm64) for both families.
- Step 1 gets the "you already have sudo" variant of the passwordless drop-in, and a `secure_path` note.
- A short "SELinux and firewalld" paragraph: both stay on; what `setup.sh` opens; `container-selinux` comes with
  containerd.io.

## Failure cases

| Case | Behaviour |
|---|---|
| `restorecon` missing (Debian) | The step is skipped; no output |
| `restorecon` fails | install.sh prints a warning with the command to run by hand; the install itself succeeded |
| sudo needs a password while install.sh checks `secure_path` | It cannot read `secure_path` without prompting: it prints the general note instead of guessing |
| etcd unit exists but is masked | `systemctl cat` succeeds, so no full unit is written; etcd fails to start as before, and the journal hint in the doc covers it |
| `useradd` missing | It falls back to `adduser --system`; otherwise it stops with a message |
| A host with both `/opt/cni/bin` and `/usr/libexec/cni` | `/opt/cni/bin` wins (first in the list) |
| Rerun of setup.sh on a host where it wrote the unit | `systemctl cat` now succeeds, so it keeps that unit; same data directory |

## Risks / Trade-offs

- **[Debian path broken by the setup.sh change]** → mitigations:
  - `TestSetupScriptKeepsDistributionEtcdUnit`;
  - the guide rerun on the Debian docs host;
  - the full Deploy Testbed run, Debian with `etcd-server`.
- **[Docker's repository changes or drops RHEL]** → the doc names EPEL `containerd` as the alternative in one
  sentence, with the CRB requirement.
- **[A test of a shell script in Go is brittle]** → the install.sh tests run the real script with fake `sudo`,
  `install`, `restorecon` and `curl` on `PATH` and a local release file. They assert calls, not wording.

## Migration Plan

No data migration. Existing Debian clusters see no change from rerunning `setup.sh` (D3). Rollback is a revert of the
commit.
