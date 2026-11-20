# Proposal

Milestone: **M248 — A Host Set Up With `cca init` Never Delivers Secrets** (bug, milestones.md Phase 248).

## Why

On 2026-11-19 the install docs were tested on a fresh Debian 13 host (192.168.100.99). After `cca init` and `setup.sh`, the agent logged `secret delivery not configured (needs node certificate and --api-server); instances that need secrets will wait`. Every instance that needs a secret stayed in `waiting-for-secrets` (reason `delivery_not_configured`) for good. The agent unit that `setup.sh` writes (`buildSetupScript`, cmd/cca/command_init.go) passes the node certificate but no `--api-server`. An init host also has no address saved by `cca join`, which is the agent's fallback (`resolveAgentAPIServerAddress` → `serverAddressConfigPath`). As a result, secret delivery ([Flow 27](../../../design/runtime-invariant-flows.md#flow-27-secret-delivery-m174)) is off on a single-host cluster and on the first control-plane node. Gate-key delivery (gate TLS) would be off too once the gate proxy is turned on.

The deploy testbed never caught this. Its control-plane VM is also in the Ansible `workers` group, so the worker role overwrites the `setup.sh` agent unit with `cca-agent.service.j2`, and that template passes `--api-server`. The bare-metal e2e node-1 runs no agent at all. As a result, no test environment ever runs the agent unit that users get from the documented install path.

## What Changes

- The agent unit in the generated `setup.sh` passes `--api-server <advertise-address>:9770`. The `cca server` unit in the same script listens on `0.0.0.0:9770`, and the node certificate's SANs include the advertise address, so the agent's mTLS client reaches its own server without new flags or certificates.
- A unit test on the generated script checks that the agent unit points at the local API server.
- Secret delivery on a `cca init` host is checked end to end. This is a by-hand check on a fresh host, because the deploy testbed overwrites this unit (see design).

Out of scope:
- **M251:** host preflight checks.
- **M249:** `cca login --token` over TLS.
- **M250:** the `--help` handling.
- **Agent flags with defaults:** making `--api-server` default to the local server when it is not set. Choosing a default address in the agent would hide misconfiguration on joined workers.
- **Other gaps between the units:** making the `setup.sh` agent unit match every flag of the Ansible template (`--proxy`, `--secret-api-listen`, `--debug-listen`). These differences are tracked separately and are not part of this bug.
- **Ansible units:** the Ansible units are unchanged, because they already pass the flag.

## Capabilities

### New Capabilities
- `host-setup`: what `cca init` and its generated `setup.sh` must install on a host so that a single-host or first control-plane node works the same as a node added with `cca join`.

### Modified Capabilities

(none)

## Impact

- Code: `buildSetupScript` in cmd/cca/command_init.go, one line in the agent unit.
- Tests: cmd/cca (generated-script test). The existing `TestShippedUnitFilesPassOnlyKnownFlags` already feeds the generated script through the agent's flag parser, so the new flag is checked against what the agent accepts.
- Docs: the install docs need no change, because the setup script now does the right thing. `design/runtime-invariant-flows.md` Flow 27 gets one line naming where the agent's API address comes from on each kind of host.
- Upgrade: hosts already set up with `cca init` keep their old unit until `setup.sh` runs again. The release note says to add `--api-server <advertise-address>:9770` to `/etc/systemd/system/cca-agent.service`, or to re-run the new `cca init` and `setup.sh`.
