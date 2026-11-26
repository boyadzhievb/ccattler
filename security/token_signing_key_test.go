// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// tokenSigningTestIssuerURL is the issuer the test clusters store at creation.
const tokenSigningTestIssuerURL = "https://ctrl-1:9770" //nolint:gosec // G101: an issuer URL, not a credential

// tokenSigningTestReplicaCount is how many replicas start at once in the
// concurrent first-start tests (the spec's four).
const tokenSigningTestReplicaCount = 4

// newTokenSigningTestReplicas creates one store with a ready keyring and one
// keyring codec per replica; the keyring is created by the first codec, so
// only the token signing bootstrap races.
func newTokenSigningTestReplicas(t *testing.T, factStore store.StateStore, replicaCount int) []*KeyringEnvelopeCodec {
	t.Helper()
	rootKeyProvider := newTestKeyProvider(t)
	replicaCodecs := make([]*KeyringEnvelopeCodec, replicaCount)
	for replicaIndex := range replicaCodecs {
		keyringCodec, codecError := NewKeyringEnvelopeCodec(context.Background(), factStore, rootKeyProvider, nil)
		if codecError != nil {
			t.Fatal(codecError)
		}
		replicaCodecs[replicaIndex] = keyringCodec
	}
	return replicaCodecs
}

// startTokenSigningReplicasOnStores runs LoadOrCreateTokenSigningIdentity
// for every replica concurrently (replica i uses replicaStores[i] and
// replicaCodecs[i]), each with its own default issuer URL.
func startTokenSigningReplicasOnStores(t *testing.T, replicaStores []store.StateStore, replicaCodecs []*KeyringEnvelopeCodec) []*TokenSigningIdentity {
	t.Helper()
	identities := make([]*TokenSigningIdentity, len(replicaCodecs))
	startGate := make(chan struct{})
	var waitGroup sync.WaitGroup
	for replicaIndex, keyringCodec := range replicaCodecs {
		waitGroup.Add(1)
		go func(replicaIndex int, keyringCodec *KeyringEnvelopeCodec) {
			defer waitGroup.Done()
			<-startGate
			defaultIssuerURL := fmt.Sprintf("https://ctrl-%d:9770", replicaIndex+1)
			identity, loadError := LoadOrCreateTokenSigningIdentity(context.Background(), replicaStores[replicaIndex], keyringCodec, "", defaultIssuerURL)
			if loadError != nil {
				t.Errorf("replica %d: %v", replicaIndex, loadError)
				return
			}
			identities[replicaIndex] = identity
		}(replicaIndex, keyringCodec)
	}
	close(startGate)
	waitGroup.Wait()
	return identities
}

// assertReplicasConvergedOnOneIdentity checks the store holds one marker and
// one key, and every replica signs with that key's kid and the stored issuer.
func assertReplicasConvergedOnOneIdentity(t *testing.T, factStore store.StateStore, identities []*TokenSigningIdentity) {
	t.Helper()
	ctx := context.Background()
	keyFacts, scanError := factStore.Scan(ctx, types.ScanTokenSigningKeys)
	if scanError != nil || len(keyFacts) != 1 {
		t.Fatalf("signing keys in the store = %d, %v; want exactly 1", len(keyFacts), scanError)
	}
	markerFact, markerError := factStore.Get(ctx, types.KeyTokenSigningInitialized)
	if markerError != nil {
		t.Fatalf("marker: %v", markerError)
	}
	var storedMarker struct {
		IssuerURL string `json:"issuer_url"` // IssuerURL is the cluster issuer stored at creation.
	}
	if unmarshalError := json.Unmarshal(markerFact.Value, &storedMarker); unmarshalError != nil {
		t.Fatal(unmarshalError)
	}
	storedKeyID := keyFacts[0].Key[len(types.ScanTokenSigningKeys):]
	for replicaIndex, identity := range identities {
		if identity == nil {
			t.Fatalf("replica %d has no identity", replicaIndex)
		}
		if identity.ActiveKeyID() != storedKeyID || identity.IssuerURL() != storedMarker.IssuerURL {
			t.Errorf("replica %d signs with kid %s, issuer %s; the store holds %s, %s",
				replicaIndex, identity.ActiveKeyID(), identity.IssuerURL(), storedKeyID, storedMarker.IssuerURL)
		}
	}
}

