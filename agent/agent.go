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
	"github.com/boyadzhievb/ccattler/storage"
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
	storageProvider storage.StorageProvider   // storageProvider manages volume attach/detach; nil means no volume support.
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

// SetStorageProvider configures the agent to use the given storage provider
// for volume attach/detach operations. When set, the agent attaches volumes
// required by a service before starting its instances.
func (nodeAgent *Agent) SetStorageProvider(storageProvider storage.StorageProvider) {
	nodeAgent.storageProvider = storageProvider
}

// SetInterval overrides the default periodic reconciliation interval.
// This is typically used in tests to speed up convergence.
func (nodeAgent *Agent) SetInterval(reconciliationInterval time.Duration) {
	nodeAgent.interval = reconciliationInterval
}

// Run starts the agent loop. It blocks until ctx is cancelled.
//
// On startup it registers the node as alive in the store, performs an initial
// reconciliation, then enters a loop that reconciles on every tick of the
// interval timer and on every placement-key change observed via the store watch.
func (nodeAgent *Agent) Run(ctx context.Context) error {
	// Register this node and write initial heartbeat.
	nodeAgent.store.Put(ctx, types.KeyObservedNode(nodeAgent.nodeID), []byte(""))
	nodeAgent.store.Put(ctx, types.KeyObservedNodeState(nodeAgent.nodeID), []byte(string(types.NodeAlive)))
	nodeAgent.writeHeartbeat(ctx)

	// Initial reconcile.
	if err := nodeAgent.executeReconciliationCycle(ctx); err != nil {
		log.Printf("agent %s: initial reconcile error: %v", nodeAgent.nodeID, err)
	}

	// Watch for placement changes and reconcile periodically.
	ticker := time.NewTicker(nodeAgent.interval)
	defer ticker.Stop()

	placementCh, err := nodeAgent.store.Watch(ctx, types.ScanPlacements, store.WatchOption{Prefix: true})
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			nodeAgent.store.Put(ctx, types.KeyObservedNodeState(nodeAgent.nodeID), []byte(string(types.NodeAlive)))
			nodeAgent.writeHeartbeat(ctx)
			if err := nodeAgent.executeReconciliationCycle(ctx); err != nil {
				log.Printf("agent %s: reconcile error: %v", nodeAgent.nodeID, err)
			}
		case _, ok := <-placementCh:
			if !ok {
				return nil
			}
			if err := nodeAgent.executeReconciliationCycle(ctx); err != nil {
				log.Printf("agent %s: reconcile error: %v", nodeAgent.nodeID, err)
			}
		}
	}
}

