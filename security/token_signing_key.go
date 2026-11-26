// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// tokenSigningBootstrapReads is how often a start reads the signing area:
// once, and once more after its own create transaction, won or lost (D2).
const tokenSigningBootstrapReads = 2

// tokenSigningMarker is the JSON stored at keyring/token-signing/initialized.
type tokenSigningMarker struct {
	IssuerURL string `json:"issuer_url"` // IssuerURL is the cluster's token issuer, fixed at creation.
	Created   string `json:"created"`    // Created is the RFC 3339 time the identity was created.
}

// TokenSigningIdentity is the cluster's API token signing identity (Flow 37):
// the active private key and its kid, every stored public key by kid, and
// the cluster issuer URL. Loaded once at start; read-only afterwards.
type TokenSigningIdentity struct {
	issuerURL        string                      // issuerURL is the stored cluster issuer ("iss").
	activeKeyID      string                      // activeKeyID is the kid of the signing key.
	activeSigningKey *ecdsa.PrivateKey           // activeSigningKey signs new tokens; never leaves this process.
	verificationKeys map[string]*ecdsa.PublicKey // verificationKeys verify tokens by kid; the JWKS publishes them.
}

// tokenSigningArea is one consistent read (a single Scan) of
// keyring/token-signing/, so no decision rests on a partial read (M284).
type tokenSigningArea struct {
	marker     *store.Fact           // marker is keyring/token-signing/initialized, nil when absent.
	active     *store.Fact           // active is keyring/token-signing/active, nil when absent.
	sealedKeys map[string]store.Fact // sealedKeys are the key/{kid} envelopes by kid.
}

// LoadOrCreateTokenSigningIdentity loads the cluster's signing identity, or
// creates it once when the area is empty (D2): a lost create re-reads and
// loads the winner's. A lost or unreadable identity fails closed; an explicit
// issuer URL must match the stored one.
func LoadOrCreateTokenSigningIdentity(ctx context.Context, factStore store.StateStore, envelopeCodec SecretEnvelopeCodec, explicitIssuerURL string, defaultIssuerURL string) (*TokenSigningIdentity, error) {
	for readAttempt := 1; readAttempt <= tokenSigningBootstrapReads; readAttempt++ {
		signingArea, readError := readTokenSigningArea(ctx, factStore)
		if readError != nil {
			return nil, readError
		}
		if signingArea.marker != nil {
			return openTokenSigningIdentity(ctx, envelopeCodec, signingArea, explicitIssuerURL)
		}
		if refusalError := signingArea.refuseWithoutMarker(); refusalError != nil {
			return nil, refusalError
		}
		if readAttempt == tokenSigningBootstrapReads {
			break
		}
		creationIssuerURL := explicitIssuerURL
		if creationIssuerURL == "" {
			creationIssuerURL = defaultIssuerURL
		}
		if createError := createTokenSigningIdentity(ctx, factStore, envelopeCodec, creationIssuerURL); createError != nil {
			return nil, createError
		}
	}
	return nil, fmt.Errorf("%s is still missing after this replica's create transaction; refusing to start", types.KeyTokenSigningInitialized)
}

// readTokenSigningArea scans keyring/token-signing/ once and sorts its facts
// into the marker, the active kid and the sealed keys.
func readTokenSigningArea(ctx context.Context, factStore store.StateStore) (tokenSigningArea, error) {
	signingArea := tokenSigningArea{sealedKeys: make(map[string]store.Fact)}
	areaFacts, scanError := factStore.Scan(ctx, types.PrefixTokenSigning)
	if scanError != nil {
		return signingArea, fmt.Errorf("scan %s: %w", types.PrefixTokenSigning, scanError)
	}
	for factIndex := range areaFacts {
		areaFact := areaFacts[factIndex]
		switch {
		case areaFact.Key == types.KeyTokenSigningInitialized:
			signingArea.marker = &areaFacts[factIndex]
		case areaFact.Key == types.KeyTokenSigningActive:
			signingArea.active = &areaFacts[factIndex]
		case strings.HasPrefix(areaFact.Key, types.ScanTokenSigningKeys):
			keyID := strings.TrimPrefix(areaFact.Key, types.ScanTokenSigningKeys)
			if keyID == "" || strings.Contains(keyID, "/") {
				return signingArea, fmt.Errorf("%s is not a signing key path (key/{kid})", areaFact.Key)
			}
			signingArea.sealedKeys[keyID] = areaFact
		}
	}
	return signingArea, nil
}

