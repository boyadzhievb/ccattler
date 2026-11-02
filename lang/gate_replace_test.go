// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// gateReplaceInitialConfig declares two gates with TLS and sticky cookies; the
// second name shares a prefix with the first to catch prefix-scan mistakes.
const gateReplaceInitialConfig = `
gate shop {
    host shop.home.arpa
    service shop port 80
    sticky cookie
    tls auto
}

gate shop-admin {
    host admin.home.arpa
    service shop port 80
    tls auto
}
`

// gateReplaceStrippedConfig re-declares only "shop", without sticky and tls.
const gateReplaceStrippedConfig = `
gate shop {
    host shop.home.arpa
    service shop port 80
}
`

// TestApplyReplacesDeclaredGate verifies re-applying a gate without its tls
// and sticky blocks deletes those facts, while a gate the file no longer
// declares (and one sharing the name prefix) is left untouched.
func TestApplyReplacesDeclaredGate(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	if applyError := Apply(ctx, factStore, gateReplaceInitialConfig); applyError != nil {
		t.Fatal(applyError)
	}
	if applyError := Apply(ctx, factStore, gateReplaceStrippedConfig); applyError != nil {
		t.Fatal(applyError)
	}

	for _, removedKey := range []string{types.KeyDesiredRouteTLSMode("shop"), types.KeyDesiredRouteSticky("shop")} {
		if _, getError := factStore.Get(ctx, removedKey); getError == nil {
			t.Errorf("%s still present after re-apply without it", removedKey)
		}
	}
	if hostFact, getError := factStore.Get(ctx, types.KeyDesiredRouteHost("shop")); getError != nil || string(hostFact.Value) != "shop.home.arpa" {
		t.Errorf("shop host lost: %v", getError)
	}
	if _, getError := factStore.Get(ctx, types.KeyDesiredRouteTLSMode("shop-admin")); getError != nil {
		t.Error("shop-admin tls/mode deleted although the file did not declare shop-admin")
	}
}

// TestDiffReportsRemovedGateFacts verifies cca diff shows the facts a
// re-apply would delete from a declared gate.
func TestDiffReportsRemovedGateFacts(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	if applyError := Apply(ctx, factStore, gateReplaceInitialConfig); applyError != nil {
		t.Fatal(applyError)
	}
	changes, diffError := Diff(ctx, factStore, gateReplaceStrippedConfig)
	if diffError != nil {
		t.Fatal(diffError)
	}
	deletedKeys := make(map[string]string)
	for _, change := range changes {
		if change.Type == "delete" {
			deletedKeys[change.Key] = change.OldValue
		}
	}
	if deletedKeys[types.KeyDesiredRouteTLSMode("shop")] != string(types.RouteTLSModeAuto) {
		t.Errorf("diff does not report tls/mode deletion: %v", deletedKeys)
	}
	for deletedKey := range deletedKeys {
		if deletedKey == types.KeyDesiredRouteTLSMode("shop-admin") {
			t.Error("diff reports deletion for undeclared gate shop-admin")
		}
	}
}
