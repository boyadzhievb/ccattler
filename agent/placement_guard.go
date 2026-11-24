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

// The rule (M267 3e, 3f): the agent writes and deletes observed/instance/{id}/*
// only through instanceFactWriter or the guarded publish transaction of
// publishInstanceStateUnlessStopped. TestAgentInstanceWritesAreGuarded checks
// the closed world: every store Transaction is on its list with a reason, and
// every Put/Delete key is a direct non-instance types.Key* call.

// maxInstanceFactChangesPerTransaction caps the changes of one guarded write,
// well under the 128 operations etcd and the memory store allow together with
// the compare; larger writes (many host ports) are split (review round 5 B1).
const maxInstanceFactChangesPerTransaction = 100

// instanceFactWriter is the agent's one writer of per-instance observed facts:
// every write compares the placement the agent's scan read, so nothing lands
// on an instance that moved off this node.
type instanceFactWriter struct {
	factStore store.StateStore // factStore is the store written.
	nodeID    string           // nodeID is this agent's node, whose placement guards each write.
}

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

// writeGuarded commits the changes about one instance, without no-ops, in
// transactions of at most maxInstanceFactChangesPerTransaction, each guarded
// by placementGuard; true when every chunk committed (3g R6-1). A lost guard
// (moved off this node) or a logged store error stops the rest.
func (instanceFactWriter instanceFactWriter) writeGuarded(ctx context.Context, instanceInfo placedInstanceInfo, changes ...store.Op) bool {
	effectiveChanges := dropNoOpChanges(ctx, instanceFactWriter.factStore, changes)
	for len(effectiveChanges) > 0 {
		chunk := effectiveChanges[:min(len(effectiveChanges), maxInstanceFactChangesPerTransaction)]
		effectiveChanges = effectiveChanges[len(chunk):]
		swapped, transactionError := instanceFactWriter.factStore.Transaction(ctx,
			placementGuard(instanceFactWriter.nodeID, instanceInfo), chunk, nil)
		if transactionError != nil {
			logging.Default().Error("failed to write instance facts", "instance", instanceInfo.id, "error", transactionError.Error())
			return false
		}
		if !swapped {
			logging.Default().Info("instance moved off this node, not writing its facts", "agent", instanceFactWriter.nodeID, "instance", instanceInfo.id)
			return false
		}
	}
	return true
}

// currentPlacement returns the instance as placed on this node right now, for
// writers without a placement scan (telemetry of a running container): false
// when it is not placed here (moved, removed) or the read fails.
func (instanceFactWriter instanceFactWriter) currentPlacement(ctx context.Context, instanceID string) (placedInstanceInfo, bool) {
	placementFact, readError := instanceFactWriter.factStore.Get(ctx, types.KeyPlacementNodeInstance(instanceFactWriter.nodeID, instanceID))
	if readError != nil {
		return placedInstanceInfo{}, false
	}
	return placedInstanceInfo{id: instanceID, placementRevision: placementFact.Revision}, true
}

// deleteUnlessPlacedElsewhere is the cleanup of an instance that left this
// node's desired set: it deletes keys unless placement/instance/{id} names
// another node (they may be the target's, M267 B2), in one transaction on that
// placement's revision (0: still absent). Read errors leave everything.
func (instanceFactWriter instanceFactWriter) deleteUnlessPlacedElsewhere(ctx context.Context, instanceID string, keys ...string) {
	placementKey := types.KeyPlacementInstance(instanceID)
	var placementRevision int64 // 0: the placement must still not exist
	placementFact, readError := instanceFactWriter.factStore.Get(ctx, placementKey)
	switch {
	case readError == nil && string(placementFact.Value) != instanceFactWriter.nodeID:
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
	effectiveDeletes := dropNoOpChanges(ctx, instanceFactWriter.factStore, deletes)
	if len(effectiveDeletes) == 0 {
		return
	}
	if _, transactionError := instanceFactWriter.factStore.Transaction(ctx,
		[]store.Compare{{Key: placementKey, Revision: placementRevision}}, effectiveDeletes, nil); transactionError != nil {
		logging.Default().Error("failed to delete instance facts", "instance", instanceID, "error", transactionError.Error())
	}
}

// statusReasonChange returns the put of observed/instance/{id}/status-reason,
// a human-readable reason shown by the CLI and API, for a guarded write.
func statusReasonChange(instanceID string, reason string) store.Op {
	return store.Op{Type: store.OpPut, Key: types.KeyObservedInstanceStatusReason(instanceID), Value: []byte(reason)}
}
