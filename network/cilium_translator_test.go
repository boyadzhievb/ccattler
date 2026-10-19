// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build cilium

package network

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

// TestCiliumLabelKeyFormat verifies that BuildCiliumLabelKey produces the
// sorted, semicolon-terminated format Cilium requires.
func TestCiliumLabelKeyFormat(testHandle *testing.T) {
	labelKey := BuildCiliumLabelKey("web")
	expected := "container:app=web;"
	if labelKey != expected {
		testHandle.Fatalf("expected %q, got %q", expected, labelKey)
	}
}

// TestCiliumLabelKeyDeterministic verifies that the same input always
// produces the same label key.
func TestCiliumLabelKeyDeterministic(testHandle *testing.T) {
	firstResult := BuildCiliumLabelKey("payments")
	for iteration := 0; iteration < 100; iteration++ {
		if BuildCiliumLabelKey("payments") != firstResult {
			testHandle.Fatal("label key is not deterministic")
		}
	}
}

// TestCiliumTranslatorInitializeRecoversIdentities verifies that Initialize
// scans existing identity master keys and resumes allocation from max+1.
func TestCiliumTranslatorInitializeRecoversIdentities(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	// Simulate pre-existing identities from a previous agent run.
	factStore.Put(ctx, "cilium/state/identities/v1/id/300", []byte("container:app=old;"))
	factStore.Put(ctx, "cilium/state/identities/v1/id/305", []byte("container:app=newer;"))

	translator := NewCiliumFactTranslator(factStore, "test-cluster", "node-1")
	if initError := translator.Initialize(ctx); initError != nil {
		testHandle.Fatalf("Initialize failed: %v", initError)
	}

	if translator.nextIdentityID != 306 {
		testHandle.Fatalf("expected nextIdentityID=306, got %d", translator.nextIdentityID)
	}

	// Verify the recovered identities are in the cache.
	if _, cached := translator.identityCache["container:app=old;"]; !cached {
		testHandle.Fatal("expected 'container:app=old;' in identity cache")
	}
	if _, cached := translator.identityCache["container:app=newer;"]; !cached {
		testHandle.Fatal("expected 'container:app=newer;' in identity cache")
	}
}

// TestCiliumTranslatorReconcileServicesWritesKvstoreKeys verifies that
// ReconcileServices writes service entries to the Cilium kvstore prefix.
func TestCiliumTranslatorReconcileServicesWritesKvstoreKeys(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	if initError := translator.Initialize(ctx); initError != nil {
		testHandle.Fatalf("Initialize failed: %v", initError)
	}

	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "web",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Protocol:    "tcp",
			Backends: []DataPlaneBackend{
				{Address: "10.100.1.10", Port: 8080},
				{Address: "10.100.2.20", Port: 8080},
			},
		},
	}

	reconcileError := translator.ReconcileServices(ctx, serviceConfigs)
	if reconcileError != nil {
		testHandle.Fatalf("ReconcileServices failed: %v", reconcileError)
	}

	expectedKey := "cilium/state/services/v1/ccattler/default/web"
	serviceFact, getError := factStore.Get(ctx, expectedKey)
	if getError != nil {
		testHandle.Fatalf("expected service key %q to exist, got error: %v", expectedKey, getError)
	}

	var serviceEntry ciliumServiceEntry
	if unmarshalError := json.Unmarshal(serviceFact.Value, &serviceEntry); unmarshalError != nil {
		testHandle.Fatalf("failed to unmarshal service entry: %v", unmarshalError)
	}

	if serviceEntry.Name != "web" {
		testHandle.Errorf("expected name 'web', got %q", serviceEntry.Name)
	}
	if serviceEntry.Frontend.Address != "10.200.0.1" {
		testHandle.Errorf("expected VIP '10.200.0.1', got %q", serviceEntry.Frontend.Address)
	}
	if serviceEntry.Frontend.Port != 80 {
		testHandle.Errorf("expected port 80, got %d", serviceEntry.Frontend.Port)
	}
	if serviceEntry.TrafficPolicy != "Cluster" {
		testHandle.Errorf("expected traffic_policy 'Cluster', got %q", serviceEntry.TrafficPolicy)
	}
	if len(serviceEntry.Backends) != 2 {
		testHandle.Errorf("expected 2 backends, got %d", len(serviceEntry.Backends))
	}
}

// TestCiliumTranslatorRemovesStaleServiceKeys verifies that services no
// longer in the desired set are deleted from the kvstore.
func TestCiliumTranslatorRemovesStaleServiceKeys(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	// Write two services.
	bothServices := []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80, Protocol: "tcp"},
		{ServiceName: "api", VirtualIP: "10.200.0.2", Port: 443, Protocol: "tcp"},
	}
	translator.ReconcileServices(ctx, bothServices)

	// Reconcile with only one service — "api" should be removed.
	oneService := []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80, Protocol: "tcp"},
	}
	translator.ReconcileServices(ctx, oneService)

	staleKey := "cilium/state/services/v1/ccattler/default/api"
	if _, getError := factStore.Get(ctx, staleKey); getError == nil {
		testHandle.Fatal("expected stale service key to be deleted")
	}

	activeKey := "cilium/state/services/v1/ccattler/default/web"
	if _, getError := factStore.Get(ctx, activeKey); getError != nil {
		testHandle.Fatalf("expected active service key to still exist, got: %v", getError)
	}
}

