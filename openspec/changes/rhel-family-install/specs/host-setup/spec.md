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

### Requirement: setup.sh opens the etcd client port on a control-plane host
When ufw or firewalld is active, `setup.sh` SHALL also open the etcd client port, 2379/tcp, next to the API port and
VXLAN, because agents on worker hosts connect to etcd on the control-plane host.

#### Scenario: firewalld active
- **GIVEN** firewalld is running
- **WHEN** the generated `setup.sh` configures the firewall
- **THEN** it adds 2379/tcp, 9770/tcp and 4789/udp permanently and reloads firewalld
- Test: `TestSetupScriptOpensEtcdClientPort` (firewalld and ufw subcases)

### Requirement: The agent finds CNI plugins in distribution directories
The containerd runtime SHALL look for CNI plugin binaries in `/opt/cni/bin` first, then in `/usr/libexec/cni` and
`/usr/lib/cni`, where distribution packages install them (EL9 `containernetworking-plugins`).

#### Scenario: Plugins from the distribution package
- **GIVEN** the CNI plugins exist only in `/usr/libexec/cni`
- **WHEN** the agent sets up an instance's network
- **THEN** the plugin search path given to the CNI library contains `/opt/cni/bin`, `/usr/libexec/cni` and
  `/usr/lib/cni`, in that order
- Test: `TestContainerdRuntimeCNIPluginDirectories`
