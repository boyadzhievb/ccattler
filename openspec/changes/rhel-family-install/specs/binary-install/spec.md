# Spec Delta

## Purpose
States what `install.sh` must leave on a host, so the installed `cca` binary runs as a systemd service and through
`sudo` on every supported Linux family, SELinux hosts included.

## ADDED Requirements

### Requirement: The installed binary carries the system label on SELinux hosts
When SELinux is available on the host, `install.sh` SHALL give the installed `cca` binary the label the policy
defines for its path (for `/usr/local/bin`, `bin_t`), not the label of the temporary download. On a host without
SELinux tools the step SHALL be skipped silently.

#### Scenario: Installed under SELinux, the binary has the policy label
- **GIVEN** a host where `restorecon` exists
- **WHEN** `install.sh` installs `cca` into the install directory
- **THEN** it runs `restorecon` on the installed file after copying it
- Test: `TestInstallScriptRestoresSELinuxLabel` (runs install.sh against a fake `sudo`, `install` and `restorecon` on PATH and checks the calls)

#### Scenario: No SELinux tools, no error
- **GIVEN** a host without `restorecon`
- **WHEN** `install.sh` runs
- **THEN** it installs `cca`, exits 0, and prints nothing about SELinux
- Test: `TestInstallScriptWithoutSELinuxTools`

#### Scenario: A systemd unit can execute the binary on an enforcing host
- **GIVEN** a fresh Rocky Linux 9 host with SELinux enforcing
- **WHEN** `cca` is installed with the quick-install command, the host is set up with `cca init` and `setup.sh`,
  and `cca-server` and `cca-agent` are started
- **THEN** both units are `active`, and the journal has no `203/EXEC` and no `Permission denied` for `/usr/local/bin/cca`
- Check: `install-host:rhel-family` (by hand, on the Rocky 9.8 VM reverted to its pre-test snapshot)

### Requirement: install.sh says when sudo will not find the binary
When the install directory is not on sudo's `secure_path`, `install.sh` SHALL print a warning naming the directory
and the fix (a sudoers drop-in adding the directory to `secure_path`, or calling `cca` by its full path), so the
next steps that use `sudo cca` do not fail without explanation.

#### Scenario: Install directory missing from secure_path
- **GIVEN** sudo's `secure_path` is `/sbin:/bin:/usr/sbin:/usr/bin` and the install directory is `/usr/local/bin`
- **WHEN** `install.sh` finishes
- **THEN** its output warns that `sudo` will not find `cca` in `/usr/local/bin` and shows the drop-in command
- Test: `TestInstallScriptWarnsWhenSudoPathLacksInstallDir`

#### Scenario: Install directory on secure_path
- **GIVEN** sudo's `secure_path` includes `/usr/local/bin`
- **WHEN** `install.sh` finishes
- **THEN** no `secure_path` warning is printed
- Test: `TestInstallScriptWarnsWhenSudoPathLacksInstallDir` (subcase `on path`)
