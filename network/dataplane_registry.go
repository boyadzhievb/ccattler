// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import "github.com/boyadzhievb/ccattler/store"

// DataPlaneFactory creates a DataPlaneProvider. The factStore is needed by
// providers that write to etcd (e.g. Cilium kvstore translation). The
// clusterName identifies the cluster in multi-cluster setups. The nodeID
// identifies this node for per-node key writes.
type DataPlaneFactory func(factStore store.StateStore, clusterName string, nodeID string) DataPlaneProvider

// dataPlaneFactories holds registered data plane provider factories keyed
// by name (e.g. "cilium"). Populated by init() in build-tagged files.
var dataPlaneFactories = map[string]DataPlaneFactory{}

// RegisterDataPlaneFactory registers a factory for the given data plane name.
// Called from init() in build-tagged source files. Panics on duplicate names.
func RegisterDataPlaneFactory(dataplaneName string, factory DataPlaneFactory) {
	if _, alreadyRegistered := dataPlaneFactories[dataplaneName]; alreadyRegistered {
		panic("network: duplicate data plane registration for " + dataplaneName)
	}
	dataPlaneFactories[dataplaneName] = factory
}

// LookupDataPlaneFactory returns the factory for the given data plane name,
// or nil if no factory is registered for that name.
func LookupDataPlaneFactory(dataplaneName string) DataPlaneFactory {
	return dataPlaneFactories[dataplaneName]
}
