package agent

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Agent is the node agent. It runs on each machine and bridges store <-> runtime.
//
// Three internal components:
//   - Observer: reads runtime state to determine what is actually running
//   - Reconciler: compares desired state (placements) with observed, calls runtime.Start/Stop
//   - Reporter: publishes actual state back to the store
type Agent struct {
	nodeID          string                   // nodeID is the unique identifier for the node this agent manages.
	store           store.StateStore         // store is the fact store used to read desired state and write observed state.
	runtime         runtime.Runtime          // runtime is the pluggable container/process runtime adapter.
	networkProvider network.NetworkProvider   // networkProvider allocates IPs for instances; nil means legacy 127.0.0.1 behavior.
	interval        time.Duration            // interval is the period between periodic reconciliation cycles.
}

// New creates a new Agent for the given node, wired to the provided state store
// and runtime adapter. The default reconciliation interval is 1 second. The
// network provider is nil by default, meaning instances get 127.0.0.1 as their
// IP. Use SetNetworkProvider to enable real IP allocation.
func New(nodeID string, stateStore store.StateStore, runtimeAdapter runtime.Runtime) *Agent {
	return &Agent{
		nodeID:   nodeID,
		store:    stateStore,
		runtime:  runtimeAdapter,
		interval: 1 * time.Second,
	}
}

// SetNetworkProvider configures the agent to use the given network provider for
// instance IP allocation. When set, instances receive unique IPs from the
// node's subnet instead of the default 127.0.0.1.
func (nodeAgent *Agent) SetNetworkProvider(networkProvider network.NetworkProvider) {
	nodeAgent.networkProvider = networkProvider
}

// SetInterval overrides the default periodic reconciliation interval.
// This is typically used in tests to speed up convergence.
func (a *Agent) SetInterval(d time.Duration) {
	a.interval = d
}

// Run starts the agent loop. It blocks until ctx is cancelled.
//
// On startup it registers the node as alive in the store, performs an initial
// reconciliation, then enters a loop that reconciles on every tick of the
// interval timer and on every placement-key change observed via the store watch.
func (a *Agent) Run(ctx context.Context) error {
	// Register this node and write initial heartbeat.
	a.store.Put(ctx, types.KeyObservedNode(a.nodeID), []byte(""))
	a.store.Put(ctx, types.KeyObservedNodeState(a.nodeID), []byte(string(types.NodeAlive)))
	a.writeHeartbeat(ctx)

	// Initial reconcile.
	if err := a.executeReconciliationCycle(ctx); err != nil {
		log.Printf("agent %s: initial reconcile error: %v", a.nodeID, err)
	}

	// Watch for placement changes and reconcile periodically.
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	placementCh, err := a.store.Watch(ctx, types.ScanPlacements, store.WatchOption{Prefix: true})
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			a.writeHeartbeat(ctx)
			if err := a.executeReconciliationCycle(ctx); err != nil {
				log.Printf("agent %s: reconcile error: %v", a.nodeID, err)
			}
		case _, ok := <-placementCh:
			if !ok {
				return nil
			}
			if err := a.executeReconciliationCycle(ctx); err != nil {
				log.Printf("agent %s: reconcile error: %v", a.nodeID, err)
			}
		}
	}
}

// executeReconciliationCycle performs a single reconciliation pass: it compares
// the set of instances placed on this node (desired) with what the runtime
// reports as actually running, starts missing instances, stops stale ones,
// and reports health for every desired instance that has a health probe.
func (a *Agent) executeReconciliationCycle(ctx context.Context) error {
	// Find instances placed on this node.
	desired, err := a.findInstancesPlacedOnThisNode(ctx)
	if err != nil {
		return err
	}

	// Get current runtime state.
	running, err := a.runtime.List(ctx)
	if err != nil {
		return err
	}
	runningByID := make(map[string]runtime.Status)
	for _, runtimeStatus := range running {
		runningByID[runtimeStatus.ID] = runtimeStatus
	}

	// Start instances that should be running but aren't.
	for _, instanceInfo := range desired {
		runtimeStatus, exists := runningByID[instanceInfo.id]

		if !exists || !runtimeStatus.Running {
			image := a.lookupServiceImageFromStore(ctx, instanceInfo.service)
			if image == "" {
				continue
			}
			if err := a.runtime.Start(ctx, runtime.Spec{
				ID:    instanceInfo.id,
				Image: image,
			}); err != nil {
				log.Printf("agent %s: failed to start %s: %v", a.nodeID, instanceInfo.id, err)
				a.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceFailed)
				continue
			}
			a.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceRunning)
		} else {
			a.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceRunning)
		}

		a.performHealthCheckAndReportResult(ctx, instanceInfo)
		delete(runningByID, instanceInfo.id)
	}

	// Stop processes that shouldn't be running (no longer placed here).
	for id, runtimeStatus := range runningByID {
		if runtimeStatus.Running {
			a.runtime.Stop(ctx, id)
			if a.networkProvider != nil {
				a.networkProvider.ReleaseIP(ctx, a.nodeID, id)
				types.DeleteNetworkAllocation(ctx, a.store, id)
			}
		}
	}

	return nil
}

// placedInstanceInfo holds the identity of an instance that the scheduler has
// placed on this node. It is used internally during reconciliation to pair
// each instance with its owning service.
type placedInstanceInfo struct {
	id      string // id is the unique instance identifier (e.g. "aaa").
	service string // service is the name of the service this instance belongs to (e.g. "web").
}