// refuseWithoutMarker refuses an area that holds signing state but no marker:
// the marker was lost, so creating a new identity would hide the loss.
func (signingArea tokenSigningArea) refuseWithoutMarker() error {
	if signingArea.active == nil && len(signingArea.sealedKeys) == 0 {
		return nil
	}
	return fmt.Errorf("token signing state without its marker (%s missing, %d keys under %s); refusing to create a new identity: restore keyring/ from an etcd backup",
		types.KeyTokenSigningInitialized, len(signingArea.sealedKeys), types.ScanTokenSigningKeys)
}

// createTokenSigningIdentity generates a P-256 key, seals it bound to its
// store key and commits marker, active kid and key in one transaction, each
// compared absent. Losing that transaction is not an error: the caller re-reads.
func createTokenSigningIdentity(ctx context.Context, factStore store.StateStore, envelopeCodec SecretEnvelopeCodec, issuerURL string) error {
	signingKey, generateError := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if generateError != nil {
		return fmt.Errorf("generate token signing key: %w", generateError)
	}
	keyID := computeKeyID(&signingKey.PublicKey)
	signingKeyStoreKey := types.KeyTokenSigningKey(keyID)
	privateKeyDER, marshalError := x509.MarshalPKCS8PrivateKey(signingKey)
	if marshalError != nil {
		return fmt.Errorf("encode token signing key: %w", marshalError)
	}
	sealedKey, sealError := envelopeCodec.SealEnvelopeForKey(ctx, signingKeyStoreKey, privateKeyDER)
	clear(privateKeyDER)
	if sealError != nil {
		return fmt.Errorf("seal %s: %w", signingKeyStoreKey, sealError)
	}
	markerJSON, markerError := json.Marshal(tokenSigningMarker{IssuerURL: issuerURL, Created: time.Now().UTC().Format(time.RFC3339)})
	if markerError != nil {
		return markerError
	}
	createOperations := []store.Op{
		{Type: store.OpPut, Key: types.KeyTokenSigningInitialized, Value: markerJSON},
		{Type: store.OpPut, Key: types.KeyTokenSigningActive, Value: []byte(keyID)},
		{Type: store.OpPut, Key: signingKeyStoreKey, Value: sealedKey},
	}
	createCompares := make([]store.Compare, 0, len(createOperations))
	for _, createOperation := range createOperations {
		createCompares = append(createCompares, store.Compare{Key: createOperation.Key, Revision: 0})
	}
	if _, transactionError := factStore.Transaction(ctx, createCompares, createOperations, nil); transactionError != nil {
		return fmt.Errorf("create the token signing identity: %w", transactionError)
	}
	return nil
}

// openTokenSigningIdentity builds the identity from an area with a marker:
// the marker's issuer (an explicit one must match), the active kid and every
// key, each opened and checked. Anything missing or unreadable is an error.
func openTokenSigningIdentity(ctx context.Context, envelopeCodec SecretEnvelopeCodec, signingArea tokenSigningArea, explicitIssuerURL string) (*TokenSigningIdentity, error) {
	var storedMarker tokenSigningMarker
	if unmarshalError := json.Unmarshal(signingArea.marker.Value, &storedMarker); unmarshalError != nil || storedMarker.IssuerURL == "" {
		return nil, fmt.Errorf("%s is not a valid marker (issuer_url missing or not JSON); restore keyring/ from an etcd backup", types.KeyTokenSigningInitialized)
	}
	if explicitIssuerURL != "" && explicitIssuerURL != storedMarker.IssuerURL {
		return nil, fmt.Errorf("token issuer URL %s differs from the cluster's issuer %s stored in %s; start with the stored URL or without one",
			explicitIssuerURL, storedMarker.IssuerURL, types.KeyTokenSigningInitialized)
	}
	if signingArea.active == nil {
		return nil, fmt.Errorf("%s is missing; refusing to start: restore keyring/ from an etcd backup", types.KeyTokenSigningActive)
	}
	activeKeyID := string(signingArea.active.Value)
	if _, hasActiveKey := signingArea.sealedKeys[activeKeyID]; !hasActiveKey {
		return nil, fmt.Errorf("%s is missing (named by %s); refusing to start: restore keyring/ from an etcd backup",
			types.KeyTokenSigningKey(activeKeyID), types.KeyTokenSigningActive)
	}
	identity := &TokenSigningIdentity{issuerURL: storedMarker.IssuerURL, activeKeyID: activeKeyID,
		verificationKeys: make(map[string]*ecdsa.PublicKey, len(signingArea.sealedKeys))}
	keyIDs := make([]string, 0, len(signingArea.sealedKeys))
	for keyID := range signingArea.sealedKeys {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		signingKey, openError := openTokenSigningKey(ctx, envelopeCodec, keyID, signingArea.sealedKeys[keyID].Value)
		if openError != nil {
			return nil, openError
		}
		identity.verificationKeys[keyID] = &signingKey.PublicKey
		if keyID == activeKeyID {
			identity.activeSigningKey = signingKey
		}
	}
	return identity, nil
}

