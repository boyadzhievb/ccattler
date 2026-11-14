# CCattler Secret Encryption Call Graph (SECRETS)

Envelope encryption, key providers (local root key file, AWS KMS, GCP Cloud KMS),
the keyring (KEKs, migration, rotation), the secret store and node secret delivery.
Who may read or deliver a secret is decided as described in [AUTHZ_GRAPH.md](AUTHZ_GRAPH.md).

## Secret Encryption & Key Providers

### Envelope Encryption (security/key_provider.go)

```
SealEnvelope(ctx, keyProvider, plaintext)                      [:97]
├── GenerateDEK() → 32-byte random AES-256 key                [:82]
├── encryptWithDEK(dek, plaintext)                             [:162]
│   └── AES-256-GCM: nonce + Seal(plaintext)
├── keyProvider.WrapKey(ctx, dek) → wrappedDEK
└── packEnvelope(wrappedDEK, ciphertext)                       [:139]
    └── [4-byte len (big-endian)][wrappedDEK][ciphertext]

OpenEnvelope(ctx, keyProvider, envelope)                       [:118]
├── unpackEnvelope(envelope) → wrappedDEK, ciphertext          [:148]
├── keyProvider.UnwrapKey(ctx, wrappedDEK) → dek
└── decryptWithDEK(dek, ciphertext)                            [:167]
    └── AES-256-GCM: Open(nonce, ciphertextBody)
```

Re-wrapping under a new KEK is done by the keyring rotation (Flow 30), not here.

### KeyProvider implementations

| Provider          | File                             | Build Tag     | Key Material                  |
|-------------------|----------------------------------|---------------|-------------------------------|
| LocalKeyProvider  | security/key_provider.go:46      | (default)     | 32-byte AES-256 on disk       |
| AWSKMSKeyProvider | security/cloud_key_providers.go  | cloudprovider | AWS KMS key ARN/alias         |
| GCPKMSKeyProvider | security/cloud_key_providers.go  | cloudprovider | GCP Cloud KMS resource name   |

Cloud providers register by name ("aws-kms", "gcp-kms") through `RegisterCloudKeyProviderFactory`
(security/cloud_key_provider_registry.go); `cca server --root-key-provider` looks them up.

### Secret keyring and envelope codec (M177 steps 1–2, Flow 29)