// TestTokenSigningKeyConcurrentFirstStartConverges: four replicas starting at
// once on an empty store end with exactly one key and one marker, and all
// sign with the same kid and issuer (spec: concurrent first start).
func TestTokenSigningKeyConcurrentFirstStartConverges(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	replicaCodecs := newTokenSigningTestReplicas(t, factStore, tokenSigningTestReplicaCount)
	replicaStores := make([]store.StateStore, len(replicaCodecs))
	for replicaIndex := range replicaStores {
		replicaStores[replicaIndex] = factStore
	}
	identities := startTokenSigningReplicasOnStores(t, replicaStores, replicaCodecs)
	if t.Failed() {
		return
	}
	assertReplicasConvergedOnOneIdentity(t, factStore, identities)
}

// winnerCommittingStore commits another replica's signing identity right
// after this replica's first read of the token-signing area, so this
// replica decides on a stale read and loses the create-only transaction.
type winnerCommittingStore struct {
	store.StateStore              // StateStore is the shared store.
	commitWinnerOnce sync.Once    // commitWinnerOnce fires the winner's bootstrap once.
	commitWinner     func() error // commitWinner runs the other replica's whole bootstrap.
	winnerError      error        // winnerError is the winner's bootstrap error.
	transactionCount int          // transactionCount counts this replica's transactions.
	transactionMutex sync.Mutex   // transactionMutex guards transactionCount.
}

// Scan reads the area, then lets the winner commit before returning the
// (now stale) result, the M284 interleaving.
func (racingStore *winnerCommittingStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	facts, scanError := racingStore.StateStore.Scan(ctx, prefix)
	if prefix == types.PrefixTokenSigning {
		racingStore.commitWinnerOnce.Do(func() { racingStore.winnerError = racingStore.commitWinner() })
	}
	return facts, scanError
}

// Transaction counts this replica's transactions and forwards them.
func (racingStore *winnerCommittingStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	racingStore.transactionMutex.Lock()
	racingStore.transactionCount++
	racingStore.transactionMutex.Unlock()
	return racingStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
}

// TestTokenSigningKeyLostRaceLoadsWinner: a replica whose read happened just
// before another replica committed loses the create-only transaction, then
// re-reads and uses the winner's key and issuer; it never reports the
// signing state as incomplete and never keeps its own key.
func TestTokenSigningKeyLostRaceLoadsWinner(t *testing.T) {
	ctx := context.Background()
	sharedStore := store.NewMemoryStore()
	defer sharedStore.Close()
	replicaCodecs := newTokenSigningTestReplicas(t, sharedStore, 2)
	var winnerIdentity *TokenSigningIdentity
	racingStore := &winnerCommittingStore{StateStore: sharedStore}
	racingStore.commitWinner = func() error {
		var winnerError error
		winnerIdentity, winnerError = LoadOrCreateTokenSigningIdentity(ctx, sharedStore, replicaCodecs[0], "", "https://winner:9770")
		return winnerError
	}

	loserIdentity, loserError := LoadOrCreateTokenSigningIdentity(ctx, racingStore, replicaCodecs[1], "", "https://loser:9770")
	if racingStore.winnerError != nil {
		t.Fatalf("winner: %v", racingStore.winnerError)
	}
	if loserError != nil {
		t.Fatalf("the replica that lost the race refused to start: %v", loserError)
	}
	if racingStore.transactionCount != 1 {
		t.Fatalf("the losing replica ran %d transactions, want exactly 1 (its lost create)", racingStore.transactionCount)
	}
	if loserIdentity.ActiveKeyID() != winnerIdentity.ActiveKeyID() || loserIdentity.IssuerURL() != "https://winner:9770" {
		t.Fatalf("loser signs with kid %s issuer %s; winner kid %s issuer https://winner:9770",
			loserIdentity.ActiveKeyID(), loserIdentity.IssuerURL(), winnerIdentity.ActiveKeyID())
	}
	assertReplicasConvergedOnOneIdentity(t, sharedStore, []*TokenSigningIdentity{winnerIdentity, loserIdentity})
}

