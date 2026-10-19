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

// TestCiliumDataPlaneReconcileVIPWritesServiceEntries verifies that the full
// CiliumDataPlane.ReconcileVIPDataPlane path translates VIP configs into
// Cilium kvstore service entries.
func TestCiliumDataPlaneReconcileVIPWritesServiceEntries(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	ciliumDP.Initialize(ctx)

	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "api",
			VirtualIP:   "10.200.0.5",
			Port:        443,
			Protocol:    "tcp",
			Backends: []DataPlaneBackend{
				{Address: "10.100.1.10", Port: 8443},
			},
		},
	}

	reconcileError := ciliumDP.ReconcileVIPDataPlane(ctx, serviceConfigs)
	if reconcileError != nil {
		testHandle.Fatalf("ReconcileVIPDataPlane failed: %v", reconcileError)
	}

	expectedKey := "cilium/state/services/v1/ccattler/default/api"
	serviceFact, getError := factStore.Get(ctx, expectedKey)
	if getError != nil {
		testHandle.Fatalf("expected service key to exist: %v", getError)
	}

	var serviceEntry ciliumServiceEntry
	if unmarshalError := json.Unmarshal(serviceFact.Value, &serviceEntry); unmarshalError != nil {
		testHandle.Fatalf("unmarshal failed: %v", unmarshalError)
	}
	if serviceEntry.Frontend.Port != 443 {
		testHandle.Errorf("expected frontend port 443, got %d", serviceEntry.Frontend.Port)
	}
	if len(serviceEntry.Backends) != 1 {
		testHandle.Fatalf("expected 1 backend, got %d", len(serviceEntry.Backends))
	}
	if serviceEntry.Backends[0].Address != "10.100.1.10" {
		testHandle.Errorf("expected backend address '10.100.1.10', got %q", serviceEntry.Backends[0].Address)
	}
}

// TestCiliumDataPlaneReconcileVIPSetsInitializedFlag verifies that the first
// ReconcileVIPDataPlane call sets the initialized flag.
func TestCiliumDataPlaneReconcileVIPSetsInitializedFlag(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	ciliumDP.Initialize(ctx)

	if ciliumDP.initialized {
		testHandle.Fatal("expected initialized=false before first reconcile")
	}

	ciliumDP.ReconcileVIPDataPlane(ctx, []ServiceVIPConfig{})

	if !ciliumDP.initialized {
		testHandle.Fatal("expected initialized=true after first reconcile")
	}
}

// TestCiliumDataPlaneReconcileRouteWritesRouteServiceEntries verifies that
// ReconcileRouteDataPlane translates route configs into Cilium service entries
// in the routes namespace.
func TestCiliumDataPlaneReconcileRouteWritesRouteServiceEntries(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	ciliumDP.Initialize(ctx)

	routeConfigs := []RouteDataPlaneConfig{
		{
			RouteName:   "web-public",
			ServiceName: "web",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Backends: []DataPlaneBackend{
				{Address: "10.100.1.10", Port: 8080},
				{Address: "10.100.2.20", Port: 8080},
			},
		},
	}

	reconcileError := ciliumDP.ReconcileRouteDataPlane(ctx, routeConfigs)
	if reconcileError != nil {
		testHandle.Fatalf("ReconcileRouteDataPlane failed: %v", reconcileError)
	}

	expectedKey := "cilium/state/services/v1/ccattler/routes/web-public"
	routeFact, getError := factStore.Get(ctx, expectedKey)
	if getError != nil {
		testHandle.Fatalf("expected route service key to exist: %v", getError)
	}

	var serviceEntry ciliumServiceEntry
	if unmarshalError := json.Unmarshal(routeFact.Value, &serviceEntry); unmarshalError != nil {
		testHandle.Fatalf("unmarshal failed: %v", unmarshalError)
	}
	if serviceEntry.Namespace != "routes" {
		testHandle.Errorf("expected namespace 'routes', got %q", serviceEntry.Namespace)
	}
	if serviceEntry.Frontend.Address != "10.200.0.1" {
		testHandle.Errorf("expected VIP '10.200.0.1', got %q", serviceEntry.Frontend.Address)
	}
	if len(serviceEntry.Backends) != 2 {
		testHandle.Fatalf("expected 2 backends, got %d", len(serviceEntry.Backends))
	}
}

