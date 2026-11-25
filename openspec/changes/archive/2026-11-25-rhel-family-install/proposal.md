# Proposal

Milestone: **M279 — RHEL-Family Install Works** (milestones.md Phase 279, bug).

## Why

The installation guide (`website/installation.md`) and the scripts behind it were written and tested on Debian and
Ubuntu only. On 2026-11-24 the guide was followed step by step on a fresh Rocky Linux 9.8 VM (SELinux enforcing,
firewalld active). A single-node cluster with two nginx instances worked only after six manual workarounds. Four of the
problems are bugs in shipped code, not in the guide:

1. **The binary is not executable by systemd.** `install.sh` (the published root script; corrected 2026-11-24, see design.md) installs `cca` with the SELinux label of its
   temp download (`user_tmp_t`), so `cca-server` and `cca-agent` fail with `203/EXEC Permission denied`.
2. **`setup.sh` assumes a Debian etcd package.**
   - It only adds a TLS drop-in to an existing `etcd.service`, but no EL9 repository (Rocky, EPEL) ships etcd, so the
     unit does not exist.
   - It relies on that unit's `WorkingDirectory` instead of setting the data directory, so etcd fails with
     `mkdir default.etcd`.
   - When etcd is missing, it tells RHEL users to run `dnf install etcd`, a package that does not exist on EL9.
3. **`setup.sh` does not open the etcd client port.** It opens 9770/tcp and 4789/udp in firewalld and ufw, but not
   2379/tcp, which agents on worker hosts use to reach etcd.
4. **Distribution CNI plugins are not found.** The agent looks only in `/opt/cni/bin` (`runtime/containerd.go:54`).
   Rocky's own `containernetworking-plugins` package (AppStream, 1.9.1) installs to `/usr/libexec/cni`.

The guide also fails on RHEL by itself:
- sudo's `secure_path` there leaves out `/usr/local/bin`, so the guide's `sudo cca …` gives "command not found";
- `dpkg --print-architecture` does not exist;
- there are no `dnf` steps;
- the passwordless-sudo step only covers hosts without sudo.

## What Changes

- **`install.sh`:** after installing `cca`, it restores the SELinux label (`restorecon`) when SELinux is available. When
  the install directory is not on sudo's `secure_path`, it says so and prints the fix, instead of leaving
  `sudo cca` to fail.
- **`cca init` / `setup.sh`:**
  - When no `etcd.service` exists, it writes a full unit and creates the `etcd` system user. That unit sets the data
    directory explicitly (`/var/lib/etcd/default`, Debian's layout). A distribution unit keeps its own data directory.
  - It finds etcd in `/usr/local/bin` too.
  - Its "etcd not found" hint names the real RHEL-family source (the etcd release binary).
  - It opens 2379/tcp in firewalld and ufw on the control-plane host.
- **Agent:** it looks for CNI plugins in `/opt/cni/bin`, then the distribution directories (`/usr/libexec/cni`,
  `/usr/lib/cni`).
- **`website/installation.md`:**
  - "Prepare a fresh host" covers Debian/Ubuntu and RHEL/AlmaLinux/Rocky.
  - containerd comes from Docker's repository (user decision 2026-11-24; it also installs `container-selinux`).
  - CNI comes from AppStream or the release tarball.
  - etcd is the checksum-verified release binary.
  - The architecture comes from `uname -m`.
  - There is a passwordless-sudo variant for users who already have sudo.
  - Notes on SELinux, firewalld and `secure_path`.
- **Milestones:** an M279 row after M278 (bugs first) and a Phase 279 section.

Out of scope:
- Other distributions (Fedora, SUSE, Arch) beyond the existing sudo table.
- Multi-node RHEL clusters: a worker join is not tested, because there is one VM. The 2379 firewall rule is checked in
  the generated script only.
- Packaging CCattler as an RPM, and an RHEL row in the CI test matrix: future work, noted in M276 part A (packaging).
- Making install.sh install somewhere other than `/usr/local/bin` by default.

## Capabilities

### New Capabilities
- `binary-install`: what `install.sh` must leave on a host: a `cca` binary that systemd can execute under SELinux,
  and a clear message when `sudo` cannot find it.

### Modified Capabilities
- `host-setup`: `setup.sh` must work without a distribution etcd package (full unit, explicit data directory, correct
  hint) and open the etcd client port. The agent must find CNI plugins in distribution directories.

## Impact

- `install.sh` (root, published; corrected 2026-11-24), `cmd/cca/command_init.go` (setup script generation), `runtime/containerd.go` (CNI directories),
  `website/installation.md`, `milestones.md`.
- Tests: `cmd/cca` setup-script tests, a shell test for install.sh, `runtime` CNI directory test.
- Graphs: none expected (no controller or store-key change). Check `NETWORK_GRAPH.md` for the CNI directory.
- Debian/Ubuntu behaviour stays the same. When a distribution `etcd.service` exists, only the drop-in is written as
  today, and it never sets the data directory: Debian's unit sets `ETCD_DATA_DIR=/var/lib/etcd/default` (checked on
  the deploy controller), and overriding it would point an existing cluster at an empty directory. The deploy cluster runs Debian, so the full Deploy Testbed run proves the
  Debian path did not break.

How it is proven done:
- the named unit tests;
- `website/installation.md` followed literally on the Rocky 9.8 VM reverted to its pre-test snapshot, with no
  workaround: single-node cluster up, Getting Started service running, VIP answering 200, no SELinux denials;
- the same guide checked on the Debian docs host (192.168.100.103), so the Debian path is not broken;
- one full Deploy Testbed run.
