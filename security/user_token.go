// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// defaultUserTokenTTL is the default expiry for user authentication tokens.
const defaultUserTokenTTL = 8 * time.Hour

// userTokenAudience is the audience claim for all user tokens.
const userTokenAudience = "ccattler"

// UserTokenIssuer mints signed JWTs for human user authentication. It reuses
// the same ES256 signing key as the WorkloadTokenIssuer so the server's
// OIDCAuthenticator can validate both workload and user tokens.
type UserTokenIssuer struct {
	signingKey *ecdsa.PrivateKey // ECDSA P-256 private key for signing tokens.
	issuerURL  string            // OIDC issuer URL (matches the server's identity).
	keyID      string            // Key ID included in the JWT header for key selection.
}

// NewUserTokenIssuer creates a user token issuer from a WorkloadTokenIssuer,
// sharing the same signing key and issuer URL.
func NewUserTokenIssuer(workloadIssuer *WorkloadTokenIssuer) *UserTokenIssuer {
	return &UserTokenIssuer{
		signingKey: workloadIssuer.signingKey,
		issuerURL:  workloadIssuer.issuerURL,
		keyID:      workloadIssuer.keyID,
	}
}

// NewUserTokenIssuerWithKey creates a user token issuer from an existing ECDSA
// private key. Used when the signing key is loaded from persistent storage.
func NewUserTokenIssuerWithKey(issuerURL string, signingKey *ecdsa.PrivateKey) *UserTokenIssuer {
	return &UserTokenIssuer{
		signingKey: signingKey,
		issuerURL:  issuerURL,
		keyID:      computeKeyID(&signingKey.PublicKey),
	}
}

// MintUserToken creates a signed JWT for the given user with team and group
// claims. The token authenticates the user against the CCattler API with
// identity-based authorization.
func (userTokenIssuer *UserTokenIssuer) MintUserToken(username string, team string, groups []string, tokenTTL time.Duration) (string, error) {
	if username == "" {
		return "", fmt.Errorf("user token: username is required")
	}
	if tokenTTL <= 0 {
		tokenTTL = defaultUserTokenTTL
	}

	currentTime := time.Now()

	header := map[string]string{
		"alg": "ES256",
		"typ": "JWT",
		"kid": userTokenIssuer.keyID,
	}

	claims := map[string]interface{}{
		"iss": userTokenIssuer.issuerURL,
		"sub": username,
		"aud": userTokenAudience,
		"iat": currentTime.Unix(),
		"exp": currentTime.Add(tokenTTL).Unix(),
	}
	if team != "" {
		claims["team"] = team
	}
	if len(groups) > 0 {
		claims["groups"] = groups
	}

	return userTokenIssuer.signJWT(header, claims)
}

// signJWT encodes and signs a JWT with the issuer's ECDSA private key.
func (userTokenIssuer *UserTokenIssuer) signJWT(header map[string]string, claims map[string]interface{}) (string, error) {
	headerJSON, marshalError := json.Marshal(header)
	if marshalError != nil {
		return "", fmt.Errorf("user token: marshal header: %w", marshalError)
	}
	headerEncoded := base64.RawURLEncoding.EncodeToString(headerJSON)

	claimsJSON, marshalError := json.Marshal(claims)
	if marshalError != nil {
		return "", fmt.Errorf("user token: marshal claims: %w", marshalError)
	}
	claimsEncoded := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signedPayload := headerEncoded + "." + claimsEncoded
	payloadHash := sha256.Sum256([]byte(signedPayload))

	signatureR, signatureS, signError := ecdsa.Sign(rand.Reader, userTokenIssuer.signingKey, payloadHash[:])
	if signError != nil {
		return "", fmt.Errorf("user token: sign: %w", signError)
	}

	keySize := (userTokenIssuer.signingKey.Curve.Params().BitSize + 7) / 8
	signatureBytes := make([]byte, 2*keySize)
	rBytes := signatureR.Bytes()
	sBytes := signatureS.Bytes()
	copy(signatureBytes[keySize-len(rBytes):keySize], rBytes)
	copy(signatureBytes[2*keySize-len(sBytes):], sBytes)

	signatureEncoded := base64.RawURLEncoding.EncodeToString(signatureBytes)
	return signedPayload + "." + signatureEncoded, nil
}

// PublicKey returns the ECDSA public key for token signature verification.
func (userTokenIssuer *UserTokenIssuer) PublicKey() *ecdsa.PublicKey {
	return &userTokenIssuer.signingKey.PublicKey
}

// IssuerURL returns the OIDC issuer URL used in the "iss" claim.
func (userTokenIssuer *UserTokenIssuer) IssuerURL() string {
	return userTokenIssuer.issuerURL
}