// TestCiliumDataPlaneCleanupRemovesPoliciesAndTranslatorKeys verifies that
// Cleanup removes both policy keys and translator keys.
func TestCiliumDataPlaneCleanupRemovesPoliciesAndTranslatorKeys(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	ciliumDP.Initialize(ctx)

	// Write VIP data.
	ciliumDP.ReconcileVIPDataPlane(ctx, []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80, Protocol: "tcp"},
	})

	// Write endpoint identity.
	ciliumDP.RegisterEndpointIdentity(ctx, "10.100.1.42", "192.168.1.10", "web", 80)

	// Write policy.
	factStore.Put(ctx, "derived/network/rule/node-1/0", []byte("10.100.1.42:10.100.1.42:80:allow"))
	ciliumDP.ReconcilePolicies(ctx)

	// Verify keys exist before cleanup.
	if _, getError := factStore.Get(ctx, "cilium/state/services/v1/ccattler/default/web"); getError != nil {
		testHandle.Fatalf("expected service key to exist before cleanup: %v", getError)
	}

	// Cleanup.
	cleanupError := ciliumDP.Cleanup(ctx)
	if cleanupError != nil {
		testHandle.Fatalf("Cleanup failed: %v", cleanupError)
	}

	// Verify service key is gone.
	if _, getError := factStore.Get(ctx, "cilium/state/services/v1/ccattler/default/web"); getError == nil {
		testHandle.Error("expected service key to be deleted after cleanup")
	}

	// Verify IP key is gone.
	if _, getError := factStore.Get(ctx, "cilium/state/ip/v1/ccattler/10.100.1.42"); getError == nil {
		testHandle.Error("expected IP key to be deleted after cleanup")
	}
}

// TestCiliumDataPlaneFactTranslatorAccessor verifies the FactTranslator
// getter returns the internal translator.
func TestCiliumDataPlaneFactTranslatorAccessor(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ciliumDP := NewCiliumDataPlane(factStore, "ccattler", "node-1")
	translator := ciliumDP.FactTranslator()
	if translator == nil {
		testHandle.Fatal("FactTranslator() returned nil")
	}
	if translator.clusterName != "ccattler" {
		testHandle.Errorf("expected clusterName 'ccattler', got %q", translator.clusterName)
	}
}

// TestCiliumTranslatorReconcileRoutesWritesRouteServiceKeys verifies that
// ReconcileRoutes translates route configs into Cilium service entries.
func TestCiliumTranslatorReconcileRoutesWritesRouteServiceKeys(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	routeConfigs := []RouteDataPlaneConfig{
		{
			RouteName:   "api-gateway",
			ServiceName: "api",
			VirtualIP:   "10.200.0.3",
			Port:        443,
			Backends: []DataPlaneBackend{
				{Address: "10.100.1.50", Port: 8443},
			},
		},
	}

	reconcileError := translator.ReconcileRoutes(ctx, routeConfigs)
	if reconcileError != nil {
		testHandle.Fatalf("ReconcileRoutes failed: %v", reconcileError)
	}

	expectedKey := "cilium/state/services/v1/ccattler/routes/api-gateway"
	routeFact, getError := factStore.Get(ctx, expectedKey)
	if getError != nil {
		testHandle.Fatalf("expected route key to exist: %v", getError)
	}

	var serviceEntry ciliumServiceEntry
	if unmarshalError := json.Unmarshal(routeFact.Value, &serviceEntry); unmarshalError != nil {
		testHandle.Fatalf("unmarshal failed: %v", unmarshalError)
	}
	if serviceEntry.Name != "api-gateway" {
		testHandle.Errorf("expected name 'api-gateway', got %q", serviceEntry.Name)
	}
	if serviceEntry.Namespace != "routes" {
		testHandle.Errorf("expected namespace 'routes', got %q", serviceEntry.Namespace)
	}
	if serviceEntry.Frontend.Protocol != "TCP" {
		testHandle.Errorf("expected protocol 'TCP', got %q", serviceEntry.Frontend.Protocol)
	}
}

