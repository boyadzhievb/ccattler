// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build cilium

package network

import "github.com/boyadzhievb/ccattler/store"

// init registers the Cilium data plane factory when the cilium build tag is active.
func init() {
	RegisterDataPlaneFactory("cilium", func(factStore store.StateStore, clusterName string, nodeID string) DataPlaneProvider {
		return NewCiliumDataPlane(factStore, clusterName, nodeID)
	})
}