// findInstancesPlacedOnThisNode scans all placement facts in the store and
// returns the subset whose target node matches this agent's nodeID. Instances
// that are in the "stopped" state are excluded.
func (a *Agent) findInstancesPlacedOnThisNode(ctx context.Context) ([]placedInstanceInfo, error) {
	placements, err := a.store.Scan(ctx, types.ScanPlacements)
	if err != nil {
		return nil, err
	}

	var result []placedInstanceInfo
	for _, f := range placements {
		nodeID := string(f.Value)
		if nodeID != a.nodeID {
			continue
		}
		instanceID := strings.TrimPrefix(f.Key, types.ScanPlacements)

		// Check instance isn't stopped.
		stateF, err := a.store.Get(ctx, types.KeyObservedInstanceState(instanceID))
		if err == nil && types.InstanceState(stateF.Value) == types.InstanceStopped {
			continue
		}

		// Get the service name.
		svcF, err := a.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
		if err != nil {
			continue
		}

		result = append(result, placedInstanceInfo{
			id:      instanceID,
			service: string(svcF.Value),
		})
	}
	return result, nil
}

// lookupServiceImageFromStore retrieves the container image reference for the
// given service from the desired-state section of the store. Returns an empty
// string if the image fact is missing.
func (a *Agent) lookupServiceImageFromStore(ctx context.Context, service string) string {
	f, err := a.store.Get(ctx, types.KeyDesiredServiceImage(service))
	if err != nil {
		return ""
	}
	return string(f.Value)
}

// performHealthCheckAndReportResult looks up the health probe configuration for
// the service that owns the given instance, executes the probe against the
// instance's IP address, and writes the resulting health status (healthy or
// unhealthy) back to the store. If no health probe is configured for the
// service, this method is a no-op.
func (a *Agent) performHealthCheckAndReportResult(ctx context.Context, instanceInfo placedInstanceInfo) {
	probe, ok := a.buildHealthProbeFromServiceConfig(ctx, instanceInfo.service)
	if !ok {
		return
	}

	ip := "127.0.0.1"
	if f, err := a.store.Get(ctx, types.KeyObservedInstanceIP(instanceInfo.id)); err == nil {
		ip = string(f.Value)
	}

	healthy := CheckHealth(ctx, probe, ip)
	status := types.HealthUnknown
	if healthy {
		status = types.HealthHealthy
	} else {
		status = types.HealthUnhealthy
	}
	a.store.Put(ctx, types.KeyObservedInstanceHealth(instanceInfo.id), []byte(string(status)))
}

// buildHealthProbeFromServiceConfig reads the health check configuration for
// the named service from the store and assembles a HealthProbe. It returns
// false if the service has no health method configured, if the method is
// unrecognized, or if no exposed port can be derived.
func (a *Agent) buildHealthProbeFromServiceConfig(ctx context.Context, service string) (HealthProbe, bool) {
	methodF, err := a.store.Get(ctx, types.KeyDesiredServiceHealthMethod(service))
	if err != nil {
		return HealthProbe{}, false
	}
	method := string(methodF.Value)

	probe := HealthProbe{Timeout: 2 * time.Second}
	switch method {
	case "http":
		probe.Type = ProbeHTTP
		if f, err := a.store.Get(ctx, types.KeyDesiredServiceHealthPath(service)); err == nil {
			probe.Path = string(f.Value)
		} else {
			probe.Path = "/"
		}
	case "tcp":
		probe.Type = ProbeTCP
	default:
		return HealthProbe{}, false
	}

	// Derive port from the first expose port on the service.
	facts, err := a.store.Scan(ctx, fmt.Sprintf("%s/service/%s/expose/", types.PrefixDesired, service))
	if err == nil && len(facts) > 0 {
		portStr := facts[0].Key[strings.LastIndex(facts[0].Key, "/")+1:]
		if p, err := strconv.Atoi(portStr); err == nil {
			probe.Port = p
		}
	}

	if probe.Port == 0 {
		return HealthProbe{}, false
	}

	return probe, true
}

// writeHeartbeat writes the current Unix-millisecond timestamp to the node's
// lease key. Millisecond granularity avoids false lease expiry from second-level
// truncation. The failure detector controller reads these timestamps to
// determine whether a node is still alive.
func (a *Agent) writeHeartbeat(ctx context.Context) {
	timestampMillis := fmt.Sprintf("%d", time.Now().UnixMilli())
	a.store.Put(ctx, types.KeyLeaseNode(a.nodeID), []byte(timestampMillis))
}

// publishInstanceStateToStore writes a complete set of observed-state facts for
// the given instance: its existence marker, owning service, current state, the
// node it is running on, and its IP address. When a NetworkProvider is
// configured, the IP is allocated from the node's subnet; otherwise 127.0.0.1
// is used as a fallback.
func (nodeAgent *Agent) publishInstanceStateToStore(ctx context.Context, instanceID, service string, state types.InstanceState) {
	nodeAgent.store.Put(ctx, types.KeyObservedInstance(instanceID), []byte(""))
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte(service))
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(state)))
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceNode(instanceID), []byte(nodeAgent.nodeID))

	instanceIP := "127.0.0.1"
	if nodeAgent.networkProvider != nil {
		allocatedIP, err := nodeAgent.networkProvider.AllocateIP(ctx, nodeAgent.nodeID, instanceID)
		if err != nil {
			log.Printf("agent %s: failed to allocate IP for %s: %v", nodeAgent.nodeID, instanceID, err)
		} else {
			instanceIP = allocatedIP
			types.WriteNetworkAllocation(ctx, nodeAgent.store, instanceID, allocatedIP)
		}
	}
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceIP(instanceID), []byte(instanceIP))
}
