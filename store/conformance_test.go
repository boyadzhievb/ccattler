package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------
// Watch loss-tolerance and compaction resync conformance tests.
//
// These tests verify the store contract: a consumer that uses
// ScanWithRevision + Watch(StartRevision = scanRevision + 1) will never
// miss an event, and the system recovers gracefully from overflow and
// compaction.
// -----------------------------------------------------------------------

// TestWatchFromRevisionCapturesGap verifies that a watch started from a
// specific revision delivers events that occurred between a prior scan
// and the watch creation, closing the observation gap.
func TestWatchFromRevisionCapturesGap(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	// Write initial state.
	memoryStore.Put(ctx, "/service/web/image", []byte("nginx:1.27"))
	memoryStore.Put(ctx, "/service/api/image", []byte("myapi:v1"))

	// Take a snapshot — this is the "scan point."
	scanResult, err := memoryStore.ScanWithRevision(ctx, "/service/")
	if err != nil {
		t.Fatal(err)
	}
	scanRevision := scanResult.Revision

	// Simulate writes that happen between scan and watch creation.
	memoryStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))
	memoryStore.Put(ctx, "/service/api/image", []byte("myapi:v2"))
	memoryStore.Put(ctx, "/node/node-1", []byte("alive")) // different prefix

	// Start watch from scanRevision + 1 — should capture the gap writes.
	eventChannel, err := memoryStore.Watch(ctx, "/service/", WatchOption{
		Prefix:        true,
		StartRevision: scanRevision + 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Collect replayed events.
	replayedEvents := drainEvents(t, eventChannel, 2, 500*time.Millisecond)

	if len(replayedEvents) != 2 {
		t.Fatalf("expected 2 replayed events from gap, got %d", len(replayedEvents))
	}

	// Verify the gap events are the correct updates.
	foundWebUpdate := false
	foundAPIUpdate := false
	for _, event := range replayedEvents {
		if event.Fact.Key == "/service/web/image" && string(event.Fact.Value) == "nginx:1.28" {
			foundWebUpdate = true
		}
		if event.Fact.Key == "/service/api/image" && string(event.Fact.Value) == "myapi:v2" {
			foundAPIUpdate = true
		}
	}
	if !foundWebUpdate {
		t.Error("missed web image update in gap replay")
	}
	if !foundAPIUpdate {
		t.Error("missed api image update in gap replay")
	}

	// Verify live events still work after replay.
	memoryStore.Put(ctx, "/service/web/instances", []byte("5"))
	liveEvents := drainEvents(t, eventChannel, 1, 500*time.Millisecond)
	if len(liveEvents) != 1 || string(liveEvents[0].Fact.Value) != "5" {
		t.Fatalf("expected live event with value '5', got %+v", liveEvents)
	}
}

// TestWatchFromRevisionZeroSkipsReplay verifies that StartRevision=0
// behaves identically to the original Watch — no historical replay.
func TestWatchFromRevisionZeroSkipsReplay(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	memoryStore.Put(ctx, "/key/a", []byte("v1"))
	memoryStore.Put(ctx, "/key/b", []byte("v2"))

	eventChannel, err := memoryStore.Watch(ctx, "/key/", WatchOption{
		Prefix:        true,
		StartRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// No historical events should be delivered.
	select {
	case event := <-eventChannel:
		t.Fatalf("expected no events for StartRevision=0, got %+v", event)
	case <-time.After(100 * time.Millisecond):
	}

	// But new writes should still trigger events.
	memoryStore.Put(ctx, "/key/c", []byte("v3"))
	liveEvents := drainEvents(t, eventChannel, 1, 500*time.Millisecond)
	if len(liveEvents) != 1 {
		t.Fatalf("expected 1 live event, got %d", len(liveEvents))
	}
}

// TestWatchFromRevisionExactKey verifies that revision-based replay respects
// exact key matching (not prefix) when Prefix is false.
func TestWatchFromRevisionExactKey(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	memoryStore.Put(ctx, "/service/web/image", []byte("nginx:1.27"))
	memoryStore.Put(ctx, "/service/api/image", []byte("myapi:v1"))

	scanResult, _ := memoryStore.ScanWithRevision(ctx, "/service/")
	scanRevision := scanResult.Revision

	memoryStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))
	memoryStore.Put(ctx, "/service/api/image", []byte("myapi:v2"))

	// Watch only /service/web/image (exact match).
	eventChannel, err := memoryStore.Watch(ctx, "/service/web/image", WatchOption{
		Prefix:        false,
		StartRevision: scanRevision + 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	replayedEvents := drainEvents(t, eventChannel, 1, 500*time.Millisecond)
	if len(replayedEvents) != 1 {
		t.Fatalf("expected 1 event for exact key watch, got %d", len(replayedEvents))
	}
	if replayedEvents[0].Fact.Key != "/service/web/image" {
		t.Fatalf("expected key /service/web/image, got %s", replayedEvents[0].Fact.Key)
	}
}

// TestWatchFromCompactedRevisionEmitsEvent verifies that when the requested
// start revision has been evicted from the MemoryStore's history buffer,
// an EventCompacted is emitted and the channel is closed.
func TestWatchFromCompactedRevisionEmitsEvent(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	// Fill the history buffer to evict old entries. The default capacity is
	// 4096, so we write more than that to guarantee eviction.
	for iteration := 0; iteration < defaultEventHistoryCapacity+500; iteration++ {
		memoryStore.Put(ctx, fmt.Sprintf("/fill/%d", iteration), []byte("x"))
	}

	// Try to watch from revision 1 — should be compacted/evicted.
	eventChannel, err := memoryStore.Watch(ctx, "/", WatchOption{
		Prefix:        true,
		StartRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case event, open := <-eventChannel:
		if event.Type != EventCompacted {
			t.Fatalf("expected EventCompacted, got type %d", event.Type)
		}
		// Channel should close after compacted event.
		if open {
			_, stillOpen := <-eventChannel
			if stillOpen {
				t.Fatal("channel should be closed after EventCompacted")
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for EventCompacted")
	}
}

// TestWatchOverflowThenResyncConverges verifies the complete overflow recovery
// pattern: overflow detected → consumer does ScanWithRevision → starts new
// watch from scan revision → state converges with no missed events.
func TestWatchOverflowThenResyncConverges(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	// Start a watch with a small effective window.
	eventChannel, err := memoryStore.Watch(ctx, "/data/", WatchOption{Prefix: true})
	if err != nil {
		t.Fatal(err)
	}

	// Flood the watch channel to trigger overflow (buffer is 64).
	for iteration := 0; iteration < 200; iteration++ {
		memoryStore.Put(ctx, fmt.Sprintf("/data/key-%d", iteration), []byte(fmt.Sprintf("v%d", iteration)))
	}

	// Drain whatever we got — expect some events and possibly an overflow.
	overflowSeen := false
	drainedCount := 0
	for {
		select {
		case event := <-eventChannel:
			if event.Type == EventOverflow {
				overflowSeen = true
			}
			drainedCount++
		default:
			goto doneDraining
		}
	}
doneDraining:

	if drainedCount < 200 && !overflowSeen {
		// Write one more to trigger overflow delivery.
		memoryStore.Put(ctx, "/data/trigger-overflow", []byte("x"))
		select {
		case event := <-eventChannel:
			if event.Type == EventOverflow {
				overflowSeen = true
			}
		case <-time.After(200 * time.Millisecond):
		}
	}

	// === RESYNC PATTERN ===
	// Step 1: Scan to get authoritative state.
	scanResult, err := memoryStore.ScanWithRevision(ctx, "/data/")
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Start a new watch from the scan revision + 1.
	resyncChannel, err := memoryStore.Watch(ctx, "/data/", WatchOption{
		Prefix:        true,
		StartRevision: scanResult.Revision + 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 3: Write new data after the resync point.
	memoryStore.Put(ctx, "/data/post-resync-1", []byte("new-1"))
	memoryStore.Put(ctx, "/data/post-resync-2", []byte("new-2"))

	// Step 4: Verify we see exactly the post-resync writes.
	postResyncEvents := drainEvents(t, resyncChannel, 2, 500*time.Millisecond)
	if len(postResyncEvents) != 2 {
		t.Fatalf("expected 2 post-resync events, got %d", len(postResyncEvents))
	}

	// Build the full state: scan snapshot + post-resync events.
	state := make(map[string]string)
	for _, fact := range scanResult.Facts {
		state[fact.Key] = string(fact.Value)
	}
	for _, event := range postResyncEvents {
		if event.Type == EventPut {
			state[event.Fact.Key] = string(event.Fact.Value)
		} else if event.Type == EventDelete {
			delete(state, event.Fact.Key)
		}
	}

	// Verify merged state matches the actual store.
	actualFacts, _ := memoryStore.Scan(ctx, "/data/")
	if len(state) != len(actualFacts) {
		t.Fatalf("state size mismatch after resync: reconstructed=%d, actual=%d", len(state), len(actualFacts))
	}
	for _, fact := range actualFacts {
		if state[fact.Key] != string(fact.Value) {
			t.Errorf("key %s: reconstructed=%q, actual=%q", fact.Key, state[fact.Key], string(fact.Value))
		}
	}
}

// TestWatchFromRevisionMonotonicallyIncreasing verifies that events delivered
// via revision-based replay arrive in monotonically increasing revision order.
func TestWatchFromRevisionMonotonicallyIncreasing(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	// Establish a baseline.
	memoryStore.Put(ctx, "/seq/a", []byte("v1"))
	scanResult, _ := memoryStore.ScanWithRevision(ctx, "/seq/")
	scanRevision := scanResult.Revision

	// Write a sequence of updates.
	for iteration := 0; iteration < 20; iteration++ {
		memoryStore.Put(ctx, fmt.Sprintf("/seq/key-%02d", iteration), []byte(fmt.Sprintf("v%d", iteration)))
	}

	// Watch from scan revision to get all updates.
	eventChannel, err := memoryStore.Watch(ctx, "/seq/", WatchOption{
		Prefix:        true,
		StartRevision: scanRevision + 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainEvents(t, eventChannel, 20, 2*time.Second)
	if len(events) < 20 {
		t.Fatalf("expected 20 events, got %d", len(events))
	}

	var lastRevision int64
	for eventIndex, event := range events {
		if event.Fact.Revision <= lastRevision {
			t.Fatalf("event %d: revision %d is not greater than previous %d",
				eventIndex, event.Fact.Revision, lastRevision)
		}
		lastRevision = event.Fact.Revision
	}
}

// TestWatchFromRevisionIncludesDeletes verifies that revision-based replay
// includes delete events, not just puts.
func TestWatchFromRevisionIncludesDeletes(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	memoryStore.Put(ctx, "/ephemeral/a", []byte("v1"))
	memoryStore.Put(ctx, "/ephemeral/b", []byte("v2"))

	scanResult, _ := memoryStore.ScanWithRevision(ctx, "/ephemeral/")
	scanRevision := scanResult.Revision

	// Delete one key after the scan.
	memoryStore.Delete(ctx, "/ephemeral/a")
	memoryStore.Put(ctx, "/ephemeral/c", []byte("v3"))

	eventChannel, _ := memoryStore.Watch(ctx, "/ephemeral/", WatchOption{
		Prefix:        true,
		StartRevision: scanRevision + 1,
	})

	events := drainEvents(t, eventChannel, 2, 500*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 delete + 1 put), got %d", len(events))
	}

	deleteFound := false
	putFound := false
	for _, event := range events {
		if event.Type == EventDelete && event.Fact.Key == "/ephemeral/a" {
			deleteFound = true
		}
		if event.Type == EventPut && event.Fact.Key == "/ephemeral/c" {
			putFound = true
		}
	}
	if !deleteFound {
		t.Error("delete event for /ephemeral/a was not replayed")
	}
	if !putFound {
		t.Error("put event for /ephemeral/c was not replayed")
	}
}

// TestEventHistoryBufferWraparound verifies that the ring buffer correctly
// handles wraparound, evicting old entries and preserving recent ones.
func TestEventHistoryBufferWraparound(t *testing.T) {
	buffer := newEventHistoryBuffer(10)

	// Fill buffer exactly.
	for revision := int64(1); revision <= 10; revision++ {
		buffer.append(revision, Event{
			Type: EventPut,
			Fact: Fact{Key: fmt.Sprintf("/key/%d", revision), Revision: revision},
		})
	}

	// All 10 entries should be available.
	events, available := buffer.eventsFromRevision(1)
	if !available {
		t.Fatal("expected events from revision 1 to be available")
	}
	if len(events) != 10 {
		t.Fatalf("expected 10 events, got %d", len(events))
	}

	// Add 5 more — oldest 5 should be evicted.
	for revision := int64(11); revision <= 15; revision++ {
		buffer.append(revision, Event{
			Type: EventPut,
			Fact: Fact{Key: fmt.Sprintf("/key/%d", revision), Revision: revision},
		})
	}

	// Revision 1 through 5 should be compacted.
	_, available = buffer.eventsFromRevision(1)
	if available {
		t.Fatal("revision 1 should be compacted after wraparound")
	}
	_, available = buffer.eventsFromRevision(5)
	if available {
		t.Fatal("revision 5 should be compacted after wraparound")
	}

	// Revision 6 onward should still be available.
	events, available = buffer.eventsFromRevision(6)
	if !available {
		t.Fatal("revision 6 should still be available")
	}
	if len(events) != 10 {
		t.Fatalf("expected 10 events (revisions 6-15), got %d", len(events))
	}
	if events[0].Fact.Revision != 6 {
		t.Fatalf("first event should be revision 6, got %d", events[0].Fact.Revision)
	}
	if events[9].Fact.Revision != 15 {
		t.Fatalf("last event should be revision 15, got %d", events[9].Fact.Revision)
	}
}

// TestEventHistoryBufferEmpty verifies that querying an empty buffer
// returns no events and reports available.
func TestEventHistoryBufferEmpty(t *testing.T) {
	buffer := newEventHistoryBuffer(10)

	events, available := buffer.eventsFromRevision(1)
	if !available {
		t.Fatal("empty buffer should report available (no events to miss)")
	}
	if len(events) != 0 {
		t.Fatalf("empty buffer should return 0 events, got %d", len(events))
	}
}

// TestWatchFromFutureRevisionReturnsEmpty verifies that requesting a start
// revision beyond the current revision returns no replayed events but
// still delivers future live events.
func TestWatchFromFutureRevisionReturnsEmpty(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	memoryStore.Put(ctx, "/key/a", []byte("v1"))
	currentRevision, _ := memoryStore.Revision(ctx)

	// Request a revision far in the future.
	eventChannel, err := memoryStore.Watch(ctx, "/key/", WatchOption{
		Prefix:        true,
		StartRevision: currentRevision + 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	// No replay events should arrive.
	select {
	case event := <-eventChannel:
		if event.Type != EventPut {
			t.Fatalf("unexpected event type from future revision watch: %d", event.Type)
		}
		t.Fatalf("expected no replay events for future revision, got %+v", event)
	case <-time.After(100 * time.Millisecond):
	}

	// Live events should still work.
	memoryStore.Put(ctx, "/key/b", []byte("v2"))
	liveEvents := drainEvents(t, eventChannel, 1, 500*time.Millisecond)
	if len(liveEvents) != 1 {
		t.Fatalf("expected 1 live event, got %d", len(liveEvents))
	}
}

// TestScanThenWatchNoGap is the canonical "gap-free observation" test.
// It simulates a controller that scans state, sets up a watch from the
// scan revision, and verifies that the combination of scan snapshot +
// watch events covers every mutation without gaps or duplicates.
func TestScanThenWatchNoGap(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	// Create initial state.
	for iteration := 0; iteration < 10; iteration++ {
		memoryStore.Put(ctx, fmt.Sprintf("/state/key-%d", iteration), []byte(fmt.Sprintf("initial-%d", iteration)))
	}

	// Step 1: Scan (the "reconciliation snapshot").
	scanResult, err := memoryStore.ScanWithRevision(ctx, "/state/")
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Some writes happen in the gap.
	memoryStore.Put(ctx, "/state/key-0", []byte("updated-0"))
	memoryStore.Put(ctx, "/state/key-5", []byte("updated-5"))
	memoryStore.Delete(ctx, "/state/key-9")
	memoryStore.Put(ctx, "/state/key-10", []byte("new-10"))

	// Step 3: Start watch from scan revision + 1.
	eventChannel, err := memoryStore.Watch(ctx, "/state/", WatchOption{
		Prefix:        true,
		StartRevision: scanResult.Revision + 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 4: More writes after watch is established.
	memoryStore.Put(ctx, "/state/key-1", []byte("updated-1"))

	// Step 5: Collect all events (gap + live).
	allEvents := drainEvents(t, eventChannel, 5, 2*time.Second)
	if len(allEvents) != 5 {
		t.Fatalf("expected 5 total events (4 gap + 1 live), got %d", len(allEvents))
	}

	// Step 6: Reconstruct state from scan + events.
	reconstructed := make(map[string]string)
	for _, fact := range scanResult.Facts {
		reconstructed[fact.Key] = string(fact.Value)
	}
	for _, event := range allEvents {
		if event.Type == EventPut {
			reconstructed[event.Fact.Key] = string(event.Fact.Value)
		} else if event.Type == EventDelete {
			delete(reconstructed, event.Fact.Key)
		}
	}

	// Step 7: Verify reconstructed state matches the live store.
	actualFacts, _ := memoryStore.Scan(ctx, "/state/")
	actualState := make(map[string]string)
	for _, fact := range actualFacts {
		actualState[fact.Key] = string(fact.Value)
	}

	if len(reconstructed) != len(actualState) {
		t.Fatalf("state size mismatch: reconstructed=%d, actual=%d", len(reconstructed), len(actualState))
	}
	for key, expectedValue := range actualState {
		if reconstructed[key] != expectedValue {
			t.Errorf("key %s: reconstructed=%q, actual=%q", key, reconstructed[key], expectedValue)
		}
	}
}

// TestWatchFromRevisionWithTransaction verifies that events from
// transactional writes are captured in the history and replayed correctly.
func TestWatchFromRevisionWithTransaction(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	memoryStore.Put(ctx, "/txn/counter", []byte("0"))
	scanResult, _ := memoryStore.ScanWithRevision(ctx, "/txn/")
	scanRevision := scanResult.Revision
	counterFact, _ := memoryStore.Get(ctx, "/txn/counter")

	// Transactional write after scan.
	memoryStore.Transaction(ctx,
		[]Compare{{Key: "/txn/counter", Revision: counterFact.Revision}},
		[]Op{
			{Type: OpPut, Key: "/txn/counter", Value: []byte("1")},
			{Type: OpPut, Key: "/txn/result", Value: []byte("success")},
		},
		nil,
	)

	// Watch from scan — should see both transaction writes.
	eventChannel, _ := memoryStore.Watch(ctx, "/txn/", WatchOption{
		Prefix:        true,
		StartRevision: scanRevision + 1,
	})

	events := drainEvents(t, eventChannel, 2, 500*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected 2 transaction events, got %d", len(events))
	}

	// Both events should share the same revision (atomic transaction).
	if events[0].Fact.Revision != events[1].Fact.Revision {
		t.Errorf("transaction events should share revision: %d vs %d",
			events[0].Fact.Revision, events[1].Fact.Revision)
	}
}

// TestEventTypeStringCoverage verifies all event types are distinguishable.
func TestEventTypeStringCoverage(t *testing.T) {
	types := []EventType{EventPut, EventDelete, EventOverflow, EventCompacted}
	seen := make(map[EventType]bool)
	for _, eventType := range types {
		if seen[eventType] {
			t.Errorf("duplicate event type value: %d", eventType)
		}
		seen[eventType] = true
	}
}

// TestWatchFromRevisionPrefixFiltering verifies that historical replay
// correctly filters by prefix, not delivering events for non-matching keys.
func TestWatchFromRevisionPrefixFiltering(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()

	memoryStore.Put(ctx, "/alpha/a", []byte("1"))
	scanResult, _ := memoryStore.ScanWithRevision(ctx, "/")
	scanRevision := scanResult.Revision

	// Write to multiple prefixes.
	memoryStore.Put(ctx, "/alpha/b", []byte("2"))
	memoryStore.Put(ctx, "/beta/c", []byte("3"))
	memoryStore.Put(ctx, "/alpha/d", []byte("4"))
	memoryStore.Put(ctx, "/gamma/e", []byte("5"))

	// Watch only /alpha/ prefix.
	eventChannel, _ := memoryStore.Watch(ctx, "/alpha/", WatchOption{
		Prefix:        true,
		StartRevision: scanRevision + 1,
	})

	events := drainEvents(t, eventChannel, 2, 500*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected 2 events for /alpha/ prefix, got %d", len(events))
	}
	for _, event := range events {
		if !strings.HasPrefix(event.Fact.Key, "/alpha/") {
			t.Errorf("unexpected key in /alpha/ watch: %s", event.Fact.Key)
		}
	}
}

// drainEvents reads up to expectedCount events from the channel within the
// given timeout. Returns whatever was collected.
func drainEvents(t *testing.T, eventChannel <-chan Event, expectedCount int, timeout time.Duration) []Event {
	t.Helper()
	var collectedEvents []Event
	timer := time.After(timeout)

	for len(collectedEvents) < expectedCount {
		select {
		case event, open := <-eventChannel:
			if !open {
				return collectedEvents
			}
			if event.Type == EventOverflow || event.Type == EventCompacted {
				continue
			}
			collectedEvents = append(collectedEvents, event)
		case <-timer:
			return collectedEvents
		}
	}
	return collectedEvents
}
