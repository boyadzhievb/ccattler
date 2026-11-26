// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyDesiredSecretGeneration returns the store path for a secret's value
// generation: a decimal number raised only when a different plaintext is
// stored (M236). Agents hash it; it never reveals the value.
// Path: desired/secret_generation/{secretName}
func KeyDesiredSecretGeneration(secretName string) string {
	return fmt.Sprintf("%s/secret_generation/%s", PrefixDesired, secretName)
}

// PrefixDesiredSecretGeneration is the scan prefix of all secret value
// generations; hidden from the raw state API and admin-only to write (M236).
var PrefixDesiredSecretGeneration = KeyDesiredSecretGeneration("")

// InitialSecretGeneration is the value generation of a secret whose value was
// never changed; it adds no config hash line, so hashes survive the upgrade.
const InitialSecretGeneration int64 = 1

// Secret delivery keys (Flow 27, M174).

// KeyObservedInstanceSecretWait returns the store path for why an instance is
// waiting for secrets. Value is JSON: {"reason":"...","secrets":[...],"next_retry":"RFC3339"}.
// Path: observed/instance/{instanceID}/secret-wait
func KeyObservedInstanceSecretWait(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/secret-wait", PrefixObserved, instanceID)
}

// KeyObservedNodeSecretDelivery returns the store path for the node's secret
// delivery protocol version ("v2" once the agent receives secrets from the
// control plane instead of decrypting locally).
// Path: observed/node/{nodeID}/secret_delivery
func KeyObservedNodeSecretDelivery(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/secret_delivery", PrefixObserved, nodeID)
}

// KeyObservedNodeLegacyKeyFile returns the store path for whether the old
// secret master key file is still present on the node ("present" | "absent").
// Path: observed/node/{nodeID}/legacy_key_file
func KeyObservedNodeLegacyKeyFile(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/legacy_key_file", PrefixObserved, nodeID)
}

// KeyDesiredServiceLegacySecretAPI returns the store path for a service's
// opt-in to the deprecated per-instance secret API (CCA_SECRET_TOKEN).
// Path: desired/service/{name}/legacy_secret_api
func KeyDesiredServiceLegacySecretAPI(name string) string {
	return fmt.Sprintf("%s/service/%s/legacy_secret_api", PrefixDesired, name)
}

// KeyObservedNodeGateKeyDelivery returns the store path for the node's
// gate-key delivery capability ("v1" once the agent fetches gate keys through
// the API instead of reading key facts).
// Path: observed/node/{nodeID}/gate_key_delivery
func KeyObservedNodeGateKeyDelivery(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/gate_key_delivery", PrefixObserved, nodeID)
}

// KeyObservedNodeGateProxy returns the store path for whether the node runs
// the gate proxy ("enabled" | "disabled").
// Path: observed/node/{nodeID}/gate_proxy
func KeyObservedNodeGateProxy(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/gate_proxy", PrefixObserved, nodeID)
}

// KeyObservedNodeGateKeys returns the store path for the node's gate-key
// readiness. Value is JSON: {"state":"ready|waiting","reason":"...","pending":[...]}.
// Path: observed/node/{nodeID}/gate_keys
func KeyObservedNodeGateKeys(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/gate_keys", PrefixObserved, nodeID)
}

// KeyObservedGateKeyMigration is the store path of the plaintext gate-key
// cutover status. Value is JSON: {"state":"sealing|waiting-for-agents|completed","plaintext_count":N}.
const KeyObservedGateKeyMigration = PrefixObserved + "/gate-keys/migration"

// GateKeyDeliveryVersion is the gate_key_delivery value reported by agents
// that fetch gate keys through POST /api/node/gate-keys.
const GateKeyDeliveryVersion = "v1"

// GateProxyState reports whether a node runs the gate proxy.
type GateProxyState string

const (
	// GateProxyEnabled means the agent runs the HTTP/HTTPS gate proxy.
	GateProxyEnabled GateProxyState = "enabled"
	// GateProxyDisabled means the agent runs no gate proxy and fetches no gate keys.
	GateProxyDisabled GateProxyState = "disabled"
)

// PrefixKeyring is the store area for the secret keyring (Flow 29): KEK
// versions wrapped by the root key, the active version, the root check and
// the legacy key state. Hidden from /api/state (not in its allowlist).
const PrefixKeyring = "keyring"

// ScanKeyringKEKs is the prefix that lists every KEK version and its state.
const ScanKeyringKEKs = PrefixKeyring + "/kek/"

// KeyKeyringActive holds the KEK version new v2 envelopes are sealed with.
const KeyKeyringActive = PrefixKeyring + "/active"

// KeyKeyringRootCheck holds a fixed value sealed with the root key; a replica
// that cannot open it has a different root key and refuses to start.
const KeyKeyringRootCheck = PrefixKeyring + "/root_check"

