// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// tokenSigningTestAdminHeader marks a test request as the cluster admin's.
const tokenSigningTestAdminHeader = "X-Token-Signing-Test-Admin" //nolint:gosec // G101: a header name, not a credential

// tokenSigningTestAdminPrincipal is the cluster admin of the test replicas.
const tokenSigningTestAdminPrincipal = "user:token-signing-admin"

// adminHeaderAuthenticator authenticates only requests carrying
// tokenSigningTestAdminHeader, as the operator minting tokens; every other
// request is left to the replica's own authenticators.
type adminHeaderAuthenticator struct{}

// Authenticate returns the admin principal for requests with the header.
func (headerAuthenticator *adminHeaderAuthenticator) Authenticate(request *http.Request) (*security.AuthenticationResult, error) {
	if request.Header.Get(tokenSigningTestAdminHeader) == "" {
		return nil, nil
	}
	return &security.AuthenticationResult{Principal: tokenSigningTestAdminPrincipal, PrincipalKind: security.PrincipalKindUser}, nil
}

// tokenSigningTestReplica is one cca server replica's API with its production
// token signing wiring.
type tokenSigningTestReplica struct {
	t         *testing.T                     // t is the running test.
	identity  *security.TokenSigningIdentity // identity is the replica's loaded signing identity.
	apiServer *api.Server                    // apiServer serves the replica's routes.
}

// tokenSigningTestCluster is one store and root key shared by test replicas.
type tokenSigningTestCluster struct {
	factStore       store.StateStore     // factStore is the shared fact store.
	rootKeyProvider security.KeyProvider // rootKeyProvider is the cluster's root key.
}

// newTokenSigningTestCluster creates an empty store and a root key.
func newTokenSigningTestCluster(t *testing.T) *tokenSigningTestCluster {
	t.Helper()
	rootKey, keyError := security.GenerateMasterKey()
	if keyError != nil {
		t.Fatal(keyError)
	}
	rootKeyProvider, providerError := security.NewLocalKeyProvider(rootKey)
	if providerError != nil {
		t.Fatal(providerError)
	}
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	return &tokenSigningTestCluster{factStore: factStore, rootKeyProvider: rootKeyProvider}
}

// parseTokenSigningTestServerArgs parses server flags as `cca server` does.
func parseTokenSigningTestServerArgs(t *testing.T, serverArgs ...string) serverCommandConfig {
	t.Helper()
	parsedConfig, parseError := parseServerCommandArgs(append([]string{"--store", "memory"}, serverArgs...))
	if parseError != nil {
		t.Fatal(parseError)
	}
	return parsedConfig
}

// startReplica starts one replica the way cca server does: keyring codec,
// then the token signing identity, then the API with its authenticators.
func (cluster *tokenSigningTestCluster) startReplica(t *testing.T, serverArgs ...string) (*tokenSigningTestReplica, error) {
	t.Helper()
	ctx := context.Background()
	parsedConfig := parseTokenSigningTestServerArgs(t, serverArgs...)
	keyringCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, cluster.factStore, cluster.rootKeyProvider, nil)
	if codecError != nil {
		return nil, codecError
	}
	identity, identityError := loadServerTokenSigningIdentity(ctx, cluster.factStore, keyringCodec, parsedConfig)
	if identityError != nil {
		return nil, identityError
	}
	apiServer := api.NewServer(cluster.factStore)
	authenticators := appendBearerTokenAuthenticators(nil, identity, cluster.factStore, parsedConfig)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(append(authenticators, &adminHeaderAuthenticator{})...))
	apiServer.SetRequirePrincipal(true)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.GrantRole(tokenSigningTestAdminPrincipal, "cluster-admin")
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	attachTokenSigningIdentity(apiServer, identity, parsedConfig)
	return &tokenSigningTestReplica{t: t, identity: identity, apiServer: apiServer}, nil
}

// mustStartReplica starts a replica and fails the test on an error.
func (cluster *tokenSigningTestCluster) mustStartReplica(t *testing.T, serverArgs ...string) *tokenSigningTestReplica {
	t.Helper()
	replica, startError := cluster.startReplica(t, serverArgs...)
	if startError != nil {
		t.Fatalf("replica refused to start: %v", startError)
	}
	return replica
}

// createUser writes auth/user/{name} as `cca user create` does.
func (cluster *tokenSigningTestCluster) createUser(t *testing.T, userName string) {
	t.Helper()
	if _, putError := cluster.factStore.Put(context.Background(), types.KeyAuthUser(userName), []byte(`{"team":"payments"}`)); putError != nil {
		t.Fatal(putError)
	}
}

