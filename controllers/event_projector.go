package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

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

// Run starts the event projector. It watches observed instance state, node
// state, placements, and effective service counts, emitting events for each
// committed state transition. Blocks until the context is cancelled.
func (eventProjector *EventProjector) Run(ctx context.Context) error {
	watchPrefixes := []string{
		types.PrefixObserved + "/instance/",
		types.PrefixObserved + "/node/",
		types.PrefixPlacement + "/instance/",
		types.PrefixEffective + "/service/",
	}

	watchChannels := make([]<-chan store.Event, 0, len(watchPrefixes))
	for _, watchPrefix := range watchPrefixes {
		watchChannel, watchError := eventProjector.factStore.Watch(ctx, watchPrefix, store.WatchOption{Prefix: true})
		if watchError != nil {
			return fmt.Errorf("event projector watch %s: %w", watchPrefix, watchError)
		}
		watchChannels = append(watchChannels, watchChannel)
	}

	mergedChannel := mergeWatchChannels(ctx, watchChannels)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case watchEvent, channelOpen := <-mergedChannel:
			if !channelOpen {
				return nil
			}
			eventProjector.projectEvent(ctx, watchEvent)
		}
	}
}

// projectEvent examines a single watch event and emits a semantic event if
// the change represents a noteworthy state transition.
func (eventProjector *EventProjector) projectEvent(ctx context.Context, watchEvent store.Event) {
	eventKind, eventTarget, eventDetail := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind == "" {
		return
	}
	eventProjector.eventLog.Emit(ctx, eventKind, eventTarget, eventDetail, "event-projector")
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

// mergeWatchChannels fans in multiple watch channels into a single output
// channel. One goroutine per input channel forwards events to the merged
// output. The output channel is closed when all input channels are closed.
func mergeWatchChannels(ctx context.Context, channels []<-chan store.Event) <-chan store.Event {
	mergedOutput := make(chan store.Event, 64)
	pendingCount := make(chan struct{}, len(channels))

	for _, inputChannel := range channels {
		go func(source <-chan store.Event) {
			defer func() { pendingCount <- struct{}{} }()
			for {
				select {
				case <-ctx.Done():
					return
				case watchEvent, channelOpen := <-source:
					if !channelOpen {
						return
					}
					select {
					case mergedOutput <- watchEvent:
					case <-ctx.Done():
						return
					}
				}
			}
		}(inputChannel)
	}

	go func() {
		for range len(channels) {
			<-pendingCount
		}
		close(mergedOutput)
	}()

	return mergedOutput
}
