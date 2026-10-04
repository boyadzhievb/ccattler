// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"fmt"
	"net/http"
	"strings"
)

// bearerTokenPrefix is the expected prefix in the Authorization header for
// bearer token authentication.
const bearerTokenPrefix = "Bearer "

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

// Authenticate reads the Authorization header for a Bearer token. Returns nil
// result and nil error when no Authorization header is present, allowing the
// chain to try the next authenticator. Returns an error when the token is
// present but invalid (bad signature, expired, wrong issuer).
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
	result, validateError := authenticator.oidcAuthenticator.Authenticate(tokenString)
	if validateError != nil {
		return nil, fmt.Errorf("bearer: %w", validateError)
	}
	return result, nil
}
