# Upgrading

## Binary upgrade

CCattler is a single binary (`cca`). To upgrade:

1. Download the new version from [GitHub Releases](https://github.com/boyadzhievb/ccattler/releases)
2. Replace the binary on each node
3. Restart services

### Control plane

```bash
# Download new binary
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash

# Restart the server
sudo systemctl restart cca-server
```

### Worker nodes

Roll upgrades one node at a time to avoid service disruption:

```bash
# On each worker node:
sudo systemctl stop cca-agent
# Replace binary (install script or manual copy)
sudo systemctl start cca-agent
```

The agent re-registers with the control plane and resumes reporting observed state. Workloads continue running during the agent restart.

## Ansible upgrade

If you deployed with Ansible, re-run the deployment script with the updated release:

```bash
DEMO_MODE=inventory curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install-demo.sh | bash
```

This updates the local binary, downloads the latest playbooks, and re-deploys across all nodes in your inventory.

## Version compatibility

- **Control plane and agents** should run the same version. Minor version mismatches are tolerated (agents can be one minor version behind the server).
- **etcd** — CCattler uses the etcd v3 API. Compatible with etcd 3.5+.
- **Certificates** — existing certificates remain valid across upgrades. The CA and node certificates are not version-dependent.

## Rolling back

If an upgrade causes issues:

1. Stop the new version on affected nodes
2. Replace with the previous binary
3. Restart services

State in etcd is not version-dependent — the previous binary can read and write the same keys.
