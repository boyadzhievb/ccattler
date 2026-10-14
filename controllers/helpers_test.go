// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestParseResourceQuantity(t *testing.T) {
	testCases := []struct {
		input    string
		expected int
	}{
		{"500m", 500},
		{"1000m", 1000},
		{"0m", 0},
		{"256Mi", 256 * 1024 * 1024},
		{"512Mi", 512 * 1024 * 1024},
		{"1Gi", 1024 * 1024 * 1024},
		{"2Gi", 2 * 1024 * 1024 * 1024},
		{"100Ki", 100 * 1024},
		{"3", 3},
		{"1024", 1024},
		{"0", 0},
		{"", 0},
		{"bogus", 0},
	}
	for _, testCase := range testCases {
		result := parseResourceQuantity(testCase.input)
		if result != testCase.expected {
			t.Errorf("parseResourceQuantity(%q): got %d, want %d", testCase.input, result, testCase.expected)
		}
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
