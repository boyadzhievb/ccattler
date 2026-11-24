# Spec Delta

## Purpose
States what `install.sh` must leave on a host, so the installed `cca` binary runs as a systemd service and through
`sudo` on every supported Linux family, SELinux hosts included, and cannot be replaced by an unprivileged user.

## ADDED Requirements

### Requirement: A binary installed with sudo is owned by root
When `install.sh` needs `sudo` to write the install directory, the installed `cca` SHALL be owned by root (uid and
gid 0) with mode 0755, never by the user who ran the script, because systemd units and `sudo cca` run it as root.
(Added during apply, 2026-11-24: security finding S1, user decision option a.)

#### Scenario: Installed into a root-owned directory
- **GIVEN** an install directory the user cannot write, so the script uses `sudo`
- **WHEN** `install.sh` installs `cca`
- **THEN** the installed file is owned by uid 0 and gid 0 with mode 0755, and no step leaves it owned by the user
- Test: `TestInstallScriptInstallsRootOwnedBinary`; check `install-host:rhel-family` (`ls -ln /usr/local/bin/cca`)

### Requirement: The installed binary carries the system label on SELinux hosts
When SELinux is enabled on the host (`selinuxenabled` succeeds), `install.sh` SHALL give the installed `cca` binary
the label the policy defines for its path (for `/usr/local/bin`, `bin_t`). On a host without SELinux tools, or with
SELinux disabled, the step SHALL be skipped silently. (Changed during apply, 2026-11-24: review finding R2.)

#### Scenario: SELinux enabled, the binary gets the policy label
- **GIVEN** a host where `selinuxenabled` succeeds
- **WHEN** `install.sh` installs `cca` into the install directory
- **THEN** it runs `restorecon` on the installed file after installing it
- Test: `TestInstallScriptRestoresSELinuxLabel` (fake `sudo`, `install`, `selinuxenabled` and `restorecon` on PATH)

#### Scenario: SELinux installed but disabled
- **GIVEN** `restorecon` exists but `selinuxenabled` fails
- **WHEN** `install.sh` runs
- **THEN** it does not run `restorecon` and prints nothing about SELinux
- Test: `TestInstallScriptRestoresSELinuxLabel` (subcase `SELinux disabled`)

#### Scenario: No SELinux tools, no error
- **GIVEN** a host without `restorecon` or `selinuxenabled`
- **WHEN** `install.sh` runs
- **THEN** it installs `cca`, exits 0, and prints nothing about SELinux
- Test: `TestInstallScriptWithoutSELinuxTools`

#### Scenario: A systemd unit can execute the binary on an enforcing host
- **GIVEN** a fresh RHEL-family host (Rocky Linux 9, AlmaLinux 10) with SELinux enforcing
- **WHEN** `cca` is installed with the quick-install command, the host is set up with `cca init` and `setup.sh`,
  and `cca-server` and `cca-agent` are started
- **THEN** both units are `active`, and the journal has no `203/EXEC` and no `Permission denied` for `/usr/local/bin/cca`
- Check: `install-host:rhel-family` (by hand, on the reverted VMs, against the published release)

### Requirement: install.sh says when sudo will not find the binary
On Linux, when the install directory is not on sudo's `secure_path`, `install.sh` SHALL print a warning naming the
directory and how to call `cca` by its full path, and its next steps SHALL use that full path.

#### Scenario: Directory missing from secure_path, full-path advice
- **GIVEN** sudo's `secure_path` is `/sbin:/bin:/usr/sbin:/usr/bin` and the install directory is `/usr/local/bin`
- **WHEN** `install.sh` finishes
- **THEN** it warns that `sudo` will not find `cca` there and its next steps call `sudo /usr/local/bin/cca status`
- Test: `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcase `missing`)

### Requirement: install.sh never advises an unsafe sudo path
`install.sh` SHALL print a sudoers drop-in only when every directory it lists is absolute and, with symlinks
resolved, owned by root and not group- or world-writable, together with all its parent directories; otherwise it
prints only the full-path advice. (Changed during apply, 2026-11-24: security findings S5 and round-2 sudo path.)

#### Scenario: Safe install directory missing from secure_path
- **GIVEN** sudo's `secure_path` is `/sbin:/bin:/usr/sbin:/usr/bin` and the install directory is `/usr/local/bin`
- **WHEN** `install.sh` finishes
- **THEN** its output warns that `sudo` will not find `cca` in `/usr/local/bin`, shows the checked drop-in command
  listing only safe directories plus `/usr/local/bin`, and the next steps call `sudo /usr/local/bin/cca`
- Test: `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcase `missing`)

#### Scenario: Unsafe directory, no drop-in
- **GIVEN** an install directory, or one of its parents, that is user-owned, group- or world-writable, relative, or
  a symlink into a user-owned directory
- **WHEN** `install.sh` finishes
- **THEN** it prints the full-path advice and no sudoers drop-in
- Test: `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcases for each case)

#### Scenario: sudo passes the caller's PATH
- **GIVEN** sudoers sets no `secure_path`, so sudo's PATH contains `~/.local/bin` and `.`
- **WHEN** `install.sh` prints the drop-in
- **THEN** neither `~/.local/bin` nor `.` appears in it
- Test: `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcase `unsafe sudo path entries`)

#### Scenario: Install directory on secure_path, or not Linux
- **GIVEN** sudo's `secure_path` includes the install directory, or the host is macOS
- **WHEN** `install.sh` finishes
- **THEN** no `secure_path` warning is printed
- Test: `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcases `on path`, `macOS`)
