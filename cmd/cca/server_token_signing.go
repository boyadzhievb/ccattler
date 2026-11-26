// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// registerServerTokenFlags registers the API token flags of `cca server`:
// the cluster issuer URL and the maximum user token lifetime (M283).
func registerServerTokenFlags(flagSet *flag.FlagSet, parsedConfig *serverCommandConfig) {
	flagSet.Func("token-issuer-url", "cluster token issuer URL, stored at the first start (default: this replica's listen address)",
		func(flagValue string) error {
			if validationError := validateTokenIssuerURL(flagValue); validationError != nil {
				return validationError
			}
			parsedConfig.tokenIssuerURL = flagValue
			return nil
		})
	flagSet.Func("max-user-token-ttl", "longest lifetime cca user token may request, e.g. 24h (default 24h)",
		func(flagValue string) error {
			maximumTTL, parseError := time.ParseDuration(flagValue)
			if parseError != nil || maximumTTL < security.DefaultUserTokenTTL {
				return fmt.Errorf("must be a duration of at least %s, the default token lifetime (e.g. 24h)", security.DefaultUserTokenTTL)
			}
			parsedConfig.maxUserTokenTTL = maximumTTL
			return nil
		})
}

// validateTokenIssuerURL accepts an absolute http or https URL without a
// path, query or fragment: the JWKS is served at {issuer}/oidc/jwks.
func validateTokenIssuerURL(issuerURL string) error {
	parsedURL, parseError := url.Parse(issuerURL)
	if parseError != nil || (parsedURL.Scheme != "https" && parsedURL.Scheme != "http") || parsedURL.Host == "" {
		return errors.New("must be an absolute http or https URL, e.g. https://ctrl-1:9770")
	}
	if parsedURL.Path != "" || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return errors.New("must not have a path, query or fragment, e.g. https://ctrl-1:9770")
	}
	return nil
}

// loadServerTokenSigningIdentity loads the cluster's token signing identity,
// or creates it on the first start with --token-issuer-url, else this
// replica's listen address, as the issuer (Flow 37). Errors stop the server.
func loadServerTokenSigningIdentity(ctx context.Context, factStore store.StateStore, envelopeCodec security.SecretEnvelopeCodec, parsedConfig serverCommandConfig) (*security.TokenSigningIdentity, error) {
	return security.LoadOrCreateTokenSigningIdentity(ctx, factStore, envelopeCodec, parsedConfig.tokenIssuerURL, resolveServerIssuerURL(parsedConfig))
}

// establishServerTokenSigningIdentity loads the identity and prints its kid
// and issuer (public values only); any error exits before the server serves
// or writes anything else, like the keyring start.
func establishServerTokenSigningIdentity(ctx context.Context, factStore store.StateStore, envelopeCodec security.SecretEnvelopeCodec, parsedConfig serverCommandConfig) *security.TokenSigningIdentity {
	identity, identityError := loadServerTokenSigningIdentity(ctx, factStore, envelopeCodec, parsedConfig)
	if identityError != nil {
		fmt.Fprintf(os.Stderr, "error: token signing identity: %v\n", identityError)
		os.Exit(1)
	}
	fmt.Printf("Token signing identity loaded (kid %s, issuer %s)\n", identity.ActiveKeyID(), identity.IssuerURL())
	return identity
}

// attachTokenSigningIdentity gives the API the issuers built on the identity
// (JWKS and discovery of the stored key set and issuer) and the maximum user
// token lifetime.
func attachTokenSigningIdentity(apiServer *api.Server, identity *security.TokenSigningIdentity, parsedConfig serverCommandConfig) {
	apiServer.SetWorkloadTokenIssuer(identity.BuildWorkloadTokenIssuer())
	apiServer.SetUserTokenIssuer(identity.BuildUserTokenIssuer())
	apiServer.SetMaxUserTokenTTL(parsedConfig.maxUserTokenTTL)
}
