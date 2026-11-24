# Spec Delta

## ADDED Requirements

### Requirement: setup.sh runs etcd without a distribution etcd package
When the host has an etcd binary but no `etcd.service` (a release binary, as on EL9 where no repository ships etcd),
`setup.sh` SHALL create the `etcd` system user and write a complete `etcd.service` that sets the data directory
explicitly to `/var/lib/etcd/default`, the same layout as Debian's package. When a distribution unit exists, it SHALL
keep that unit and its data directory, and add only the TLS drop-in.

#### Scenario: No etcd unit, release binary in /usr/local/bin
- **GIVEN** the generated `setup.sh` runs where `etcd` is `/usr/local/bin/etcd` and no `etcd.service` exists
- **WHEN** it configures etcd
- **THEN** it creates the `etcd` user, writes `/etc/systemd/system/etcd.service` with `ExecStart` pointing at that
  binary and `ETCD_DATA_DIR=/var/lib/etcd/default`, and etcd starts with its data there
- Test: `TestSetupScriptWritesEtcdUnitWhenNoneExists` (generated script content) and check `install-host:rhel-family`

#### Scenario: Distribution etcd unit present
- **GIVEN** a host with a distribution `etcd.service` (Debian `etcd-server`)
- **WHEN** the generated `setup.sh` configures etcd
- **THEN** it writes only the TLS drop-in, which does not set `ETCD_DATA_DIR`, so an existing cluster keeps its data
  directory (`/var/lib/etcd/default` on Debian), and it does not replace the distribution unit
- Test: `TestSetupScriptKeepsDistributionEtcdUnit`; full Deploy Testbed run (Debian)

#### Scenario: etcd found outside root's PATH
- **GIVEN** `etcd` is installed in `/usr/local/bin` and sudo's `secure_path` does not include that directory
- **WHEN** `setup.sh` runs with `sudo`
- **THEN** it finds `/usr/local/bin/etcd` and does not report etcd as missing
- Test: `TestSetupScriptFindsEtcdInUsrLocalBin`

### Requirement: setup.sh names a real etcd source for each distribution family
When no etcd binary is found, `setup.sh` SHALL stop with a non-zero exit and name a source that exists for each
family: the `etcd-server` package on Debian/Ubuntu, and the checksum-verified etcd release binary on RHEL, AlmaLinux
and Rocky (no EL9 repository ships etcd).

#### Scenario: etcd missing
- **GIVEN** no etcd binary on the host
- **WHEN** `setup.sh` reaches the etcd step
- **THEN** it exits non-zero, its message names `apt install etcd-server` for Debian/Ubuntu and the etcd release
  download for RHEL-family hosts, and it does not suggest `dnf install etcd`
- Test: `TestSetupScriptEtcdMissingHintNamesRealSources`

### Requirement: setup.sh opens the etcd client port only to the networks the operator names
Agents on worker hosts connect to etcd on the control-plane host. When ufw or firewalld is active, `setup.sh` SHALL
open 2379/tcp only from the networks given to `cca init --etcd-client-cidr`, and never from every source. Without
the flag it SHALL leave the port closed and print the exact firewalld or ufw command that opens it for a network.
(Changed during apply, 2026-11-24, user decision on security finding S2: option b.)

#### Scenario: Networks given
- **GIVEN** `cca init --etcd-client-cidr 192.0.2.0/24` and firewalld running
- **WHEN** the generated `setup.sh` configures the firewall
- **THEN** it allows 2379/tcp only from 192.0.2.0/24 (a source-restricted rule), adds 9770/tcp and 4789/udp as
  before, and reloads firewalld; the ufw subcase uses `ufw allow from 192.0.2.0/24 to any port 2379 proto tcp`
- Test: `TestSetupScriptOpensEtcdClientPortOnlyToGivenNetworks` (firewalld and ufw subcases, several CIDRs)

#### Scenario: No networks given
- **GIVEN** `cca init` without `--etcd-client-cidr`
- **WHEN** the generated `setup.sh` configures the firewall
- **THEN** it does not open 2379/tcp from any source, and prints the firewalld and ufw commands that open it for a
  network, naming the flag
- Test: `TestSetupScriptLeavesEtcdClientPortClosedWithoutNetworks`

#### Scenario: Invalid network
- **GIVEN** `cca init --etcd-client-cidr 192.0.2.0/33` (or any value that is not an IPv4/IPv6 CIDR)
- **WHEN** `cca init` runs
- **THEN** it exits with an error naming the value, and writes no files
- Test: `TestInitRejectsInvalidEtcdClientCIDR`

#### Scenario: A network that covers every address
- **GIVEN** `cca init --etcd-client-cidr 0.0.0.0/0`, `::/0` or `::ffff:0.0.0.0/96` (an IPv4-mapped form of every IPv4
  address)
- **WHEN** `cca init` runs
- **THEN** it exits with an error saying the flag must name the cluster's networks, not every source, and writes no
  files (added during apply, 2026-11-24: security round-2 finding, which reproduced `0.0.0.0/0` opening etcd to all)
- Test: `TestInitRejectsInvalidEtcdClientCIDR` (subcases for the three values, and the leading-zero forms
  `0.0.0.0/00`, `::/000`, `::ffff:0.0.0.0/096`, which review round 3 reproduced getting through)

#### Scenario: Networks that together cover every address
- **GIVEN** `cca init --etcd-client-cidr 0.0.0.0/1,128.0.0.0/1` (or any set whose union is a whole IPv4 or IPv6
  family)
- **WHEN** `cca init` runs
- **THEN** it exits with the same error as for a single all-covering network, and writes no files (added during apply,
  2026-11-24, user decision on review round 3, option ii)
- Test: `TestInitRejectsInvalidEtcdClientCIDR` (subcase `union covers every address`)

#### Scenario: Wide or public network
- **GIVEN** `cca init --etcd-client-cidr` with a network wider than /8 (IPv4) or /32 (IPv6), or one containing
  public (non-private, non-loopback) addresses
- **WHEN** `cca init` runs
- **THEN** it accepts the value, prints a warning naming it and saying that every cluster-CA certificate can use
  etcd (M280), and writes the rule
- Test: `TestInitWarnsOnWideEtcdClientCIDR`

#### Scenario: Every written rule is re-checked
- **GIVEN** any accepted `--etcd-client-cidr` value, among them the forms above
- **WHEN** the generated `setup.sh` is read back
- **THEN** no 2379 rule in it, firewalld or ufw, covers every address of a family, alone or together with the
  others
- Test: `TestSetupScriptEtcdRulesNeverCoverEveryAddress` (parses the rules out of the generated script)

### Requirement: The agent finds CNI plugins in distribution directories
The containerd runtime SHALL look for CNI plugin binaries in `/opt/cni/bin` first, then in `/usr/libexec/cni` and
`/usr/lib/cni`, where distribution packages install them (EL9 `containernetworking-plugins`).

#### Scenario: Plugins from the distribution package
- **GIVEN** the CNI plugins exist only in `/usr/libexec/cni`
- **WHEN** the agent sets up an instance's network
- **THEN** the plugin search path given to the CNI library contains `/opt/cni/bin`, `/usr/libexec/cni` and
  `/usr/lib/cni`, in that order
- Test: `TestContainerdRuntimeCNIPluginDirectories`