// openTokenSigningKey opens one sealed key and checks it is a P-256 key whose
// kid matches its store key; every error names the store key.
func openTokenSigningKey(ctx context.Context, envelopeCodec SecretEnvelopeCodec, keyID string, sealedKey []byte) (*ecdsa.PrivateKey, error) {
	signingKeyStoreKey := types.KeyTokenSigningKey(keyID)
	privateKeyDER, openError := envelopeCodec.OpenEnvelopeForKey(ctx, signingKeyStoreKey, sealedKey)
	if openError != nil {
		return nil, fmt.Errorf("open %s: %w; refusing to start (never a new identity): check the root key, or restore keyring/ from an etcd backup", signingKeyStoreKey, openError)
	}
	defer clear(privateKeyDER)
	parsedKey, parseError := x509.ParsePKCS8PrivateKey(privateKeyDER)
	if parseError != nil {
		return nil, fmt.Errorf("parse %s: %w", signingKeyStoreKey, parseError)
	}
	signingKey, isECDSA := parsedKey.(*ecdsa.PrivateKey)
	if !isECDSA || signingKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%s is not an ECDSA P-256 key", signingKeyStoreKey)
	}
	if computedKeyID := computeKeyID(&signingKey.PublicKey); computedKeyID != keyID {
		return nil, fmt.Errorf("%s holds the key of kid %s", signingKeyStoreKey, computedKeyID)
	}
	return signingKey, nil
}

// IssuerURL returns the cluster issuer URL stored at creation.
func (identity *TokenSigningIdentity) IssuerURL() string {
	return identity.issuerURL
}

// ActiveKeyID returns the kid of the key that signs new tokens.
func (identity *TokenSigningIdentity) ActiveKeyID() string {
	return identity.activeKeyID
}

// VerificationKeyForKeyID returns the stored public key of keyID; a token
// without a kid or with an unknown one is refused (D3).
func (identity *TokenSigningIdentity) VerificationKeyForKeyID(keyID string) (*ecdsa.PublicKey, error) {
	if keyID == "" {
		return nil, errors.New("oidc: token has no key id (kid)")
	}
	verificationKey, isKnown := identity.verificationKeys[keyID]
	if !isKnown {
		return nil, fmt.Errorf("oidc: unknown signing key id %q", keyID)
	}
	return verificationKey, nil
}

// BuildWorkloadTokenIssuer returns the issuer that signs with the active key
// and publishes every stored key in its JWKS and the stored issuer in discovery.
func (identity *TokenSigningIdentity) BuildWorkloadTokenIssuer() *WorkloadTokenIssuer {
	workloadTokenIssuer := NewWorkloadTokenIssuerWithKey(identity.issuerURL, identity.activeSigningKey)
	workloadTokenIssuer.publishedKeys = identity.verificationKeys
	return workloadTokenIssuer
}

// BuildUserTokenIssuer returns the user token issuer on the active key.
func (identity *TokenSigningIdentity) BuildUserTokenIssuer() *UserTokenIssuer {
	return NewUserTokenIssuerWithKey(identity.issuerURL, identity.activeSigningKey)
}

// BuildInternalTokenAuthenticator returns the authenticator for this
// cluster's own JWTs: stored issuer, audience "ccattler", key chosen by kid,
// and the user-record revision check (M216).
func (identity *TokenSigningIdentity) BuildInternalTokenAuthenticator(factStore store.StateStore) *LocalUserRecordAuthenticator {
	internalConfig := OIDCConfig{
		Issuer:       identity.issuerURL,
		Audience:     userTokenAudience,
		ClaimMapping: ClaimMapping{PrincipalClaim: "sub", TeamClaim: "team"},
	}
	return NewLocalUserRecordAuthenticator(NewBearerTokenAuthenticator(NewOIDCAuthenticatorWithKeyResolver(internalConfig, identity)), factStore)
}