```
executeServerCommand [cmd/cca/command_server.go:343]
  keyringCodec := startSecretEnvelopeCodec(ctx, factStore, cfg); NewSecretStore(factStore, keyringCodec)
  keyringCodec → startControllersWithLeaderElection (route-tls, gate-key-migration, keyring-migration, keyring-rotation)
               → configureAndStartAPIServer → SetKeyringAdministration(keyringCodec, version)
startSecretEnvelopeCodec                                [cmd/cca/server_secret_keys.go:28]
├── loadSecretKeyProviders(ctx, cfg)
│   ├── store = memory → newEphemeralSecretKeyProvider(), no legacy   [never on disk]
│   ├── LoadLegacySecretMasterKeyFile(/etc/ccattler/secret-master.key)  [missing → nil legacy]
│   └── loadSecretRootKeyProvider(ctx, cfg, legacyKey)
│       ├── --root-key-provider → loadCloudRootKeyProvider     [LookupCloudKeyProviderFactory; wrap/unwrap probe]
│       └── else LoadSecretRootKeyFile + ValidateSecretRootKeyDistinctFromLegacy → NewLocalKeyProvider
├── NewKeyringEnvelopeCodec(ctx, factStore, root, legacy)   [security/envelope_codec.go]
│   ├── ensureKeyring                                    [security/keyring.go; every bootstrap key compared rev 0]
│   │   refuseKeyringRebootstrap: other keyring/ keys or v2 envelopes without keyring/active → error
│   │   writes keyring/kek/1, kek/1/state, active, root_check, legacy, observed/keyring/migration
│   │   fresh (no legacy, no envelopes) → legacy none; else present
│   └── Refresh (serialized) → loadKeyringSnapshot (root_check, unwrap KEKs, active, legacy state)
├── go RunKeyringWatch(ctx) [security/envelope_codec.go:163] [subscribe keyring/, then refreshKeyringFromWatch once (no gap between load and watch);
│                                                          closed watch → re-subscribe; 60s fallback;
│                                                          ErrKeyringRootCheckFailed (root key rotated) → exitWithSecretKeyError]
├── go RunKeyringReplicaReporter(ctx, nodeID, version)   [observed/keyring/replica/{id}: rewritten every 10s, lease 30s]
└── any error → exitWithSecretKeyError                   [no silent key generation]

keyring/legacy (authority): none | present → migrating → retired   [advanceLegacyKeyState, security/keyring_migration.go:112]
KeyringEnvelopeCodec.SealEnvelopeForKey [security/envelope_codec.go:111] → legacy present ? SealEnvelope(legacy) : sealEnvelopeV2(active KEK, AAD=storeKey)
KeyringEnvelopeCodec.OpenEnvelopeForKey [:125] → isEnvelopeV2 ? openEnvelopeV2Payload(KEK[version]) : legacy (refused once retired)
Callers: SecretStore.PutSecret/GetSecretThroughStore, CredentialStore, SealSecretValue(codec, storeKey, ...),
         RouteTLSController and GateKeyMigrationController (the server passes keyringCodec directly)

KEK rotation gate (M178 step 1, Flow 30 step 1):
cca secret rotate → executeSecretRotate → postSecretAdminRequest
POST /api/secret/rotate → handleSecretRotate [api/keyring_migration.go:83; secret.write at cluster via beginKeyringRequest]
├── CheckKeyringRotationGate(buildVersion)               [security/keyring_rotation.go:84]
│   ├── keyring/legacy = none | retired (AllowsKEKRotation [:68])
│   ├── fewer than keyringMaxRetainedKEKVersions (8) KEK versions
│   ├── intent/keyring/rotation absent (one rotation at a time)
│   └── checkReplicaReports(version, "") → observed/keyring/replica/*
├── unmet → writeKeyringGateRefusal (409 {error, unmet[]})
└── StartKeyringRotation(principal) [:111] → Refresh + verifyKeyringRootCheck, then
    txn [keyring/active, keyring/legacy, keyring/root_check revs; intent create-only]
    put intent/keyring/rotation {target_version = active + 1} + observed/keyring/rotation {state: in_progress}
    ErrKeyringRotationConflict → 409; any other error → 500
cca secret keyring-status → executeSecretKeyringStatus → fetchStateFactsWithPrefix(observed/keyring/) [GET /api/state]
KeyringRotationController.Reconcile [controllers/keyring_rotation.go:92] (leader; Watch keyring/, intent/keyring/rotation,
  observed/keyring/rotation, observed/keyring/replica/, secrets/, credentials/; --keyring-retire-grace, default 168h, min 1h)
├── parseKeyringRotationView [:294] → recordKEKFact [:330] (keyring/kek/{v}, /state, /retiring_since; keyring/legacy)
├── no intent ∧ no non-active version (hasNonActiveKEKVersion [:273]) → nothing
├── intent ∧ active < target → advanceToTargetKEK [:124]
│   ├── target ≠ active + 1 or legacy not none|retired → ignored (warn; forged intent)
│   ├── kek/{target} absent → createTargetKEK [:141] → CreateWrappedKEK [security/keyring_rotation.go:206] (Refresh first)
│   │   put kek/{target} + kek/{target}/state = pending (runner: create-only)
│   └── else → ProveKEKVersion [security/keyring_rotation.go:197] → switchActiveKEK [:155]:
│       put keyring/active = target, target active, previous draining (one group)
├── PlanKEKRewrapBatch(facts, active, 40) [security/keyring_rotation.go:225]
│   ├── rewrapEnvelopeV2 [security/envelope_v2.go:85] (DEK re-wrapped; nonce, ciphertext, AAD unchanged)
│   └── unreadable → recordUnreadable [:188] (count + first 10 keys)
├── no rewrites → planVersionTransitions [:169] (ReplicasNotOnKEK [security/keyring_rotation.go:289])
│   ├── draining, 0 envelopes, every report on active → retiring + retiring_since
│   ├── retiring, named again → draining
│   └── retiring, every report on active, grace elapsed (retireGraceElapsed [:212]) → delete kek/{v}, /state, /retiring_since
└── planRotationStatus [:221]: rewrapped, remaining, unreadable_count, unreadable_keys;
    no rewrites ∧ no draining version awaiting reports (hasDrainingKEKVersionAwaitingReplicas [:261]) →
    delete intent, state completed (unreadable-only versions stay draining); unchanged status not written
Write domain: keyring-rotation → keyring/kek/, keyring/active, secrets/, credentials/, intent/keyring/rotation, observed/keyring/rotation

Legacy key migration (M177 step 3, Flow 29 steps 5–6):
cca secret migrate-keys --confirm-backup → executeSecretMigrateKeys → postSecretAdminRequest
POST /api/secret/migrate-keys → handleSecretMigrateKeys [api/keyring_migration.go:141; cluster.admin via beginKeyringAdministration]
├── CheckLegacyMigrationGate(req, buildVersion)          [security/keyring_migration.go:34]
│   ├── confirm_backup set; keyring/legacy = present
│   ├── checkReplicaReports(version, "") → observed/keyring/replica/* (version, active KEK, root_check ok)
│   └── NodesNotReporting(observed/node/, secret_delivery, v2)   [security/node_reports.go; registered nodes only]
├── unmet → writeKeyringGateRefusal (409 {error, unmet[]})
└── StartLegacyMigration(principal) [:93] → advanceLegacyKeyState(present → migrating,
    + intent/keyring/legacy_migration + migration {state: in_progress}); writes are v2
KeyringMigrationController.Reconcile [controllers/keyring_migration.go:63] (leader; Watch secrets/, credentials/, keyring/legacy, observed/keyring/migration)
├── keyring/legacy ≠ migrating → no changes
├── PlanLegacyMigrationBatch(facts, 50) [security/keyring_migration.go:149]   [v1 → open(legacy) → sealEnvelopeV2(AAD = key)]
│   unreadable v1 counted, never rewritten or deleted
└── puts rewrites (runner revision guard per key) + migration {migrated, legacy_v1_envelope_count, unreadable_count}
    count 0 → completed; v1 found while completed → in_progress again
Write domain: keyring-migration → secrets/, credentials/, observed/keyring/migration   [controllers/topological_sort.go]

Legacy key retirement (M177 step 4, Flow 29 step 7):
cca secret retire-legacy-key → executeSecretRetireLegacyKey → postSecretAdminRequest
POST /api/secret/retire-legacy-key → handleSecretRetireLegacyKey [api/keyring_migration.go:115; cluster.admin]
├── CheckLegacyRetirementGate [security/keyring_retirement.go:24]
│   ├── keyring/legacy = migrating; migration completed with count 0
│   ├── checkReplicaReports("", migrating) → every live replica acts on migrating (seals v2)
│   └── checkRetirementStoreConditions
│       ├── countV1Envelopes (full scan secrets/ + credentials/)
│       ├── plaintext desired|derived/route/*/tls/key facts
│       └── nodesHoldingLegacyKeyFile = NodesNotReporting(legacy_key_file, absent) − API replicas
├── unmet → writeKeyringGateRefusal (409)
└── RetireLegacyKey [:112] → advanceLegacyKeyState(migrating → retired)   [guards keyring/legacy + migration revisions]
Every replica: Refresh sees retired → legacyKeyProvider = nil (warn); OpenEnvelopeForKey(v1) refused
Ansible: worker role removes secret-master.key on non-control-plane hosts; control-plane roles only with
  retire_legacy_secret_key=true (HA: also secret-master.key.<ts>~ backups); HA stages keys in a per-run 0700 tempdir, deleted after use

cca secret generate-root-key [--path]  → GenerateSecretRootKeyFile (O_EXCL, 0400, fsync)
Root key rotation (M178 Flow 30 part B; driven by deploy/ansible/rotate-root-key.yml):
cca secret root-key-status [--path] → executeSecretRootKeyStatus [cmd/cca/server_secret_keys.go:207] (read-only)
└── InspectRootKeyFiles [security/root_key_rotation.go:114] → loadRootKeyFileState [:255] (path, path.next)
    → stage idle | staged | committed | invalid; old present?
cca secret rotate-root-key --commit|--finish [--path] → executeSecretRotateRootKey [cmd/cca/server_secret_keys.go:173]
├── openLocalControlPlaneStore (resolveStoreDefaultsForDaemon) ; WaitForStoppedKeyringReplicas(45s) [:78]
├── --commit → CommitRootKeyRotation(path, legacy key) [:142]
│   ├── committed → finishStagedRootKey [:207] (swap only)
│   ├── refuse: not staged, .old present, loadValidatedRootKeyPair [:226] (.next ≠ current, ≠ legacy),
│   │   liveKeyringReplicas re-read [:100]
│   ├── rewrapKeyringUnderRootKey [:281] → buildRootKeyRewrapTransaction [:298]
│   │   intent/keyring/rotation must be absent; rewrapWithProof [:351] (wrap + unwrap dry run)
│   │   ONE txn [revisions: every keyring/kek/{v}, root_check, keyring/active;
│   │            absent: intent/keyring/rotation, keyring/kek/{max+1}, keyring/kek/{max+1}/state]
│   │   put keyring/kek/{v} = wrap(next, unwrap(current, ·)), put root_check = wrap(next, fixed value)
│   └── swapRootKeyFiles [:365]: path → path.old, path.next → path, syncDirectory [:377]
└── --finish → FinishRootKeyRotation [:184]: committed → finishStagedRootKey; idle → no-op; staged → refuse
Playbook: rotate-root-key.yml finishes with .old shredded or archived as secret-root.key.retired-<time>,
  shreds /tmp/cca-init/secret-root.key; both control-plane roles shred cca init's key copies after setup.sh
Stale-replica fence: CreateWrappedKEK and StartKeyringRotation call Refresh first (root_check must open)
cca init (first node)                  → writeInitSecretKeys → secret-root.key only (new clusters start on v2)
cca init --ca (HA secondary)           → no secret keys; Ansible copies the first node's files
```

