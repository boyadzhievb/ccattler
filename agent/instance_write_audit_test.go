// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// errInjectedWriteFailure is the store error auditedInstanceStore returns for
// the write it is told to fail (an etcd timeout as the agent sees it).
var errInjectedWriteFailure = errors.New("etcd: request timed out (injected)")

// errInjectedReadFailure is the store error auditedInstanceStore returns for
// the state read it is told to fail (3i.3: told apart from a write failure).
var errInjectedReadFailure = errors.New("etcd: read timed out (injected)")

// sweepFaultKind is the kind of the one fault a sweep case injects (3g.0, 3h.1).
type sweepFaultKind string

// The sweep's fault kinds: a store error on write N, a lost compare on
// transaction N, a store error on the Nth read of an instance state.
const (
	faultWriteError     sweepFaultKind = "fail write"
	faultLostCompare    sweepFaultKind = "lose compare of write"
	faultStateReadError sweepFaultKind = "fail state read"
)

// injectedFault is the fault of one sweep case: its kind and its 1-based number.
type injectedFault struct {
	kind   sweepFaultKind // kind is what goes wrong.
	number int            // number is the write or state read it hits (0: none).
}

// instanceKeyPrefixes are the prefixes of per-instance keys the audit checks:
// observed/instance/{id}[/...], its telemetry and network/allocation/{id}.
var instanceKeyPrefixes = []string{types.ScanObservedInstances, types.ScanObservedTelemetryInstances, types.ScanNetworkAllocations}

// placementRuleException is one D4 exception to "every per-instance write
// compares placement/node/{self}/{id}": the writes it allows and why.
type placementRuleException struct {
	reason string                                                                  // reason is the D4 bullet that allows it.
	allows func(change store.Op, compared map[string]bool, instanceID string) bool // allows reports whether the exception covers the change.
}

// placementRuleExceptions are the D4 exceptions, each matched on the change
// and on the compares of its transaction (not on which function wrote it).
var placementRuleExceptions = []placementRuleException{
	{
		reason: "stopped, on the placement/instance/{id} revision the move check read (3f P2)",
		allows: func(change store.Op, compared map[string]bool, instanceID string) bool {
			return compared[types.KeyPlacementInstance(instanceID)] && change.Type == store.OpPut &&
				change.Key == types.KeyObservedInstanceState(instanceID) && string(change.Value) == string(types.InstanceStopped)
		},
	},
	{
		reason: "cleanup of an instance that left the desired set: deletes of its pull status, network allocation and " +
			"secret wait on placement/instance/{id} (3e, B2; only these keys, 3h R7-4)",
		allows: func(change store.Op, compared map[string]bool, instanceID string) bool {
			cleanupKeys := []string{types.KeyObservedInstancePullStatus(instanceID), types.KeyNetworkAllocation(instanceID),
				types.KeyObservedInstanceSecretWait(instanceID)}
			return compared[types.KeyPlacementInstance(instanceID)] && change.Type == store.OpDelete && slices.Contains(cleanupKeys, change.Key)
		},
	},
	{
		reason: "the source's pending after a move, on the observed node and state revisions (D4)",
		allows: func(change store.Op, compared map[string]bool, instanceID string) bool {
			guarded := compared[types.KeyObservedInstanceNode(instanceID)] && compared[types.KeyObservedInstanceState(instanceID)]
			pendingState := change.Type == store.OpPut && change.Key == types.KeyObservedInstanceState(instanceID) &&
				string(change.Value) == string(types.InstancePending)
			readinessDelete := change.Type == store.OpDelete && change.Key == types.KeyObservedInstanceProbeState(instanceID, types.ProbeReadiness)
			return guarded && (pendingState || readinessDelete)
		},
	},
}

// committedInstanceWrite is one committed store write that changed instance keys.
type committedInstanceWrite struct {
	writeNumber int             // writeNumber is the write's 1-based position among the store's writes.
	changes     []store.Op      // changes are the committed changes of instance keys.
	compares    []store.Compare // compares are the transaction's compares (none for a plain Put or Delete).
}

