# Spec Delta

## Purpose
States what `install.sh` must leave on a host: the `cca` binary for its OS and CPU, checksum-verified, owned by root
and, on SELinux hosts, labelled so systemd can run it. Nothing else; cluster prerequisites are in the guide.

## ADDED Requirements

### Requirement: install.sh installs only the binary
`install.sh` SHALL download the release archive for the host's OS and CPU, verify it against the release's
`checksums.txt`, and install `cca`. It SHALL NOT check or install cluster prerequisites, and SHALL NOT print any
command computed from the host (no sudoers, `chown` or `chmod` advice). (Rewritten during apply, 2026-11-25, user
decision: a binary-only installer like kind and Homebrew, after 7 review rounds on host-computed logic.)

#### Scenario: Fixed output only
- **GIVEN** any supported host
- **WHEN** `install.sh` runs
- **THEN** it prints each privileged command before running it, then fixed next steps, and its output contains no
  `sudoers`, `secure_path=`, `chown` or `chmod`
- Test: `TestInstallScriptPrintsOnlyFixedAdvice` (subcases `Debian`, `RHEL`, `other Linux`)

### Requirement: A bad checksum installs nothing
`install.sh` SHALL stop with a non-zero exit and install nothing when the archive's SHA-256 differs from its entry in
`checksums.txt`, or when the entry is missing. The entry is matched by exact file name.

#### Scenario: Checksum mismatch or missing entry
- **GIVEN** an archive whose SHA-256 differs from `checksums.txt`, or a `checksums.txt` without its exact name
- **WHEN** `install.sh` verifies it
- **THEN** it exits non-zero and no `cca` is installed
- Test: `TestInstallScriptAbortsOnBadChecksum` (subcases `mismatch`, `missing entry`)

### Requirement: The installed binary is owned by root
`install.sh` SHALL install `cca` with `install -o 0 -g 0 -m 0755`, running it through `sudo` unless already root,
because systemd units and `sudo cca` run it as root. (Security finding S1.)

#### Scenario: Root-owned install
- **GIVEN** a non-root user
- **WHEN** `install.sh` installs `cca`
- **THEN** it runs `sudo install -o 0 -g 0 -m 0755 … <INSTALL_PATH>/cca`, and no step leaves the file owned by the user
- Test: `TestInstallScriptInstallsRootOwnedBinary`; check `install-host:rhel-family` (`ls -ln /usr/local/bin/cca`)

### Requirement: The RHEL family gets the SELinux label and one fixed sudo note
`install.sh` SHALL take the RHEL path when `/etc/os-release` `ID` or `ID_LIKE` names `rhel`, `fedora` or `centos`, and
the Debian path for `debian` or `ubuntu`. On the RHEL path it SHALL run `restorecon` on the installed binary when
`selinuxenabled` succeeds, and print one fixed note that `sudo` there does not search `/usr/local/bin`. The other
paths SHALL do neither.

#### Scenario: The family decides the path
- **GIVEN** `os-release` with `ID=rocky` (`ID_LIKE="rhel centos fedora"`), `ID=almalinux`, `ID=fedora`, `ID=debian`,
  `ID=ubuntu`, or `ID=arch`
- **WHEN** `install.sh` runs with `selinuxenabled` succeeding
- **THEN** the first three run `restorecon` and print the sudo note; the others do neither
- Test: `TestInstallScriptPicksFamilyPath` (one subcase per value)

#### Scenario: SELinux disabled on the RHEL path
- **GIVEN** the RHEL path and `selinuxenabled` failing or missing
- **WHEN** `install.sh` runs
- **THEN** it runs no `restorecon` and prints nothing about SELinux, and still prints the sudo note
- Test: `TestInstallScriptRestoresSELinuxLabel` (subcases `enabled`, `disabled`, `missing`)

#### Scenario: A systemd unit can execute the binary on an enforcing host
- **GIVEN** a fresh RHEL-family host (Rocky Linux 9, AlmaLinux 10) with SELinux enforcing
- **WHEN** `cca` is installed with the quick-install command, the host is set up with `cca init` and `setup.sh`, and
  `cca-server` and `cca-agent` are started
- **THEN** both units are `active`, and the journal has no `203/EXEC` and no `Permission denied` for `/usr/local/bin/cca`
- Check: `install-host:rhel-family` (by hand, on the reverted VMs, against the published release)
