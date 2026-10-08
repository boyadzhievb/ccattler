// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// helperSetupNetworkPolicyCluster creates a 2-node simulated cluster with
// the full controller set including the NetworkPolicyController and simulator
// agents. Returns the store, cancel function, and network provider.
func helperSetupNetworkPolicyCluster(t *testing.T) (store.StateStore, context.CancelFunc, *network.SimulatorNetworkProvider) {
	t.Helper()

	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	controllerRunner := controllers.NewRunner(factStore,
		controllers.NewInstanceController(),
		scheduler.NewScheduler(),
		controllers.NewEndpointController(),
		controllers.NewFailureController(),
		controllers.NewIntentResolverController(),
		controllers.NewNetworkController(),
		controllers.NewNetworkPolicyController(),
	)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go func() {
		_ = controllerRunner.Run(ctx)
	}()

	for _, nodeID := range []string{"node-1", "node-2"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
		go func() {
			_ = nodeAgent.Run(ctx)
		}()
	}

	return factStore, cancel, simulatorNetworkProvider
}

// TestNetworkPolicyDSLToCompiledRules verifies the full pipeline: DSL
// network block → policy/network/ facts → controller → derived/network/rule/ facts.
func TestNetworkPolicyDSLToCompiledRules(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	factStore, cancel, _ := helperSetupNetworkPolicyCluster(t)
	defer cancel()
	defer factStore.Close()

	ctx := context.Background()

	// Apply DSL with two services and a network policy
	dslInput := `
service web {
    image "nginx:1.27"
    instances 1
    expose 8080
    resources {
        cpu 100m
        memory 128Mi
    }
}

service api {
    image "myapi:v1"
    instances 1
    expose 443
    resources {
        cpu 100m
        memory 128Mi
    }
}

network {
    allow web -> api port 443
}
`
	applyErr := lang.Apply(ctx, factStore, dslInput)
	if applyErr != nil {
		t.Fatalf("DSL apply failed: %v", applyErr)
	}

	// Wait for the policy fact to appear
	waitForFact(t, ctx, factStore, types.ScanNetworkPolicies, func(facts []store.Fact) bool {
		return len(facts) >= 1
	})

	// Verify the identity-based policy fact was written
	policyFacts, _ := factStore.Scan(ctx, types.ScanNetworkPolicies)
	if len(policyFacts) != 1 {
		t.Fatalf("expected 1 policy fact, got %d", len(policyFacts))
	}
	policyValue := string(policyFacts[0].Value)
	if !strings.Contains(policyValue, "web") || !strings.Contains(policyValue, "api") {
		t.Errorf("policy value should reference web and api: %s", policyValue)
	}

	// Wait for endpoints to be created (instances must be running first)
	waitForFact(t, ctx, factStore, types.ScanEndpoints, func(facts []store.Fact) bool {
		hasWebEndpoint := false
		hasAPIEndpoint := false
		for _, fact := range facts {
			relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
			if strings.HasPrefix(relativePath, "web/") {
				hasWebEndpoint = true
			}
			if strings.HasPrefix(relativePath, "api/") {
				hasAPIEndpoint = true
			}
		}
		return hasWebEndpoint && hasAPIEndpoint
	})

	// Wait for compiled derived rules to appear
	waitForFact(t, ctx, factStore, types.ScanDerivedNetworkRules, func(facts []store.Fact) bool {
		return len(facts) >= 1
	})

	derivedRules, _ := factStore.Scan(ctx, types.ScanDerivedNetworkRules)
	if len(derivedRules) == 0 {
		t.Fatal("expected at least 1 derived network rule, got 0")
	}

	// Verify the derived rule contains IP addresses and the allow action
	ruleValue := string(derivedRules[0].Value)
	parts := strings.SplitN(ruleValue, ":", 4)
	if len(parts) != 4 {
		t.Fatalf("derived rule has wrong format: %s", ruleValue)
	}
	if parts[2] != "443" {
		t.Errorf("expected port 443 in derived rule, got %s", parts[2])
	}
	if parts[3] != "allow" {
		t.Errorf("expected action allow in derived rule, got %s", parts[3])
	}
}