// auditedInstanceStore is the agent-test store decorator of M267 3g.0: it
// injects one fault (a failed write, a lost compare, a failed state read) and
// records every committed write of an instance key, so tests check the
// placement rule on real store operations, not on source syntax.
type auditedInstanceStore struct {
	store.StateStore                          // StateStore serves every call that is not failed.
	nodeID           string                   // nodeID is the agent's node, whose placement every write must compare.
	fault            injectedFault            // fault is the one fault to inject (number 0: none).
	mutex            sync.Mutex               // mutex guards the counters below (probes write concurrently).
	writeCount       int                      // writeCount counts Put, Delete, PutWithLease and Transaction calls.
	stateReadCount   int                      // stateReadCount counts Gets of observed/instance/{id}/state.
	committedWrites  []committedInstanceWrite // committedWrites are the committed writes of instance keys.
}

// newAuditedInstanceStore wraps innerStore for nodeID's agent with fault
// injected; at the end of the test it fails for every committed instance write
// that breaks the rule.
func newAuditedInstanceStore(t *testing.T, innerStore store.StateStore, nodeID string, fault injectedFault) *auditedInstanceStore {
	t.Helper()
	auditedStore := &auditedInstanceStore{StateStore: innerStore, nodeID: nodeID, fault: fault}
	t.Cleanup(func() {
		for _, violation := range auditedStore.placementRuleViolations() {
			t.Errorf("audit (%s): %s", nodeID, violation)
		}
	})
	return auditedStore
}

// setupAuditedTestAgent is setupProbeTestAgent with the agent's writes
// audited (newAuditedInstanceStore); the memory store is closed by the test.
func setupAuditedTestAgent(t *testing.T, nodeID string) (*store.MemoryStore, *runtime.SimulatorRuntime, *Agent) {
	t.Helper()
	memoryStore, simulatorRuntime := store.NewMemoryStore(), runtime.NewSimulatorRuntime()
	t.Cleanup(func() { memoryStore.Close() })
	return memoryStore, simulatorRuntime, New(nodeID, newAuditedInstanceStore(t, memoryStore, nodeID, injectedFault{}), simulatorRuntime)
}

// startWrite counts a write and reports whether it is the one to fail.
func (auditedStore *auditedInstanceStore) startWrite() (int, bool) {
	auditedStore.mutex.Lock()
	defer auditedStore.mutex.Unlock()
	auditedStore.writeCount++
	return auditedStore.writeCount, auditedStore.fault.kind == faultWriteError && auditedStore.writeCount == auditedStore.fault.number
}

// Get fails the chosen read of an instance state (3h.1), else delegates.
func (auditedStore *auditedInstanceStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	if instanceID, isInstanceKey := instanceIDOfKey(key); isInstanceKey && key == types.KeyObservedInstanceState(instanceID) {
		auditedStore.mutex.Lock()
		auditedStore.stateReadCount++
		mustFail := auditedStore.fault.kind == faultStateReadError && auditedStore.stateReadCount == auditedStore.fault.number
		auditedStore.mutex.Unlock()
		if mustFail {
			return nil, errInjectedReadFailure
		}
	}
	return auditedStore.StateStore.Get(ctx, key)
}

// recordCommitted keeps the instance-key changes of a committed write.
func (auditedStore *auditedInstanceStore) recordCommitted(writeNumber int, compares []store.Compare, changes []store.Op) {
	var instanceChanges []store.Op
	for _, change := range changes {
		if _, isInstanceKey := instanceIDOfKey(change.Key); isInstanceKey {
			instanceChanges = append(instanceChanges, change)
		}
	}
	if len(instanceChanges) == 0 {
		return
	}
	auditedStore.mutex.Lock()
	defer auditedStore.mutex.Unlock()
	auditedStore.committedWrites = append(auditedStore.committedWrites,
		committedInstanceWrite{writeNumber: writeNumber, changes: instanceChanges, compares: slices.Clone(compares)})
}

// Put fails when it is the chosen write, else delegates and records.
func (auditedStore *auditedInstanceStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	writeNumber, mustFail := auditedStore.startWrite()
	if mustFail {
		return 0, errInjectedWriteFailure
	}
	revision, putError := auditedStore.StateStore.Put(ctx, key, value)
	if putError == nil {
		auditedStore.recordCommitted(writeNumber, nil, []store.Op{{Type: store.OpPut, Key: key, Value: value}})
	}
	return revision, putError
}

