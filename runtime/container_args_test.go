// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"strings"
	"testing"
)

// TestBuildRunArgumentsMemoryIsBytes verifies that the --memory flag uses the
// MemoryB value directly (already in bytes). A previous bug multiplied by
// 1024*1024, producing a 512 TiB limit instead of 512 MiB.
func TestBuildRunArgumentsMemoryIsBytes(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:      "test-1",
		Image:   "nginx:1.27",
		MemoryB: 536870912, // 512 MiB in bytes
	}

	args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	foundMemoryFlag := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--memory=") {
			foundMemoryFlag = arg
			break
		}
	}

	expectedFlag := "--memory=536870912"
	if foundMemoryFlag != expectedFlag {
		t.Errorf("memory flag: got %q, want %q", foundMemoryFlag, expectedFlag)
	}

	// Guard against the double-conversion regression: 536870912 * 1024 * 1024 = 562949953421312
	if strings.Contains(foundMemoryFlag, "562949953421312") {
		t.Fatal("memory value is double-converted (bytes * 1024 * 1024) — MemoryB is already in bytes")
	}
}

// TestBuildRunArgumentsMemoryZeroOmitsFlag verifies that no --memory flag is
// added when MemoryB is zero (no memory limit configured).
func TestBuildRunArgumentsMemoryZeroOmitsFlag(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:      "test-2",
		Image:   "nginx:1.27",
		MemoryB: 0,
	}

	args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, arg := range args {
		if strings.HasPrefix(arg, "--memory=") {
			t.Errorf("--memory flag should not be present when MemoryB is 0, got %q", arg)
		}
	}
}

// TestBuildRunArgumentsCPUFormat verifies that the --cpus flag correctly
// converts millicores to the decimal format expected by nerdctl/docker.
func TestBuildRunArgumentsCPUFormat(t *testing.T) {
	tests := []struct {
		cpuMillicores int64
		expectedFlag  string
	}{
		{500, "--cpus=0.500"},
		{1000, "--cpus=1.000"},
		{2500, "--cpus=2.500"},
		{250, "--cpus=0.250"},
	}

	for _, testCase := range tests {
		containerRuntime := NewContainerRuntime()
		spec := Spec{
			ID:    "test-cpu",
			Image: "nginx:1.27",
			CPUm:  testCase.cpuMillicores,
		}

		args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
		if err != nil {
			t.Fatal(err)
		}

		foundCPUFlag := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--cpus=") {
				foundCPUFlag = arg
				break
			}
		}

		if foundCPUFlag != testCase.expectedFlag {
			t.Errorf("cpu %dm: got %q, want %q", testCase.cpuMillicores, foundCPUFlag, testCase.expectedFlag)
		}
	}
}

// TestBuildRunArgumentsPortMapping verifies that port arguments are constructed
// correctly with incremental host port allocation.
func TestBuildRunArgumentsPortMapping(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:    "test-ports",
		Image: "nginx:1.27",
		Ports: []int{8080, 9090},
	}

	args, firstHostPort, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	if firstHostPort != 8080 {
		t.Errorf("first host port: got %d, want 8080", firstHostPort)
	}

	portArgs := []string{}
	for argIndex, arg := range args {
		if arg == "-p" && argIndex+1 < len(args) {
			portArgs = append(portArgs, args[argIndex+1])
		}
	}

	if len(portArgs) != 2 {
		t.Fatalf("expected 2 port mappings, got %d: %v", len(portArgs), portArgs)
	}
	if portArgs[0] != "8080:8080" {
		t.Errorf("first port mapping: got %q, want %q", portArgs[0], "8080:8080")
	}
	if portArgs[1] != "9090:9090" {
		t.Errorf("second port mapping: got %q, want %q", portArgs[1], "9090:9090")
	}
}

// TestBuildRunArgumentsInvalidImageReturnsError verifies that an invalid image
// reference is rejected before reaching nerdctl.
func TestBuildRunArgumentsInvalidImageReturnsError(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:    "test-invalid",
		Image: "; rm -rf /",
	}

	_, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err == nil {
		t.Fatal("expected error for invalid image reference")
	}
}

// TestBuildRunArgumentsNetworkFlags verifies that network flags are added when
// both network name and IP are configured.
func TestBuildRunArgumentsNetworkFlags(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	containerRuntime.networkName = "ccattler-net"
	spec := Spec{
		ID:          "test-net",
		Image:       "nginx:1.27",
		ServiceName: "web",
		IP:          "10.100.0.5",
	}

	args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	argsJoined := strings.Join(args, " ")
	if !strings.Contains(argsJoined, "--network ccattler-net") {
		t.Error("missing --network flag")
	}
	if !strings.Contains(argsJoined, "--ip 10.100.0.5") {
		t.Error("missing --ip flag")
	}
	if !strings.Contains(argsJoined, "--network-alias web") {
		t.Error("missing --network-alias flag")
	}
}
