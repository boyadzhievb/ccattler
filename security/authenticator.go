// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"fmt"
	"net/http"
)

// Authenticator maps an HTTP request to a principal identity. Implementations
// inspect credentials (certificates, tokens, headers) and return an
// AuthenticationResult on success or an error when credentials are absent or
// invalid.
type Authenticator interface {
	// Authenticate inspects the request for credentials and returns the
	// extracted identity. Returns a nil result and nil error when this
	// authenticator does not recognise the credential type (allowing the
	// chain to try the next authenticator).
	Authenticate(request *http.Request) (*AuthenticationResult, error)
}

// AuthenticatorChain evaluates a list of authenticators in order. The first
// authenticator that returns a non-nil result wins. If every authenticator
// returns nil (unrecognised credentials), the chain returns an error.
type AuthenticatorChain struct {
	authenticators []Authenticator // ordered list — first match wins
}

// NewAuthenticatorChain creates a chain that evaluates authenticators in the
// given order.
func NewAuthenticatorChain(authenticators ...Authenticator) *AuthenticatorChain {
	return &AuthenticatorChain{authenticators: authenticators}
}

// Authenticate iterates the chain until one authenticator succeeds. A hard
// error (invalid signature, expired token) short-circuits — the request is
// rejected rather than falling through to the next authenticator.
func (chain *AuthenticatorChain) Authenticate(request *http.Request) (*AuthenticationResult, error) {
	for _, authenticator := range chain.authenticators {
		result, authError := authenticator.Authenticate(request)
		if authError != nil {
			return nil, authError
		}
		if result != nil {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no authenticator accepted the request")
}