// PutWithLease fails when it is the chosen write, else delegates and records.
func (auditedStore *auditedInstanceStore) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (int64, error) {
	writeNumber, mustFail := auditedStore.startWrite()
	if mustFail {
		return 0, errInjectedWriteFailure
	}
	revision, putError := auditedStore.StateStore.PutWithLease(ctx, key, value, leaseID)
	if putError == nil {
		auditedStore.recordCommitted(writeNumber, nil, []store.Op{{Type: store.OpPut, Key: key, Value: value}})
	}
	return revision, putError
}

// Delete fails when it is the chosen write, else delegates and records.
func (auditedStore *auditedInstanceStore) Delete(ctx context.Context, key string) error {
	writeNumber, mustFail := auditedStore.startWrite()
	if mustFail {
		return errInjectedWriteFailure
	}
	deleteError := auditedStore.StateStore.Delete(ctx, key)
	if deleteError == nil {
		auditedStore.recordCommitted(writeNumber, nil, []store.Op{{Type: store.OpDelete, Key: key}})
	}
	return deleteError
}

// Transaction fails when it is the chosen write, else delegates and records
// the branch that committed.
func (auditedStore *auditedInstanceStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	writeNumber, mustFail := auditedStore.startWrite()
	if mustFail {
		return false, errInjectedWriteFailure
	}
	if auditedStore.fault.kind == faultLostCompare && writeNumber == auditedStore.fault.number {
		return false, nil // another writer changed a compared key first; nothing applied
	}
	swapped, transactionError := auditedStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
	switch {
	case transactionError != nil:
	case swapped:
		auditedStore.recordCommitted(writeNumber, compares, onSuccess)
	default:
		auditedStore.recordCommitted(writeNumber, compares, onFailure)
	}
	return swapped, transactionError
}

// writesCommitted returns how many writes the store saw and the committed
// instance writes, for the sweep.
func (auditedStore *auditedInstanceStore) writesCommitted() (int, []committedInstanceWrite) {
	auditedStore.mutex.Lock()
	defer auditedStore.mutex.Unlock()
	return auditedStore.writeCount, slices.Clone(auditedStore.committedWrites)
}

// faultCounts returns how many of each fault's targets a run without a fault
// saw: writes for a failed write or a lost compare, state reads for a read error.
func (auditedStore *auditedInstanceStore) faultCounts() map[sweepFaultKind]int {
	auditedStore.mutex.Lock()
	defer auditedStore.mutex.Unlock()
	return map[sweepFaultKind]int{faultWriteError: auditedStore.writeCount, faultLostCompare: auditedStore.writeCount,
		faultStateReadError: auditedStore.stateReadCount}
}

// placementRuleViolations returns one line per committed instance change whose
// write neither compared placement/node/{self}/{id} nor is a D4 exception.
func (auditedStore *auditedInstanceStore) placementRuleViolations() []string {
	_, committedWrites := auditedStore.writesCommitted()
	var violations []string
	for _, committedWrite := range committedWrites {
		compared := make(map[string]bool, len(committedWrite.compares))
		for _, compare := range committedWrite.compares {
			compared[compare.Key] = true
		}
		for _, change := range committedWrite.changes {
			instanceID, _ := instanceIDOfKey(change.Key)
			if compared[types.KeyPlacementNodeInstance(auditedStore.nodeID, instanceID)] ||
				slices.ContainsFunc(placementRuleExceptions, func(exception placementRuleException) bool {
					return exception.allows(change, compared, instanceID)
				}) {
				continue
			}
			changeKind := map[store.OpType]string{store.OpPut: "put", store.OpDelete: "delete"}[change.Type]
			violations = append(violations, fmt.Sprintf("write %d committed %s %s without comparing %s (compares %v)",
				committedWrite.writeNumber, changeKind, change.Key, types.KeyPlacementNodeInstance(auditedStore.nodeID, instanceID),
				slices.Sorted(maps.Keys(compared))))
		}
	}
	return violations
}

// instanceIDOfKey returns the instance ID of a per-instance key
// (instanceKeyPrefixes), false for any other key.
func instanceIDOfKey(key string) (string, bool) {
	for _, prefix := range instanceKeyPrefixes {
		if rest, hasPrefix := strings.CutPrefix(key, prefix); hasPrefix && rest != "" {
			instanceID, _, _ := strings.Cut(rest, "/")
			return instanceID, true
		}
	}
	return "", false
}