// executeReconciliationCycle performs a single reconciliation pass: it compares
// the set of instances placed on this node (desired) with what the runtime
// reports as actually running, starts missing instances, stops stale ones,
// and reports health for every desired instance that has a health probe.
func (nodeAgent *Agent) executeReconciliationCycle(ctx context.Context) error {
	// Find instances placed on this node.
	desired, err := nodeAgent.findInstancesPlacedOnThisNode(ctx)
	if err != nil {
		return err
	}

	// Get current runtime state.
	running, err := nodeAgent.runtime.List(ctx)
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
			// Check volume readiness before starting.
			if nodeAgent.storageProvider != nil {
				volumesReady, attachErr := nodeAgent.ensureVolumesAttachedForInstance(ctx, instanceInfo)
				if attachErr != nil {
					log.Printf("agent %s: volume error for %s: %v", nodeAgent.nodeID, instanceInfo.id, attachErr)
				}
				if !volumesReady {
					delete(runningByID, instanceInfo.id)
					continue
				}
			}

			image := nodeAgent.lookupServiceImageFromStore(ctx, instanceInfo.service)
			if image == "" {
				continue
			}
			envVars := nodeAgent.resolveServiceConfigEnvVars(ctx, instanceInfo.service)
			if err := nodeAgent.runtime.Start(ctx, runtime.Spec{
				ID:    instanceInfo.id,
				Image: image,
				Env:   envVars,
			}); err != nil {
				log.Printf("agent %s: failed to start %s: %v", nodeAgent.nodeID, instanceInfo.id, err)
				nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceFailed)
				nodeAgent.store.Put(ctx, types.KeyObservedInstanceImage(instanceInfo.id), []byte(image))
				continue
			}
			nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceRunning)
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceImage(instanceInfo.id), []byte(image))
		} else {
			nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceRunning)
		}

		nodeAgent.performHealthCheckAndReportResult(ctx, instanceInfo)
		delete(runningByID, instanceInfo.id)
	}

	// Stop processes that shouldn't be running (no longer placed here).
	for instanceID, runtimeStatus := range runningByID {
		if runtimeStatus.Running {
			if nodeAgent.storageProvider != nil {
				nodeAgent.detachVolumesForInstance(ctx, instanceID)
			}
			nodeAgent.runtime.Stop(ctx, instanceID)
			if nodeAgent.networkProvider != nil {
				nodeAgent.networkProvider.ReleaseIP(ctx, nodeAgent.nodeID, instanceID)
				types.DeleteNetworkAllocation(ctx, nodeAgent.store, instanceID)
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
func (nodeAgent *Agent) findInstancesPlacedOnThisNode(ctx context.Context) ([]placedInstanceInfo, error) {
	placements, err := nodeAgent.store.Scan(ctx, types.ScanPlacements)
	if err != nil {
		return nil, err
	}

	var result []placedInstanceInfo
	for _, placementFact := range placements {
		nodeID := string(placementFact.Value)
		if nodeID != nodeAgent.nodeID {
			continue
		}
		instanceID := strings.TrimPrefix(placementFact.Key, types.ScanPlacements)

		// Check instance isn't stopped.
		stateFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceState(instanceID))
		if err == nil && types.InstanceState(stateFact.Value) == types.InstanceStopped {
			continue
		}

		// Get the service name.
		serviceFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
		if err != nil {
			continue
		}

		result = append(result, placedInstanceInfo{
			id:      instanceID,
			service: string(serviceFact.Value),
		})
	}
	return result, nil
}

// lookupServiceImageFromStore retrieves the container image reference for the
// given service from the desired-state section of the store. Returns an empty
// string if the image fact is missing.
func (nodeAgent *Agent) lookupServiceImageFromStore(ctx context.Context, service string) string {
	factEntry, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceImage(service))
	if err != nil {
		return ""
	}
	return string(factEntry.Value)
}

// performHealthCheckAndReportResult looks up the health probe configuration for
// the service that owns the given instance, executes the probe against the
// instance's IP address, and writes the resulting health status (healthy or
// unhealthy) back to the store. If no health probe is configured for the
// service, this method is a no-op.
func (nodeAgent *Agent) performHealthCheckAndReportResult(ctx context.Context, instanceInfo placedInstanceInfo) {
	probe, ok := nodeAgent.buildHealthProbeFromServiceConfig(ctx, instanceInfo.service)
	if !ok {
		return
	}

	ip := "127.0.0.1"
	if factEntry, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceIP(instanceInfo.id)); err == nil {
		ip = string(factEntry.Value)
	}

	healthy := CheckHealth(ctx, probe, ip)
	status := types.HealthUnknown
	if healthy {
		status = types.HealthHealthy
	} else {
		status = types.HealthUnhealthy
	}
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceHealth(instanceInfo.id), []byte(string(status)))
}

