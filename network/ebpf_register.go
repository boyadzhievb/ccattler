// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build ebpf

package network

import "github.com/boyadzhievb/ccattler/store"

// init registers the eBPF data plane factory when the ebpf build tag is
// active. The eBPF provider manages local BPF maps and does not require the
// fact store, cluster name, or node ID — those parameters are accepted only
// to satisfy the DataPlaneFactory signature.
func init() {
	RegisterDataPlaneFactory("ebpf", func(_ store.StateStore, _ string, _ string) DataPlaneProvider {
		return NewEbpfDataPlane()
	})
}