// TestCiliumTranslatorReconcileRoutesMultipleBackends verifies that route
// reconciliation handles multiple backends correctly.
func TestCiliumTranslatorReconcileRoutesMultipleBackends(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	routeConfigs := []RouteDataPlaneConfig{
		{
			RouteName:   "web-route",
			ServiceName: "web",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Backends: []DataPlaneBackend{
				{Address: "10.100.1.10", Port: 8080},
				{Address: "10.100.2.20", Port: 8080},
				{Address: "10.100.3.30", Port: 8080},
			},
		},
	}

	translator.ReconcileRoutes(ctx, routeConfigs)

	routeKey := "cilium/state/services/v1/ccattler/routes/web-route"
	routeFact, _ := factStore.Get(ctx, routeKey)

	var serviceEntry ciliumServiceEntry
	json.Unmarshal(routeFact.Value, &serviceEntry)
	if len(serviceEntry.Backends) != 3 {
		testHandle.Fatalf("expected 3 backends, got %d", len(serviceEntry.Backends))
	}
}

// TestCiliumPolicyCleanupRemovesAllPolicyKeys verifies that CleanupPolicies
// removes all previously written policy keys.
func TestCiliumPolicyCleanupRemovesAllPolicyKeys(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	policyTranslator := NewCiliumPolicyTranslator(factStore, "ccattler")

	// Manually track some policy keys as if ReconcilePolicies had written them.
	policyTranslator.writtenPolicyKeys["cilium/state/policy/v1/ccattler/256-257-80"] = true
	policyTranslator.writtenPolicyKeys["cilium/state/policy/v1/ccattler/256-258-443"] = true

	// Write those keys to the store so Delete has something to delete.
	factStore.Put(ctx, "cilium/state/policy/v1/ccattler/256-257-80", []byte(`{"action":"allow"}`))
	factStore.Put(ctx, "cilium/state/policy/v1/ccattler/256-258-443", []byte(`{"action":"deny"}`))

	cleanupError := policyTranslator.CleanupPolicies(ctx)
	if cleanupError != nil {
		testHandle.Fatalf("CleanupPolicies failed: %v", cleanupError)
	}

	// Verify both keys are gone.
	if _, getError := factStore.Get(ctx, "cilium/state/policy/v1/ccattler/256-257-80"); getError == nil {
		testHandle.Error("expected first policy key to be deleted")
	}
	if _, getError := factStore.Get(ctx, "cilium/state/policy/v1/ccattler/256-258-443"); getError == nil {
		testHandle.Error("expected second policy key to be deleted")
	}

	// Verify the tracking map is reset.
	if len(policyTranslator.writtenPolicyKeys) != 0 {
		testHandle.Errorf("expected empty writtenPolicyKeys, got %d entries", len(policyTranslator.writtenPolicyKeys))
	}
}

// TestCiliumPolicyReconcileMalformedRuleSkipped verifies that malformed compiled
// rules (not 4 colon-separated fields) are skipped without error.
func TestCiliumPolicyReconcileMalformedRuleSkipped(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	policyTranslator := NewCiliumPolicyTranslator(factStore, "ccattler")

	// Write a malformed compiled rule (only 2 fields instead of 4).
	factStore.Put(ctx, "derived/network/rule/node-1/0", []byte("10.100.1.10:bad"))

	identityResolver := func(ipAddress string) int { return 256 }

	reconcileError := policyTranslator.ReconcilePolicies(ctx, identityResolver)
	if reconcileError != nil {
		testHandle.Fatalf("ReconcilePolicies should not fail on malformed rules: %v", reconcileError)
	}

	// No policy keys should have been written.
	policyFacts, _ := factStore.Scan(ctx, "cilium/state/policy/")
	if len(policyFacts) != 0 {
		testHandle.Errorf("expected 0 policy keys for malformed rule, got %d", len(policyFacts))
	}
}

