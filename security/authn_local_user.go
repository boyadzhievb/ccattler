// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// localUserRecordLookupTimeout bounds the store read made per authenticated
// user request.
const localUserRecordLookupTimeout = 5 * time.Second

// LocalUserRecordAuthenticator wraps the authenticator for user tokens minted
// by this cluster (`cca user token`) and accepts a token only while its user
// record exists at the exact store revision the token was minted from (M216).
type LocalUserRecordAuthenticator struct {
	innerAuthenticator Authenticator    // innerAuthenticator validates the token signature, issuer, audience and expiry.
	factStore          store.StateStore // factStore holds the user records (auth/user/{name}).
}

// NewLocalUserRecordAuthenticator wraps innerAuthenticator, which must accept
// only this cluster's own user tokens (issuer + audience "ccattler").
func NewLocalUserRecordAuthenticator(innerAuthenticator Authenticator, factStore store.StateStore) *LocalUserRecordAuthenticator {
	return &LocalUserRecordAuthenticator{innerAuthenticator: innerAuthenticator, factStore: factStore}
}

// Authenticate runs the inner authenticator, then refuses a user token whose
// user record is gone or was written since the token was minted (deleted,
// re-created, edited). A store error refuses (fail closed); other
// credentials pass through unchanged.
func (localUserAuthenticator *LocalUserRecordAuthenticator) Authenticate(request *http.Request) (*AuthenticationResult, error) {
	result, authError := localUserAuthenticator.innerAuthenticator.Authenticate(request)
	if authError != nil || result == nil || result.PrincipalKind != PrincipalKindUser {
		return result, authError
	}
	userName := strings.TrimPrefix(result.Principal, "user:")
	lookupContext, cancel := context.WithTimeout(request.Context(), localUserRecordLookupTimeout)
	defer cancel()
	userFact, getError := localUserAuthenticator.factStore.Get(lookupContext, types.KeyAuthUser(userName))
	if errors.Is(getError, store.ErrKeyNotFound) {
		return nil, fmt.Errorf("user token: user %q does not exist", userName)
	}
	if getError != nil {
		return nil, fmt.Errorf("user token: read user %q: %w", userName, getError)
	}
	if result.UserRecordRevision == 0 || result.UserRecordRevision != userFact.Revision {
		return nil, fmt.Errorf("user token: minted for an earlier version of user %q", userName)
	}
	return result, nil
}
