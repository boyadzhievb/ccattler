// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultMergedEventBuffer is the channel buffer size for the merged watch
// channel used by the event projector. Large enough to absorb burst events
// without blocking individual watch goroutines.
const defaultMergedEventBuffer = 64

// EventProjector watches committed state transitions in the store and emits
// semantic events to the event log. Unlike the previous approach of deriving
// events from controller-proposed changes, the projector observes actual
// committed state, making events authoritative regardless of which component
// caused the change.
type EventProjector struct {
	factStore store.StateStore
	eventLog  *types.EventLog
}

// NewEventProjector creates an EventProjector that watches the given store
// and emits events to the provided event log.
func NewEventProjector(factStore store.StateStore, eventLog *types.EventLog) *EventProjector {
	return &EventProjector{
		factStore: factStore,
		eventLog:  eventLog,
	}
}

// watchReconnectBackoff is the pause before re-establishing a watch after
// the watch channel closes (e.g. due to etcd compaction).
const watchReconnectBackoff = 2 * time.Second

// Run starts the event projector. It watches observed instance state, node
// state, placements, and effective service counts, emitting events for each
// committed state transition. Each prefix gets its own goroutine with
// automatic reconnect on channel closure. Blocks until ctx is cancelled.
func (eventProjector *EventProjector) Run(ctx context.Context) error {
	watchPrefixes := []string{
		types.PrefixObserved + "/instance/",
		types.PrefixDerived + "/instance/",
		types.PrefixObserved + "/node/",
		types.PrefixPlacement + "/instance/",
		types.PrefixEffective + "/service/",
	}

	mergedChannel := make(chan store.Event, defaultMergedEventBuffer)

	for _, watchPrefix := range watchPrefixes {
		go eventProjector.watchPrefixWithReconnect(ctx, watchPrefix, mergedChannel)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case watchEvent := <-mergedChannel:
			eventProjector.projectEvent(ctx, watchEvent)
		}
	}
}

// watchPrefixWithReconnect watches a single store prefix, forwarding events
// to the merged channel. When the watch channel closes (etcd compaction,
// store shutdown), it waits briefly and re-establishes the watch from the
// last processed revision to avoid gaps in event history. Handles
// EventCompacted and EventOverflow by performing a full resync scan. Only
// exits when ctx is cancelled.
func (eventProjector *EventProjector) watchPrefixWithReconnect(
	ctx context.Context,
	watchPrefix string,
	mergedChannel chan<- store.Event,
) {
	var lastProcessedRevision int64

	for {
		if ctx.Err() != nil {
			return
		}

		watchOptions := store.WatchOption{Prefix: true}
		if lastProcessedRevision > 0 {
			watchOptions.StartRevision = lastProcessedRevision + 1
		}

		watchChannel, watchError := eventProjector.factStore.Watch(ctx, watchPrefix, watchOptions)
		if watchError != nil {
			logging.Default().Error("event projector watch failed",
				"prefix", watchPrefix, "error", watchError.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchReconnectBackoff):
				continue
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case watchEvent, channelOpen := <-watchChannel:
				if !channelOpen {
					logging.Default().Warn("event projector watch closed, reconnecting",
						"prefix", watchPrefix,
						"last_revision", fmt.Sprintf("%d", lastProcessedRevision))
					break
				}
				if watchEvent.Type == store.EventCompacted || watchEvent.Type == store.EventOverflow {
					logging.Default().Warn("event projector resync required",
						"prefix", watchPrefix,
						"event_type", fmt.Sprintf("%d", watchEvent.Type))
					resyncRevision := eventProjector.resyncFromScan(ctx, watchPrefix)
					if resyncRevision > lastProcessedRevision {
						lastProcessedRevision = resyncRevision
					}
					break
				}
				if watchEvent.Fact.Revision > lastProcessedRevision {
					lastProcessedRevision = watchEvent.Fact.Revision
				}
				select {
				case mergedChannel <- watchEvent:
				case <-ctx.Done():
					return
				}
				continue
			}
			break
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(watchReconnectBackoff):
		}
	}
}

// resyncFromScan performs a full prefix scan and returns the store revision
// at which the scan was performed. This establishes a baseline after watch
// compaction or overflow so the next watch can resume without gaps.
func (eventProjector *EventProjector) resyncFromScan(ctx context.Context, prefix string) int64 {
	scanResult, scanError := eventProjector.factStore.ScanWithRevision(ctx, prefix)
	if scanError != nil {
		logging.Default().Error("event projector resync scan failed",
			"prefix", prefix, "error", scanError.Error())
		return 0
	}
	return scanResult.Revision
}

