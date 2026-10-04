// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestStatefulDNSCreatesPerOrdinalEntries verifies that running stateful
// instances with IPs and ordinals get per-instance DNS entries.
func TestStatefulDNSCreatesPerOrdinalEntries(t *testing.T) {
	dnsController := NewStatefulDNSController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "running"),
		kv(types.KeyObservedInstanceIP("postgres-0"), "10.0.1.4"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
		kv(types.KeyObservedInstanceService("postgres-1"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-1"), "running"),
		kv(types.KeyObservedInstanceIP("postgres-1"), "10.0.2.8"),
		kv(types.KeyObservedInstanceOrdinal("postgres-1"), "1"),
	)

	changes, reconcileErr := dnsController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 DNS changes, got %d", len(changes))
	}

	assertChangeKey(t, changes[0], types.KeyNetworkDNSInstance("postgres-0"))
	assertChangeValue(t, changes[0], "postgres-0.ccattler.local=10.0.1.4")
	assertChangeKey(t, changes[1], types.KeyNetworkDNSInstance("postgres-1"))
	assertChangeValue(t, changes[1], "postgres-1.ccattler.local=10.0.2.8")
}

// TestStatefulDNSSkipsNonStatefulInstances verifies that non-stateful service
// instances do not get per-instance DNS entries.
func TestStatefulDNSSkipsNonStatefulInstances(t *testing.T) {
	dnsController := NewStatefulDNSController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("abc123"), "web"),
		kv(types.KeyObservedInstanceState("abc123"), "running"),
		kv(types.KeyObservedInstanceIP("abc123"), "10.0.1.4"),
	)

	changes, reconcileErr := dnsController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for non-stateful service, got %d", len(changes))
	}
}

// TestStatefulDNSSkipsPendingInstances verifies that pending stateful
// instances do not get DNS entries.
func TestStatefulDNSSkipsPendingInstances(t *testing.T) {
	dnsController := NewStatefulDNSController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "pending"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
	)

	changes, reconcileErr := dnsController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for pending instance, got %d", len(changes))
	}
}

// TestStatefulDNSDeletesStaleDNSEntries verifies that DNS entries are removed
// when the instance no longer qualifies (e.g. stopped or IP removed).
func TestStatefulDNSDeletesStaleDNSEntries(t *testing.T) {
	dnsController := NewStatefulDNSController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "stopped"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
		// Existing DNS entry for stopped instance
		kv(types.KeyNetworkDNSInstance("postgres-0"), "postgres-0.ccattler.local=10.0.1.4"),
	)

	changes, reconcileErr := dnsController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 delete change, got %d", len(changes))
	}
	if changes[0].Type != store.OpDelete {
		t.Errorf("expected OpDelete, got %d", changes[0].Type)
	}
	assertChangeKey(t, changes[0], types.KeyNetworkDNSInstance("postgres-0"))
}

// TestStatefulDNSControllerInterface verifies that StatefulDNSController
// satisfies the Controller interface.
func TestStatefulDNSControllerInterface(t *testing.T) {
	dnsController := NewStatefulDNSController()
	var _ Controller = dnsController

	if dnsController.Name() != "stateful-dns" {
		t.Errorf("name: got %q, want %q", dnsController.Name(), "stateful-dns")
	}
	if len(dnsController.Watch()) != 3 {
		t.Errorf("expected 3 watch prefixes, got %d", len(dnsController.Watch()))
	}
}