### SecretStore (security/secrets.go)

```
SecretStore.PutSecret(ctx, name, plaintext)                    [:58]
├── SealEnvelope(ctx, keyProvider, plaintext)
├── base64.StdEncoding.EncodeToString(envelope)
└── factStore.Put("secrets/<name>", encoded)

SecretStore.GetSecret(ctx, name)                               [:77]
└── GetSecretThroughStore(ctx, factStore, name)

SecretStore.GetSecretThroughStore(ctx, readStore, name)        [:84, M174]
├── readStore.Get("secrets/<name>")                            [e.g. store bound to service:secret-delivery]
├── base64.StdEncoding.DecodeString(value)
└── OpenEnvelope(ctx, keyProvider, envelope)                   [current provider, shared with rotation]

SecretStore.DeleteSecret(ctx, name)                            [:101]
└── factStore.Delete("secrets/<name>")

SecretStore.ListSecrets(ctx)                                   [:107]
└── factStore.Scan("secrets/") → trim prefix → names

SecretStore.IsAuthorized(ctx, serviceName, secretName)         [:123]
└── factStore.Get("desired/service/{svc}/secret/{name}")       [grant check]

SecretStore.GetSecretForService(ctx, svc, secret)              [:134]
├── IsAuthorized → check grant
├── factStore.Get(grantKey) → mountPath
└── GetSecret → plaintext
```

