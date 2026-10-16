// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import "context"

// KMSKeyProviderFactory creates a KeyProvider for the given key ID and region.
// The region may be empty for providers that don't use it (e.g. GCP uses
// the key resource name directly).
type KMSKeyProviderFactory func(ctx context.Context, keyID string, region string) (KeyProvider, error)

// kmsKeyProviderFactories holds registered KMS key provider factories keyed by
// provider name (e.g. "aws-kms", "gcp-kms"). Populated by init() in
// build-tagged files.
var kmsKeyProviderFactories = map[string]KMSKeyProviderFactory{}

// RegisterKMSKeyProviderFactory registers a factory for the named KMS provider.
// Called from init() in build-tagged source files. Panics on duplicate names.
func RegisterKMSKeyProviderFactory(providerName string, factory KMSKeyProviderFactory) {
	if _, alreadyRegistered := kmsKeyProviderFactories[providerName]; alreadyRegistered {
		panic("security: duplicate KMS provider registration for " + providerName)
	}
	kmsKeyProviderFactories[providerName] = factory
}

// LookupKMSKeyProviderFactory returns the factory for the named KMS provider,
// or nil if none is registered.
func LookupKMSKeyProviderFactory(providerName string) KMSKeyProviderFactory {
	return kmsKeyProviderFactories[providerName]
}
