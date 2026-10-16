//go:build cloudprovider

// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package cloud

import (
	"context"
	"strings"
	"testing"
)

func TestAWSProviderConstructionAndName(testing *testing.T) {
	ctx := context.Background()
	awsProvider, constructionError := NewAWSCloudProvider(ctx, "us-east-1")
	if constructionError != nil {
		if strings.Contains(constructionError.Error(), "credentials") {
			testing.Skipf("skipping: no AWS credentials available: %v", constructionError)
		}
		testing.Fatalf("NewAWSCloudProvider failed: %v", constructionError)
	}
	if awsProvider.ProviderName() != "aws" {
		testing.Errorf("expected provider name aws, got %q", awsProvider.ProviderName())
	}
}

func TestGCPProviderConstructionAndName(testing *testing.T) {
	ctx := context.Background()
	gcpProvider, constructionError := NewGCPCloudProvider(ctx, "my-project", "us-central1")
	if constructionError != nil {
		if strings.Contains(constructionError.Error(), "credentials") {
			testing.Skipf("skipping: no GCP credentials available: %v", constructionError)
		}
		testing.Fatalf("NewGCPCloudProvider failed: %v", constructionError)
	}
	if gcpProvider.ProviderName() != "gcp" {
		testing.Errorf("expected provider name gcp, got %q", gcpProvider.ProviderName())
	}
}

func TestGCPProviderRequiresProjectForMethods(testing *testing.T) {
	ctx := context.Background()
	gcpProvider, constructionError := NewGCPCloudProvider(ctx, "", "us-central1")
	if constructionError != nil {
		if strings.Contains(constructionError.Error(), "credentials") {
			testing.Skipf("skipping: no GCP credentials available: %v", constructionError)
		}
		testing.Fatalf("NewGCPCloudProvider failed: %v", constructionError)
	}

	_, listError := gcpProvider.ListInstances(ctx)
	if listError == nil {
		testing.Error("expected error from ListInstances with empty project")
	}
	_, createError := gcpProvider.CreateInstance(ctx, InstanceConfig{})
	if createError == nil {
		testing.Error("expected error from CreateInstance with empty project")
	}
	if _, ensureError := gcpProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{}); ensureError == nil {
		testing.Error("expected error from EnsureLoadBalancer with empty project")
	}
}

func TestAWSSanitizeResourceName(testing *testing.T) {
	testCases := []struct {
		prefix   string
		name     string
		expected string
	}{
		{"cca-", "web", "cca-web"},
		{"cca-", "payments/checkout", "cca-payments-checkout"},
		{"cca-tg-", "very-long-service-name-that-exceeds-limits", "cca-tg-very-long-service-name-th"},
		{"cca-", "dots.and_underscores", "cca-dots-and-underscores"},
	}
	for _, testCase := range testCases {
		actual := awsSanitizeResourceName(testCase.prefix, testCase.name)
		if actual != testCase.expected {
			testing.Errorf("awsSanitizeResourceName(%q, %q) = %q, want %q", testCase.prefix, testCase.name, actual, testCase.expected)
		}
		if len(actual) > awsResourceNameMaxLength {
			testing.Errorf("name %q exceeds max length %d", actual, awsResourceNameMaxLength)
		}
	}
}

func TestGCPSanitizeResourceName(testing *testing.T) {
	testCases := []struct {
		prefix   string
		name     string
		expected string
	}{
		{"cca-", "web", "cca-web"},
		{"cca-tp-", "payments/checkout", "cca-tp-payments-checkout"},
		{"cca-route-", "10.244.1.0/24", "cca-route-10-244-1-0-24"},
	}
	for _, testCase := range testCases {
		actual := gcpSanitizeResourceName(testCase.prefix, testCase.name)
		if actual != testCase.expected {
			testing.Errorf("gcpSanitizeResourceName(%q, %q) = %q, want %q", testCase.prefix, testCase.name, actual, testCase.expected)
		}
		if len(actual) > gcpResourceNameMaxLength {
			testing.Errorf("name %q exceeds max length %d", actual, gcpResourceNameMaxLength)
		}
	}
}

func TestAWSProviderRequiresConfigForOperations(testing *testing.T) {
	ctx := context.Background()
	awsProvider, constructionError := NewAWSCloudProvider(ctx, "us-east-1")
	if constructionError != nil {
		if strings.Contains(constructionError.Error(), "credentials") {
			testing.Skipf("skipping: no AWS credentials available: %v", constructionError)
		}
		testing.Fatalf("NewAWSCloudProvider failed: %v", constructionError)
	}

	_, ensureError := awsProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{ServiceName: "web"})
	if ensureError == nil {
		testing.Error("expected error from EnsureLoadBalancer without subnets configured")
	}

	routeError := awsProvider.EnsureRoute(ctx, RouteConfig{DestinationCIDR: "10.0.0.0/24"})
	if routeError == nil {
		testing.Error("expected error from EnsureRoute without route table configured")
	}
}