// KeyKeyringLegacy records the legacy master key state: "none" (fresh
// cluster), "present" (v1 envelopes may exist) or "retired".
const KeyKeyringLegacy = PrefixKeyring + "/legacy"

// KeyKeyringKEK returns the store path of a KEK version wrapped by the root key.
// Path: keyring/kek/{version}
func KeyKeyringKEK(kekVersion int) string {
	return fmt.Sprintf("%s%d", ScanKeyringKEKs, kekVersion)
}

// KeyKeyringKEKState returns the store path of a KEK version's state
// ("active" | "retiring" | "retired").
// Path: keyring/kek/{version}/state
func KeyKeyringKEKState(kekVersion int) string {
	return fmt.Sprintf("%s%d/state", ScanKeyringKEKs, kekVersion)
}

// KeyKeyringKEKRetiringSince returns the store path of the RFC 3339 time a
// KEK version became retiring; it is deleted after the retire grace (Flow 30).
func KeyKeyringKEKRetiringSince(kekVersion int) string {
	return fmt.Sprintf("%s%d/retiring_since", ScanKeyringKEKs, kekVersion)
}

// PrefixTokenSigning is the keyring area of the cluster's API token signing
// identity (M283, Flow 37); like all of keyring/, never served by an API.
const PrefixTokenSigning = PrefixKeyring + "/token-signing/"

// KeyTokenSigningInitialized is the signing identity's marker, JSON
// {issuer_url, created}: present means the identity was created once and a
// missing key is a loss, never a fresh start.
const KeyTokenSigningInitialized = PrefixTokenSigning + "initialized"

// KeyTokenSigningActive holds the kid of the key that signs new tokens.
const KeyTokenSigningActive = PrefixTokenSigning + "active"

// ScanTokenSigningKeys lists the sealed signing keys; every one verifies.
const ScanTokenSigningKeys = PrefixTokenSigning + "key/"

// KeyTokenSigningKey returns the store path of a signing key: a keyring
// envelope of its PKCS#8 private key, bound to this path.
// Path: keyring/token-signing/key/{kid}
func KeyTokenSigningKey(keyID string) string {
	return ScanTokenSigningKeys + keyID
}

// KeyObservedKeyringMigration holds the legacy key migration status as JSON
// (state, legacy_v1_envelope_count, unreadable_count, ...).
const KeyObservedKeyringMigration = PrefixObserved + "/keyring/migration"

// ScanObservedKeyringReplicas lists the API replicas' keyring reports.
const ScanObservedKeyringReplicas = PrefixObserved + "/keyring/replica/"

// KeyObservedKeyringReplica returns the store path of an API replica's
// keyring report (version, active KEK, root check), written with a lease.
// Path: observed/keyring/replica/{nodeID}
func KeyObservedKeyringReplica(nodeID string) string {
	return ScanObservedKeyringReplicas + nodeID
}

// SecretDeliveryVersion is what an agent reports at
// observed/node/{id}/secret_delivery once it receives secrets from the
// control plane instead of decrypting locally (M174); the M177 migration gate
// requires it from every node.
const SecretDeliveryVersion = "v2"

// Node report attribute names under observed/node/{id}/ that cutover gates
// check (security.NodesNotReporting).
const (
	// NodeReportSecretDelivery holds SecretDeliveryVersion once the agent receives secrets from the control plane.
	NodeReportSecretDelivery = "secret_delivery"
	// NodeReportGateKeyDelivery holds GateKeyDeliveryVersion once the agent fetches gate keys (M176).
	NodeReportGateKeyDelivery = "gate_key_delivery"
	// NodeReportLegacyKeyFile holds LegacyKeyFilePresent or LegacyKeyFileAbsent.
	NodeReportLegacyKeyFile = "legacy_key_file"
)

// Values of observed/node/{id}/legacy_key_file.
const (
	// LegacyKeyFilePresent: /etc/ccattler/secret-master.key exists on the node.
	LegacyKeyFilePresent = "present"
	// LegacyKeyFileAbsent: the legacy key file is gone from the node.
	LegacyKeyFileAbsent = "absent"
)

// KeyIntentKeyringLegacyMigration records the operator's request to migrate
// v1 envelopes to v2 (cca secret migrate-keys, Flow 29 step 5).
const KeyIntentKeyringLegacyMigration = PrefixIntent + "/keyring/legacy_migration"

// KeyIntentKeyringRotation records the operator's request to rotate the KEK
// (cca secret rotate, Flow 30); it exists while a rotation runs.
const KeyIntentKeyringRotation = PrefixIntent + "/keyring/rotation"

// KeyObservedKeyringRotation holds the KEK rotation status as JSON (state,
// target_version, started, ...).
const KeyObservedKeyringRotation = PrefixObserved + "/keyring/rotation"
