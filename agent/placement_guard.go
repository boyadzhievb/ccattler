// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"bytes"
	"context"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

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

// dropUnchangedPuts returns the puts whose stored value differs (or cannot be
// read), so moving them into one transaction writes no more keys than the
// separate idempotent Puts did (etcd transactions write unchanged values, M266).
func dropUnchangedPuts(ctx context.Context, factStore store.StateStore, puts []store.Op) []store.Op {
	changedPuts := make([]store.Op, 0, len(puts))
	for _, put := range puts {
		if storedFact, getError := factStore.Get(ctx, put.Key); getError == nil && bytes.Equal(storedFact.Value, put.Value) {
			continue
		}
		changedPuts = append(changedPuts, put)
	}
	return changedPuts
}

// putGuardedInstanceFact writes one fact about an instance unless the value is
// unchanged or the instance moved off nodeID since the scan (placementGuard):
// a late probe result of the source never reaches the target's instance
// (M267 R3-S1). Store errors are logged as errors.
func putGuardedInstanceFact(ctx context.Context, factStore store.StateStore, nodeID string, instanceInfo placedInstanceInfo, key string, value []byte) {
	changedPuts := dropUnchangedPuts(ctx, factStore, []store.Op{{Type: store.OpPut, Key: key, Value: value}})
	if len(changedPuts) == 0 {
		return
	}
	swapped, transactionError := factStore.Transaction(ctx, placementGuard(nodeID, instanceInfo), changedPuts, nil)
	if transactionError != nil {
		logging.Default().Error("failed to write instance fact", "instance", instanceInfo.id, "key", key, "error", transactionError.Error())
		return
	}
	if !swapped {
		logging.Default().Info("instance moved off this node, not writing its fact", "agent", nodeID, "instance", instanceInfo.id, "key", key)
	}
}