// projectEvent examines a single watch event and emits a semantic event if
// the change represents a noteworthy state transition.
func (eventProjector *EventProjector) projectEvent(ctx context.Context, watchEvent store.Event) {
	eventKind, eventTarget, eventDetail := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind == "" {
		return
	}
	if _, emitError := eventProjector.eventLog.Emit(ctx, eventKind, eventTarget, eventDetail, "event-projector"); emitError != nil {
		logging.Default().Error("failed to emit event", "error", emitError.Error())
	}
}

// classifyWatchEventAsSemanticEvent examines a committed store watch event and
// returns the event kind, target, and detail if it represents a noteworthy
// state transition. Uses the Prev field to detect actual transitions rather
// than just new values.
func classifyWatchEventAsSemanticEvent(watchEvent store.Event) (string, string, string) {
	factKey := watchEvent.Fact.Key
	factValue := string(watchEvent.Fact.Value)

	var previousValue string
	if watchEvent.Prev != nil {
		previousValue = string(watchEvent.Prev.Value)
	}

	if factValue == previousValue {
		return "", "", ""
	}

	observedInstancePrefix := types.PrefixObserved + "/instance/"
	if strings.HasPrefix(factKey, observedInstancePrefix) && strings.HasSuffix(factKey, "/state") {
		instanceID := strings.TrimPrefix(factKey, observedInstancePrefix)
		instanceID = strings.TrimSuffix(instanceID, "/state")

		switch factValue {
		case string(types.InstanceRunning):
			return "instance.running", "instance/" + instanceID, fmt.Sprintf("instance %s is now running", instanceID)
		case string(types.InstanceFailed):
			return "instance.failed", "instance/" + instanceID, fmt.Sprintf("instance %s has failed", instanceID)
		case string(types.InstancePending):
			if previousValue == "" {
				return "instance.created", "instance/" + instanceID, fmt.Sprintf("instance %s created (pending)", instanceID)
			}
			return "instance.pending", "instance/" + instanceID, fmt.Sprintf("instance %s is now pending", instanceID)
		case string(types.InstanceStarting):
			return "instance.starting", "instance/" + instanceID, fmt.Sprintf("instance %s is starting", instanceID)
		case string(types.InstanceStopped):
			return "instance.stopped", "instance/" + instanceID, fmt.Sprintf("instance %s stopped", instanceID)
		}
	}

	derivedInstancePrefix := types.PrefixDerived + "/instance/"
	if strings.HasPrefix(factKey, derivedInstancePrefix) {
		rest := strings.TrimPrefix(factKey, derivedInstancePrefix)
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) == 2 {
			instanceID := parts[0]
			switch parts[1] {
			case "node_failure":
				return "instance.failed", "instance/" + instanceID, fmt.Sprintf("instance %s marked failed (node unreachable)", instanceID)
			case "controller_stopped":
				return "instance.stopped", "instance/" + instanceID, fmt.Sprintf("instance %s stopped by controller", instanceID)
			}
		}
	}

	placementPrefix := types.PrefixPlacement + "/instance/"
	if strings.HasPrefix(factKey, placementPrefix) && watchEvent.Type == store.EventPut {
		instanceID := strings.TrimPrefix(factKey, placementPrefix)
		return "instance.placed", "instance/" + instanceID, fmt.Sprintf("instance %s placed on node %s", instanceID, factValue)
	}

	observedNodePrefix := types.PrefixObserved + "/node/"
	if strings.HasPrefix(factKey, observedNodePrefix) && strings.HasSuffix(factKey, "/state") {
		nodeID := strings.TrimPrefix(factKey, observedNodePrefix)
		nodeID = strings.TrimSuffix(nodeID, "/state")
		return "node." + factValue, "node/" + nodeID, fmt.Sprintf("node %s is now %s", nodeID, factValue)
	}

	effectivePrefix := types.PrefixEffective + "/service/"
	if strings.HasPrefix(factKey, effectivePrefix) && strings.HasSuffix(factKey, "/instances") {
		serviceName := strings.TrimPrefix(factKey, effectivePrefix)
		serviceName = strings.TrimSuffix(serviceName, "/instances")
		return "service.scaled", "service/" + serviceName, fmt.Sprintf("service %s scaled to %s instances", serviceName, factValue)
	}

	return "", "", ""
}
