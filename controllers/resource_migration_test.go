// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// migrationTestTimeout bounds how long a migration test waits for the gate.
const migrationTestTimeout = 5 * time.Second

// limitedTransactionStore wraps a store and refuses, like etcd, any
// transaction whose compares plus operations exceed etcdTransactionOperationLimit.
// It also fails the first failingTransactions transactions with an error.
type limitedTransactionStore struct {
	store.StateStore
	mutex                sync.Mutex // mutex guards the counters
	failingTransactions  int        // failingTransactions is how many transactions still fail
	committedOperations  []int      // committedOperations records the item count of each committed transaction
	committedTransaction atomic.Int64
}

// Transaction enforces the etcd item limit and the injected failures, then
// delegates to the wrapped store.
func (limitedStore *limitedTransactionStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	limitedStore.mutex.Lock()
	transactionItems := len(compares) + len(onSuccess) + len(onFailure)
	if transactionItems > etcdTransactionOperationLimit {
		limitedStore.mutex.Unlock()
		return false, fmt.Errorf("etcdserver: too many operations in txn request (%d)", transactionItems)
	}
	if limitedStore.failingTransactions > 0 {
		limitedStore.failingTransactions--
		limitedStore.mutex.Unlock()
		return false, errors.New("injected store failure")
	}
	limitedStore.mutex.Unlock()
	committed, transactionError := limitedStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
	if committed && transactionError == nil {
		limitedStore.mutex.Lock()
		limitedStore.committedOperations = append(limitedStore.committedOperations, transactionItems)
		limitedStore.mutex.Unlock()
		limitedStore.committedTransaction.Add(1)
	}
	return committed, transactionError
}

// putLegacyServices stores serviceCount services, each with bare intent and
// effective cpu and memory values (4 legacy values per service).
func putLegacyServices(t *testing.T, factStore store.StateStore, serviceCount int) {
	t.Helper()
	legacyFacts := make(map[string]string)
	for serviceIndex := 0; serviceIndex < serviceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc%02d", serviceIndex)
		legacyFacts[types.KeyIntentAutoscalerServiceResourcesCPU(serviceName)] = "750"
		legacyFacts[types.KeyEffectiveServiceResourcesCPU(serviceName)] = "750"
		legacyFacts[types.KeyIntentAutoscalerServiceResourcesMemory(serviceName)] = "805306368"
		legacyFacts[types.KeyEffectiveServiceResourcesMemory(serviceName)] = "805306368"
	}
	putFacts(t, factStore, legacyFacts)
}

// TestMigrationBatchesStayWithinTransactionLimit: design D9 (C1). 33 services
// × 4 legacy values need several transactions; each stays within the etcd
// item limit (compares + ops), every value is migrated, and no service is
// split across transactions.
func TestMigrationBatchesStayWithinTransactionLimit(t *testing.T) {
	const legacyServiceCount = 33
	limitedStore := &limitedTransactionStore{StateStore: store.NewMemoryStore()}
	defer func() { _ = limitedStore.Close() }()
	putLegacyServices(t, limitedStore, legacyServiceCount)

	rewrittenCount, migrationError := MigrateUnitlessResourceRecommendations(context.Background(), limitedStore)
	if migrationError != nil {
		t.Fatalf("migration of %d services failed: %v", legacyServiceCount, migrationError)
	}
	if rewrittenCount != legacyServiceCount*4 {
		t.Errorf("rewrote %d values, want %d", rewrittenCount, legacyServiceCount*4)
	}
	if len(limitedStore.committedOperations) < 2 {
		t.Errorf("committed %d transactions, want several", len(limitedStore.committedOperations))
	}
	for transactionIndex, transactionItems := range limitedStore.committedOperations {
		if transactionItems > etcdTransactionOperationLimit {
			t.Errorf("transaction %d has %d items, over the limit %d", transactionIndex, transactionItems, etcdTransactionOperationLimit)
		}
	}

	var plannedRewrites []plannedResourceRewrite
	for serviceIndex := 0; serviceIndex < legacyServiceCount; serviceIndex++ {
		for valueIndex := 0; valueIndex < 4; valueIndex++ {
			plannedRewrites = append(plannedRewrites, plannedResourceRewrite{
				serviceName: fmt.Sprintf("svc%02d", serviceIndex), factKey: fmt.Sprintf("k%02d-%d", serviceIndex, valueIndex),
			})
		}
	}
	batchOfService := make(map[string]int)
	for batchIndex, batch := range batchRewritesByService(plannedRewrites) {
		if batchItems := len(batch) * transactionItemsPerRewrite; batchItems > etcdTransactionOperationLimit {
			t.Errorf("batch %d has %d items, over the limit", batchIndex, batchItems)
		}
		for _, rewrite := range batch {
			if earlierBatch, seen := batchOfService[rewrite.serviceName]; seen && earlierBatch != batchIndex {
				t.Errorf("service %s split across batches %d and %d", rewrite.serviceName, earlierBatch, batchIndex)
			}
			batchOfService[rewrite.serviceName] = batchIndex
		}
	}
}

