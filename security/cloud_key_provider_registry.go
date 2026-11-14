// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import "context"

// CloudKeyProviderFactory creates a KeyProvider backed by a cloud key service
// (AWS KMS, GCP Cloud KMS) for the given key ID and region. The region may be
// empty for providers that don't use it (GCP uses the key resource name).
type CloudKeyProviderFactory func(ctx context.Context, keyID string, region string) (KeyProvider, error)

// cloudKeyProviderFactories holds registered cloud key provider factories keyed
// by provider name ("aws-kms", "gcp-kms"). Populated by init() in build-tagged
// files.
var cloudKeyProviderFactories = map[string]CloudKeyProviderFactory{}

// RegisterCloudKeyProviderFactory registers a factory for the named cloud key
// provider. Called from init() in build-tagged source files. Panics on
// duplicate names.
func RegisterCloudKeyProviderFactory(providerName string, factory CloudKeyProviderFactory) {
	if _, alreadyRegistered := cloudKeyProviderFactories[providerName]; alreadyRegistered {
		panic("security: duplicate cloud key provider registration for " + providerName)
	}
	cloudKeyProviderFactories[providerName] = factory
}

// LookupCloudKeyProviderFactory returns the factory for the named cloud key
// provider, or nil if none is registered.
func LookupCloudKeyProviderFactory(providerName string) CloudKeyProviderFactory {
	return cloudKeyProviderFactories[providerName]
}
