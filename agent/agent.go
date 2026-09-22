package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Agent is the node agent. It runs on each machine and bridges store <-> runtime.
//
// Delegates to three sub-components:
//   - ProbeScheduler: health checks and probe execution on an independent timer
//   - NodeReporter: telemetry collection, heartbeat, and node registration
//   - DataPlaneReconciler: VIP DNAT rule programming via the data plane provider
type Agent struct {
	nodeID               string                 // nodeID is the unique identifier for the node this agent manages.
	store                store.StateStore       // store is the fact store used to read desired state and write observed state.
	runtime              runtime.Runtime        // runtime is the pluggable container/process runtime adapter.
	networkProvider      network.NetworkProvider // networkProvider allocates IPs for instances; nil means legacy 127.0.0.1 behavior.
	storageProvider      storage.StorageProvider // storageProvider manages volume attach/detach; nil means no volume support.
	secretProvider       SecretProvider         // secretProvider retrieves decrypted secrets; nil means no secret support.
	materializedSecrets  []MaterializedSecret   // materializedSecrets tracks secrets written for running instances.
	advertiseAddress     string                 // advertiseAddress is this node's LAN-routable IP for cross-host data plane.
	interval             time.Duration          // interval is the period between periodic reconciliation cycles.
	probeScheduler       *ProbeScheduler        // probeScheduler runs health checks and probes independently of reconciliation.
	nodeReporter         *NodeReporter          // nodeReporter collects telemetry and publishes node state to the store.
	dataPlaneReconciler  *DataPlaneReconciler   // dataPlaneReconciler programs VIP DNAT rules via the data plane provider.
}