// TestInstanceWriteAuditDetectsUnguardedWrite is the audit's control: a
// test-only unguarded Put, an unguarded transaction and a state delete under the
// cleanup compare (3h R7-4) are reported; a guarded write and a cleanup are not.
func TestInstanceWriteAuditDetectsUnguardedWrite(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	auditedStore := &auditedInstanceStore{StateStore: memoryStore, nodeID: "node-1"}
	setupInstanceInStore(ctx, memoryStore, "web", "web-1", "node-1", "127.0.0.1")
	placementFact, getError := memoryStore.Get(ctx, types.KeyPlacementNodeInstance("node-1", "web-1"))
	if getError != nil {
		t.Fatal(getError)
	}

	_, _ = auditedStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte("running"))
	_, _ = auditedStore.Transaction(ctx, nil, []store.Op{{Type: store.OpPut, Key: types.KeyNetworkAllocation("web-1"), Value: []byte("10.0.0.1")}}, nil)
	_, _ = auditedStore.Transaction(ctx, []store.Compare{{Key: types.KeyPlacementNodeInstance("node-1", "web-1"), Revision: placementFact.Revision}},
		[]store.Op{{Type: store.OpPut, Key: types.KeyObservedInstanceImage("web-1"), Value: []byte("nginx")}}, nil)
	_, _ = auditedStore.Put(ctx, types.KeyObservedNodeAddress("node-1"), []byte("127.0.0.1"))
	instancePlacementFact, getError := memoryStore.Get(ctx, types.KeyPlacementInstance("web-1"))
	if getError != nil {
		t.Fatal(getError)
	}
	cleanupCompare := []store.Compare{{Key: types.KeyPlacementInstance("web-1"), Revision: instancePlacementFact.Revision}}
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstancePullStatus("web-1"), []byte("pulling"))
	_, _ = auditedStore.Transaction(ctx, cleanupCompare, []store.Op{{Type: store.OpDelete, Key: types.KeyObservedInstancePullStatus("web-1")}}, nil)
	_, _ = auditedStore.Transaction(ctx, cleanupCompare, []store.Op{{Type: store.OpDelete, Key: types.KeyObservedInstanceState("web-1")}}, nil)

	violations := auditedStore.placementRuleViolations()
	if len(violations) != 3 {
		t.Fatalf("audit reported %d violations, want 3 (the unguarded Put and transaction, the state delete under the cleanup compare): %v",
			len(violations), violations)
	}
}

// publishSweepScenario is one publish path the sweep fails write by write.
type publishSweepScenario struct {
	name      string                                                                                       // name is the path.
	nodeID    string                                                                                       // nodeID is the publishing agent's node.
	portCount int                                                                                          // portCount is how many host ports the runtime maps.
	prepare   func(ctx context.Context, memoryStore *store.MemoryStore, portRuntime *manyHostPortsRuntime) // prepare sets the facts before the path.
	run       func(ctx context.Context, nodeAgent *Agent, instanceInfo placedInstanceInfo)                 // run is the path under test.
	startPath bool                                                                                         // startPath: a failed publish must keep the start facts (3f B2).
}

// sweepStalePort is a host port left by an earlier incarnation (or the source
// node of a move); the sweep runtime never maps it.
const sweepStalePort = "41000"

// runStartPath runs the start path for web-1.
func runStartPath(ctx context.Context, nodeAgent *Agent, instanceInfo placedInstanceInfo) {
	nodeAgent.startInstanceAndPublishState(ctx, instanceInfo, "nginx:1.27", "config-hash",
		runtime.Spec{ID: instanceInfo.id, ServiceName: instanceInfo.service, Image: "nginx:1.27"})
}

// prepareStaleRunningPath starts web-1's container and stores it starting on
// this node with stale host ports, as before a running-path publish.
func prepareStaleRunningPath(nodeID string) func(context.Context, *store.MemoryStore, *manyHostPortsRuntime) {
	return func(ctx context.Context, memoryStore *store.MemoryStore, portRuntime *manyHostPortsRuntime) {
		_ = portRuntime.Start(ctx, runtime.Spec{ID: "web-1", ServiceName: "web", Image: "nginx:1.27"})
		_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte(types.InstanceStarting))
		_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceNode("web-1"), []byte(nodeID))
		_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceHostPortForPort("web-1", 9000), []byte(sweepStalePort))
		_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceHostPortForPort("web-1", 9000+portRuntime.portCount-1), []byte(sweepStalePort))
	}
}

