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

	args, portMappings, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(portMappings) != 2 {
		t.Fatalf("expected 2 port mappings, got %d", len(portMappings))
	}
	if portMappings[8080] != 8080 {
		t.Errorf("host port for 8080: got %d, want 8080", portMappings[8080])
	}
	if portMappings[9090] != 9090 {
		t.Errorf("host port for 9090: got %d, want 9090", portMappings[9090])
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

// TestBuildRunArgumentsNetworkFlagsDocker verifies that Docker gets
// --network-alias for DNS service discovery.
func TestBuildRunArgumentsNetworkFlagsDocker(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	containerRuntime.networkName = "ccattler-net"
	containerRuntime.containerCommand = "docker"
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
		t.Error("Docker should use --network-alias for DNS")
	}
}

// TestBuildRunArgumentsNetworkFlagsNerdctl verifies that nerdctl gets
// --hostname for DNS service discovery (nerdctl v2 removed --network-alias).
func TestBuildRunArgumentsNetworkFlagsNerdctl(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	containerRuntime.networkName = "ccattler-net"
	containerRuntime.containerCommand = "nerdctl"
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
	if !strings.Contains(argsJoined, "--hostname web") {
		t.Error("nerdctl should use --hostname for DNS")
	}
	if strings.Contains(argsJoined, "--network-alias") {
		t.Error("nerdctl v2 does not support --network-alias")
	}
}

// TestBuildRunArgumentsNetworkWithoutIPUsesDNSAlias verifies that containers
// are placed on the named network with DNS even when no specific IP is
// allocated. This enables service-name resolution via embedded DNS.
func TestBuildRunArgumentsNetworkWithoutIPUsesDNSAlias(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	containerRuntime.networkName = "cca-net"
	containerRuntime.containerCommand = "nerdctl"
	spec := Spec{
		ID:          "test-dns-only",
		Image:       "postgres:16",
		ServiceName: "metadata-db",
	}

	args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	argsJoined := strings.Join(args, " ")
	if !strings.Contains(argsJoined, "--network cca-net") {
		t.Error("missing --network flag when networkName is set without IP")
	}
	if strings.Contains(argsJoined, "--ip") {
		t.Error("--ip flag should not be present when spec.IP is empty")
	}
	if !strings.Contains(argsJoined, "--hostname metadata-db") {
		t.Error("missing --hostname for DNS service discovery")
	}
}

func TestBuildRunArgumentsCommandAppendedAfterImage(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:      "test-cmd",
		Image:   "apache/airflow:2.10.5",
		Command: []string{"sh", "-c", "airflow webserver"},
	}

	args, _, buildError := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if buildError != nil {
		t.Fatal(buildError)
	}

	imageIndex := -1
	for argIndex, argValue := range args {
		if argValue == "apache/airflow:2.10.5" {
			imageIndex = argIndex
			break
		}
	}
	if imageIndex < 0 {
		t.Fatal("image not found in args")
	}
	if imageIndex+3 >= len(args) {
		t.Fatalf("not enough args after image: %v", args)
	}
	if args[imageIndex+1] != "sh" || args[imageIndex+2] != "-c" || args[imageIndex+3] != "airflow webserver" {
		t.Errorf("command not appended as sh -c after image: got %v", args[imageIndex+1:])
	}
}

func TestBuildRunArgumentsShellCommandPreservesFullString(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	complexCommand := "source /init/secrets-store/envfile && java -jar -Dspring.profiles.active=default app.jar --spring.config.location=/opt/app.yaml"
	spec := Spec{
		ID:      "test-shell-cmd",
		Image:   "openjdk:21",
		Command: []string{"sh", "-c", complexCommand},
	}

	args, _, buildError := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if buildError != nil {
		t.Fatal(buildError)
	}

	lastArg := args[len(args)-1]
	if lastArg != complexCommand {
		t.Errorf("shell command was split or modified: got %q, want %q", lastArg, complexCommand)
	}
}