// buildHealthProbeFromServiceConfig reads the health check configuration for
// the named service from the store and assembles a HealthProbe. It returns
// false if the service has no health method configured, if the method is
// unrecognized, or if no exposed port can be derived.
func (nodeAgent *Agent) buildHealthProbeFromServiceConfig(ctx context.Context, service string) (HealthProbe, bool) {
	methodFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceHealthMethod(service))
	if err != nil {
		return HealthProbe{}, false
	}
	method := string(methodFact.Value)

	probe := HealthProbe{Timeout: 2 * time.Second}
	switch method {
	case "http":
		probe.Type = ProbeHTTP
		if factEntry, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceHealthPath(service)); err == nil {
			probe.Path = string(factEntry.Value)
		} else {
			probe.Path = "/"
		}
	case "tcp":
		probe.Type = ProbeTCP
	default:
		return HealthProbe{}, false
	}

	// Derive port from the first expose port on the service.
	facts, err := nodeAgent.store.Scan(ctx, fmt.Sprintf("%s/service/%s/expose/", types.PrefixDesired, service))
	if err == nil && len(facts) > 0 {
		portStr := facts[0].Key[strings.LastIndex(facts[0].Key, "/")+1:]
		if parsedPort, err := strconv.Atoi(portStr); err == nil {
			probe.Port = parsedPort
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
func (nodeAgent *Agent) writeHeartbeat(ctx context.Context) {
	timestampMillis := fmt.Sprintf("%d", time.Now().UnixMilli())
	nodeAgent.store.Put(ctx, types.KeyLeaseNode(nodeAgent.nodeID), []byte(timestampMillis))
}

// resolveServiceConfigEnvVars reads the desired config env vars for a service
// from the store and returns them as a map for the runtime spec.
func (nodeAgent *Agent) resolveServiceConfigEnvVars(ctx context.Context, serviceName string) map[string]string {
	prefix := types.ScanDesiredServiceConfig(serviceName)
	facts, err := nodeAgent.store.Scan(ctx, prefix)
	if err != nil || len(facts) == 0 {
		return nil
	}

	envVars := make(map[string]string)
	envPrefix := fmt.Sprintf("%s/service/%s/config/env/", types.PrefixDesired, serviceName)
	for _, fact := range facts {
		if strings.HasPrefix(fact.Key, envPrefix) {
			envVarName := strings.TrimPrefix(fact.Key, envPrefix)
			envVars[envVarName] = string(fact.Value)
		}
	}
	return envVars
}

// lookupServiceVolumeMountsFromStore reads the desired volume mounts for a
// service. Returns a map from volume name to mount path. Returns an empty map
// if the service has no volume mounts.
func (nodeAgent *Agent) lookupServiceVolumeMountsFromStore(ctx context.Context, serviceName string) map[string]string {
	prefix := fmt.Sprintf("%s/service/%s/volume/", types.PrefixDesired, serviceName)
	facts, err := nodeAgent.store.Scan(ctx, prefix)
	if err != nil || len(facts) == 0 {
		return nil
	}

	volumeMounts := make(map[string]string)
	for _, fact := range facts {
		volumeName := strings.TrimPrefix(fact.Key, prefix)
		mountPath := string(fact.Value)
		volumeMounts[volumeName] = mountPath
	}
	return volumeMounts
}

// ensureVolumesAttachedForInstance checks whether all volumes required by the
// instance's service are available, and attaches them to this node if so.
// Returns (true, nil) when all volumes are ready. Returns (false, nil) when a
// volume exists but is attached to a different node (the instance should wait).
// Returns (false, error) on unexpected failures.
func (nodeAgent *Agent) ensureVolumesAttachedForInstance(ctx context.Context, instanceInfo placedInstanceInfo) (bool, error) {
	volumeMounts := nodeAgent.lookupServiceVolumeMountsFromStore(ctx, instanceInfo.service)
	if len(volumeMounts) == 0 {
		return true, nil
	}

	for volumeName := range volumeMounts {
		volumeStateFact, err := nodeAgent.store.Get(ctx, types.KeyObservedVolumeState(volumeName))
		if err != nil {
			return false, nil
		}

		volumeState := types.VolumeState(volumeStateFact.Value)

		if volumeState == types.VolumeAttached {
			nodeFact, err := nodeAgent.store.Get(ctx, types.KeyObservedVolumeNode(volumeName))
			if err == nil && string(nodeFact.Value) == nodeAgent.nodeID {
				continue
			}
			return false, nil
		}

		mountPath, err := nodeAgent.storageProvider.AttachVolume(ctx, volumeName, nodeAgent.nodeID)
		if err != nil {
			return false, fmt.Errorf("attaching volume %s: %w", volumeName, err)
		}

		sizeFact, _ := nodeAgent.store.Get(ctx, types.KeyObservedVolumeSize(volumeName))
		sizeValue := ""
		if sizeFact.Value != nil {
			sizeValue = string(sizeFact.Value)
		}

		types.WriteObservedVolume(ctx, nodeAgent.store, types.Volume{
			Name:      volumeName,
			Size:      sizeValue,
			State:     types.VolumeAttached,
			Node:      nodeAgent.nodeID,
			Instance:  instanceInfo.id,
			MountPath: mountPath,
		})
	}

	return true, nil
}

// detachVolumesForInstance finds volumes attached to the given instance and
// detaches them, setting their state back to available.
func (nodeAgent *Agent) detachVolumesForInstance(ctx context.Context, instanceID string) {
	serviceFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
	if err != nil {
		return
	}
	serviceName := string(serviceFact.Value)

	volumeMounts := nodeAgent.lookupServiceVolumeMountsFromStore(ctx, serviceName)
	for volumeName := range volumeMounts {
		nodeAgent.storageProvider.DetachVolume(ctx, volumeName, nodeAgent.nodeID)

		sizeFact, _ := nodeAgent.store.Get(ctx, types.KeyObservedVolumeSize(volumeName))
		sizeValue := ""
		if sizeFact.Value != nil {
			sizeValue = string(sizeFact.Value)
		}

		types.WriteObservedVolume(ctx, nodeAgent.store, types.Volume{
			Name:  volumeName,
			Size:  sizeValue,
			State: types.VolumeAvailable,
		})
	}
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
