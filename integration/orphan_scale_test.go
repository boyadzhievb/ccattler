// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// orphanStopTimeout bounds how long the controllers may take to stop orphans.
const orphanStopTimeout = 5 * time.Second

// TestOrphanScaleInstancesStopped: spec service-scaling, "Orphan instances
// from an earlier scale". Before M217, `scale nosuch 2` left a user intent and
// running instances for a name nobody declared; the intent resolver and the
// instance controller must stop them, and leave a declared service alone.
func TestOrphanScaleInstancesStopped(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	controllerRunner := controllers.NewRunner(factStore,
		controllers.NewIntentResolverController(), controllers.NewInstanceController())
	controllerRunner.SetDebounce(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	factStore.Put(ctx, types.KeyIntentUserServiceInstances("nosuch"), []byte("2"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("nosuch"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyIntentUserServiceInstances("web"), []byte("1"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("1"))
	for instanceID, serviceName := range map[string]string{"nosuch-a": "nosuch", "nosuch-b": "nosuch", "web-a": "web"} {
		factStore.Put(ctx, types.KeyObservedInstance(instanceID), []byte(""))
		factStore.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte(serviceName))
		factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceRunning)))
	}

	startRunner(t, factStore, ctx, cancel, controllerRunner)

	waitFor(t, orphanStopTimeout, "both orphan instances stopped, effective count 0, web untouched", func() bool {
		for _, orphanID := range []string{"nosuch-a", "nosuch-b"} {
			stoppedFact, _ := factStore.Get(ctx, types.KeyDerivedInstanceControllerStopped(orphanID))
			if stoppedFact == nil || string(stoppedFact.Value) != "true" {
				return false
			}
		}
		effectiveFact, _ := factStore.Get(ctx, types.KeyEffectiveServiceInstances("nosuch"))
		webStoppedFact, _ := factStore.Get(ctx, types.KeyDerivedInstanceControllerStopped("web-a"))
		webEffectiveFact, _ := factStore.Get(ctx, types.KeyEffectiveServiceInstances("web"))
		return effectiveFact != nil && string(effectiveFact.Value) == "0" && webStoppedFact == nil &&
			webEffectiveFact != nil && string(webEffectiveFact.Value) == "1"
	})
}
