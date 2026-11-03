// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"net"
	"testing"
)

// TestVXLANInterfaceRepairs covers the state an agent stopped mid-creation
// leaves behind: the interface exists but is down (deploy run 41662762813).
func TestVXLANInterfaceRepairs(t *testing.T) {
	testCases := []struct {
		name         string
		flags        net.Flags
		mtu          int
		wantNeedsUp  bool
		wantNeedsMTU bool
	}{
		{"created but never set up", net.FlagBroadcast | net.FlagMulticast, vxlanBridgeMTU, true, false},
		{"down with default MTU", net.FlagBroadcast, 1500, true, true},
		{"up with wrong MTU", net.FlagUp | net.FlagBroadcast, 1500, false, true},
		{"correct", net.FlagUp | net.FlagBroadcast | net.FlagMulticast, vxlanBridgeMTU, false, false},
	}
	for _, testCase := range testCases {
		needsUp, needsMTU := vxlanInterfaceRepairs(testCase.flags, testCase.mtu)
		if needsUp != testCase.wantNeedsUp || needsMTU != testCase.wantNeedsMTU {
			t.Errorf("%s: got up=%v mtu=%v, want up=%v mtu=%v", testCase.name, needsUp, needsMTU, testCase.wantNeedsUp, testCase.wantNeedsMTU)
		}
	}
}