func TestBuildRunArgumentsEmptyCommandOmitted(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:    "test-no-cmd",
		Image: "nginx:1.27",
	}

	args, _, buildError := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if buildError != nil {
		t.Fatal(buildError)
	}

	lastArg := args[len(args)-1]
	if lastArg != "nginx:1.27" {
		t.Errorf("last arg should be image when no command, got %q", lastArg)
	}
}

// TestBuildRunArgumentsDNSFlagInjected verifies that when SetDNS is called,
// containers receive --dns pointing at the node's address for service name
// resolution via the CCattler DNS server.
func TestBuildRunArgumentsDNSFlagInjected(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	containerRuntime.SetDNS("192.168.124.10")
	spec := Spec{
		ID:    "test-dns",
		Image: "nginx:1.27",
	}

	args, _, buildError := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if buildError != nil {
		t.Fatal(buildError)
	}

	argsJoined := strings.Join(args, " ")
	if !strings.Contains(argsJoined, "--dns 192.168.124.10") {
		t.Errorf("missing --dns flag in args: %v", args)
	}
	if !strings.Contains(argsJoined, "--dns-search "+defaultDNSSearchDomain) {
		t.Errorf("missing --dns-search flag in args: %v", args)
	}
}

// TestBuildRunArgumentsNoDNSFlagWhenUnset verifies that no --dns flag is added
// when SetDNS has not been called, preserving the runtime's default behavior.
func TestBuildRunArgumentsNoDNSFlagWhenUnset(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:    "test-no-dns",
		Image: "nginx:1.27",
	}

	args, _, buildError := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if buildError != nil {
		t.Fatal(buildError)
	}

	for _, argValue := range args {
		if argValue == "--dns" {
			t.Error("--dns flag should not be present when dnsAddress is empty")
		}
		if argValue == "--dns-search" {
			t.Error("--dns-search flag should not be present when dnsAddress is empty")
		}
	}
}

// TestBuildRunArgumentsLogRotation verifies that log rotation flags are emitted
// when LogMaxSize or LogMaxFiles are set on the Spec.
func TestBuildRunArgumentsLogRotation(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:          "test-log",
		Image:       "nginx:1.27",
		LogMaxSize:  "10m",
		LogMaxFiles: 3,
	}

	args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	argsJoined := strings.Join(args, " ")
	if !strings.Contains(argsJoined, "--log-driver json-file") {
		t.Error("expected --log-driver json-file when log rotation is configured")
	}
	if !strings.Contains(argsJoined, "--log-opt max-size=10m") {
		t.Error("expected --log-opt max-size=10m")
	}
	if !strings.Contains(argsJoined, "--log-opt max-file=3") {
		t.Error("expected --log-opt max-file=3")
	}
}

// TestBuildRunArgumentsLogRotationOmittedWhenUnset verifies that no log driver
// flags are added when neither LogMaxSize nor LogMaxFiles are set.
func TestBuildRunArgumentsLogRotationOmittedWhenUnset(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:    "test-nolog",
		Image: "nginx:1.27",
	}

	args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, argValue := range args {
		if argValue == "--log-driver" {
			t.Error("--log-driver should not be present when log rotation is not configured")
		}
	}
}

// TestBuildRunArgumentsLogRotationMaxSizeOnly verifies that setting only
// LogMaxSize still emits the log driver and the max-size opt.
func TestBuildRunArgumentsLogRotationMaxSizeOnly(t *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{
		ID:         "test-logsize",
		Image:      "nginx:1.27",
		LogMaxSize: "50m",
	}

	args, _, err := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if err != nil {
		t.Fatal(err)
	}

	argsJoined := strings.Join(args, " ")
	if !strings.Contains(argsJoined, "--log-driver json-file") {
		t.Error("expected --log-driver json-file")
	}
	if !strings.Contains(argsJoined, "--log-opt max-size=50m") {
		t.Error("expected --log-opt max-size=50m")
	}
	if strings.Contains(argsJoined, "max-file") {
		t.Error("--log-opt max-file should not be present when LogMaxFiles is 0")
	}
}