// TestCiliumPolicyReconcileDenyAction verifies that deny rules produce Cilium
// policy entries with action "deny".
func TestCiliumPolicyReconcileDenyAction(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	policyTranslator := NewCiliumPolicyTranslator(factStore, "ccattler")

	factStore.Put(ctx, "derived/network/rule/node-1/0", []byte("10.100.1.10:10.100.1.20:443:deny"))

	identityResolver := func(ipAddress string) int {
		if ipAddress == "10.100.1.10" {
			return 256
		}
		if ipAddress == "10.100.1.20" {
			return 257
		}
		return 0
	}

	policyTranslator.ReconcilePolicies(ctx, identityResolver)

	policyKey := "cilium/state/policy/v1/ccattler/256-257-443"
	policyFact, getError := factStore.Get(ctx, policyKey)
	if getError != nil {
		testHandle.Fatalf("expected deny policy key to exist: %v", getError)
	}

	var entry ciliumPolicyEntry
	json.Unmarshal(policyFact.Value, &entry)
	if entry.Action != "deny" {
		testHandle.Errorf("expected action 'deny', got %q", entry.Action)
	}
}

// TestCiliumPolicyReconcileZeroIdentitySkipped verifies that rules where
// either source or destination IP has no known identity are skipped.
func TestCiliumPolicyReconcileZeroIdentitySkipped(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	policyTranslator := NewCiliumPolicyTranslator(factStore, "ccattler")

	factStore.Put(ctx, "derived/network/rule/node-1/0", []byte("10.100.1.10:10.100.1.20:80:allow"))

	// Source resolves but destination does not.
	identityResolver := func(ipAddress string) int {
		if ipAddress == "10.100.1.10" {
			return 256
		}
		return 0
	}

	policyTranslator.ReconcilePolicies(ctx, identityResolver)

	policyFacts, _ := factStore.Scan(ctx, "cilium/state/policy/")
	if len(policyFacts) != 0 {
		testHandle.Errorf("expected 0 policy keys when identity is unknown, got %d", len(policyFacts))
	}
}

// TestCiliumPolicyStalePolicyKeysRemoved verifies that policy keys from a
// previous reconciliation are removed when those rules no longer exist.
func TestCiliumPolicyStalePolicyKeysRemoved(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	policyTranslator := NewCiliumPolicyTranslator(factStore, "ccattler")
	identityResolver := func(ipAddress string) int {
		if ipAddress == "10.100.1.10" {
			return 256
		}
		if ipAddress == "10.100.1.20" {
			return 257
		}
		return 0
	}

	// First reconciliation: one rule.
	factStore.Put(ctx, "derived/network/rule/node-1/0", []byte("10.100.1.10:10.100.1.20:80:allow"))
	policyTranslator.ReconcilePolicies(ctx, identityResolver)

	policyKey := "cilium/state/policy/v1/ccattler/256-257-80"
	if _, getError := factStore.Get(ctx, policyKey); getError != nil {
		testHandle.Fatalf("expected policy key after first reconcile: %v", getError)
	}

	// Second reconciliation: rule removed.
	factStore.Delete(ctx, "derived/network/rule/node-1/0")
	policyTranslator.ReconcilePolicies(ctx, identityResolver)

	if _, getError := factStore.Get(ctx, policyKey); getError == nil {
		testHandle.Error("expected stale policy key to be deleted after second reconcile")
	}
}