// prepareMoveTarget stores web-1 as the source node-1 published it (running,
// ready, its own host port) before node-2, the target, starts it.
func prepareMoveTarget(ctx context.Context, memoryStore *store.MemoryStore, portRuntime *manyHostPortsRuntime) {
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte(types.InstanceRunning))
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceNode("web-1"), []byte("node-1"))
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness), []byte(types.ReadinessProbeReady))
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceHostPortForPort("web-1", 9000), []byte(sweepStalePort))
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceHostPortForPort("web-1", 9000+portRuntime.portCount-1), []byte(sweepStalePort))
}

// publishSweepScenarios are the start path, the running path and a move
// target, each with few host ports and with more than one transaction holds.
func publishSweepScenarios() []publishSweepScenario {
	noFacts := func(context.Context, *store.MemoryStore, *manyHostPortsRuntime) {}
	runRunningPath := func(ctx context.Context, nodeAgent *Agent, instanceInfo placedInstanceInfo) {
		nodeAgent.publishRunningInstance(ctx, instanceInfo, "")
	}
	var scenarios []publishSweepScenario
	for _, portCount := range []int{2, manyHostPortsTestPortCount} {
		scenarios = append(scenarios,
			publishSweepScenario{name: "start path", nodeID: "node-1", portCount: portCount, prepare: noFacts, run: runStartPath, startPath: true},
			publishSweepScenario{name: "running path", nodeID: "node-1", portCount: portCount, prepare: prepareStaleRunningPath("node-1"), run: runRunningPath},
			publishSweepScenario{name: "move target", nodeID: "node-2", portCount: portCount, prepare: prepareMoveTarget, run: runStartPath, startPath: true},
		)
	}
	return scenarios
}

// runPublishSweepCase runs scenario with fault injected and returns the
// store, the agent, the audited store and the facts before the path.
func runPublishSweepCase(t *testing.T, scenario publishSweepScenario, fault injectedFault) (*store.MemoryStore, *Agent, *auditedInstanceStore, map[string]string) {
	t.Helper()
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	t.Cleanup(func() { memoryStore.Close() })
	portRuntime := &manyHostPortsRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime(), portCount: scenario.portCount}
	auditedStore := newAuditedInstanceStore(t, memoryStore, scenario.nodeID, fault)
	nodeAgent := New(scenario.nodeID, auditedStore, portRuntime)
	nodeAgent.SetNetworkProvider(network.NewSimulatorNetworkProvider())
	setupInstanceInStore(ctx, memoryStore, "web", "web-1", scenario.nodeID, "127.0.0.1")
	scenario.prepare(ctx, memoryStore, portRuntime)
	instanceInfo := scannedInstanceInfo(t, ctx, nodeAgent, "web-1")
	factsBefore := snapshotInstanceFacts(t, ctx, memoryStore, "web-1")
	scenario.run(ctx, nodeAgent, instanceInfo)
	return memoryStore, nodeAgent, auditedStore, factsBefore
}