// tokenSigningTestKeySet is a two-key signing set written to the store
// directly: K1 no longer signs, K2 is active.
type tokenSigningTestKeySet struct {
	factStore    store.StateStore      // factStore holds the keyring and the key set.
	keyringCodec *KeyringEnvelopeCodec // keyringCodec sealed both keys.
	olderKey     *ecdsa.PrivateKey     // olderKey is K1, verify only.
	activeKey    *ecdsa.PrivateKey     // activeKey is K2, the signing key.
}

// writeTokenSigningTestKey seals signingKey for its key ID and puts it.
func writeTokenSigningTestKey(t *testing.T, factStore store.StateStore, keyringCodec *KeyringEnvelopeCodec, signingKey *ecdsa.PrivateKey) string {
	t.Helper()
	ctx := context.Background()
	keyID := computeKeyID(&signingKey.PublicKey)
	privateKeyDER, marshalError := x509.MarshalPKCS8PrivateKey(signingKey)
	if marshalError != nil {
		t.Fatal(marshalError)
	}
	sealedKey, sealError := keyringCodec.SealEnvelopeForKey(ctx, types.KeyTokenSigningKey(keyID), privateKeyDER)
	if sealError != nil {
		t.Fatal(sealError)
	}
	if _, putError := factStore.Put(ctx, types.KeyTokenSigningKey(keyID), sealedKey); putError != nil {
		t.Fatal(putError)
	}
	return keyID
}

// newTwoKeyTokenSigningSet writes K1 and K2, the marker and active = K2.
func newTwoKeyTokenSigningSet(t *testing.T) *tokenSigningTestKeySet {
	t.Helper()
	ctx := context.Background()
	keyringCodec, factStore := newFreshKeyringCodec(t)
	keySet := &tokenSigningTestKeySet{factStore: factStore, keyringCodec: keyringCodec}
	keySet.olderKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keySet.activeKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	writeTokenSigningTestKey(t, factStore, keyringCodec, keySet.olderKey)
	activeKeyID := writeTokenSigningTestKey(t, factStore, keyringCodec, keySet.activeKey)
	markerJSON, _ := json.Marshal(map[string]string{"issuer_url": tokenSigningTestIssuerURL, "created": time.Now().UTC().Format(time.RFC3339)})
	factStore.Put(ctx, types.KeyTokenSigningInitialized, markerJSON)
	factStore.Put(ctx, types.KeyTokenSigningActive, []byte(activeKeyID))
	return keySet
}

// putTestUser writes auth/user/{name} and returns its revision.
func putTestUser(t *testing.T, factStore store.StateStore, userName string) int64 {
	t.Helper()
	userRevision, putError := factStore.Put(context.Background(), types.KeyAuthUser(userName), []byte(`{"team":"payments"}`))
	if putError != nil {
		t.Fatal(putError)
	}
	return userRevision
}

// authenticateBearerToken presents token to authenticator as an API request.
func authenticateBearerToken(authenticator Authenticator, token string) (*AuthenticationResult, error) {
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return authenticator.Authenticate(request)
}

// mintTestUserToken signs a user token for userName with signingKey under the
// test issuer, with the key's own kid.
func mintTestUserToken(t *testing.T, signingKey *ecdsa.PrivateKey, userName string, userRevision int64) string {
	t.Helper()
	token, mintError := NewUserTokenIssuerWithKey(tokenSigningTestIssuerURL, signingKey).MintUserToken(userName, "", nil, time.Hour, userRevision)
	if mintError != nil {
		t.Fatal(mintError)
	}
	return token
}

