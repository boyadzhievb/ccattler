// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestStaleBridgeAddresses: cca0 may keep only the gateway of the current node
// subnet; a gateway of an earlier subnet (redeploy with a new cluster CIDR) or
// any other IPv4 address must go, IPv6 addresses are left alone (M222).
func TestStaleBridgeAddresses(t *testing.T) {
	testCases := []struct {
		name             string   // name describes the bridge state.
		currentAddresses []string // currentAddresses are the bridge's addresses in CIDR form.
		subnetCIDR       string   // subnetCIDR is the node's container subnet.
		wantStale        []string // wantStale are the addresses to remove, in input order.
	}{
		{name: "no bridge addresses", currentAddresses: nil, subnetCIDR: "10.67.102.0/24", wantStale: nil},
		{name: "correct gateway only", currentAddresses: []string{"10.67.102.1/24"}, subnetCIDR: "10.67.102.0/24", wantStale: nil},
		{name: "gateway of an earlier subnet", currentAddresses: []string{"10.45.17.1/24"}, subnetCIDR: "10.67.102.0/24", wantStale: []string{"10.45.17.1/24"}},
		{name: "earlier and current gateway", currentAddresses: []string{"10.45.17.1/24", "10.67.102.1/24"}, subnetCIDR: "10.67.102.0/24", wantStale: []string{"10.45.17.1/24"}},
		{name: "right address, wrong prefix length", currentAddresses: []string{"10.67.102.1/16"}, subnetCIDR: "10.67.102.0/24", wantStale: []string{"10.67.102.1/16"}},
		{name: "IPv6 link-local kept", currentAddresses: []string{"fe80::1/64", "10.45.17.1/24"}, subnetCIDR: "10.67.102.0/24", wantStale: []string{"10.45.17.1/24"}},
		{name: "subnet given with a host address", currentAddresses: []string{"10.67.102.1/24"}, subnetCIDR: "10.67.102.7/24", wantStale: nil},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stale, staleError := staleBridgeAddresses(testCase.currentAddresses, testCase.subnetCIDR)
			if staleError != nil {
				t.Fatal(staleError)
			}
			if !slices.Equal(stale, testCase.wantStale) {
				t.Errorf("staleBridgeAddresses(%v, %s) = %v, want %v", testCase.currentAddresses, testCase.subnetCIDR, stale, testCase.wantStale)
			}
		})
	}
	if _, invalidError := staleBridgeAddresses([]string{"10.67.102.1/24"}, "not-a-cidr"); invalidError == nil {
		t.Error("an invalid subnet must be an error, not an empty list")
	}
}

// TestEnsureBridgeAddressMatchesSubnetOnARealBridge creates a bridge with the
// gateway of an earlier subnet and checks that only that address is removed.
// It needs root and its own network namespace; run it with
// `unshare --user --map-root-user --net go test -run …` (skipped otherwise).
func TestEnsureBridgeAddressMatchesSubnetOnARealBridge(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root in a private network namespace (unshare --user --map-root-user --net)")
	}
	const testBridgeName = "cca-test0"
	for _, setupCommand := range [][]string{
		{"ip", "link", "add", testBridgeName, "type", "bridge"},
		{"ip", "addr", "add", "10.45.17.1/24", "dev", testBridgeName},
		{"ip", "addr", "add", "10.67.102.1/24", "dev", testBridgeName},
	} {
		if output, runError := exec.Command(setupCommand[0], setupCommand[1:]...).CombinedOutput(); runError != nil { //nolint:gosec // fixed test commands
			t.Fatalf("%v: %v %s", setupCommand, runError, output)
		}
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", testBridgeName).Run() })

	if ensureError := ensureBridgeAddressMatchesSubnet(testBridgeName, "10.67.102.0/24"); ensureError != nil {
		t.Fatal(ensureError)
	}
	output, _ := exec.Command("ip", "-4", "-o", "addr", "show", "dev", testBridgeName).CombinedOutput()
	if strings.Contains(string(output), "10.45.17.1/24") || !strings.Contains(string(output), "10.67.102.1/24") {
		t.Errorf("bridge addresses after ensure:\n%s\nwant only 10.67.102.1/24", output)
	}
	if missingError := ensureBridgeAddressMatchesSubnet("cca-missing0", "10.67.102.0/24"); missingError != nil {
		t.Errorf("a missing bridge must not be an error: %v", missingError)
	}
}

// TestContainerdRuntimeCNIPluginDirectories: the CNI library searches /opt/cni/bin
// first, then the distribution directories (EL9 installs to /usr/libexec/cni).
func TestContainerdRuntimeCNIPluginDirectories(t *testing.T) {
	cniNetwork, initError := newContainerdCNINetwork(buildCNIConflist("cca-test", "10.100.1.0/24"))
	if initError != nil {
		t.Fatalf("newContainerdCNINetwork: %v", initError)
	}
	wantedDirectories := []string{"/opt/cni/bin", "/usr/libexec/cni", "/usr/lib/cni"}
	if pluginDirectories := cniNetwork.GetConfig().PluginDirs; !slices.Equal(pluginDirectories, wantedDirectories) {
		t.Errorf("CNI plugin directories = %v, want %v", pluginDirectories, wantedDirectories)
	}
}

// installScriptCNILoopPattern matches the CNI directory loop in the root install.sh.
var installScriptCNILoopPattern = regexp.MustCompile(`for cni_directory in ([^;]+); do`)

// TestInstallScriptCNIDirectoriesMatchAgent: install.sh's prerequisite check looks
// in the same CNI directories, in the same order, as the agent (one list per language).
func TestInstallScriptCNIDirectoriesMatchAgent(t *testing.T) {
	scriptContent, readError := os.ReadFile(filepath.Join("..", "install.sh"))
	if readError != nil {
		t.Fatalf("read install.sh: %v", readError)
	}
	loopMatch := installScriptCNILoopPattern.FindSubmatch(scriptContent)
	if loopMatch == nil {
		t.Fatal("install.sh has no `for cni_directory in ...; do` loop")
	}
	if scriptDirectories := strings.Fields(string(loopMatch[1])); !slices.Equal(scriptDirectories, cniPluginDirectories) {
		t.Errorf("install.sh CNI directories = %v, agent cniPluginDirectories = %v", scriptDirectories, cniPluginDirectories)
	}
}
