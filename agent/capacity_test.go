// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import "testing"

func TestParseMemTotalMiB(t *testing.T) {
	meminfo := "MemTotal:        4003840 kB\nMemFree:          131072 kB\n"
	totalMiB, parseError := parseMemTotalMiB(meminfo)
	if parseError != nil || totalMiB != 3910 {
		t.Fatalf("got %d MiB, %v; want 3910 MiB", totalMiB, parseError)
	}
	if _, missingError := parseMemTotalMiB("MemFree: 1 kB\n"); missingError == nil {
		t.Fatal("meminfo without MemTotal must be an error")
	}
}

func TestAllocatableMemoryMiB(t *testing.T) {
	testCases := map[int64]int64{
		3910:  3142,  // 4 GiB deploy VM: 768 MiB minimum reserve
		16384: 14746, // 16 GiB: a tenth (1638 MiB) is larger than the minimum
		512:   0,     // smaller than the reserve: nothing schedulable
	}
	for totalMiB, wantMiB := range testCases {
		if gotMiB := allocatableMemoryMiB(totalMiB); gotMiB != wantMiB {
			t.Errorf("allocatableMemoryMiB(%d) = %d, want %d", totalMiB, gotMiB, wantMiB)
		}
	}
}