// TestCiliumPolicyReconcileZeroPortHandled verifies that rules with port "0"
// or empty port are handled (destinationPort=0).
func TestCiliumPolicyReconcileZeroPortHandled(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	policyTranslator := NewCiliumPolicyTranslator(factStore, "ccattler")
	identityResolver := func(ipAddress string) int {
		if ipAddress == "10.100.1.10" {
			return 256
		}
		if ipAddress == "10.100.1.20" {
			return 257
		}
		return 0
	}

	factStore.Put(ctx, "derived/network/rule/node-1/0", []byte("10.100.1.10:10.100.1.20:0:allow"))
	policyTranslator.ReconcilePolicies(ctx, identityResolver)

	policyKey := "cilium/state/policy/v1/ccattler/256-257-0"
	policyFact, getError := factStore.Get(ctx, policyKey)
	if getError != nil {
		testHandle.Fatalf("expected policy key with port 0: %v", getError)
	}

	var entry ciliumPolicyEntry
	json.Unmarshal(policyFact.Value, &entry)
	if entry.DestinationPort != 0 {
		testHandle.Errorf("expected destination port 0, got %d", entry.DestinationPort)
	}
}

// TestCiliumTranslatorIdentityReuseForSameService verifies that writing
// multiple endpoints for the same service reuses the same identity ID.
func TestCiliumTranslatorIdentityReuseForSameService(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	// Two different IPs for the same service.
	translator.WriteEndpointIPIdentity(ctx, "10.100.1.10", "192.168.1.10", "web", 80)
	translator.WriteEndpointIPIdentity(ctx, "10.100.1.11", "192.168.1.10", "web", 80)

	// Both should resolve to the same identity.
	id1 := translator.ResolveIPToIdentity("10.100.1.10")
	id2 := translator.ResolveIPToIdentity("10.100.1.11")

	if id1 != id2 {
		testHandle.Errorf("expected same identity for same service, got %d and %d", id1, id2)
	}
	if id1 != 256 {
		testHandle.Errorf("expected identity 256, got %d", id1)
	}
}

// TestCiliumTranslatorDifferentServicesGetDifferentIdentities verifies that
// endpoints for different services get distinct identity IDs.
func TestCiliumTranslatorDifferentServicesGetDifferentIdentities(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	translator.WriteEndpointIPIdentity(ctx, "10.100.1.10", "192.168.1.10", "web", 80)
	translator.WriteEndpointIPIdentity(ctx, "10.100.1.20", "192.168.1.10", "api", 443)

	webIdentity := translator.ResolveIPToIdentity("10.100.1.10")
	apiIdentity := translator.ResolveIPToIdentity("10.100.1.20")

	if webIdentity == apiIdentity {
		testHandle.Errorf("expected different identities, both got %d", webIdentity)
	}
	if webIdentity != 256 || apiIdentity != 257 {
		testHandle.Errorf("expected identities 256 and 257, got %d and %d", webIdentity, apiIdentity)
	}
}

// TestCiliumTranslatorEndpointWithoutPort verifies that endpoints with
// containerPort=0 don't include named ports in the IP identity pair.
func TestCiliumTranslatorEndpointWithoutPort(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	translator.WriteEndpointIPIdentity(ctx, "10.100.1.42", "192.168.1.10", "worker", 0)

	ipKey := "cilium/state/ip/v1/ccattler/10.100.1.42"
	ipFact, _ := factStore.Get(ctx, ipKey)

	var ipPair ciliumIPIdentityPair
	json.Unmarshal(ipFact.Value, &ipPair)
	if len(ipPair.NamedPorts) != 0 {
		testHandle.Errorf("expected no named ports for containerPort=0, got %v", ipPair.NamedPorts)
	}
}

// TestCiliumTranslatorWriteIdentityMasterKeySkipsDuplicate verifies that
// writing the same identity master key twice does not fail or overwrite.
func TestCiliumTranslatorWriteIdentityMasterKeySkipsDuplicate(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	// Write first endpoint — creates master key for identity 256.
	translator.WriteEndpointIPIdentity(ctx, "10.100.1.10", "192.168.1.10", "web", 80)

	// Write second endpoint for same service — should skip master key write.
	translator.WriteEndpointIPIdentity(ctx, "10.100.1.11", "192.168.1.10", "web", 80)

	masterKey := "cilium/state/identities/v1/id/256"
	masterFact, _ := factStore.Get(ctx, masterKey)
	if string(masterFact.Value) != "container:app=web;" {
		testHandle.Errorf("expected label key 'container:app=web;', got %q", string(masterFact.Value))
	}
}