// TestNetworkPolicyDenyRule verifies that a deny rule in the DSL is
// compiled through to a derived deny rule.
func TestNetworkPolicyDenyRule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	factStore, cancel, _ := helperSetupNetworkPolicyCluster(t)
	defer cancel()
	defer factStore.Close()

	ctx := context.Background()

	dslInput := `
service frontend {
    image "nginx:1.27"
    instances 1
    expose 8080
    resources {
        cpu 100m
        memory 128Mi
    }
}

service database {
    image "postgres:16"
    instances 1
    expose 5432
    resources {
        cpu 100m
        memory 128Mi
    }
}

network {
    deny frontend -> database port 5432
}
`
	applyErr := lang.Apply(ctx, factStore, dslInput)
	if applyErr != nil {
		t.Fatalf("DSL apply failed: %v", applyErr)
	}

	// Wait for endpoints of both services
	waitForFact(t, ctx, factStore, types.ScanEndpoints, func(facts []store.Fact) bool {
		hasFrontend := false
		hasDatabase := false
		for _, fact := range facts {
			relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
			if strings.HasPrefix(relativePath, "frontend/") {
				hasFrontend = true
			}
			if strings.HasPrefix(relativePath, "database/") {
				hasDatabase = true
			}
		}
		return hasFrontend && hasDatabase
	})

	// Wait for derived deny rule
	waitForFact(t, ctx, factStore, types.ScanDerivedNetworkRules, func(facts []store.Fact) bool {
		for _, fact := range facts {
			if strings.Contains(string(fact.Value), "deny") {
				return true
			}
		}
		return false
	})

	derivedRules, _ := factStore.Scan(ctx, types.ScanDerivedNetworkRules)
	foundDeny := false
	for _, rule := range derivedRules {
		parts := strings.SplitN(string(rule.Value), ":", 4)
		if len(parts) == 4 && parts[3] == "deny" && parts[2] == "5432" {
			foundDeny = true
		}
	}
	if !foundDeny {
		t.Error("expected a derived deny rule for port 5432")
	}
}

// TestNetworkPolicyMultipleRules verifies that multiple allow/deny rules
// in a single network block all produce compiled derived rules.
func TestNetworkPolicyMultipleRules(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	factStore, cancel, _ := helperSetupNetworkPolicyCluster(t)
	defer cancel()
	defer factStore.Close()

	ctx := context.Background()

	dslInput := `
service web {
    image "nginx:1.27"
    instances 1
    expose 8080
    resources {
        cpu 100m
        memory 128Mi
    }
}

service api {
    image "myapi:v1"
    instances 1
    expose 443
    resources {
        cpu 100m
        memory 128Mi
    }
}

service database {
    image "postgres:16"
    instances 1
    expose 5432
    resources {
        cpu 100m
        memory 128Mi
    }
}

network {
    allow web -> api port 443
    deny web -> database port 5432
}
`
	applyErr := lang.Apply(ctx, factStore, dslInput)
	if applyErr != nil {
		t.Fatalf("DSL apply failed: %v", applyErr)
	}

	// Verify 2 policy facts were written
	waitForFact(t, ctx, factStore, types.ScanNetworkPolicies, func(facts []store.Fact) bool {
		return len(facts) >= 2
	})

	// Wait for all 3 services to have endpoints
	waitForFact(t, ctx, factStore, types.ScanEndpoints, func(facts []store.Fact) bool {
		services := make(map[string]bool)
		for _, fact := range facts {
			relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
			firstPart := strings.SplitN(relativePath, "/", 2)[0]
			services[firstPart] = true
		}
		return services["web"] && services["api"] && services["database"]
	})

	// Wait for derived rules for both policies
	waitForFact(t, ctx, factStore, types.ScanDerivedNetworkRules, func(facts []store.Fact) bool {
		hasAllow := false
		hasDeny := false
		for _, fact := range facts {
			if strings.HasSuffix(string(fact.Value), ":allow") {
				hasAllow = true
			}
			if strings.HasSuffix(string(fact.Value), ":deny") {
				hasDeny = true
			}
		}
		return hasAllow && hasDeny
	})

	derivedRules, _ := factStore.Scan(ctx, types.ScanDerivedNetworkRules)
	allowCount := 0
	denyCount := 0
	for _, rule := range derivedRules {
		if strings.HasSuffix(string(rule.Value), ":allow") {
			allowCount++
		}
		if strings.HasSuffix(string(rule.Value), ":deny") {
			denyCount++
		}
	}
	if allowCount < 1 {
		t.Errorf("expected at least 1 allow rule, got %d", allowCount)
	}
	if denyCount < 1 {
		t.Errorf("expected at least 1 deny rule, got %d", denyCount)
	}
}

// waitForFact polls the store until the condition function returns true or
// the timeout expires.
func waitForFact(t *testing.T, ctx context.Context, factStore store.StateStore, prefix string, condition func([]store.Fact) bool) {
	t.Helper()
	const maxWaitDuration = 10 * time.Second
	const pollInterval = 50 * time.Millisecond

	deadline := time.Now().Add(maxWaitDuration)
	for time.Now().Before(deadline) {
		facts, scanErr := factStore.Scan(ctx, prefix)
		if scanErr == nil && condition(facts) {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for condition on prefix %s", prefix)
}
