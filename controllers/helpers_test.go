// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestCollectResourceQuantitiesReadEffectiveFirst checks the controllers'
// request readers (M243 D6): effective wins over desired, CPU and memory each
// use their own parser, and an unreadable effective value is reported, never
// replaced by the desired one or read as 0.
func TestCollectResourceQuantitiesReadEffectiveFirst(t *testing.T) {
	facts := []store.Fact{
		{Key: types.KeyDesiredServiceResourcesCPU("web"), Value: []byte("250m")},
		{Key: types.KeyEffectiveServiceResourcesCPU("web"), Value: []byte("1.5")},
		{Key: types.KeyDesiredServiceResourcesCPU("api"), Value: []byte("2")},
		{Key: types.KeyDesiredServiceResourcesCPU("db"), Value: []byte("500m")},
		{Key: types.KeyEffectiveServiceResourcesCPU("db"), Value: []byte("lots")},
		{Key: types.KeyDesiredServiceResourcesMemory("web"), Value: []byte("1G")},
		{Key: types.KeyDesiredServiceResourcesMemory("api"), Value: []byte("512MB")},
	}
	store.SortFacts(facts)

	cpuMillicores, unreadableCPU := collectCPUMillicoresBySuffix(facts, "resources/cpu", "test", "skipped")
	if cpuMillicores["web"] != 1500 || cpuMillicores["api"] != 2000 {
		t.Errorf("cpu = %v, want web 1500 (effective) and api 2000 (bare cores)", cpuMillicores)
	}
	if _, hasDB := cpuMillicores["db"]; hasDB || !unreadableCPU["db"] {
		t.Errorf("db's unreadable effective cpu: values %v, unreadable %v; want db only unreadable", cpuMillicores, unreadableCPU)
	}

	memoryBytes, unreadableMemory := collectMemoryBytesBySuffix(facts, "resources/memory", "test", "skipped")
	if memoryBytes["web"] != 1000000000 || !unreadableMemory["api"] || len(memoryBytes) != 1 {
		t.Errorf("memory = %v, unreadable %v; want web 1000000000 and api unreadable", memoryBytes, unreadableMemory)
	}
}

func TestSplitServiceFactKeyHierarchical(t *testing.T) {
	knownServiceNames := map[string]bool{
		"payments/checkout": true,
		"payments/database": true,
		"frontend/web":      true,
	}
	prefix := types.ScanDesiredServices

	testCases := []struct {
		factKey      string
		wantName     string
		wantSuffix   string
		wantHasParts bool
	}{
		{prefix + "payments/checkout/image", "payments/checkout", "image", true},
		{prefix + "payments/checkout/instances", "payments/checkout", "instances", true},
		{prefix + "payments/checkout/scale/horizontal/min", "payments/checkout", "scale/horizontal/min", true},
		{prefix + "payments/database/image", "payments/database", "image", true},
		{prefix + "frontend/web/instances", "frontend/web", "instances", true},
		{prefix + "payments/checkout", "", "", false},
	}

	for _, testCase := range testCases {
		gotName, gotSuffix, gotHasParts := splitServiceFactKey(testCase.factKey, prefix, knownServiceNames)
		if gotName != testCase.wantName || gotSuffix != testCase.wantSuffix || gotHasParts != testCase.wantHasParts {
			t.Errorf("splitServiceFactKey(%q): got (%q, %q, %v), want (%q, %q, %v)",
				testCase.factKey, gotName, gotSuffix, gotHasParts,
				testCase.wantName, testCase.wantSuffix, testCase.wantHasParts)
		}
	}
}

func TestDiscoverServiceNamesFromFacts(t *testing.T) {
	prefix := types.ScanDesiredServices
	facts := []store.Fact{
		{Key: prefix + "payments/checkout", Value: []byte("")},
		{Key: prefix + "payments/checkout/image", Value: []byte("checkout:v1")},
		{Key: prefix + "payments/checkout/instances", Value: []byte("2")},
		{Key: prefix + "payments/database", Value: []byte("")},
		{Key: prefix + "payments/database/image", Value: []byte("postgres:16")},
		{Key: prefix + "api", Value: []byte("")},
		{Key: prefix + "api/image", Value: []byte("my-api:v3")},
	}

	serviceNames := discoverServiceNamesFromFacts(facts, prefix)

	if len(serviceNames) != 3 {
		t.Fatalf("expected 3 service names, got %d: %v", len(serviceNames), serviceNames)
	}
	for _, expectedName := range []string{"payments/checkout", "payments/database", "api"} {
		if !serviceNames[expectedName] {
			t.Errorf("missing service name %q", expectedName)
		}
	}
}