// TestInternalAuthenticatorSelectsKeyByKid: with K1 and active K2 in the
// store, a token signed by K1 and a new token are both accepted, and the new
// token carries K2's kid (spec: two keys).
func TestInternalAuthenticatorSelectsKeyByKid(t *testing.T) {
	keySet := newTwoKeyTokenSigningSet(t)
	identity, loadError := LoadOrCreateTokenSigningIdentity(context.Background(), keySet.factStore, keySet.keyringCodec, "", "https://ignored:9770")
	if loadError != nil {
		t.Fatal(loadError)
	}
	userRevision := putTestUser(t, keySet.factStore, "alice")
	internalAuthenticator := identity.BuildInternalTokenAuthenticator(keySet.factStore)

	olderToken := mintTestUserToken(t, keySet.olderKey, "alice", userRevision)
	newToken, mintError := identity.BuildUserTokenIssuer().MintUserToken("alice", "", nil, time.Hour, userRevision)
	if mintError != nil {
		t.Fatal(mintError)
	}
	if newKeyID := unverifiedTestTokenKeyID(t, newToken); newKeyID != computeKeyID(&keySet.activeKey.PublicKey) {
		t.Fatalf("new token kid = %s, want K2 %s", newKeyID, computeKeyID(&keySet.activeKey.PublicKey))
	}
	for tokenName, token := range map[string]string{"token signed by K1": olderToken, "new token (K2)": newToken} {
		result, authError := authenticateBearerToken(internalAuthenticator, token)
		if authError != nil || result == nil || result.Principal != "user:alice" {
			t.Errorf("%s: result %+v, error %v; want user:alice", tokenName, result, authError)
		}
	}
}

// unverifiedTestTokenKeyID returns the kid from a JWT header.
func unverifiedTestTokenKeyID(t *testing.T, token string) string {
	t.Helper()
	headerSegment, _, _ := strings.Cut(token, ".")
	headerJSON, decodeError := base64.RawURLEncoding.DecodeString(headerSegment)
	if decodeError != nil {
		t.Fatal(decodeError)
	}
	var header map[string]string
	if unmarshalError := json.Unmarshal(headerJSON, &header); unmarshalError != nil {
		t.Fatal(unmarshalError)
	}
	return header["kid"]
}

// TestJWKSPublishesEveryKey: the JWKS of an identity with K1 and K2 lists
// both kids with their public keys, in kid order.
func TestJWKSPublishesEveryKey(t *testing.T) {
	keySet := newTwoKeyTokenSigningSet(t)
	identity, loadError := LoadOrCreateTokenSigningIdentity(context.Background(), keySet.factStore, keySet.keyringCodec, "", "https://ignored:9770")
	if loadError != nil {
		t.Fatal(loadError)
	}
	jwksDocument := identity.BuildWorkloadTokenIssuer().JWKSDocument()
	wantKeys := map[string]*ecdsa.PublicKey{
		computeKeyID(&keySet.olderKey.PublicKey):  &keySet.olderKey.PublicKey,
		computeKeyID(&keySet.activeKey.PublicKey): &keySet.activeKey.PublicKey,
	}
	if len(jwksDocument.Keys) != len(wantKeys) {
		t.Fatalf("JWKS has %d keys, want %d: %+v", len(jwksDocument.Keys), len(wantKeys), jwksDocument.Keys)
	}
	for keyIndex, publishedKey := range jwksDocument.Keys {
		wantKey, isKnown := wantKeys[publishedKey.KeyID]
		if !isKnown {
			t.Fatalf("JWKS publishes unknown kid %s", publishedKey.KeyID)
		}
		parsedKey, _, parseError := parseJWKEntry(map[string]interface{}{"kty": publishedKey.KeyType, "crv": publishedKey.Curve, "x": publishedKey.X, "y": publishedKey.Y, "kid": publishedKey.KeyID})
		if parseError != nil || !parsedKey.Equal(wantKey) {
			t.Errorf("JWKS key %s does not match the stored key: %v", publishedKey.KeyID, parseError)
		}
		if keyIndex > 0 && jwksDocument.Keys[keyIndex-1].KeyID >= publishedKey.KeyID {
			t.Errorf("JWKS keys not in kid order: %s before %s", jwksDocument.Keys[keyIndex-1].KeyID, publishedKey.KeyID)
		}
	}
}