// New creates a new Agent for the given node, wired to the provided state store
// and runtime adapter. The default reconciliation interval is 1 second. The
// network provider is nil by default, meaning instances get 127.0.0.1 as their
// IP. Use SetNetworkProvider to enable real IP allocation.
func New(nodeID string, stateStore store.StateStore, runtimeAdapter runtime.Runtime) *Agent {
	defaultInterval := 1 * time.Second
	return &Agent{
		nodeID:   nodeID,
		store:    stateStore,
		runtime:  runtimeAdapter,
		interval: defaultInterval,
		probeScheduler: NewProbeScheduler(nodeID, stateStore, runtimeAdapter, defaultInterval),
		nodeReporter:   NewNodeReporter(nodeID, stateStore, runtimeAdapter, ""),
		dataPlaneReconciler: NewDataPlaneReconciler(nodeID, stateStore, nil, ""),
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

// SetSecretProvider configures the agent to materialize secrets for instances.
// When set, the agent writes secret files before starting instances and removes
// them when instances stop.
func (nodeAgent *Agent) SetSecretProvider(secretProvider SecretProvider) {
	nodeAgent.secretProvider = secretProvider
}

// SetDataPlaneProvider configures the agent to program VIP DNAT forwarding
// rules on each reconciliation cycle. When set, the agent reads VIP and
// endpoint facts from the store and calls the provider to ensure iptables
// rules match the desired state.
func (nodeAgent *Agent) SetDataPlaneProvider(dataPlaneProvider network.DataPlaneProvider) {
	nodeAgent.dataPlaneReconciler = NewDataPlaneReconciler(nodeAgent.nodeID, nodeAgent.store, dataPlaneProvider, nodeAgent.advertiseAddress)
}

// SetAdvertiseAddress configures the LAN-routable IP address for this node.
// This address is published to the store so other nodes' data plane
// providers can build cross-host DNAT targets.
func (nodeAgent *Agent) SetAdvertiseAddress(advertiseAddress string) {
	nodeAgent.advertiseAddress = advertiseAddress
	nodeAgent.nodeReporter.advertiseAddress = advertiseAddress
	nodeAgent.dataPlaneReconciler.advertiseAddress = advertiseAddress
}

// DataPlaneProvider returns the configured data plane provider, or nil if
// no data plane is configured. Used during shutdown for cleanup.
func (nodeAgent *Agent) DataPlaneProvider() network.DataPlaneProvider {
	return nodeAgent.dataPlaneReconciler.dataPlaneProvider
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
	nodeAgent.nodeReporter.RegisterNode(ctx)
	nodeAgent.nodeReporter.PublishAliveState(ctx)
	nodeAgent.nodeReporter.WriteHeartbeat(ctx)
	nodeAgent.nodeReporter.PublishAdvertiseAddress(ctx)

	// Initial reconcile.
	if err := nodeAgent.executeReconciliationCycle(ctx); err != nil {
		logging.Default().Error("initial reconcile error", "agent", nodeAgent.nodeID, "error", err.Error())
	}

	// Start the independent probe scheduler goroutine.
	go nodeAgent.probeScheduler.Run(ctx, nodeAgent.findInstancesPlacedOnThisNode)

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
			nodeAgent.nodeReporter.PublishAliveState(ctx)
			nodeAgent.nodeReporter.WriteHeartbeat(ctx)
			if err := nodeAgent.executeReconciliationCycle(ctx); err != nil {
				logging.Default().Error("reconcile error", "agent", nodeAgent.nodeID, "error", err.Error())
			}
			nodeAgent.nodeReporter.CollectAndReportTelemetry(ctx)
			nodeAgent.dataPlaneReconciler.Reconcile(ctx)
		case watchEvent, ok := <-placementCh:
			if !ok {
				return nil
			}
			if watchEvent.Type == store.EventOverflow {
				logging.Default().Warn("watch events dropped, triggering full resync", "agent", nodeAgent.nodeID)
			}
			if watchEvent.Type == store.EventCompacted {
				logging.Default().Warn("watch revision compacted, triggering full resync", "agent", nodeAgent.nodeID)
			}
			if err := nodeAgent.executeReconciliationCycle(ctx); err != nil {
				logging.Default().Error("reconcile error", "agent", nodeAgent.nodeID, "error", err.Error())
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

	// Reconcile each desired instance: start missing, observe running.
	for _, instanceInfo := range desired {
		runtimeStatus, exists := runningByID[instanceInfo.id]
		if !exists || !runtimeStatus.Running {
			nodeAgent.reconcileDesiredInstance(ctx, instanceInfo)
		} else {
			observedState := nodeAgent.observeInstanceState(ctx, instanceInfo.id)
			nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, observedState)
		}
		delete(runningByID, instanceInfo.id)
	}

	if nodeAgent.secretProvider != nil && len(nodeAgent.materializedSecrets) > 0 {
		nodeAgent.refreshMaterializedSecrets(ctx)
	}

	// Clean up stale instances no longer placed on this node.
	for instanceID, runtimeStatus := range runningByID {
		if runtimeStatus.Running {
			nodeAgent.cleanupUndesiredInstance(ctx, instanceID)
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

// reconcileDesiredInstance brings up a single instance that should be running
// but isn't. Handles init steps, volume attachment, secret materialization,
// config/env resolution, network allocation, container start, host port
// publishing, and state observation. If any prerequisite fails (image lookup,
// init, volumes), the instance is skipped for this cycle.
func (nodeAgent *Agent) reconcileDesiredInstance(ctx context.Context, instanceInfo placedInstanceInfo) {
	image := nodeAgent.lookupServiceImageFromStore(ctx, instanceInfo.service)
	if image == "" {
		return
	}

	if nodeAgent.hasInitSteps(ctx, instanceInfo.service) {
		initSucceeded := nodeAgent.executeInitializationSteps(ctx, instanceInfo, image)
		if !initSucceeded {
			logging.Default().Warn("init failed, skipping workload start", "agent", nodeAgent.nodeID, "instance", instanceInfo.id)
			return
		}
	}

	if nodeAgent.storageProvider != nil {
		volumesReady, attachError := nodeAgent.ensureVolumesAttachedForInstance(ctx, instanceInfo)
		if attachError != nil {
			logging.Default().Error("volume error", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", attachError.Error())
		}
		if !volumesReady {
			return
		}
	}

	if nodeAgent.secretProvider != nil {
		materialized := nodeAgent.materializeSecretsForInstance(ctx, instanceInfo)
		nodeAgent.materializedSecrets = append(nodeAgent.materializedSecrets, materialized...)
	}

	envVars := nodeAgent.resolveServiceConfigEnvVars(ctx, instanceInfo.service)
	configFiles := nodeAgent.resolveServiceConfigFiles(ctx, instanceInfo.service)
	exposedPorts := nodeAgent.lookupServiceExposedPortsFromStore(ctx, instanceInfo.service)
	cpuMillicores, memoryBytes := nodeAgent.nodeReporter.lookupServiceResourcesFromStore(ctx, instanceInfo.service)

	allocatedIP := ""
	if nodeAgent.networkProvider != nil {
		networkIP, allocateError := nodeAgent.networkProvider.AllocateIP(ctx, nodeAgent.nodeID, instanceInfo.id)
		if allocateError != nil {
			logging.Default().Error("failed to allocate IP", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", allocateError.Error())
		} else {
			allocatedIP = networkIP
		}
	}

	if startError := nodeAgent.runtime.Start(ctx, runtime.Spec{
		ID:          instanceInfo.id,
		ServiceName: instanceInfo.service,
		Image:       image,
		Env:         envVars,
		ConfigFiles: configFiles,
		Ports:       exposedPorts,
		IP:          allocatedIP,
		CPUm:        cpuMillicores,
		MemoryB:     memoryBytes,
	}); startError != nil {
		logging.Default().Error("failed to start instance", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", startError.Error())
		if nodeAgent.secretProvider != nil {
			nodeAgent.cleanupSecretsForInstance(instanceInfo.id)
		}
		nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceFailed)
		nodeAgent.store.Put(ctx, types.KeyObservedInstanceImage(instanceInfo.id), []byte(image))
		return
	}

	if containerRuntime, isContainer := nodeAgent.runtime.(*runtime.ContainerRuntime); isContainer {
		hostPort := containerRuntime.HostPortForInstance(instanceInfo.id)
		publishInstanceHostPort(ctx, nodeAgent.store, instanceInfo.id, hostPort)
	}

	observedState := nodeAgent.observeInstanceState(ctx, instanceInfo.id)
	nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, observedState)
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceImage(instanceInfo.id), []byte(image))
}

// cleanupUndesiredInstance tears down an instance that is no longer placed on
// this node. Cleans up secrets, detaches volumes, stops the runtime process,
// removes probe state, releases the network IP, and deletes the network
// allocation fact.
func (nodeAgent *Agent) cleanupUndesiredInstance(ctx context.Context, instanceID string) {
	if nodeAgent.secretProvider != nil {
		nodeAgent.cleanupSecretsForInstance(instanceID)
	}
	if nodeAgent.storageProvider != nil {
		nodeAgent.detachVolumesForInstance(ctx, instanceID)
	}
	nodeAgent.runtime.Stop(ctx, instanceID)
	nodeAgent.probeScheduler.CleanupInstance(instanceID)
	if nodeAgent.networkProvider != nil {
		nodeAgent.networkProvider.ReleaseIP(ctx, nodeAgent.nodeID, instanceID)
		types.DeleteNetworkAllocation(ctx, nodeAgent.store, instanceID)
	}
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

// lookupServiceExposedPortsFromStore reads all exposed port declarations for
// the given service and returns them as a slice of port numbers.
func (nodeAgent *Agent) lookupServiceExposedPortsFromStore(ctx context.Context, service string) []int {
	exposeFacts, err := nodeAgent.store.Scan(ctx, fmt.Sprintf("%s/service/%s/expose/", types.PrefixDesired, service))
	if err != nil || len(exposeFacts) == 0 {
		return nil
	}

	ports := make([]int, 0, len(exposeFacts))
	for _, fact := range exposeFacts {
		portStr := fact.Key[strings.LastIndex(fact.Key, "/")+1:]
		if parsedPort, err := strconv.Atoi(portStr); err == nil {
			ports = append(ports, parsedPort)
		}
	}
	return ports
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

// resolveServiceConfigFiles reads the desired config file mount definitions for
// a service from the store and returns them as a map from container-absolute
// file path to file content string.
func (nodeAgent *Agent) resolveServiceConfigFiles(ctx context.Context, serviceName string) map[string]string {
	prefix := types.ScanDesiredServiceConfig(serviceName)
	facts, err := nodeAgent.store.Scan(ctx, prefix)
	if err != nil || len(facts) == 0 {
		return nil
	}

	configFileMounts := make(map[string]string)
	filePrefix := fmt.Sprintf("%s/service/%s/config/file/", types.PrefixDesired, serviceName)
	for _, fact := range facts {
		if strings.HasPrefix(fact.Key, filePrefix) {
			containerFilePath := strings.TrimPrefix(fact.Key, filePrefix)
			configFileMounts[containerFilePath] = string(fact.Value)
		}
	}
	return configFileMounts
}

// materializeSecretsForInstance reads the secret grants for a service, retrieves
// each secret from the provider, and writes the plaintext to the mount path on
// the local filesystem. Returns the list of materialized secrets for later cleanup.
func (nodeAgent *Agent) materializeSecretsForInstance(ctx context.Context, instanceInfo placedInstanceInfo) []MaterializedSecret {
	secretPrefix := types.ScanDesiredServiceSecrets(instanceInfo.service)
	grantFacts, err := nodeAgent.store.Scan(ctx, secretPrefix)
	if err != nil || len(grantFacts) == 0 {
		return nil
	}

	var materialized []MaterializedSecret
	for _, grantFact := range grantFacts {
		secretName := strings.TrimPrefix(grantFact.Key, secretPrefix)
		plaintext, mountPath, err := nodeAgent.secretProvider.GetSecretForService(ctx, instanceInfo.service, secretName)
		if err != nil {
			logging.Default().Error("secret retrieval failed", "agent", nodeAgent.nodeID, "secret", secretName, "service", instanceInfo.service, "instance", instanceInfo.id, "error", err.Error())
			continue
		}

		parentDirectory := filepath.Dir(mountPath)
		if err := os.MkdirAll(parentDirectory, 0700); err != nil {
			logging.Default().Error("mkdir failed", "agent", nodeAgent.nodeID, "path", parentDirectory, "error", err.Error())
			continue
		}

		if err := os.WriteFile(mountPath, plaintext, 0600); err != nil {
			logging.Default().Error("write secret failed", "agent", nodeAgent.nodeID, "secret", secretName, "path", mountPath, "error", err.Error())
			continue
		}

		materialized = append(materialized, MaterializedSecret{
			InstanceID: instanceInfo.id,
			SecretName: secretName,
			MountPath:  mountPath,
		})
	}

	return materialized
}

// cleanupSecretsForInstance removes secret files that were materialized for the
// given instance and removes them from the tracking list.
func (nodeAgent *Agent) cleanupSecretsForInstance(instanceID string) {
	remaining := nodeAgent.materializedSecrets[:0]
	for _, materializedSecret := range nodeAgent.materializedSecrets {
		if materializedSecret.InstanceID == instanceID {
			if err := os.Remove(materializedSecret.MountPath); err != nil && !os.IsNotExist(err) {
				logging.Default().Warn("remove secret failed", "agent", nodeAgent.nodeID, "path", materializedSecret.MountPath, "error", err.Error())
			}
			continue
		}
		remaining = append(remaining, materializedSecret)
	}
	nodeAgent.materializedSecrets = remaining
}

// refreshMaterializedSecrets re-reads all materialized secrets and overwrites
// any whose content has changed. This enables secret rotation without restarting
// instances — the file is atomically replaced.
func (nodeAgent *Agent) refreshMaterializedSecrets(ctx context.Context) {
	for _, materializedSecret := range nodeAgent.materializedSecrets {
		serviceFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(materializedSecret.InstanceID))
		if err != nil {
			continue
		}
		serviceName := string(serviceFact.Value)

		plaintext, _, err := nodeAgent.secretProvider.GetSecretForService(ctx, serviceName, materializedSecret.SecretName)
		if err != nil {
			continue
		}

		existingContent, readErr := os.ReadFile(materializedSecret.MountPath)
		if readErr != nil || string(existingContent) != string(plaintext) {
			os.WriteFile(materializedSecret.MountPath, plaintext, 0600)
			logging.Default().Info("rotated secret", "agent", nodeAgent.nodeID, "secret", materializedSecret.SecretName, "instance", materializedSecret.InstanceID)
		}
	}
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

		if volumeState != types.VolumeAvailable && volumeState != types.VolumeMigrating {
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

		var usedBytes, capacityBytes int64
		usedBytes, capacityBytes, _ = nodeAgent.storageProvider.VolumeUsage(ctx, volumeName)

		types.WriteObservedVolume(ctx, nodeAgent.store, types.Volume{
			Name:          volumeName,
			Size:          sizeValue,
			State:         types.VolumeAttached,
			Node:          nodeAgent.nodeID,
			Instance:      instanceInfo.id,
			MountPath:     mountPath,
			UsedBytes:     usedBytes,
			CapacityBytes: capacityBytes,
		})

		nodeAgent.store.Delete(ctx, types.KeyObservedVolumeMigrationSource(volumeName))
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

// observeInstanceState queries the runtime for the actual state of an instance
// and returns the corresponding InstanceState. This ensures state reporting is
// based on runtime observation rather than treating a successful Start() call
// as proof of liveness.
func (nodeAgent *Agent) observeInstanceState(ctx context.Context, instanceID string) types.InstanceState {
	observedStatus, statusError := nodeAgent.runtime.Status(ctx, instanceID)
	if statusError != nil {
		return types.InstanceStarting
	}
	if observedStatus.Running {
		return types.InstanceRunning
	}
	if observedStatus.ExitCode != 0 || observedStatus.Error != "" {
		return types.InstanceFailed
	}
	return types.InstanceStarting
}

// publishInstanceStateToStore writes a complete set of observed-state facts for
// the given instance: its existence marker, owning service, current state, the
// node it is running on, and its IP address. When a NetworkProvider is configured,
// an IP is allocated from the node's subnet. Without a provider, no IP is
// assigned — consumers must check for the IP fact before using it.
func (nodeAgent *Agent) publishInstanceStateToStore(ctx context.Context, instanceID, service string, state types.InstanceState) {
	nodeAgent.store.Put(ctx, types.KeyObservedInstance(instanceID), []byte(""))
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte(service))
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(state)))
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceNode(instanceID), []byte(nodeAgent.nodeID))

	if nodeAgent.networkProvider != nil {
		allocatedIP, allocateError := nodeAgent.networkProvider.AllocateIP(ctx, nodeAgent.nodeID, instanceID)
		if allocateError != nil {
			logging.Default().Error("failed to allocate IP", "agent", nodeAgent.nodeID, "instance", instanceID, "error", allocateError.Error())
		} else {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceIP(instanceID), []byte(allocatedIP))
			types.WriteNetworkAllocation(ctx, nodeAgent.store, instanceID, allocatedIP)
		}
	}
}
