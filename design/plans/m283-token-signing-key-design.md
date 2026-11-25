# M283 — Token Signing Key: Design Outline

Outline of the design approved by the user on 2026-11-25 (milestones.md, Phase 283). The store key names are
proposals; the OpenSpec change decides them. Today's code is at the bottom.

## 1. Server start: one shared key, fail closed

```
cca server start (any replica)
│
├─ keyring ready?  keyring/root_check opens with this root key?
│     └─ no → refuse to serve (ErrKeyringRootCheckFailed, security/keyring.go:81)
│
└─ loadOrCreateTokenSigningKeys
     │
     ├─ read  keyring/token-signing/initialized      (the marker)
     │  read  keyring/token-signing/key/<kid> …      (sealed private keys)
     │  read  keyring/token-signing/active           (the kid that signs)
     │
     ├─ marker absent, no keys ── fresh cluster ──────────────────────────────┐
     │                                                                         │
     │   generate ECDSA P-256, kid = hash(public key)                          │
     │   seal: SealEnvelopeForKey(storeKey = keyring/token-signing/key/<kid>)  │
     │   Txn IF rev(marker) == 0                                               │
     │       THEN put marker + key/<kid> + active=<kid>   ── won  → use it     │
     │       ELSE (another replica won)                   ── lost → discard    │
     │            local key, load the winner's set ◄──────────────────────────┘
     │
     ├─ marker present, every key opens ── normal start → load the key set
     │
     └─ marker present, but a key is missing, corrupt or does not open
           → FAIL startup. Never generate a replacement identity.
│
▼
TokenSigner   (active kid's private key, in memory only)
TokenVerifier (every public key in the set, selected by kid)
JWKS          (the same public key set on every replica)
```

## 2. Issue and verify: any replica, any time

```
cca user token alice --ttl 8h
   │
   ▼  replica A
TokenSigner ── TTL ≤ maxTokenTTL? no → 400 ───────────────────────────────┐
   │ yes                                                                    │
   ▼                                                                        │
JWT { header.kid = active, sub = alice, exp, user record revision }        │
                                                                            │
request with that token ──► replica B (or A after a restart)               │
   │                                                                        │
   ├─ 1. signature: header.kid → public key from the shared set            │
   │       unknown kid or bad signature → 401                              │
   ├─ 2. M216: user record revision still matches?                         │
   │       user deleted or recreated → 401                                 │
   └─ 3. principal user:alice → capability check as today                  │
```

Signature validity (step 1) and user revocation (step 2) stay separate checks.

## 3. Rotation: coordinated in the store, retention by lifetime

```
time ───────────────────────────────────────────────────────────────────────►

K1 ███████████████ signs ███████████████│ verify only ░░░░░░░░░░░░░░░░│ removed
                                        │                              │
K2              added (verify only) ░░░░│████████ signs ████████████████████████
                     │                  │                              │
          every replica has loaded K2   K2 becomes active      last K1 token
          (before it may sign)          (one store write)      + maxTokenTTL
                                                               + clock skew
                                                               + JWKS cache time
```

- One active key signs. The others only verify.
- No replica picks the active key on its own; the store holds it.
- A key is removed only when no token signed by it can still be valid. More than one old key can be held if
  rotations come faster than the retention period.
- This milestone covers only the multi-key verification contract and its tests. An operator rotation command
  comes later.

## 4. Where the key may and may not go

```
                    ┌──────────── control plane only ────────────┐
etcd (sealed) ────► │ cca server: TokenSigner, TokenVerifier     │ ──► JWKS: public keys only
                    └────────────────────────────────────────────┘
        ✗ agents   ✗ secret APIs   ✗ /api/state   ✗ status   ✗ logs   ✗ audit records
```

A test proves that the private key appears in none of these.

## 5. Today, for comparison (the bug)

```
replica A start ── ecdsa.GenerateKey ──► key KA (memory only)    workload_token.go:31, command_server.go:658
replica B start ── ecdsa.GenerateKey ──► key KB (memory only)
restart of A    ── ecdsa.GenerateKey ──► key KA' (KA is lost)

token signed by KA ──► B: 401   ──► A after restart: 401   ──► JWKS differs per replica and per restart
```

## 6. Acceptance tests

| Test | Result |
|---|---|
| Concurrent first start | Every replica converges on one key |
| Restart | A token minted before the restart is still valid |
| Replica A → B | A's token is accepted by B, and B's by A |
| JWKS | Every replica publishes the same key set |
| Rotation | New tokens carry the new kid; old tokens verify until they expire |
| Retention | An old public key is never removed while its tokens can be valid |
| Lost or corrupt key, wrong root key | Startup is refused; no new key is generated |
| Private key confinement | The key appears in no agent, API, status, log or audit output |
| User deletion or recreation | Old tokens are rejected (M216) |
| Maximum TTL | A request above the maximum is refused |
| Testbed | An `e2e-admin` `CCA_TOKEN` works across server restarts and replicas (the guide's step 3) |
