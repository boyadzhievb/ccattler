// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"testing"
)

// TestParseContainerListOutputTypical verifies parsing of typical nerdctl ps output.
func TestParseContainerListOutputTypical(testContext *testing.T) {
	output := "web-abc123\ta1b2c3d4e5f6\tnginx:1.28\tUp 2 hours\n" +
		"api-def456\tf6g7h8i9j0k1\tnode:20\tUp 2 hours\n" +
		"worker-ghi789\tl2m3n4o5p6q7\tpython:3.12\tExited (0) 1 hour ago\n"

	containers := parseContainerListOutput(output)

	if len(containers) != 3 {
		testContext.Fatalf("expected 3 containers, got %d", len(containers))
	}

	if containers[0].Name != "web-abc123" {
		testContext.Errorf("expected name web-abc123, got %q", containers[0].Name)
	}
	if containers[0].ID != "a1b2c3d4e5f6" {
		testContext.Errorf("expected ID a1b2c3d4e5f6, got %q", containers[0].ID)
	}
	if containers[0].Image != "nginx:1.28" {
		testContext.Errorf("expected image nginx:1.28, got %q", containers[0].Image)
	}
	if !containers[0].Running {
		testContext.Error("expected web-abc123 to be running")
	}

	if containers[2].Running {
		testContext.Error("expected worker-ghi789 to be stopped")
	}
	if containers[2].Status != "Exited (0) 1 hour ago" {
		testContext.Errorf("expected exited status, got %q", containers[2].Status)
	}
}

// TestParseContainerListOutputEmpty verifies that empty input returns no containers.
func TestParseContainerListOutputEmpty(testContext *testing.T) {
	containers := parseContainerListOutput("")
	if len(containers) != 0 {
		testContext.Errorf("expected 0 containers for empty input, got %d", len(containers))
	}
}

// TestParseContainerListOutputMalformed verifies that malformed lines are skipped.
func TestParseContainerListOutputMalformed(testContext *testing.T) {
	output := "web-abc123\ta1b2c3d4e5f6\tnginx:1.28\tUp 2 hours\n" +
		"bad-line-no-tabs\n" +
		"api-def456\tf6g7h8i9j0k1\tnode:20\tUp 30 minutes\n"

	containers := parseContainerListOutput(output)
	if len(containers) != 2 {
		testContext.Errorf("expected 2 containers (skip malformed), got %d", len(containers))
	}
}

// TestParseContainerStatsOutputTypical verifies parsing of typical nerdctl stats output.
func TestParseContainerStatsOutputTypical(testContext *testing.T) {
	output := "web-abc123\t1.23%\t45.6MiB / 7.8GiB\n" +
		"api-def456\t95.50%\t1.2GiB / 16GiB\n"

	usages := parseContainerStatsOutput(output)

	if len(usages) != 2 {
		testContext.Fatalf("expected 2 usages, got %d", len(usages))
	}

	if usages[0].Name != "web-abc123" {
		testContext.Errorf("expected name web-abc123, got %q", usages[0].Name)
	}
	if usages[0].CPUMillicores != 12 {
		testContext.Errorf("expected cpu 12m (1.23%% * 10), got %d", usages[0].CPUMillicores)
	}
	expectedMemoryBytes := int64(47815065)
	if usages[0].MemoryBytes != expectedMemoryBytes {
		testContext.Errorf("expected memory %d, got %d", expectedMemoryBytes, usages[0].MemoryBytes)
	}

	if usages[1].CPUMillicores != 955 {
		testContext.Errorf("expected cpu 955m (95.50%% * 10), got %d", usages[1].CPUMillicores)
	}
}

// TestParseContainerStatsOutputEmpty verifies that empty input returns no usages.
func TestParseContainerStatsOutputEmpty(testContext *testing.T) {
	usages := parseContainerStatsOutput("")
	if len(usages) != 0 {
		testContext.Errorf("expected 0 usages for empty input, got %d", len(usages))
	}
}

// TestParseContainerStatsOutputZeroCPU verifies parsing when CPU is 0.00%.
func TestParseContainerStatsOutputZeroCPU(testContext *testing.T) {
	output := "idle-container\t0.00%\t512KiB / 2GiB\n"

	usages := parseContainerStatsOutput(output)
	if len(usages) != 1 {
		testContext.Fatalf("expected 1 usage, got %d", len(usages))
	}
	if usages[0].CPUMillicores != 0 {
		testContext.Errorf("expected cpu 0m, got %d", usages[0].CPUMillicores)
	}
	if usages[0].MemoryBytes != 512*1024 {
		testContext.Errorf("expected memory %d, got %d", 512*1024, usages[0].MemoryBytes)
	}
}