Key rotation re-wraps DEKs through the keyring rotation controller (Flow 30, above), not through SecretStore.

### Node Secret Delivery (security/secret_delivery.go, api/node_secret_delivery.go; Flow 27, M174)

Wired by `configureSecretStoreAndDelivery` (cmd/cca/command_server.go): `AuthorizedStore`
with service principal `service:secret-delivery` (role `secret-delivery`, read-only).
`bootstrapControlPlaneNodeEnrollment` → `security.EnsureControlPlaneNodeEnrolled` writes
`enrollment/node/{server cert CN}` create-only at startup: the control-plane node got its
certificate from `cca init`, never ran `cca join`, and its agent shares that certificate.

```
Server.handleNodeSecretDelivery  POST /api/node/secrets {instance_id}
├── requireNodeMTLSCaller: node principal AND auth method == mtls AND request.TLS != nil   [else 403]
├── SecretDeliveryService.DeliverInstanceSecrets(ctx, nodeID=cert CN, instanceID)
│   ├── authorizeNodeForInstance
│   │   ├── getOptionalDeliveryFact                           [missing → refusal; other store error → 500, not audited]
│   │   ├── checkDeliveryNode (shared with gate-key delivery)
│   │   │   ├── Get("enrollment/node/{node}")                  [not enrolled → not_authorized]
│   │   │   └── Get("observed/node/{node}/state") == disabled  [→ not_authorized]
│   │   ├── Get("placement/instance/{id}") == node             [→ not_authorized]
│   │   ├── Get("observed/instance/{id}/service") → service    [agent-writable until M181]
│   │   └── checkInstanceIsServiceInstance
│   │       ├── Get("derived/instance/{id}/job_owner")         [present → not_authorized: a job named like a service]
│   │       └── Get("desired/service/{svc}")                   [missing → not_authorized: service not declared]
│   ├── resolveRequiredSecretNames(service)
│   │   ├── Scan("desired/service/{svc}/secret/") → granted names
│   │   ├── collectReferencedSecretNames                       [connection secrets, image_pull_secret]
│   │   └── referenced but not granted → not_authorized (names)
│   │   └── granted name is system-owned (route/…, types.IsSystemOwnedSecretName) → not_authorized
│   ├── SecretStore.GetSecretThroughStore(deliveryStore, name)  [missing → secret_missing, all-or-nothing]
│   └── auditDecision / auditRefusal                           [names only; refusals rate-limited 1/min per node|instance|reason]
└── writeSecretDeliveryRefusal                                 [403 not_authorized, 409 secret_missing]
```