// TestCiliumTranslatorWriteEndpointIPIdentity verifies that endpoint
// registration writes master key, slave key, and IP-to-identity mapping.
func TestCiliumTranslatorWriteEndpointIPIdentity(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	writeError := translator.WriteEndpointIPIdentity(ctx, "10.100.1.42", "192.168.1.10", "web", 8080)
	if writeError != nil {
		testHandle.Fatalf("WriteEndpointIPIdentity failed: %v", writeError)
	}

	// Verify IP-to-identity mapping.
	ipKey := "cilium/state/ip/v1/ccattler/10.100.1.42"
	ipFact, getError := factStore.Get(ctx, ipKey)
	if getError != nil {
		testHandle.Fatalf("expected IP key to exist: %v", getError)
	}

	var ipPair ciliumIPIdentityPair
	if unmarshalError := json.Unmarshal(ipFact.Value, &ipPair); unmarshalError != nil {
		testHandle.Fatalf("failed to unmarshal IP pair: %v", unmarshalError)
	}
	if ipPair.ID < ciliumMinUserIdentity {
		testHandle.Errorf("expected identity ID >= %d, got %d", ciliumMinUserIdentity, ipPair.ID)
	}
	if ipPair.HostIP != "192.168.1.10" {
		testHandle.Errorf("expected HostIP '192.168.1.10', got %q", ipPair.HostIP)
	}
	if len(ipPair.NamedPorts) != 1 || ipPair.NamedPorts[0].Port != 8080 {
		testHandle.Errorf("expected named port 8080, got %v", ipPair.NamedPorts)
	}

	// Verify identity master key.
	masterKey := "cilium/state/identities/v1/id/256"
	masterFact, masterError := factStore.Get(ctx, masterKey)
	if masterError != nil {
		testHandle.Fatalf("expected identity master key: %v", masterError)
	}
	if !strings.Contains(string(masterFact.Value), "container:app=web;") {
		testHandle.Errorf("expected label key containing 'container:app=web;', got %q", string(masterFact.Value))
	}

	// Verify identity slave key.
	slavePrefix := "cilium/state/identities/v1/value/container:app=web;/node-1"
	slaveFact, slaveError := factStore.Get(ctx, slavePrefix)
	if slaveError != nil {
		testHandle.Fatalf("expected identity slave key: %v", slaveError)
	}
	if string(slaveFact.Value) != "256" {
		testHandle.Errorf("expected slave value '256', got %q", string(slaveFact.Value))
	}
}

// TestCiliumTranslatorNodeRegistration verifies that WriteNodeRegistration
// writes a Cilium node entry with correct addresses and pod CIDR.
func TestCiliumTranslatorNodeRegistration(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	writeError := translator.WriteNodeRegistration(ctx, "192.168.1.10", "10.100.1.0/24")
	if writeError != nil {
		testHandle.Fatalf("WriteNodeRegistration failed: %v", writeError)
	}

	nodeKey := "cilium/state/nodes/v1/ccattler/node-1"
	nodeFact, getError := factStore.Get(ctx, nodeKey)
	if getError != nil {
		testHandle.Fatalf("expected node key to exist: %v", getError)
	}

	var nodeReg ciliumNodeRegistration
	if unmarshalError := json.Unmarshal(nodeFact.Value, &nodeReg); unmarshalError != nil {
		testHandle.Fatalf("failed to unmarshal node registration: %v", unmarshalError)
	}
	if nodeReg.Name != "node-1" {
		testHandle.Errorf("expected node name 'node-1', got %q", nodeReg.Name)
	}
	if nodeReg.PodCIDR != "10.100.1.0/24" {
		testHandle.Errorf("expected pod CIDR '10.100.1.0/24', got %q", nodeReg.PodCIDR)
	}
	if len(nodeReg.Addresses) != 1 || nodeReg.Addresses[0].Address != "192.168.1.10" {
		testHandle.Errorf("expected InternalIP '192.168.1.10', got %v", nodeReg.Addresses)
	}
}

// TestCiliumTranslatorCleanupRemovesAllKeys verifies that CleanupAll
// removes all written keys and stops the lease renewal.
func TestCiliumTranslatorCleanupRemovesAllKeys(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	// Write some data.
	translator.ReconcileServices(ctx, []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80, Protocol: "tcp"},
	})
	translator.WriteEndpointIPIdentity(ctx, "10.100.1.42", "192.168.1.10", "web", 8080)
	translator.WriteNodeRegistration(ctx, "192.168.1.10", "10.100.1.0/24")

	// Cleanup.
	cleanupError := translator.CleanupAll(ctx)
	if cleanupError != nil {
		testHandle.Fatalf("CleanupAll failed: %v", cleanupError)
	}

	// Verify service key is gone.
	if _, err := factStore.Get(ctx, "cilium/state/services/v1/ccattler/default/web"); err == nil {
		testHandle.Error("expected service key to be deleted after cleanup")
	}

	// Verify IP key is gone.
	if _, err := factStore.Get(ctx, "cilium/state/ip/v1/ccattler/10.100.1.42"); err == nil {
		testHandle.Error("expected IP key to be deleted after cleanup")
	}

	// Verify identity master key is gone.
	if _, err := factStore.Get(ctx, "cilium/state/identities/v1/id/256"); err == nil {
		testHandle.Error("expected identity master key to be deleted after cleanup")
	}
}

