// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package cloud

import "context"

// CloudProviderFactory creates a CloudProvider for the given region. The ctx
// carries cancellation and credentials. The projectID is provider-specific
// (e.g. GCP project ID, empty for AWS). Returns an error if the provider
// cannot be initialized.
type CloudProviderFactory func(ctx context.Context, projectID string, region string) (CloudProvider, error)

// cloudProviderFactories holds registered cloud provider factories keyed by
// provider name (e.g. "aws", "gcp"). Populated by init() in build-tagged files.
var cloudProviderFactories = map[string]CloudProviderFactory{}

// RegisterCloudProviderFactory registers a factory for the given provider name.
// Called from init() in build-tagged source files. Panics on duplicate names.
func RegisterCloudProviderFactory(providerName string, factory CloudProviderFactory) {
	if _, alreadyRegistered := cloudProviderFactories[providerName]; alreadyRegistered {
		panic("cloud: duplicate provider registration for " + providerName)
	}
	cloudProviderFactories[providerName] = factory
}

// LookupCloudProviderFactory returns the factory for the given provider name,
// or nil if no factory is registered for that name.
func LookupCloudProviderFactory(providerName string) CloudProviderFactory {
	return cloudProviderFactories[providerName]
}
