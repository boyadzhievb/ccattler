// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package runtime

import (
	"fmt"
	"path/filepath"
)

// createNetworkNamespace is a stub for non-Linux platforms. Container
// networking requires Linux kernel namespaces.
func createNetworkNamespace(instanceID string) (string, error) {
	return "", fmt.Errorf("network namespaces are only supported on Linux (instance %s)", instanceID)
}

// deleteNetworkNamespace is a no-op stub for non-Linux platforms.
func deleteNetworkNamespace(instanceID string) {
	_ = filepath.Join(netnsBaseDirectory, "cca-"+instanceID)
}