// checkPublishRule checks the rule after one sweep case: running on node N
// means every host port stored is N's (this node: the runtime's mapping,
// another node: unchanged, 3h.1); a final publish that did not commit left the
// facts as they were, apart from an intermediate starting publish (3i.2).
func checkPublishRule(t *testing.T, scenario publishSweepScenario, memoryStore *store.MemoryStore, nodeAgent *Agent,
	auditedStore *auditedInstanceStore, factsBefore map[string]string) {
	t.Helper()
	ctx := context.Background()
	factsAfter := snapshotInstanceFacts(t, ctx, memoryStore, "web-1")
	stateKey := types.KeyObservedInstanceState("web-1")
	if factsAfter[stateKey] == string(types.InstanceRunning) && factsAfter[types.KeyObservedInstanceNode("web-1")] == scenario.nodeID {
		var wrongHostPorts []string
		for portIndex := 0; portIndex < scenario.portCount; portIndex++ {
			hostPortKey := types.KeyObservedInstanceHostPortForPort("web-1", 9000+portIndex)
			if wantHostPort := strconv.Itoa(30000 + portIndex); factsAfter[hostPortKey] != wantHostPort {
				wrongHostPorts = append(wrongHostPorts, fmt.Sprintf("%d=%q (want %s)", 9000+portIndex, factsAfter[hostPortKey], wantHostPort))
			}
		}
		if len(wrongHostPorts) > 0 {
			t.Errorf("running on %s with %d of %d host ports wrong, first %s", scenario.nodeID, len(wrongHostPorts), scenario.portCount, wrongHostPorts[0])
		}
	}
	if publishedNode := factsAfter[types.KeyObservedInstanceNode("web-1")]; factsAfter[stateKey] == string(types.InstanceRunning) && publishedNode != scenario.nodeID {
		checkHostPortsUnchanged(t, publishedNode, factsBefore, factsAfter)
	}
	finalPublished, startingPublish := committedStatePublishes(auditedStore)
	if finalPublished {
		return
	}
	startingKeys := make(map[string]bool) // keys the intermediate starting publish may write (3i.2)
	if startingPublish != nil {
		checkStartingPublish(t, scenario, nodeAgent, factsAfter)
		startingKeys = startingPublishKeys("web-1")
	}
	var changedFacts []string
	for _, key := range slices.Sorted(maps.Keys(factsAfter)) {
		ownHostPort := startingPublish != nil && isRuntimeHostPortFact(key, factsAfter[key])
		if factsAfter[key] != factsBefore[key] && !startingKeys[key] && !ownHostPort {
			changedFacts = append(changedFacts, fmt.Sprintf("%s %q -> %q", key, factsBefore[key], factsAfter[key]))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(factsBefore)) {
		if _, stillThere := factsAfter[key]; !stillThere && !startingKeys[key] {
			changedFacts = append(changedFacts, key+" deleted")
		}
	}
	if len(changedFacts) > 0 {
		t.Errorf("final state publish did not commit, yet %d facts changed beyond the starting publish, first %s", len(changedFacts), changedFacts[0])
	}
	if scenario.startPath && len(nodeAgent.uncommittedStartFacts["web-1"]) == 0 {
		t.Errorf("final state publish did not commit and the start facts were not kept for the next cycle")
	}
}

// committedStatePublishes reports whether a final state publish (any state
// but starting) committed, and the intermediate starting publish of an
// overflow (3h R7-1) if it committed.
func committedStatePublishes(auditedStore *auditedInstanceStore) (bool, *committedInstanceWrite) {
	_, committedWrites := auditedStore.writesCommitted()
	finalPublished := false
	var startingPublish *committedInstanceWrite
	for writeIndex, committedWrite := range committedWrites {
		for _, change := range committedWrite.changes {
			if change.Type != store.OpPut || change.Key != types.KeyObservedInstanceState("web-1") {
				continue
			}
			if string(change.Value) == string(types.InstanceStarting) {
				startingPublish = &committedWrites[writeIndex]
			} else {
				finalPublished = true
			}
		}
	}
	return finalPublished, startingPublish
}

// startingPublishKeys are the keys the intermediate starting publish may
// change (3h R7-1, 3i R8-1): state, node, the identity facts and readiness.
func startingPublishKeys(instanceID string) map[string]bool {
	return map[string]bool{
		types.KeyObservedInstanceState(instanceID): true, types.KeyObservedInstanceNode(instanceID): true,
		types.KeyObservedInstance(instanceID): true, types.KeyObservedInstanceService(instanceID): true,
		types.KeyObservedInstanceIP(instanceID): true, types.KeyNetworkAllocation(instanceID): true,
		types.KeyObservedInstanceProbeState(instanceID, types.ProbeReadiness): true,
	}
}

// checkStartingPublish checks the facts after an intermediate starting publish
// whose final publish did not commit (3i.1): starting on this node with this
// node's IP and allocation, no readiness, the start facts kept.
func checkStartingPublish(t *testing.T, scenario publishSweepScenario, nodeAgent *Agent, factsAfter map[string]string) {
	t.Helper()
	nodeIP, allocateError := nodeAgent.networkProvider.AllocateIP(context.Background(), scenario.nodeID, "web-1")
	if allocateError != nil {
		t.Fatal(allocateError)
	}
	gotState, gotNode := factsAfter[types.KeyObservedInstanceState("web-1")], factsAfter[types.KeyObservedInstanceNode("web-1")]
	gotIP, gotAllocation := factsAfter[types.KeyObservedInstanceIP("web-1")], factsAfter[types.KeyNetworkAllocation("web-1")]
	readiness, hasReadiness := factsAfter[types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness)]
	if gotState != string(types.InstanceStarting) || gotNode != scenario.nodeID || gotIP != nodeIP || gotAllocation != nodeIP || hasReadiness {
		t.Errorf("after the starting publish: state=%q node=%q ip=%q allocation=%q readiness=%q (present %v), want starting on %s with ip %s, no readiness",
			gotState, gotNode, gotIP, gotAllocation, readiness, hasReadiness, scenario.nodeID, nodeIP)
	}
	if scenario.startPath && len(nodeAgent.uncommittedStartFacts["web-1"]) == 0 {
		t.Errorf("only the starting publish committed and the start facts were not kept for the next cycle")
	}
}

