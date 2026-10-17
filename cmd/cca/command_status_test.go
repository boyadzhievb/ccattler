// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"testing"

	"github.com/boyadzhievb/ccattler/api"
)

func TestParseTagFilterArgsSingle(testContext *testing.T) {
	tagFilters := parseTagFilterArgs([]string{"--tag", "team=payments"})
	if tagFilters["team"] != "payments" {
		testContext.Errorf("expected team=payments, got %v", tagFilters)
	}
}

func TestParseTagFilterArgsMultiple(testContext *testing.T) {
	tagFilters := parseTagFilterArgs([]string{"--tag", "team=payments", "--tag", "tier=frontend"})
	if tagFilters["team"] != "payments" {
		testContext.Errorf("team: got %q, want %q", tagFilters["team"], "payments")
	}
	if tagFilters["tier"] != "frontend" {
		testContext.Errorf("tier: got %q, want %q", tagFilters["tier"], "frontend")
	}
}

func TestParseTagFilterArgsEmpty(testContext *testing.T) {
	tagFilters := parseTagFilterArgs([]string{})
	if len(tagFilters) != 0 {
		testContext.Errorf("expected empty filters, got %v", tagFilters)
	}
}

func TestParseTagFilterArgsIgnoresUnrelatedFlags(testContext *testing.T) {
	tagFilters := parseTagFilterArgs([]string{"--follow", "--tag", "env=prod"})
	if len(tagFilters) != 1 || tagFilters["env"] != "prod" {
		testContext.Errorf("expected env=prod only, got %v", tagFilters)
	}
}

func TestFilterServicesByTagsMatchesAll(testContext *testing.T) {
	services := []api.ServiceStatus{
		{Name: "web", Tags: map[string]string{"team": "payments", "tier": "frontend"}},
		{Name: "api", Tags: map[string]string{"team": "payments", "tier": "backend"}},
		{Name: "worker", Tags: map[string]string{"team": "infra"}},
	}

	filtered := filterServicesByTags(services, map[string]string{"team": "payments"})
	if len(filtered) != 2 {
		testContext.Fatalf("expected 2 services, got %d", len(filtered))
	}

	filtered = filterServicesByTags(services, map[string]string{"team": "payments", "tier": "frontend"})
	if len(filtered) != 1 || filtered[0].Name != "web" {
		testContext.Errorf("expected [web], got %v", filtered)
	}
}

func TestFilterServicesByTagsNoFiltersReturnsAll(testContext *testing.T) {
	services := []api.ServiceStatus{
		{Name: "web"},
		{Name: "api"},
	}
	filtered := filterServicesByTags(services, map[string]string{})
	if len(filtered) != 2 {
		testContext.Errorf("expected all services returned, got %d", len(filtered))
	}
}

func TestFilterServicesByTagsNoMatchReturnsEmpty(testContext *testing.T) {
	services := []api.ServiceStatus{
		{Name: "web", Tags: map[string]string{"team": "payments"}},
	}
	filtered := filterServicesByTags(services, map[string]string{"team": "infra"})
	if len(filtered) != 0 {
		testContext.Errorf("expected no matches, got %d", len(filtered))
	}
}

func TestFilterInstancesByServiceTagsFiltersCorrectly(testContext *testing.T) {
	services := []api.ServiceStatus{
		{Name: "web", Tags: map[string]string{"tier": "frontend"}},
		{Name: "api", Tags: map[string]string{"tier": "backend"}},
	}
	instances := []api.InstanceStatus{
		{ID: "w1", ServiceName: "web"},
		{ID: "w2", ServiceName: "web"},
		{ID: "a1", ServiceName: "api"},
	}

	filtered := filterInstancesByServiceTags(instances, services, map[string]string{"tier": "frontend"})
	if len(filtered) != 2 {
		testContext.Fatalf("expected 2 instances, got %d", len(filtered))
	}
	for _, instance := range filtered {
		if instance.ServiceName != "web" {
			testContext.Errorf("expected only web instances, got %s", instance.ServiceName)
		}
	}
}

func TestFilterInstancesByServiceTagsNoFilterReturnsAll(testContext *testing.T) {
	instances := []api.InstanceStatus{
		{ID: "w1", ServiceName: "web"},
		{ID: "a1", ServiceName: "api"},
	}
	filtered := filterInstancesByServiceTags(instances, nil, map[string]string{})
	if len(filtered) != 2 {
		testContext.Errorf("expected all instances, got %d", len(filtered))
	}
}

func TestFormatServiceTagsDeterministicOrder(testContext *testing.T) {
	tags := map[string]string{"tier": "frontend", "env": "prod", "team": "payments"}
	formatted := formatServiceTags(tags)
	expected := "env=prod,team=payments,tier=frontend"
	if formatted != expected {
		testContext.Errorf("got %q, want %q", formatted, expected)
	}
}

func TestFormatServiceTagsEmpty(testContext *testing.T) {
	formatted := formatServiceTags(nil)
	if formatted != "-" {
		testContext.Errorf("got %q, want %q", formatted, "-")
	}
}
