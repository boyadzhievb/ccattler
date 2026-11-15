// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrAuthenticationUnavailable marks a credential that could not be checked
// because a dependency failed (store read, webhook unreachable), as opposed
// to a credential that was checked and refused. Open streams tolerate a few
// in a row (M216); a new request is still refused.
var ErrAuthenticationUnavailable = errors.New("authentication unavailable")

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

// freshAuthenticationContextKey marks a request whose credential must be
// checked at its source again, not answered from a cache.
type freshAuthenticationContextKey struct{}

// WithFreshAuthentication returns ctx marked so authenticators with a result
// cache (webhook) skip it: an open stream's re-check must see a revocation
// at the next interval (M216).
func WithFreshAuthentication(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshAuthenticationContextKey{}, true)
}

// freshAuthenticationRequested reports whether ctx was marked by
// WithFreshAuthentication.
func freshAuthenticationRequested(ctx context.Context) bool {
	fresh, _ := ctx.Value(freshAuthenticationContextKey{}).(bool)
	return fresh
}