// TestCiliumTranslatorRecoverIdentityWithNonNumericID verifies that
// non-numeric identity IDs in the store are gracefully skipped during recovery.
func TestCiliumTranslatorRecoverIdentityWithNonNumericID(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	// Write a valid identity and an invalid one.
	factStore.Put(ctx, "cilium/state/identities/v1/id/300", []byte("container:app=valid;"))
	factStore.Put(ctx, "cilium/state/identities/v1/id/not-a-number", []byte("container:app=broken;"))

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	initError := translator.Initialize(ctx)
	if initError != nil {
		testHandle.Fatalf("Initialize should succeed despite non-numeric ID: %v", initError)
	}

	// Should resume from 301 (max valid ID + 1).
	if translator.nextIdentityID != 301 {
		testHandle.Errorf("expected nextIdentityID=301, got %d", translator.nextIdentityID)
	}

	// The non-numeric ID should not be in the cache.
	if _, cached := translator.identityCache["container:app=broken;"]; cached {
		testHandle.Error("non-numeric identity should not be in the cache")
	}
}

// TestCiliumDataPlaneFactoryRegistration verifies that the Cilium data plane
// factory is registered and returns a functional CiliumDataPlane.
func TestCiliumDataPlaneFactoryRegistration(testHandle *testing.T) {
	factory := LookupDataPlaneFactory("cilium")
	if factory == nil {
		testHandle.Fatal("expected 'cilium' factory to be registered")
	}

	factStore := store.NewMemoryStore()
	defer factStore.Close()

	provider := factory(factStore, "test-cluster", "node-1")
	if provider == nil {
		testHandle.Fatal("factory returned nil provider")
	}

	// Verify the returned provider implements the optional interfaces.
	if _, ok := provider.(DataPlaneInitializer); !ok {
		testHandle.Error("factory provider should implement DataPlaneInitializer")
	}
	if _, ok := provider.(DataPlaneNodeRegistrar); !ok {
		testHandle.Error("factory provider should implement DataPlaneNodeRegistrar")
	}
	if _, ok := provider.(EndpointIdentityRegistrar); !ok {
		testHandle.Error("factory provider should implement EndpointIdentityRegistrar")
	}
	if _, ok := provider.(DataPlanePolicyReconciler); !ok {
		testHandle.Error("factory provider should implement DataPlanePolicyReconciler")
	}
}

// TestCiliumDataPlaneFactoryLookupUnknownReturnsNil verifies that looking up
// an unregistered data plane name returns nil.
func TestCiliumDataPlaneFactoryLookupUnknownReturnsNil(testHandle *testing.T) {
	factory := LookupDataPlaneFactory("nonexistent-dataplane")
	if factory != nil {
		testHandle.Fatal("expected nil for unregistered data plane name")
	}
}

// TestCiliumTranslatorRemoveStaleServiceKeysOnEmptyReconcile verifies that
// reconciling with an empty service list removes all previously written keys.
func TestCiliumTranslatorRemoveStaleServiceKeysOnEmptyReconcile(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	translator := NewCiliumFactTranslator(factStore, "ccattler", "node-1")
	translator.Initialize(ctx)

	// Write services.
	translator.ReconcileServices(ctx, []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80, Protocol: "tcp"},
		{ServiceName: "api", VirtualIP: "10.200.0.2", Port: 443, Protocol: "tcp"},
	})

	// Reconcile with empty list — both should be removed.
	translator.ReconcileServices(ctx, []ServiceVIPConfig{})

	if _, getError := factStore.Get(ctx, "cilium/state/services/v1/ccattler/default/web"); getError == nil {
		testHandle.Error("expected 'web' service key to be deleted")
	}
	if _, getError := factStore.Get(ctx, "cilium/state/services/v1/ccattler/default/api"); getError == nil {
		testHandle.Error("expected 'api' service key to be deleted")
	}
}
