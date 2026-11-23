// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"bytes"
	"context"
	"errors"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// The rule (M267 3e): every agent write or delete under observed/instance/{id}/
// goes through writeGuardedInstanceFacts (or the guarded publish transaction of
// publishInstanceStateUnlessStopped), so nothing reaches an instance that moved
// off this node. Exceptions: TestAgentInstanceWritesAreGuarded lists them.

// placementGuard returns the compare that keeps an agent's write about an
// instance from landing after the instance moved off nodeID: the revision of
// placement/node/{nodeID}/{id} its scan read (a move deletes the key, M267
// R2-1). None when the revision is unknown (0: infos built without a scan).
func placementGuard(nodeID string, instanceInfo placedInstanceInfo) []store.Compare {
	if instanceInfo.placementRevision == 0 {
		return nil
	}
	return []store.Compare{{Key: types.KeyPlacementNodeInstance(nodeID, instanceInfo.id), Revision: instanceInfo.placementRevision}}
}

// dropNoOpChanges returns the changes that would change the store: puts whose
// stored value differs (or cannot be read) and deletes of keys that exist, so a
// transaction writes no more keys than idempotent Puts did (etcd transactions
// write unchanged values, M266).
func dropNoOpChanges(ctx context.Context, factStore store.StateStore, changes []store.Op) []store.Op {
	effectiveChanges := make([]store.Op, 0, len(changes))
	for _, change := range changes {
		storedFact, getError := factStore.Get(ctx, change.Key)
		unchangedPut := change.Type == store.OpPut && getError == nil && bytes.Equal(storedFact.Value, change.Value)
		missingDelete := change.Type == store.OpDelete && errors.Is(getError, store.ErrKeyNotFound)
		if unchangedPut || missingDelete {
			continue
		}
		effectiveChanges = append(effectiveChanges, change)
	}
	return effectiveChanges
}

// writeGuardedInstanceFacts commits the changes about one instance in one
// transaction guarded by placementGuard, without its no-op changes (none left:
// no write). A lost guard (the instance moved off nodeID) writes nothing; store
// errors are logged as errors. Returns true when committed or nothing to do.
func writeGuardedInstanceFacts(ctx context.Context, factStore store.StateStore, nodeID string, instanceInfo placedInstanceInfo, changes []store.Op) bool {
	effectiveChanges := dropNoOpChanges(ctx, factStore, changes)
	if len(effectiveChanges) == 0 {
		return true
	}
	swapped, transactionError := factStore.Transaction(ctx, placementGuard(nodeID, instanceInfo), effectiveChanges, nil)
	if transactionError != nil {
		logging.Default().Error("failed to write instance facts", "instance", instanceInfo.id, "error", transactionError.Error())
		return false
	}
	if !swapped {
		logging.Default().Info("instance moved off this node, not writing its facts", "agent", nodeID, "instance", instanceInfo.id)
	}
	return swapped
}

// putGuardedInstanceFact writes one fact about an instance through
// writeGuardedInstanceFacts: never after the instance moved off nodeID.
func putGuardedInstanceFact(ctx context.Context, factStore store.StateStore, nodeID string, instanceInfo placedInstanceInfo, key string, value []byte) {
	writeGuardedInstanceFacts(ctx, factStore, nodeID, instanceInfo, []store.Op{{Type: store.OpPut, Key: key, Value: value}})
}

// deleteGuardedInstanceFact deletes one fact about an instance through
// writeGuardedInstanceFacts: never after the instance moved off nodeID.
func deleteGuardedInstanceFact(ctx context.Context, factStore store.StateStore, nodeID string, instanceInfo placedInstanceInfo, key string) {
	writeGuardedInstanceFacts(ctx, factStore, nodeID, instanceInfo, []store.Op{{Type: store.OpDelete, Key: key}})
}

// currentPlacementInfo returns the instance as placed on nodeID right now, for
// writers without a placement scan (telemetry of a running container): false
// when it is not placed here (moved, removed) or the read fails.
func currentPlacementInfo(ctx context.Context, factStore store.StateStore, nodeID string, instanceID string) (placedInstanceInfo, bool) {
	placementFact, readError := factStore.Get(ctx, types.KeyPlacementNodeInstance(nodeID, instanceID))
	if readError != nil {
		return placedInstanceInfo{}, false
	}
	return placedInstanceInfo{id: instanceID, placementRevision: placementFact.Revision}, true
}

// deleteFactsUnlessPlacedElsewhere is the cleanup of an instance that left this
// node's desired set: it deletes keys unless placement/instance/{id} names
// another node (they may be the target's, M267 B2), in one transaction on that
// placement's revision (0: still absent). Read errors leave everything.
func deleteFactsUnlessPlacedElsewhere(ctx context.Context, factStore store.StateStore, nodeID string, instanceID string, keys ...string) {
	placementKey := types.KeyPlacementInstance(instanceID)
	var placementRevision int64 // 0: the placement must still not exist
	placementFact, readError := factStore.Get(ctx, placementKey)
	switch {
	case readError == nil && string(placementFact.Value) != nodeID:
		return // placed on another node: the facts may already be the target's
	case readError == nil:
		placementRevision = placementFact.Revision
	case !errors.Is(readError, store.ErrKeyNotFound):
		logging.Default().Error("cannot read placement, leaving instance facts", "instance", instanceID, "error", readError.Error())
		return
	}
	deletes := make([]store.Op, 0, len(keys))
	for _, key := range keys {
		deletes = append(deletes, store.Op{Type: store.OpDelete, Key: key})
	}
	effectiveDeletes := dropNoOpChanges(ctx, factStore, deletes)
	if len(effectiveDeletes) == 0 {
		return
	}
	if _, transactionError := factStore.Transaction(ctx,
		[]store.Compare{{Key: placementKey, Revision: placementRevision}}, effectiveDeletes, nil); transactionError != nil {
		logging.Default().Error("failed to delete instance facts", "instance", instanceID, "error", transactionError.Error())
	}
}

// statusReasonChange returns the put of observed/instance/{id}/status-reason,
// a human-readable reason shown by the CLI and API, for a guarded write.
func statusReasonChange(instanceID string, reason string) store.Op {
	return store.Op{Type: store.OpPut, Key: types.KeyObservedInstanceStatusReason(instanceID), Value: []byte(reason)}
}
