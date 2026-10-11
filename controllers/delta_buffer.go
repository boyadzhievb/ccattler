// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"sync"

	"github.com/boyadzhievb/ccattler/store"
)

// deltaBuffer accumulates watch events between reconciliation cycles for
// controllers that implement DeltaController. Multiple events for the same
// key are coalesced: the latest fact value is kept, but the original Prev
// from the first event is preserved so the controller sees the full delta.
type deltaBuffer struct {
	bufferMutex      sync.Mutex
	coalescedEvents  map[string]store.Event
	overflowDetected bool
}

// newDeltaBuffer creates an empty delta buffer ready to accumulate events.
func newDeltaBuffer() *deltaBuffer {
	return &deltaBuffer{
		coalescedEvents: make(map[string]store.Event),
	}
}

// appendEvent adds a watch event to the buffer. If the key already has an
// accumulated event, the new fact replaces the old but the original Prev is
// retained so the consumer sees the complete state transition.
func (buffer *deltaBuffer) appendEvent(watchEvent store.Event) {
	buffer.bufferMutex.Lock()
	defer buffer.bufferMutex.Unlock()

	factKey := watchEvent.Fact.Key
	if existingEvent, alreadyTracked := buffer.coalescedEvents[factKey]; alreadyTracked {
		watchEvent.Prev = existingEvent.Prev
	}
	buffer.coalescedEvents[factKey] = watchEvent
}

// markOverflow signals that events were lost (watch compaction or overflow).
// The next drain will return nil, forcing a full reconciliation.
func (buffer *deltaBuffer) markOverflow() {
	buffer.bufferMutex.Lock()
	defer buffer.bufferMutex.Unlock()
	buffer.overflowDetected = true
}

// drain returns all accumulated events and resets the buffer. Returns nil if
// the buffer overflowed, signaling that a full reconcile is needed instead.
func (buffer *deltaBuffer) drain() []store.Event {
	buffer.bufferMutex.Lock()
	defer buffer.bufferMutex.Unlock()

	if buffer.overflowDetected {
		buffer.overflowDetected = false
		buffer.coalescedEvents = make(map[string]store.Event)
		return nil
	}

	drainedEvents := make([]store.Event, 0, len(buffer.coalescedEvents))
	for _, coalescedEvent := range buffer.coalescedEvents {
		drainedEvents = append(drainedEvents, coalescedEvent)
	}
	buffer.coalescedEvents = make(map[string]store.Event)
	return drainedEvents
}