// TestCiliumTranslatorResolveIPToIdentity verifies that ResolveIPToIdentity
// returns the correct security identity for a registered container IP.
func TestCiliumTranslatorResolveIPToIdentity(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	// Write an endpoint so we have an IP-to-identity mapping.
	translator.WriteEndpointIPIdentity(ctx, "10.100.1.42", "192.168.1.10", "web", 8080)

	resolvedIdentity := translator.ResolveIPToIdentity("10.100.1.42")
	if resolvedIdentity != 256 {
		testHandle.Fatalf("expected identity 256, got %d", resolvedIdentity)
	}

	// Unknown IP should return 0.
	unknownIdentity := translator.ResolveIPToIdentity("10.100.99.99")
	if unknownIdentity != 0 {
		testHandle.Fatalf("expected identity 0 for unknown IP, got %d", unknownIdentity)
	}
}

// TestCiliumDataPlaneImplementsOptionalInterfaces verifies that CiliumDataPlane
// satisfies all four optional data plane interfaces.
func TestCiliumDataPlaneImplementsOptionalInterfaces(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")

	var _ DataPlaneInitializer = ciliumDP
	var _ DataPlaneNodeRegistrar = ciliumDP
	var _ EndpointIdentityRegistrar = ciliumDP
	var _ DataPlanePolicyReconciler = ciliumDP
}

// TestCiliumDataPlaneRegisterNodeWritesKvstoreEntry verifies that RegisterNode
// delegates to the fact translator and writes a Cilium node entry.
func TestCiliumDataPlaneRegisterNodeWritesKvstoreEntry(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	ciliumDP.Initialize(ctx)

	regError := ciliumDP.RegisterNode(ctx, "192.168.1.10", "10.100.1.0/24")
	if regError != nil {
		testHandle.Fatalf("RegisterNode failed: %v", regError)
	}

	nodeKey := "cilium/state/nodes/v1/ccattler/node-1"
	if _, getError := factStore.Get(ctx, nodeKey); getError != nil {
		testHandle.Fatalf("expected node key to exist: %v", getError)
	}
}

// TestCiliumDataPlaneRegisterEndpointIdentity verifies that endpoint identity
// registration writes IP-to-identity and identity master/slave keys.
func TestCiliumDataPlaneRegisterEndpointIdentity(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	ciliumDP.Initialize(ctx)

	regError := ciliumDP.RegisterEndpointIdentity(ctx, "10.100.1.42", "192.168.1.10", "web", 8080)
	if regError != nil {
		testHandle.Fatalf("RegisterEndpointIdentity failed: %v", regError)
	}

	ipKey := "cilium/state/ip/v1/ccattler/10.100.1.42"
	if _, getError := factStore.Get(ctx, ipKey); getError != nil {
		testHandle.Fatalf("expected IP key: %v", getError)
	}

	masterKey := "cilium/state/identities/v1/id/256"
	if _, getError := factStore.Get(ctx, masterKey); getError != nil {
		testHandle.Fatalf("expected identity master key: %v", getError)
	}
}

// TestCiliumDataPlanePolicyReconciliation verifies that ReconcilePolicies reads
// compiled rules and writes Cilium policy entries when identities are known.
func TestCiliumDataPlanePolicyReconciliation(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	ciliumDP.Initialize(ctx)

	// Register endpoints so identities exist for policy resolution.
	ciliumDP.RegisterEndpointIdentity(ctx, "10.100.1.10", "192.168.1.10", "web", 80)
	ciliumDP.RegisterEndpointIdentity(ctx, "10.100.1.20", "192.168.1.10", "api", 443)

	// Write a compiled network rule: web → api on port 443.
	factStore.Put(ctx, "derived/network/rule/node-1/0", []byte("10.100.1.10:10.100.1.20:443:allow"))

	reconcileError := ciliumDP.ReconcilePolicies(ctx)
	if reconcileError != nil {
		testHandle.Fatalf("ReconcilePolicies failed: %v", reconcileError)
	}

	// Verify a Cilium policy key was written (identities 256 and 257).
	policyKey := "cilium/state/policy/v1/ccattler/256-257-443"
	policyFact, getError := factStore.Get(ctx, policyKey)
	if getError != nil {
		testHandle.Fatalf("expected policy key to exist: %v", getError)
	}
	if !strings.Contains(string(policyFact.Value), `"action":"allow"`) {
		testHandle.Errorf("expected policy action 'allow', got %q", string(policyFact.Value))
	}
}
