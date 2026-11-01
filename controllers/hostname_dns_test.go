// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestHostnameDNSCreatesEntriesForRunningInstances verifies that running
// instances with IPs get hostname DNS entries using the container hostname
// format: "{service}-{idPrefix}".
func TestHostnameDNSCreatesEntriesForRunningInstances(t *testing.T) {
	dnsController := NewHostnameDNSController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("abcdef1234567890"), "worker"),
		kv(types.KeyObservedInstanceState("abcdef1234567890"), "running"),
		kv(types.KeyObservedInstanceIP("abcdef1234567890"), "10.100.1.5"),
		kv(types.KeyObservedInstanceService("fedcba0987654321"), "scheduler"),
		kv(types.KeyObservedInstanceState("fedcba0987654321"), "running"),
		kv(types.KeyObservedInstanceIP("fedcba0987654321"), "10.100.2.3"),
	)

	changes, reconcileErr := dnsController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 DNS changes, got %d", len(changes))
	}

	assertChangeKey(t, changes[0], types.KeyNetworkDNSHostname("scheduler-fedcba0987"))
	assertChangeValue(t, changes[0], "10.100.2.3")
	assertChangeKey(t, changes[1], types.KeyNetworkDNSHostname("worker-abcdef1234"))
	assertChangeValue(t, changes[1], "10.100.1.5")
}

// TestHostnameDNSSkipsNonRunningInstances verifies that stopped and pending
// instances do not get hostname DNS entries.
func TestHostnameDNSSkipsNonRunningInstances(t *testing.T) {
	dnsController := NewHostnameDNSController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("stopped123456"), "worker"),
		kv(types.KeyObservedInstanceState("stopped123456"), "stopped"),
		kv(types.KeyObservedInstanceIP("stopped123456"), "10.100.1.5"),
		kv(types.KeyObservedInstanceService("pending123456"), "worker"),
		kv(types.KeyObservedInstanceState("pending123456"), "pending"),
	)

	changes, reconcileErr := dnsController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for non-running instances, got %d", len(changes))
	}
}

// TestHostnameDNSDeletesStaleEntries verifies that hostname DNS entries are
// removed when the instance stops or loses its IP.
func TestHostnameDNSDeletesStaleEntries(t *testing.T) {
	dnsController := NewHostnameDNSController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("stopped123456"), "worker"),
		kv(types.KeyObservedInstanceState("stopped123456"), "stopped"),
		kv(types.KeyNetworkDNSHostname("worker-stopped123"), "10.100.1.5"),
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
	assertChangeKey(t, changes[0], types.KeyNetworkDNSHostname("worker-stopped123"))
}

// TestHostnameDNSUpdatesIPChange verifies that a hostname DNS entry is updated
// when the instance's IP changes.
func TestHostnameDNSUpdatesIPChange(t *testing.T) {
	dnsController := NewHostnameDNSController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("abcdef1234567890"), "worker"),
		kv(types.KeyObservedInstanceState("abcdef1234567890"), "running"),
		kv(types.KeyObservedInstanceIP("abcdef1234567890"), "10.100.1.99"),
		kv(types.KeyNetworkDNSHostname("worker-abcdef1234"), "10.100.1.5"),
	)

	changes, reconcileErr := dnsController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 update change, got %d", len(changes))
	}
	assertChangeKey(t, changes[0], types.KeyNetworkDNSHostname("worker-abcdef1234"))
	assertChangeValue(t, changes[0], "10.100.1.99")
}

// TestHostnameDNSControllerInterface verifies that HostnameDNSController
// satisfies the Controller interface.
func TestHostnameDNSControllerInterface(t *testing.T) {
	dnsController := NewHostnameDNSController()
	var _ Controller = dnsController

	if dnsController.Name() != "hostname-dns" {
		t.Errorf("name: got %q, want %q", dnsController.Name(), "hostname-dns")
	}
	if len(dnsController.Watch()) != 2 {
		t.Errorf("expected 2 watch prefixes, got %d", len(dnsController.Watch()))
	}
}

// TestBuildContainerHostnameTruncatesLongID verifies that the hostname
// builder truncates long instance IDs to 10 characters.
func TestBuildContainerHostnameTruncatesLongID(t *testing.T) {
	hostname := buildContainerHostname("worker", "abcdef1234567890")
	expected := "worker-abcdef1234"
	if hostname != expected {
		t.Errorf("got %q, want %q", hostname, expected)
	}
}

// TestBuildContainerHostnameShortID verifies that short instance IDs are used
// as-is without padding.
func TestBuildContainerHostnameShortID(t *testing.T) {
	hostname := buildContainerHostname("web", "abc")
	expected := "web-abc"
	if hostname != expected {
		t.Errorf("got %q, want %q", hostname, expected)
	}
}