// TestControllersStartOnlyAfterResourceMigrationSucceeds: design D9 (C2). The
// store fails the first 3 transactions; the gate retries with back-off and
// starts the controllers only after the migration committed, never before.
func TestControllersStartOnlyAfterResourceMigrationSucceeds(t *testing.T) {
	const injectedFailures = 3
	limitedStore := &limitedTransactionStore{StateStore: store.NewMemoryStore(), failingTransactions: injectedFailures}
	defer func() { _ = limitedStore.Close() }()
	putLegacyServices(t, limitedStore, 1)

	ctx, cancel := context.WithTimeout(context.Background(), migrationTestTimeout)
	defer cancel()
	controllersStarted := make(chan int64, 1)
	gateDone := make(chan bool, 1)
	go func() {
		gateDone <- runControllersAfterResourceMigration(ctx, limitedStore, resourceMigrationBackoff{
			initialDelay: time.Millisecond, maximumDelay: 4 * time.Millisecond,
		}, func() { controllersStarted <- limitedStore.committedTransaction.Load() })
	}()

	select {
	case committedAtStart := <-controllersStarted:
		if committedAtStart == 0 {
			t.Errorf("controllers started before the migration committed")
		}
	case <-ctx.Done():
		t.Fatalf("controllers never started after %d injected failures", injectedFailures)
	}
	if started := <-gateDone; !started {
		t.Errorf("gate reported the controllers as not started")
	}
	if storedValue := readValue(limitedStore, types.KeyEffectiveServiceResourcesCPU("svc00")); storedValue != "750m" {
		t.Errorf("effective cpu = %q after the gate, want 750m", storedValue)
	}
}

// TestControllersNeverStartWhileResourceMigrationFails: design D9 (C2). While
// every transaction fails the controllers never start, and cancelling the
// context ends the retries.
func TestControllersNeverStartWhileResourceMigrationFails(t *testing.T) {
	limitedStore := &limitedTransactionStore{StateStore: store.NewMemoryStore(), failingTransactions: 1 << 30}
	defer func() { _ = limitedStore.Close() }()
	putLegacyServices(t, limitedStore, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	controllersStarted := false
	started := runControllersAfterResourceMigration(ctx, limitedStore, resourceMigrationBackoff{
		initialDelay: time.Millisecond, maximumDelay: 2 * time.Millisecond,
	}, func() { controllersStarted = true })
	if started || controllersStarted {
		t.Errorf("controllers started while the migration kept failing")
	}
}

// TestResourceMigrationBackoffDoublesUpToMaximum checks the retry delays:
// they double from the initial delay and stop growing at the maximum.
func TestResourceMigrationBackoffDoublesUpToMaximum(t *testing.T) {
	backoff := resourceMigrationBackoff{initialDelay: time.Second, maximumDelay: 5 * time.Second}
	wantDelays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for attemptIndex, wantDelay := range wantDelays {
		if gotDelay := backoff.delayAfterAttempt(attemptIndex + 1); gotDelay != wantDelay {
			t.Errorf("delay after attempt %d = %v, want %v", attemptIndex+1, gotDelay, wantDelay)
		}
	}
}

// casLossStore wraps a store and, on the second transaction only, first
// deletes and re-puts one of that transaction's guarded keys with its same
// value (a same-value put does not move the revision), so the CAS fails once. It counts how often
// each key is put by a committed transaction.
type casLossStore struct {
	store.StateStore
	transactionCount int            // transactionCount counts Transaction calls
	committedPuts    map[string]int // committedPuts counts committed puts per key
	lostCAS          bool           // lostCAS is set once a guarded key was moved
}

// Transaction makes the second call lose its CAS, then delegates and records
// the puts of committed transactions.
func (lossStore *casLossStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	lossStore.transactionCount++
	if lossStore.transactionCount == 2 && len(compares) > 0 {
		guardedFact, getError := lossStore.Get(ctx, compares[0].Key)
		if getError == nil {
			_ = lossStore.Delete(ctx, guardedFact.Key)
			_, _ = lossStore.Put(ctx, guardedFact.Key, guardedFact.Value)
			lossStore.lostCAS = true
		}
	}
	committed, transactionError := lossStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
	if committed && transactionError == nil {
		for _, operation := range onSuccess {
			lossStore.committedPuts[operation.Key]++
		}
	}
	return committed, transactionError
}

// TestMigrationRecoversFromCASLossInLaterBatch: design D9 (3c follow-up).
// Batch 1 commits, batch 2 loses its CAS once. The migration still completes,
// every value ends in unit form, and batch 1's values are never rewritten
// again (a second pass would not turn 750m into 750000m).
func TestMigrationRecoversFromCASLossInLaterBatch(t *testing.T) {
	const legacyServiceCount = 33
	lossStore := &casLossStore{StateStore: store.NewMemoryStore(), committedPuts: make(map[string]int)}
	defer func() { _ = lossStore.Close() }()
	putLegacyServices(t, lossStore, legacyServiceCount)

	rewrittenCount, migrationError := MigrateUnitlessResourceRecommendations(context.Background(), lossStore)
	if migrationError != nil {
		t.Fatalf("migration failed after one lost CAS: %v", migrationError)
	}
	if !lossStore.lostCAS {
		t.Fatalf("no CAS was lost; the test proves nothing")
	}
	if lossStore.transactionCount < 3 {
		t.Fatalf("%d transactions, want at least 3 (batch 1, lost batch 2, retried batch 2)", lossStore.transactionCount)
	}
	if rewrittenCount == 0 {
		t.Errorf("the retried pass reports 0 rewritten values, want the remaining ones")
	}
	for serviceIndex := 0; serviceIndex < legacyServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc%02d", serviceIndex)
		for factKey, wantValue := range map[string]string{
			types.KeyIntentAutoscalerServiceResourcesCPU(serviceName):    "750m",
			types.KeyEffectiveServiceResourcesCPU(serviceName):           "750m",
			types.KeyIntentAutoscalerServiceResourcesMemory(serviceName): "768Mi",
			types.KeyEffectiveServiceResourcesMemory(serviceName):        "768Mi",
		} {
			if gotValue := readValue(lossStore, factKey); gotValue != wantValue {
				t.Errorf("%s = %q, want %q", factKey, gotValue, wantValue)
			}
			if putCount := lossStore.committedPuts[factKey]; putCount != 1 {
				t.Errorf("%s written by %d committed transactions, want exactly 1", factKey, putCount)
			}
		}
	}
}
