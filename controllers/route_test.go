// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestRouteControllerResolvesBackends(t *testing.T) {
	routeController := NewRouteController()

	facts := buildFacts(
		kv(types.KeyDesiredRoute("web-route"), "route"),
		kv(types.KeyDesiredRouteService("web-route"), "web"),
		kv(types.KeyDesiredRoutePort("web-route"), "8080"),
		kv(types.KeyEndpoint("web", "web-1", 8080), "10.0.1.4:8080"),
		kv(types.KeyEndpoint("web", "web-2", 8080), "10.0.1.5:8080"),
	)

	changes, err := routeController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Key != types.KeyDerivedRouteBackends("web-route") {
		t.Errorf("key: got %s, want %s", changes[0].Key, types.KeyDerivedRouteBackends("web-route"))
	}
	expectedBackends := "10.0.1.4:8080,10.0.1.5:8080"
	if string(changes[0].Value) != expectedBackends {
		t.Errorf("value: got %s, want %s", changes[0].Value, expectedBackends)
	}
}

func TestRouteControllerNoEndpoints(t *testing.T) {
	routeController := NewRouteController()

	facts := buildFacts(
		kv(types.KeyDesiredRoute("web-route"), "route"),
		kv(types.KeyDesiredRouteService("web-route"), "web"),
		kv(types.KeyDesiredRoutePort("web-route"), "8080"),
	)

	changes, err := routeController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change (empty backends), got %d", len(changes))
	}
	if string(changes[0].Value) != "" {
		t.Errorf("expected empty backends, got %s", changes[0].Value)
	}
}

func TestRouteControllerFiltersPort(t *testing.T) {
	routeController := NewRouteController()

	facts := buildFacts(
		kv(types.KeyDesiredRoute("api-route"), "route"),
		kv(types.KeyDesiredRouteService("api-route"), "api"),
		kv(types.KeyDesiredRoutePort("api-route"), "9090"),
		kv(types.KeyEndpoint("api", "api-1", 8080), "10.0.1.4:8080"),
		kv(types.KeyEndpoint("api", "api-1", 9090), "10.0.1.4:9090"),
		kv(types.KeyEndpoint("api", "api-2", 9090), "10.0.1.5:9090"),
	)

	changes, err := routeController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	expectedBackends := "10.0.1.4:9090,10.0.1.5:9090"
	if string(changes[0].Value) != expectedBackends {
		t.Errorf("value: got %s, want %s", changes[0].Value, expectedBackends)
	}
}

func TestRouteControllerNoPortReturnsAll(t *testing.T) {
	routeController := NewRouteController()

	facts := buildFacts(
		kv(types.KeyDesiredRoute("all-route"), "route"),
		kv(types.KeyDesiredRouteService("all-route"), "web"),
		kv(types.KeyEndpoint("web", "web-1", 8080), "10.0.1.4:8080"),
		kv(types.KeyEndpoint("web", "web-1", 9090), "10.0.1.4:9090"),
	)

	changes, err := routeController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	expectedBackends := "10.0.1.4:8080,10.0.1.4:9090"
	if string(changes[0].Value) != expectedBackends {
		t.Errorf("value: got %s, want %s", changes[0].Value, expectedBackends)
	}
}

func TestRouteControllerRemovesStaleDerived(t *testing.T) {
	routeController := NewRouteController()

	facts := buildFacts(
		kv(types.KeyDerivedRouteBackends("old-route"), "10.0.1.4:8080"),
	)

	changes, err := routeController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 delete, got %d changes", len(changes))
	}
	if changes[0].Type != store.OpDelete {
		t.Errorf("expected OpDelete, got %d", changes[0].Type)
	}
	if changes[0].Key != types.KeyDerivedRouteBackends("old-route") {
		t.Errorf("key: got %s, want %s", changes[0].Key, types.KeyDerivedRouteBackends("old-route"))
	}
}

func TestRouteControllerIdempotent(t *testing.T) {
	routeController := NewRouteController()

	backendsKey := types.KeyDerivedRouteBackends("web-route")
	facts := buildFacts(
		kv(types.KeyDesiredRoute("web-route"), "route"),
		kv(types.KeyDesiredRouteService("web-route"), "web"),
		kv(types.KeyEndpoint("web", "web-1", 8080), "10.0.1.4:8080"),
		kv(backendsKey, "10.0.1.4:8080"),
	)

	changes, err := routeController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes (already converged), got %d", len(changes))
	}
}

func TestRouteControllerMultipleRoutes(t *testing.T) {
	routeController := NewRouteController()

	facts := buildFacts(
		kv(types.KeyDesiredRoute("frontend"), "route"),
		kv(types.KeyDesiredRouteService("frontend"), "web"),
		kv(types.KeyDesiredRoute("backend"), "route"),
		kv(types.KeyDesiredRouteService("backend"), "api"),
		kv(types.KeyEndpoint("web", "web-1", 8080), "10.0.1.4:8080"),
		kv(types.KeyEndpoint("api", "api-1", 9090), "10.0.2.4:9090"),
	)

	changes, err := routeController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(changes))
	}

	changesByKey := make(map[string]string)
	for _, change := range changes {
		changesByKey[change.Key] = string(change.Value)
	}

	backendKey := types.KeyDerivedRouteBackends("backend")
	if changesByKey[backendKey] != "10.0.2.4:9090" {
		t.Errorf("backend route: got %s, want 10.0.2.4:9090", changesByKey[backendKey])
	}
	frontendKey := types.KeyDerivedRouteBackends("frontend")
	if changesByKey[frontendKey] != "10.0.1.4:8080" {
		t.Errorf("frontend route: got %s, want 10.0.1.4:8080", changesByKey[frontendKey])
	}
}

func TestRouteControllerWatchPrefixes(t *testing.T) {
	routeController := NewRouteController()
	watchPrefixes := routeController.Watch()

	if len(watchPrefixes) != 3 {
		t.Fatalf("expected 3 watch prefixes, got %d", len(watchPrefixes))
	}
	if watchPrefixes[0] != types.ScanDesiredRoutes {
		t.Errorf("watch[0]: got %s, want %s", watchPrefixes[0], types.ScanDesiredRoutes)
	}
	if watchPrefixes[1] != types.ScanEndpoints {
		t.Errorf("watch[1]: got %s, want %s", watchPrefixes[1], types.ScanEndpoints)
	}
	if watchPrefixes[2] != types.ScanDerivedRoutes {
		t.Errorf("watch[2]: got %s, want %s", watchPrefixes[2], types.ScanDerivedRoutes)
	}
}
