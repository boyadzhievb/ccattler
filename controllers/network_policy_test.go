package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestNetworkPolicyControllerCompilesSingleRule verifies that a single
// identity-based allow rule with one source and one target instance
// produces the correct per-node derived rule.
func TestNetworkPolicyControllerCompilesSingleRule(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()

	// Policy: allow web -> api port 443
	_, _ = memStore.Put(ctx, types.KeyNetworkPolicyRule("web-to-api-0"),
		[]byte("web:api:443:allow"))

	// Endpoint for web instance on node-1
	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-1", 8080),
		[]byte("10.0.1.1:8080"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-1"),
		[]byte("node-1"))

	// Endpoint for api instance on node-2
	_, _ = memStore.Put(ctx, types.KeyEndpoint("api", "api-1", 443),
		[]byte("10.0.2.1:443"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("api-1"),
		[]byte("node-2"))

	networkPolicyController := NewNetworkPolicyController()
	facts := scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, reconcileErr := networkPolicyController.Reconcile(ctx, facts)
	if reconcileErr != nil {
		t.Fatalf("Reconcile failed: %v", reconcileErr)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %+v", len(changes), changes)
	}

	expectedKey := types.KeyDerivedNetworkRule("node-2", 0)
	expectedValue := "10.0.1.1:10.0.2.1:443:allow"

	if changes[0].Key != expectedKey {
		t.Errorf("expected key %s, got %s", expectedKey, changes[0].Key)
	}
	if string(changes[0].Value) != expectedValue {
		t.Errorf("expected value %s, got %s", expectedValue, string(changes[0].Value))
	}
}

// TestNetworkPolicyControllerDenyRule verifies that a deny rule is compiled
// to the target node with the correct action.
func TestNetworkPolicyControllerDenyRule(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()

	_, _ = memStore.Put(ctx, types.KeyNetworkPolicyRule("web-to-db-0"),
		[]byte("web:database:5432:deny"))

	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-1", 8080),
		[]byte("10.0.1.1:8080"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-1"),
		[]byte("node-1"))

	_, _ = memStore.Put(ctx, types.KeyEndpoint("database", "db-1", 5432),
		[]byte("10.0.3.1:5432"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("db-1"),
		[]byte("node-3"))

	networkPolicyController := NewNetworkPolicyController()
	facts := scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, reconcileErr := networkPolicyController.Reconcile(ctx, facts)
	if reconcileErr != nil {
		t.Fatalf("Reconcile failed: %v", reconcileErr)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}

	expectedValue := "10.0.1.1:10.0.3.1:5432:deny"
	if string(changes[0].Value) != expectedValue {
		t.Errorf("expected value %s, got %s", expectedValue, string(changes[0].Value))
	}
}

// TestNetworkPolicyControllerMultipleInstancesFanOut verifies that a rule
// with multiple source and target instances generates the cross-product
// of IP-based rules on the correct nodes.
func TestNetworkPolicyControllerMultipleInstancesFanOut(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()

	_, _ = memStore.Put(ctx, types.KeyNetworkPolicyRule("web-to-api-0"),
		[]byte("web:api:443:allow"))

	// 2 web instances on node-1
	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-1", 8080), []byte("10.0.1.1:8080"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-2", 8080), []byte("10.0.1.2:8080"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-1"), []byte("node-1"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-2"), []byte("node-1"))

	// 2 api instances on node-2 and node-3
	_, _ = memStore.Put(ctx, types.KeyEndpoint("api", "api-1", 443), []byte("10.0.2.1:443"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("api", "api-2", 443), []byte("10.0.3.1:443"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("api-1"), []byte("node-2"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("api-2"), []byte("node-3"))

	networkPolicyController := NewNetworkPolicyController()
	facts := scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, reconcileErr := networkPolicyController.Reconcile(ctx, facts)
	if reconcileErr != nil {
		t.Fatalf("Reconcile failed: %v", reconcileErr)
	}

	// 2 source × 2 target = 4 rules total, split across 2 nodes (2 per node)
	if len(changes) != 4 {
		t.Fatalf("expected 4 changes, got %d: %+v", len(changes), changes)
	}

	// Verify node-2 gets 2 rules and node-3 gets 2 rules
	node2Rules := 0
	node3Rules := 0
	for _, change := range changes {
		if containsSubstring(change.Key, "node-2") {
			node2Rules++
		}
		if containsSubstring(change.Key, "node-3") {
			node3Rules++
		}
	}
	if node2Rules != 2 {
		t.Errorf("expected 2 rules for node-2, got %d", node2Rules)
	}
	if node3Rules != 2 {
		t.Errorf("expected 2 rules for node-3, got %d", node3Rules)
	}
}

// TestNetworkPolicyControllerIdempotent verifies that reconciling the same
// state twice produces no changes on the second pass.
func TestNetworkPolicyControllerIdempotent(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()

	_, _ = memStore.Put(ctx, types.KeyNetworkPolicyRule("web-to-api-0"),
		[]byte("web:api:443:allow"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-1", 8080),
		[]byte("10.0.1.1:8080"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-1"),
		[]byte("node-1"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("api", "api-1", 443),
		[]byte("10.0.2.1:443"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("api-1"),
		[]byte("node-2"))

	networkPolicyController := NewNetworkPolicyController()

	// First reconcile — produces changes
	facts := scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, _ := networkPolicyController.Reconcile(ctx, facts)
	applyChangesToStore(ctx, t, memStore, changes)

	// Second reconcile — should produce no changes
	facts = scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, reconcileErr := networkPolicyController.Reconcile(ctx, facts)
	if reconcileErr != nil {
		t.Fatalf("Reconcile failed: %v", reconcileErr)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes on idempotent reconcile, got %d: %+v", len(changes), changes)
	}
}

// TestNetworkPolicyControllerRemovesStaleRulesOnEndpointRemoval verifies
// that when an instance disappears (no endpoint), its compiled rules are deleted.
func TestNetworkPolicyControllerRemovesStaleRulesOnEndpointRemoval(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()

	_, _ = memStore.Put(ctx, types.KeyNetworkPolicyRule("web-to-api-0"),
		[]byte("web:api:443:allow"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-1", 8080),
		[]byte("10.0.1.1:8080"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-1"),
		[]byte("node-1"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("api", "api-1", 443),
		[]byte("10.0.2.1:443"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("api-1"),
		[]byte("node-2"))

	networkPolicyController := NewNetworkPolicyController()

	// First reconcile
	facts := scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, _ := networkPolicyController.Reconcile(ctx, facts)
	applyChangesToStore(ctx, t, memStore, changes)

	// Remove the api endpoint (instance died)
	_ = memStore.Delete(ctx, types.KeyEndpoint("api", "api-1", 443))

	// Second reconcile — should delete the derived rule
	facts = scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, reconcileErr := networkPolicyController.Reconcile(ctx, facts)
	if reconcileErr != nil {
		t.Fatalf("Reconcile failed: %v", reconcileErr)
	}

	deleteCount := 0
	for _, change := range changes {
		if change.Type == store.OpDelete {
			deleteCount++
		}
	}
	if deleteCount != 1 {
		t.Fatalf("expected 1 delete change, got %d deletes out of %d changes",
			deleteCount, len(changes))
	}
}

// TestNetworkPolicyControllerInstanceMigration verifies that when an instance
// moves to a new node, the compiled rules shift to the new target node.
func TestNetworkPolicyControllerInstanceMigration(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()

	_, _ = memStore.Put(ctx, types.KeyNetworkPolicyRule("web-to-api-0"),
		[]byte("web:api:443:allow"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-1", 8080),
		[]byte("10.0.1.1:8080"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-1"),
		[]byte("node-1"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("api", "api-1", 443),
		[]byte("10.0.2.1:443"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("api-1"),
		[]byte("node-2"))

	networkPolicyController := NewNetworkPolicyController()

	// Initial reconcile — rule on node-2
	facts := scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, _ := networkPolicyController.Reconcile(ctx, facts)
	applyChangesToStore(ctx, t, memStore, changes)

	// Migrate api-1 to node-3 and update its IP
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("api-1"), []byte("node-3"))
	_, _ = memStore.Put(ctx, types.KeyEndpoint("api", "api-1", 443), []byte("10.0.3.1:443"))

	// Reconcile after migration
	facts = scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, reconcileErr := networkPolicyController.Reconcile(ctx, facts)
	if reconcileErr != nil {
		t.Fatalf("Reconcile failed: %v", reconcileErr)
	}

	// Should have: 1 delete (old node-2 rule) + 1 put (new node-3 rule)
	putCount := 0
	deleteCount := 0
	for _, change := range changes {
		switch change.Type {
		case store.OpPut:
			putCount++
			if !containsSubstring(change.Key, "node-3") {
				t.Errorf("expected put on node-3, got key %s", change.Key)
			}
		case store.OpDelete:
			deleteCount++
			if !containsSubstring(change.Key, "node-2") {
				t.Errorf("expected delete on node-2, got key %s", change.Key)
			}
		}
	}
	if putCount != 1 {
		t.Errorf("expected 1 put, got %d", putCount)
	}
	if deleteCount != 1 {
		t.Errorf("expected 1 delete, got %d", deleteCount)
	}
}

// TestNetworkPolicyControllerNoPoliciesProducesNoRules verifies that with
// no policy rules, the controller produces no changes even when endpoints exist.
func TestNetworkPolicyControllerNoPoliciesProducesNoRules(t *testing.T) {
	ctx := context.Background()
	memStore := store.NewMemoryStore()

	_, _ = memStore.Put(ctx, types.KeyEndpoint("web", "web-1", 8080),
		[]byte("10.0.1.1:8080"))
	_, _ = memStore.Put(ctx, types.KeyPlacementInstance("web-1"),
		[]byte("node-1"))

	networkPolicyController := NewNetworkPolicyController()
	facts := scanAllWatchedPrefixes(ctx, t, memStore, networkPolicyController.Watch())
	changes, reconcileErr := networkPolicyController.Reconcile(ctx, facts)
	if reconcileErr != nil {
		t.Fatalf("Reconcile failed: %v", reconcileErr)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes with no policies, got %d", len(changes))
	}
}

// scanAllWatchedPrefixes gathers facts from all prefixes a controller watches
// and sorts them by key, mimicking what the runner does before calling Reconcile.
func scanAllWatchedPrefixes(ctx context.Context, t *testing.T, memStore *store.MemoryStore, prefixes []string) []store.Fact {
	t.Helper()
	var allFacts []store.Fact
	for _, prefix := range prefixes {
		facts, scanErr := memStore.Scan(ctx, prefix)
		if scanErr != nil {
			t.Fatalf("scan %s failed: %v", prefix, scanErr)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)
	return allFacts
}

// applyChangesToStore writes a slice of controller changes to the store.
func applyChangesToStore(ctx context.Context, t *testing.T, memStore *store.MemoryStore, changes []Change) {
	t.Helper()
	for _, change := range changes {
		switch change.Type {
		case store.OpPut:
			_, putErr := memStore.Put(ctx, change.Key, change.Value)
			if putErr != nil {
				t.Fatalf("put %s failed: %v", change.Key, putErr)
			}
		case store.OpDelete:
			deleteErr := memStore.Delete(ctx, change.Key)
			if deleteErr != nil {
				t.Fatalf("delete %s failed: %v", change.Key, deleteErr)
			}
		}
	}
}

// containsSubstring checks whether a string contains a given substring.
func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) && searchForSubstring(haystack, needle)
}

// searchForSubstring performs a linear search for a substring.
func searchForSubstring(haystack, needle string) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
