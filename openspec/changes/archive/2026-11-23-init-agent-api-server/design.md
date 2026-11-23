# Design

## Context

There are three ways a host gets its agent unit, and each finds its API address in a different place:

| Host | Who writes `cca-agent.service` | Where the agent gets its API address |
|---|---|---|
| `cca init` host (single host, first control plane) | `setup.sh` from `buildSetupScript` (cmd/cca/command_init.go) | **nowhere**: no `--api-server`, and `/etc/ccattler/server-address` (`serverAddressConfigPath`) is written only by `cca join` |
| Worker added with `cca join` (install docs) | the user, from website/installation.md | `--api-server CONTROL_PLANE_IP:9770` in the documented unit, or the saved `/etc/ccattler/server-address` |
| Ansible worker (deploy testbed, bare metal) | `deploy/ansible/roles/worker/templates/cca-agent.service.j2` | `--api-server <first control plane>:9770` |

`configureAgentSecretDelivery` and the gate-key setup in cmd/cca/command_agent.go turn delivery off when `resolveAgentAPIServerAddress` returns `""`. The secret delivery chain is [Flow 27](../../../design/runtime-invariant-flows.md#flow-27-secret-delivery-m174). This change alters only where the agent's API address comes from on an init host. The flow's steps stay the same.

The same `setup.sh` already installs `cca server --listen 0.0.0.0:9770` with `/etc/ccattler/pki/node.pem`. `buildCertificateSANs` puts the advertise address and 127.0.0.1 in that certificate, so both addresses pass the agent's TLS verification against `ca.pem`.

The deploy testbed's control-plane VM is also in the Ansible `workers` group (deploy/ansible/deploy-inventory.ini), so the Ansible template overwrites the `setup.sh` agent unit there. On the bare-metal e2e cluster, node-1 runs no agent. No CI environment runs the `setup.sh` agent unit.

## Goals / Non-Goals

**Goals:**
- On a host set up only with `cca init` and `setup.sh`, the agent has secret delivery enabled from its first start. Gate-key delivery (started only with `--proxy`, which the generated unit does not pass) gets the same address when the proxy is turned on.
- A unit test fails if the generated agent unit loses its API address again.

**Non-Goals:**
- Changing the agent's defaults or its fallback order. `--api-server` stays first, then the address saved by `cca join`.
- Making the `setup.sh` agent unit match the Ansible template flag for flag (`--proxy`, `--secret-api-listen`, `--debug-listen`, `--network-mode`).
- Adding a CI environment that runs the plain install path. A `cca init`-only job on a single VM is a reasonable follow-up, but it would need a testbed host.

## Decisions

**D1. Pass `--api-server <advertise-address>:9770` in the generated agent unit.**
`buildSetupScript` already takes the advertise address and uses it for the server's DNS listener and the agent's `--advertise-address`. The port is the same constant the server unit uses: `defaultAPIListenPort`, in place of the literal `9770` that `buildSetupScript` writes today. That keeps the two units from drifting apart.

Alternatives considered:
- *`127.0.0.1:9770`:* this also works with the certificate. However, a joined worker reaches the control plane at its advertise address, and the server's audit and authorization view the caller's source address the same way for every node. Using the advertise address makes an init host behave like any other node. If the advertise address is wrong, `cca join` breaks on every worker anyway, so the loopback address buys nothing.
- *Write `/etc/ccattler/server-address` in `setup.sh`, as `cca join` does:* this fixes the agent without a flag, but it hides the address in a second file that the unit does not show. The documented worker unit and the Ansible template both pass the flag, so the generated unit should do the same.
- *Default `--api-server` to the local server in the agent:* rejected. A joined worker that lost its saved address would then try its own host, fail its TLS checks in a confusing way, and no longer report `delivery not configured`.

For comparison, kubeadm writes a kubelet kubeconfig that points at the API server's advertise address on the control-plane node, the same as on workers. Nomad and Consul agents on a server node also get the server address through `retry_join` or the same config as clients. In both, the node that created the cluster uses the same client path as the nodes that join it.

**D2. Testing.**
- `TestSetupScriptAgentUsesLocalAPIServer` (cmd/cca) parses the generated script. It checks that the `cca agent` invocation has `--api-server <advertise>:<port>`, and that the `cca server` invocation in the same script listens on the same port. A test that only matched a string could pass with the flag in the wrong unit, so the test uses `extractDaemonInvocations` from daemon_flags_test.go.
- `TestShippedUnitFilesPassOnlyKnownFlags` already parses the generated agent command line with the real agent flag set, so it covers the new flag with no change.
- End to end: CI never runs this unit, so the closing check is by hand on a fresh host (task 3.2, `install-host:secret-delivery`). That host is the Debian 13 install-docs host (192.168.100.99) or any fresh VM. The by-hand check covers the full documented path: `cca init`, `setup.sh`, then apply a service that mounts a secret.
- The closing full Deploy Testbed run still applies (CLAUDE.md), to show nothing else regressed. It does not exercise this unit.

## Failure cases

| Case | What happens |
|---|---|
| `--advertise-address` given as a hostname or an IPv6 address | A hostname is not put in the certificate SANs (`buildCertificateSANs` adds only IPs), and the script's etcd URLs already refuse it, so `setup.sh` fails before the agent starts. This is pre-existing. The new `--api-server` value is built with `net.JoinHostPort`, so an IPv6 address is bracketed there, but the rest of the script is not IPv6-safe (out of scope). |
| Advertise address was detected wrongly at `cca init` (for example 127.0.0.1 on a host with no route) | The agent connects to the advertise address that `cca init` printed. The certificate covers it, so delivery works locally. Joined workers fail, as they do today; the advertise address is documented as the address workers use. |
| `cca-server` not yet up when the agent starts | Delivery requests fail and are retried by the agent's existing delivery loop. Instances wait in `waiting-for-secrets` until the server answers. Delivery is not turned off. |
| The host's IP changes after `cca init` | The server's certificate and the etcd URLs in `setup.sh` already name the old address. The agent fails with a TLS or connection error in its log. Re-running `cca init` is already the documented fix, so this change adds nothing new here. |
| A host set up before this fix | Its unit has no `--api-server`. Behaviour is unchanged until the user edits the unit or re-runs `setup.sh`. The release note gives the one-line fix. |
| `--api-server` given and `/etc/ccattler/server-address` exists (an init host that was also joined, which is not supported) | The flag wins, as today. |

## Risks / Trade-offs

- Coverage stays by hand until a CI job runs the plain install path. The unit test guards the generated text, but not the system behaviour on a real host.
- Re-running `setup.sh` on an existing host rewrites the agent unit and restarts the agent. That is today's behaviour and is unchanged.