Gate-key delivery (`POST /api/node/gate-keys`, Flow 28, M176) uses the same store principal
(role `secret-delivery` also reads `desired/route/`, `derived/route/`), `requireNodeMTLSCaller`,
`checkDeliveryNode` and `refusalAuditLimiter`; it additionally requires
`observed/node/{id}/gate_proxy = enabled`. Call tree: NETWORK_GRAPH.md "Gate-key delivery to agents".
`/api/secret` refuses get/set/delete of system-owned `route/…` names (403) and hides them from the
list and from `/api/status`; secret rotation still re-wraps them. The compiler rejects services and `key secret` referencing them.
`RequiredCapabilityForFactWrite`: `desired/route/{gate}/tls/key_secret` needs `secret.use` (like service secret grants).
Server-side `lang.Diff(..., allowLocalFiles=false)`, `applyDirect` and the policy gate call `lang.ValidateNoLocalFileReferences` before compiling (no server file reads).

---

## Store Key Patterns

### Reads (security layer)
| Key Pattern | Where Read |
|---|---|
| `secrets/{name}` | SecretStore.GetSecret |
| `desired/service/{svc}/secret/{name}` | SecretStore.IsAuthorized |

### Writes (security layer)
| Key Pattern | Where Written |
|---|---|
| `secrets/{name}` | SecretStore.PutSecret, keyring migration and rotation controllers |

---

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `SealEnvelope` | SecretStore.PutSecret |
| `OpenEnvelope` | SecretStore.GetSecret |
| `GenerateDEK` | SealEnvelope |
| `encryptWithDEK` | SealEnvelope, LocalKeyProvider.WrapKey |
| `decryptWithDEK` | OpenEnvelope, LocalKeyProvider.UnwrapKey |
| `packEnvelope` | SealEnvelope |
| `unpackEnvelope` | OpenEnvelope |

---

## Timing Constants

| Constant | Value | File:Line |
|---|---|---|
| dekLength | 32 bytes | security/key_provider.go:40 |

---

## File Index

| File | Purpose |
|---|---|
| security/key_provider.go:97 | SealEnvelope — envelope encrypt |
| security/key_provider.go:118 | OpenEnvelope — envelope decrypt |
| security/secrets.go:58 | SecretStore.PutSecret |
| security/secrets.go:77 | SecretStore.GetSecret |