// isRuntimeHostPortFact reports whether key = value is a host port of this
// node's runtime mapping: overflow ports written after the starting publish.
func isRuntimeHostPortFact(key string, value string) bool {
	portText, isHostPort := strings.CutPrefix(key, types.KeyObservedInstanceHostPort("web-1")+"/")
	containerPort, parseError := strconv.Atoi(portText)
	return isHostPort && parseError == nil && value == strconv.Itoa(containerPort-9000+30000)
}

// checkHostPortsUnchanged fails when a host port changed while the instance
// stays running on publishedNode, another node: those ports must stay its own.
func checkHostPortsUnchanged(t *testing.T, publishedNode string, factsBefore map[string]string, factsAfter map[string]string) {
	t.Helper()
	hostPortPrefix := types.KeyObservedInstanceHostPort("web-1") + "/"
	var changedHostPorts []string
	for _, key := range slices.Sorted(maps.Keys(factsAfter)) {
		if strings.HasPrefix(key, hostPortPrefix) && factsAfter[key] != factsBefore[key] {
			changedHostPorts = append(changedHostPorts, fmt.Sprintf("%s %q -> %q", key, factsBefore[key], factsAfter[key]))
		}
	}
	if len(changedHostPorts) > 0 {
		t.Errorf("running on %s, yet %d of its host ports changed, first %s", publishedNode, len(changedHostPorts), changedHostPorts[0])
	}
}

// TestPublishRuleHoldsWhenAnyWriteFails: M267 3g.0, 3h.1. Each publish path
// runs once per store write (failed, then with its compare lost) and once per
// state read (failed); after each run the state, node and host ports agree, a
// failed publish changes nothing and the audit finds every write guarded.
func TestPublishRuleHoldsWhenAnyWriteFails(t *testing.T) {
	for _, scenario := range publishSweepScenarios() {
		scenarioName := fmt.Sprintf("%s, %d host ports", scenario.name, scenario.portCount)
		t.Run(scenarioName, func(t *testing.T) {
			memoryStore, nodeAgent, auditedStore, factsBefore := runPublishSweepCase(t, scenario, injectedFault{})
			faultCounts := auditedStore.faultCounts()
			if gotState := readFactValue(context.Background(), memoryStore, types.KeyObservedInstanceState("web-1")); gotState != string(types.InstanceRunning) {
				t.Fatalf("without a failure the path published %q, want running; the sweep proves nothing", gotState)
			}
			checkPublishRule(t, scenario, memoryStore, nodeAgent, auditedStore, factsBefore)
			for _, faultKind := range []sweepFaultKind{faultWriteError, faultLostCompare, faultStateReadError} {
				for faultNumber := 1; faultNumber <= faultCounts[faultKind]; faultNumber++ {
					fault := injectedFault{kind: faultKind, number: faultNumber}
					t.Run(string(faultKind)+" "+strconv.Itoa(faultNumber), func(t *testing.T) {
						memoryStore, nodeAgent, auditedStore, factsBefore := runPublishSweepCase(t, scenario, fault)
						checkPublishRule(t, scenario, memoryStore, nodeAgent, auditedStore, factsBefore)
					})
				}
			}
		})
	}
}
