// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// bearerTokenPrefix is the expected prefix in the Authorization header for
// bearer token authentication.
const bearerTokenPrefix = "Bearer "

// minimumJWTSegments is the number of dot-separated segments in a valid JWT.
const minimumJWTSegments = 3

// looksLikeJWT returns true when the token has the three dot-separated segments
// expected of a JWT (header.payload.signature). This check avoids sending
// non-JWT bearer tokens (like hex local-tokens) into the OIDC validator, which
// would return hard errors and short-circuit the authenticator chain.
func looksLikeJWT(token string) bool {
	return strings.Count(token, ".") == minimumJWTSegments-1
}

// unverifiedJWTIssuer decodes the payload of a JWT without verifying it and
// returns its "iss" claim, or "" when it cannot be read. Used only to route a
// token to the authenticator for its issuer; the signature is verified there.
func unverifiedJWTIssuer(token string) string {
	tokenSegments := strings.Split(token, ".")
	if len(tokenSegments) != minimumJWTSegments {
		return ""
	}
	payloadJSON, decodeError := base64.RawURLEncoding.DecodeString(tokenSegments[1])
	if decodeError != nil {
		return ""
	}
	var issuerClaim struct {
		Issuer string `json:"iss"`
	}
	if json.Unmarshal(payloadJSON, &issuerClaim) != nil {
		return ""
	}
	return issuerClaim.Issuer
}

// BearerTokenAuthenticator reads an "Authorization: Bearer <token>" header and
// delegates validation to an existing OIDCAuthenticator. This is a thin adapter
// that bridges HTTP-layer credential extraction with the token-level OIDC
// verification.
type BearerTokenAuthenticator struct {
	oidcAuthenticator *OIDCAuthenticator // delegates JWT verification
}

// NewBearerTokenAuthenticator creates a bearer token authenticator backed by
// the given OIDC authenticator for JWT validation.
func NewBearerTokenAuthenticator(oidcAuthenticator *OIDCAuthenticator) *BearerTokenAuthenticator {
	return &BearerTokenAuthenticator{oidcAuthenticator: oidcAuthenticator}
}

// Authenticate reads the Authorization header for a Bearer token. It claims
// only JWTs whose "iss" equals the configured issuer; anything else returns
// nil, nil so the next authenticator can try. A claimed token that fails
// validation (bad signature, expired, wrong audience) returns an error.
func (authenticator *BearerTokenAuthenticator) Authenticate(request *http.Request) (*AuthenticationResult, error) {
	authorizationHeader := request.Header.Get("Authorization")
	if authorizationHeader == "" {
		return nil, nil
	}
	if !strings.HasPrefix(authorizationHeader, bearerTokenPrefix) {
		return nil, nil
	}
	tokenString := strings.TrimPrefix(authorizationHeader, bearerTokenPrefix)
	if tokenString == "" {
		return nil, fmt.Errorf("bearer: empty token")
	}
	// Only attempt OIDC validation when the token looks like a JWT (three
	// dot-separated segments). Non-JWT tokens (e.g. local-token hex strings)
	// pass through so the next authenticator in the chain can handle them.
	if !looksLikeJWT(tokenString) {
		return nil, nil
	}
	if unverifiedJWTIssuer(tokenString) != authenticator.oidcAuthenticator.config.Issuer {
		return nil, nil
	}
	result, validateError := authenticator.oidcAuthenticator.Authenticate(tokenString)
	if validateError != nil {
		return nil, fmt.Errorf("bearer: %w", validateError)
	}
	return result, nil
}
