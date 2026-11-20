// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package tenant

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// putTenantCPUService stores a flat-named service owned by tenantName with the
// given CPU request and instance count.
func putTenantCPUService(ctx context.Context, factStore store.StateStore, serviceName string, tenantName string, cpuText string, instanceCount string) {
	factStore.Put(ctx, types.KeyDesiredService(serviceName), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("app:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceOwner(serviceName), []byte(tenantName))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU(serviceName), []byte(cpuText))
	factStore.Put(ctx, types.KeyDesiredServiceInstances(serviceName), []byte(instanceCount))
}

// TestTenantCPUUsageReadsCoresWithSharedParser checks M243 D6 for tenant
// usage and fair share: cpu 0.5 with 2 instances is 1000 millicores (the old
// reader made it 0), and an unreadable value is left out instead of guessed.
func TestTenantCPUUsageReadsCoresWithSharedParser(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	putTenantCPUService(ctx, factStore, "api", "payments", "0.5", "2")
	putTenantCPUService(ctx, factStore, "broken", "payments", "lots", "3")
	registry := NewTenantRegistry(factStore)

	if cpuUsage := NewQuotaAdmission(factStore, registry).computeTenantCPUUsage(ctx, "payments"); cpuUsage != 1000 {
		t.Errorf("quota CPU usage = %d millicores, want 1000", cpuUsage)
	}
	if fairShareCPU := NewFairScheduler(factStore, registry).computeTenantCPU(ctx, "payments"); fairShareCPU != 1000 {
		t.Errorf("fair-share CPU = %d millicores, want 1000", fairShareCPU)
	}
}