// TestInternalAuthenticatorRefusesUnknownKid: a token whose kid is not in the
// key set, a token without a kid, and a token whose signature does not match
// its kid are all refused (spec: unknown key ID).
func TestInternalAuthenticatorRefusesUnknownKid(t *testing.T) {
	keySet := newTwoKeyTokenSigningSet(t)
	identity, loadError := LoadOrCreateTokenSigningIdentity(context.Background(), keySet.factStore, keySet.keyringCodec, "", "https://ignored:9770")
	if loadError != nil {
		t.Fatal(loadError)
	}
	userRevision := putTestUser(t, keySet.factStore, "alice")
	internalAuthenticator := identity.BuildInternalTokenAuthenticator(keySet.factStore)
	foreignKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	claims := map[string]interface{}{"iss": tokenSigningTestIssuerURL, "sub": "alice", "aud": userTokenAudience,
		"exp": time.Now().Add(time.Hour).Unix(), UserRecordRevisionClaim: userRevision}
	foreignSigner := NewUserTokenIssuerWithKey(tokenSigningTestIssuerURL, foreignKey)
	mismatchedToken, _ := foreignSigner.signJWT(map[string]string{"alg": "ES256", "typ": "JWT", "kid": computeKeyID(&keySet.olderKey.PublicKey)}, claims)
	noKeyIDToken, _ := foreignSigner.signJWT(map[string]string{"alg": "ES256", "typ": "JWT"}, claims)
	activeSigner := NewUserTokenIssuerWithKey(tokenSigningTestIssuerURL, keySet.activeKey)
	activeKeyWithoutKeyID, _ := activeSigner.signJWT(map[string]string{"alg": "ES256", "typ": "JWT"}, claims)
	refusedTokens := map[string]string{
		"unknown kid":                      mintTestUserToken(t, foreignKey, "alice", userRevision),
		"kid of K1, signed by another key": mismatchedToken,
		"no kid, foreign key":              noKeyIDToken,
		"no kid, signed by the active key": activeKeyWithoutKeyID,
	}
	for caseName, token := range refusedTokens {
		if result, authError := authenticateBearerToken(internalAuthenticator, token); authError == nil || result != nil {
			t.Errorf("%s: accepted (%+v), want refused", caseName, result)
		}
	}
}

// TestLegacyRetirementGateCountsTokenSigningKey: on an upgraded cluster the
// signing key is a v1 envelope; with every secret migrated but the signing
// key still v1, the retirement gate's full scan counts it and refuses (D4).
func TestLegacyRetirementGateCountsTokenSigningKey(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 2)
	if _, loadError := LoadOrCreateTokenSigningIdentity(ctx, fixture.factStore, fixture.keyringCodec, "", tokenSigningTestIssuerURL); loadError != nil {
		t.Fatal(loadError)
	}
	if startError := fixture.keyringCodec.StartLegacyMigration(ctx, "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	secretFacts, _ := fixture.factStore.Scan(ctx, SecretStorePrefix)
	migrationBatch, planError := fixture.keyringCodec.PlanLegacyMigrationBatch(ctx, secretFacts, len(secretFacts))
	if planError != nil {
		t.Fatal(planError)
	}
	for _, rewrite := range migrationBatch.Rewrites {
		fixture.factStore.Put(ctx, rewrite.Key, rewrite.Value)
	}
	completedJSON, _ := json.Marshal(KeyringMigrationStatus{State: KeyringMigrationCompleted, Migrated: len(migrationBatch.Rewrites)})
	fixture.factStore.Put(ctx, types.KeyObservedKeyringMigration, completedJSON)
	fixture.factStore.Put(ctx, types.KeyObservedNodeLegacyKeyFile("worker-1"), []byte(types.LegacyKeyFileAbsent))
	_ = fixture.keyringCodec.Refresh(ctx)
	fixture.keyringCodec.publishKeyringReplicaReport(ctx, "cp-1", "v-test", 0)

	unmetConditions, gateError := fixture.keyringCodec.CheckLegacyRetirementGate(ctx)
	if gateError != nil {
		t.Fatal(gateError)
	}
	if !strings.Contains(strings.Join(unmetConditions, "; "), "full scan found 1 v1 envelopes") {
		t.Fatalf("retirement gate with a v1 signing key: %v; want the full scan to count it", unmetConditions)
	}
}