// serve sends one request to the replica and returns status and body.
func (replica *tokenSigningTestReplica) serve(request *http.Request) (int, []byte) {
	recorder := httptest.NewRecorder()
	replica.apiServer.Handler().ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

// mintUserToken mints a token through POST /api/user/token as the admin, as
// `cca user token` does; it returns the status, the token and the body.
func (replica *tokenSigningTestReplica) mintUserToken(userName string, tokenTTL string) (int, string, string) {
	request := httptest.NewRequest(http.MethodPost, "/api/user/token", bytes.NewBufferString(`{"username":"`+userName+`","ttl":"`+tokenTTL+`"}`))
	request.Header.Set(tokenSigningTestAdminHeader, "yes")
	statusCode, responseBody := replica.serve(request)
	var tokenResponse struct {
		Token string `json:"token"` // Token is the minted JWT.
	}
	_ = json.Unmarshal(responseBody, &tokenResponse)
	return statusCode, tokenResponse.Token, string(responseBody)
}

// mustMintUserToken mints a token and fails the test unless it is minted.
func (replica *tokenSigningTestReplica) mustMintUserToken(userName string) string {
	replica.t.Helper()
	statusCode, token, responseBody := replica.mintUserToken(userName, "1h")
	if statusCode != http.StatusOK || token == "" {
		replica.t.Fatalf("mint token for %s: %d %s", userName, statusCode, responseBody)
	}
	return token
}

// whoami presents token to GET /api/auth/whoami and returns the status and
// the principal the replica authenticated.
func (replica *tokenSigningTestReplica) whoami(token string) (int, string) {
	request := httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	statusCode, responseBody := replica.serve(request)
	var whoamiResponse struct {
		Principal string `json:"principal"` // Principal is the authenticated identity.
	}
	_ = json.Unmarshal(responseBody, &whoamiResponse)
	return statusCode, whoamiResponse.Principal
}

// publicDocument returns an OIDC document (discovery or JWKS); the API asks
// for authentication on these routes too, so the admin fetches them.
func (replica *tokenSigningTestReplica) publicDocument(path string) string {
	replica.t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set(tokenSigningTestAdminHeader, "yes")
	statusCode, responseBody := replica.serve(request)
	if statusCode != http.StatusOK {
		replica.t.Fatalf("GET %s: %d %s", path, statusCode, responseBody)
	}
	return string(responseBody)
}

// snapshotStore returns every key and value in the store.
func snapshotStore(t *testing.T, factStore store.StateStore) map[string]string {
	t.Helper()
	facts, scanError := factStore.Scan(context.Background(), "")
	if scanError != nil {
		t.Fatal(scanError)
	}
	snapshot := make(map[string]string, len(facts))
	for _, fact := range facts {
		snapshot[fact.Key] = string(fact.Value)
	}
	return snapshot
}

// assertStoreUnchanged fails when the store differs from before.
func assertStoreUnchanged(t *testing.T, factStore store.StateStore, before map[string]string) {
	t.Helper()
	if after := snapshotStore(t, factStore); !maps.Equal(before, after) {
		t.Fatalf("the refused start changed the store: %d keys before, %d after", len(before), len(after))
	}
}

// TestUserTokenSurvivesServerRestart: a token minted with cca user token is
// still accepted after cca-server restarts (spec: restart).
func TestUserTokenSurvivesServerRestart(t *testing.T) {
	cluster := newTokenSigningTestCluster(t)
	cluster.createUser(t, "alice")
	token := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls").mustMintUserToken("alice")

	restartedReplica := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls")
	if statusCode, principal := restartedReplica.whoami(token); statusCode != http.StatusOK || principal != "user:alice" {
		t.Fatalf("after the restart: %d %q, want 200 user:alice", statusCode, principal)
	}
}

// TestUserTokenAcceptedByOtherReplica: two replicas with different listen
// addresses on one store accept each other's tokens with the same principal.
func TestUserTokenAcceptedByOtherReplica(t *testing.T) {
	cluster := newTokenSigningTestCluster(t)
	cluster.createUser(t, "alice")
	replicaA := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls")
	replicaB := cluster.mustStartReplica(t, "--listen", "10.0.0.2:9770", "--tls")

	for mintingName, minting := range map[string]*tokenSigningTestReplica{"A": replicaA, "B": replicaB} {
		token := minting.mustMintUserToken("alice")
		for verifyingName, verifying := range map[string]*tokenSigningTestReplica{"A": replicaA, "B": replicaB} {
			if statusCode, principal := verifying.whoami(token); statusCode != http.StatusOK || principal != "user:alice" {
				t.Errorf("token from %s on %s: %d %q, want 200 user:alice", mintingName, verifyingName, statusCode, principal)
			}
		}
	}
}

// TestJWKSIdenticalAcrossReplicas: both replicas serve the same /oidc/jwks
// and the same discovery issuer, the first replica's, stored at creation.
func TestJWKSIdenticalAcrossReplicas(t *testing.T) {
	cluster := newTokenSigningTestCluster(t)
	replicaA := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls")
	replicaB := cluster.mustStartReplica(t, "--listen", "10.0.0.2:9770", "--tls")

	for _, documentPath := range []string{"/oidc/jwks", "/.well-known/openid-configuration"} {
		if documentA, documentB := replicaA.publicDocument(documentPath), replicaB.publicDocument(documentPath); documentA != documentB {
			t.Errorf("%s differs:\nA: %s\nB: %s", documentPath, documentA, documentB)
		}
	}
	var discoveryDocument security.OIDCDiscoveryResponse
	_ = json.Unmarshal([]byte(replicaB.publicDocument("/.well-known/openid-configuration")), &discoveryDocument)
	if discoveryDocument.Issuer != "https://10.0.0.1:9770" {
		t.Fatalf("replica B's issuer = %q, want the stored https://10.0.0.1:9770", discoveryDocument.Issuer)
	}
}

// TestServerRefusesDifferentTokenIssuerURL: a replica started with an
// explicit --token-issuer-url other than the stored one refuses to start,
// naming both URLs, and the store is unchanged.
func TestServerRefusesDifferentTokenIssuerURL(t *testing.T) {
	cluster := newTokenSigningTestCluster(t)
	cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls", "--token-issuer-url", "https://ctrl-1:9770")
	storeBefore := snapshotStore(t, cluster.factStore)

	_, startError := cluster.startReplica(t, "--listen", "10.0.0.2:9770", "--tls", "--token-issuer-url", "https://other:9770")
	if startError == nil || !strings.Contains(startError.Error(), "https://ctrl-1:9770") || !strings.Contains(startError.Error(), "https://other:9770") {
		t.Fatalf("start with a different issuer: %v; want a refusal naming both URLs", startError)
	}
	assertStoreUnchanged(t, cluster.factStore, storeBefore)
}

// initializedTokenSigningCluster starts one replica so the cluster's signing
// identity exists, and returns the cluster and the active key's store key.
func initializedTokenSigningCluster(t *testing.T) (*tokenSigningTestCluster, string) {
	t.Helper()
	cluster := newTokenSigningTestCluster(t)
	firstReplica := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls")
	return cluster, types.KeyTokenSigningKey(firstReplica.identity.ActiveKeyID())
}

// TestServerRefusesStartWhenSigningKeyMissing: an initialized cluster whose
// key record was deleted refuses to start, naming the store key, and no new
// key appears (spec: key record deleted).
func TestServerRefusesStartWhenSigningKeyMissing(t *testing.T) {
	cluster, activeKeyStoreKey := initializedTokenSigningCluster(t)
	if deleteError := cluster.factStore.Delete(context.Background(), activeKeyStoreKey); deleteError != nil {
		t.Fatal(deleteError)
	}
	storeBefore := snapshotStore(t, cluster.factStore)

	_, startError := cluster.startReplica(t, "--listen", "10.0.0.2:9770", "--tls")
	if startError == nil || !strings.Contains(startError.Error(), activeKeyStoreKey) {
		t.Fatalf("start without the signing key: %v; want a refusal naming %s", startError, activeKeyStoreKey)
	}
	assertStoreUnchanged(t, cluster.factStore, storeBefore)
}

// TestServerRefusesCorruptSigningKey: a corrupt key envelope refuses the
// start, naming the store key, and the store is unchanged.
func TestServerRefusesCorruptSigningKey(t *testing.T) {
	cluster, activeKeyStoreKey := initializedTokenSigningCluster(t)
	ctx := context.Background()
	keyFact, getError := cluster.factStore.Get(ctx, activeKeyStoreKey)
	if getError != nil {
		t.Fatal(getError)
	}
	corruptEnvelope := bytes.Clone(keyFact.Value)
	corruptEnvelope[len(corruptEnvelope)-1] ^= 0xff
	cluster.factStore.Put(ctx, activeKeyStoreKey, corruptEnvelope)
	storeBefore := snapshotStore(t, cluster.factStore)

	_, startError := cluster.startReplica(t, "--listen", "10.0.0.2:9770", "--tls")
	if startError == nil || !strings.Contains(startError.Error(), activeKeyStoreKey) {
		t.Fatalf("start with a corrupt signing key: %v; want a refusal naming %s", startError, activeKeyStoreKey)
	}
	assertStoreUnchanged(t, cluster.factStore, storeBefore)
}

// TestServerRefusesSigningKeyWithWrongRootKey: a replica with a different
// root key stops at the keyring root check, before any signing-key read; and
// a codec from another keyring cannot open the signing key either. Neither
// changes the store.
func TestServerRefusesSigningKeyWithWrongRootKey(t *testing.T) {
	cluster, activeKeyStoreKey := initializedTokenSigningCluster(t)
	storeBefore := snapshotStore(t, cluster.factStore)

	otherRootKey, _ := security.GenerateMasterKey()
	otherRootKeyProvider, _ := security.NewLocalKeyProvider(otherRootKey)
	wrongRootReplica := &tokenSigningTestCluster{factStore: cluster.factStore, rootKeyProvider: otherRootKeyProvider}
	if _, startError := wrongRootReplica.startReplica(t, "--listen", "10.0.0.2:9770", "--tls"); !errors.Is(startError, security.ErrKeyringRootCheckFailed) {
		t.Fatalf("start with another root key: %v; want ErrKeyringRootCheckFailed", startError)
	}
	assertStoreUnchanged(t, cluster.factStore, storeBefore)

	foreignKeyringCodec, codecError := security.NewKeyringEnvelopeCodec(context.Background(), store.NewMemoryStore(), otherRootKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	_, loadError := loadServerTokenSigningIdentity(context.Background(), cluster.factStore, foreignKeyringCodec,
		parseTokenSigningTestServerArgs(t, "--listen", "10.0.0.2:9770", "--tls"))
	if loadError == nil || !strings.Contains(loadError.Error(), activeKeyStoreKey) {
		t.Fatalf("load with another keyring's KEK: %v; want a refusal naming %s", loadError, activeKeyStoreKey)
	}
	assertStoreUnchanged(t, cluster.factStore, storeBefore)
}

// TestUserTokenTTLAboveMaximumRefused: with the default maximum, a 25h token
// request is refused with 400 naming 24h, and no token is minted; 24h and
// the 8h default still mint (spec: too long).
func TestUserTokenTTLAboveMaximumRefused(t *testing.T) {
	cluster := newTokenSigningTestCluster(t)
	cluster.createUser(t, "alice")
	replica := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls")

	statusCode, token, responseBody := replica.mintUserToken("alice", "25h")
	if statusCode != http.StatusBadRequest || token != "" || !strings.Contains(responseBody, "24h") {
		t.Fatalf("25h token: %d token=%q %s; want 400 naming 24h and no token", statusCode, token, responseBody)
	}
	for _, allowedTTL := range []string{"24h", ""} {
		if statusCode, token, responseBody = replica.mintUserToken("alice", allowedTTL); statusCode != http.StatusOK || token == "" {
			t.Errorf("ttl %q: %d %s; want a token", allowedTTL, statusCode, responseBody)
		}
	}
}

// TestDeletedUserTokenRefusedAfterRestart: deleting alice revokes her token,
// also on a restarted replica, although its signing key stays trusted.
func TestDeletedUserTokenRefusedAfterRestart(t *testing.T) {
	cluster := newTokenSigningTestCluster(t)
	cluster.createUser(t, "alice")
	cluster.createUser(t, "bob")
	firstReplica := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls")
	aliceToken := firstReplica.mustMintUserToken("alice")
	bobToken := firstReplica.mustMintUserToken("bob")
	if deleteError := cluster.factStore.Delete(context.Background(), types.KeyAuthUser("alice")); deleteError != nil {
		t.Fatal(deleteError)
	}

	restartedReplica := cluster.mustStartReplica(t, "--listen", "10.0.0.1:9770", "--tls")
	if statusCode, principal := restartedReplica.whoami(aliceToken); statusCode != http.StatusUnauthorized {
		t.Fatalf("deleted alice's token after the restart: %d %q, want 401", statusCode, principal)
	}
	if statusCode, principal := restartedReplica.whoami(bobToken); statusCode != http.StatusOK || principal != "user:bob" {
		t.Fatalf("bob's token after the restart: %d %q, want 200 (the signing key stays trusted)", statusCode, principal)
	}
}
