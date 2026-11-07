// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tenant"
	"github.com/boyadzhievb/ccattler/types"
)

// ClusterStatus is the structured representation of the full cluster state.
type ClusterStatus struct {
	Services        []ServiceStatus       `json:"services"`
	Instances       []InstanceStatus      `json:"instances"`
	Nodes           []NodeStatus          `json:"nodes"`
	Networking      []NetworkStatus       `json:"networking,omitempty"`
	Volumes         []VolumeStatus        `json:"volumes,omitempty"`
	Secrets         []SecretStatus        `json:"secrets,omitempty"`
	Config          []ConfigStatus        `json:"config,omitempty"`
	CloudIdentities []CloudIdentityStatus `json:"cloud_identities,omitempty"`
	Endpoints       []EndpointStatus      `json:"endpoints,omitempty"`
	Gates           []GateStatus          `json:"gates,omitempty"`
}

// ServiceStatus represents one service in the cluster status.
type ServiceStatus struct {
	Name           string            `json:"name"`
	Image          string            `json:"image"`
	DesiredCount   int               `json:"desired"`
	RunningCount   int               `json:"running"`
	ExposedPorts   []int             `json:"ports,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	CrashLoopCount int               `json:"crash_loop_count,omitempty"`
	CircuitBroken  bool              `json:"circuit_broken,omitempty"`
}

// InstanceStatus represents one instance in the cluster status.
type InstanceStatus struct {
	ID            string `json:"id"`
	ServiceName   string `json:"service"`
	State         string `json:"state"`
	NodeID        string `json:"node"`
	IPAddress     string `json:"ip"`
	HealthState   string `json:"health"`
	CPUMillis     string `json:"cpu,omitempty"`
	MemoryBytes   string `json:"memory,omitempty"`
	InitPhase     string `json:"init_phase,omitempty"`
	Restarts      string `json:"restarts,omitempty"`
	Startup       string `json:"startup,omitempty"`
	Liveness      string `json:"liveness,omitempty"`
	Readiness     string `json:"readiness,omitempty"`
	StatusReason  string `json:"status_reason,omitempty"`
	ExitReason    string `json:"exit_reason,omitempty"`
	InitProgress  string `json:"init_progress,omitempty"`
	ProbeProgress string `json:"probe_progress,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

// NodeStatus represents one node in the cluster status.
type NodeStatus struct {
	ID                string `json:"id"`
	State             string `json:"state"`
	PlacedInstances   int    `json:"instances"`
	AvailableCPU      int64  `json:"available_cpu"`
	CapacityCPU       int64  `json:"capacity_cpu"`
	AvailableMemory   int64  `json:"available_memory"`
	CapacityMemory    int64  `json:"capacity_memory"`
	UtilizationCPU    string `json:"utilization_cpu,omitempty"`
	UtilizationMemory string `json:"utilization_memory,omitempty"`
}

// NetworkStatus represents a service's networking configuration.
type NetworkStatus struct {
	ServiceName string `json:"service"`
	VIP         string `json:"vip"`
	Port        int    `json:"port"`
	DNS         string `json:"dns"`
}

// VolumeStatus represents one persistent volume in the cluster status.
type VolumeStatus struct {
	Name          string                   `json:"name"`
	Size          string                   `json:"size"`
	State         string                   `json:"state"`
	Node          string                   `json:"node,omitempty"`
	Instance      string                   `json:"instance,omitempty"`
	MountPath     string                   `json:"mount_path,omitempty"`
	UsedBytes     int64                    `json:"used_bytes,omitempty"`
	CapacityBytes int64                    `json:"capacity_bytes,omitempty"`
	Access        string                   `json:"access"`                // Access is "shared" (many nodes) or "exclusive" (one node).
	Nodes         []string                 `json:"nodes,omitempty"`       // Nodes lists every node the volume is attached on, sorted.
	Instances     []string                 `json:"instances,omitempty"`   // Instances lists every instance the volume is mounted into, sorted.
	Attachments   []types.VolumeAttachment `json:"attachments,omitempty"` // Attachments lists each shared attachment with its node, instance and mount path.
}

// SecretStatus represents one secret in the cluster status. Only the name and
// list of services with grants are exposed — never the secret value.
type SecretStatus struct {
	Name      string   `json:"name"`       // secret name
	GrantedTo []string `json:"granted_to"` // services authorized to access this secret
}

// ConfigStatus represents one config entry (env var or file) for a service.
type ConfigStatus struct {
	Service string `json:"service"` // owning service name
	Type    string `json:"type"`    // "env" or "file"
	Key     string `json:"key"`     // env var name or file path
	Value   string `json:"value"`   // config value (redacted for sensitive keys)
}

// sensitiveConfigKeyPatterns lists substrings that indicate a config key holds
// a credential or secret. Matching is case-insensitive.
var sensitiveConfigKeyPatterns = []string{
	"PASSWORD",
	"PASSWD",
	"SECRET",
	"TOKEN",
	"API_KEY",
	"APIKEY",
	"PRIVATE_KEY",
	"ACCESS_KEY",
	"CREDENTIAL",
	"AUTH",
	"CONN_CMD",
}

// redactedConfigValuePlaceholder is the replacement shown for sensitive values.
const redactedConfigValuePlaceholder = "[REDACTED]"

// redactSensitiveConfigValue returns "[REDACTED]" if the config key name
// matches any known sensitive pattern, otherwise returns the original value.
func redactSensitiveConfigValue(configKey string, configValue string) string {
	upperKey := strings.ToUpper(configKey)
	for _, pattern := range sensitiveConfigKeyPatterns {
		if strings.Contains(upperKey, pattern) {
			return redactedConfigValuePlaceholder
		}
	}
	return configValue
}

// CloudIdentityStatus represents a cloud identity declaration with its
// provider, bound services, and per-instance credential state.
type CloudIdentityStatus struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Services []string `json:"services,omitempty"`
}

// EndpointStatus represents one service endpoint mapping an instance port to
// network addresses. Endpoints are derived facts produced by the endpoint
// controller and consumed by the data plane for VIP DNAT programming.
type EndpointStatus struct {
	Service         string `json:"service"`
	InstanceID      string `json:"instance_id"`
	Port            int    `json:"port"`
	Address         string `json:"address"`
	Hostname        string `json:"hostname,omitempty"`
	InternalAddress string `json:"internal_address,omitempty"`
	ExternalAddress string `json:"external_address,omitempty"`
}

// buildStatusFromStore collects the full cluster state with a fixed number of
// store scans (loadStatusFactSnapshot) and assembles it into a ClusterStatus
// by delegating to per-section builders. A non-nil visibility limited to
// teams keeps only those teams' services, instances and gates; nil or a
// cluster-scope visibility sees everything (Flow 25). A failed required scan
// is returned instead of an empty cluster, so a store outage or denial is
// visible to the caller.
func buildStatusFromStore(ctx context.Context, factStore store.StateStore, visibility *workloadVisibility, includeAllInstances bool) (ClusterStatus, error) {
	snapshot, loadError := loadStatusFactSnapshot(ctx, factStore)
	if loadError != nil {
		return ClusterStatus{}, fmt.Errorf("load status facts: %w", loadError)
	}
	allInstances := listSortedInstancesFromSnapshot(snapshot)
	sortedServiceNames := discoverSortedServiceNames(snapshot.factsUnder(types.ScanDesiredServices))
	gateStatusList := buildGateStatusList(
		groupGateFactAttributes(types.ScanDesiredRoutes, snapshot.factsUnder(types.ScanDesiredRoutes)),
		groupGateFactAttributes(types.ScanDerivedRoutes, snapshot.factsUnder(types.ScanDerivedRoutes)))

	if visibility != nil && !visibility.allTeams {
		sortedServiceNames = tenant.FilterServiceNamesByTeams(sortedServiceNames, visibility.teams, collectServiceOwners(snapshot, sortedServiceNames))
		allInstances = filterInstancesByServiceNames(allInstances, sortedServiceNames)
		gateStatusList = filterGatesByServiceNames(gateStatusList, sortedServiceNames)
	}

	return ClusterStatus{
		Services:        collectServiceStatus(snapshot, sortedServiceNames, allInstances),
		Instances:       collectInstanceStatus(snapshot, allInstances, includeAllInstances),
		Nodes:           collectNodeStatus(snapshot, allInstances),
		Networking:      collectNetworkingStatus(snapshot),
		Volumes:         collectVolumeStatus(snapshot),
		Secrets:         collectSecretStatus(snapshot, sortedServiceNames),
		Config:          collectConfigStatus(snapshot, sortedServiceNames),
		CloudIdentities: collectCloudIdentityStatus(snapshot),
		Endpoints:       collectEndpointStatus(snapshot),
		Gates:           gateStatusList,
	}, nil
}

// listSortedInstancesFromSnapshot builds every instance from its observed and
// derived facts (derived markers such as controller_stopped override the
// observed state, as in types.ListInstances), sorted by ID.
func listSortedInstancesFromSnapshot(snapshot *statusFactSnapshot) []types.Instance {
	groupedFields := types.GroupFactFieldsByID(types.ScanObservedInstances, snapshot.factsUnder(types.ScanObservedInstances), nil)
	groupedFields = types.GroupFactFieldsByID(types.ScanDerivedInstances, snapshot.factsUnder(types.ScanDerivedInstances), groupedFields)
	allInstances := types.InstancesFromFields(groupedFields)
	sort.Slice(allInstances, func(i, j int) bool { return allInstances[i].ID < allInstances[j].ID })
	return allInstances
}

// collectServiceOwners returns service name → explicit owner for services
// that have a non-empty owner fact, for team visibility filtering.
func collectServiceOwners(snapshot *statusFactSnapshot, serviceNames []string) map[string]string {
	ownerByService := make(map[string]string)
	for _, serviceName := range serviceNames {
		if owner, exists := snapshot.value(types.KeyDesiredServiceOwner(serviceName)); exists && owner != "" {
			ownerByService[serviceName] = owner
		}
	}
	return ownerByService
}

// filterInstancesByServiceNames returns only instances whose service name
// is in the provided set. Used for tenant visibility filtering.
func filterInstancesByServiceNames(allInstances []types.Instance, visibleServiceNames []string) []types.Instance {
	serviceNameSet := make(map[string]bool, len(visibleServiceNames))
	for _, serviceName := range visibleServiceNames {
		serviceNameSet[serviceName] = true
	}

	var visibleInstances []types.Instance
	for _, instance := range allInstances {
		if serviceNameSet[instance.Service] {
			visibleInstances = append(visibleInstances, instance)
		}
	}
	return visibleInstances
}

// discoverSortedServiceNames returns the deduplicated, alphabetically sorted
// service names among desired-service facts. Only root marker keys (empty
// value) are collected, which correctly handles hierarchical names like
// "payments/checkout".
func discoverSortedServiceNames(desiredServiceFacts []store.Fact) []string {
	var serviceNames []string
	for _, fact := range desiredServiceFacts {
		if string(fact.Value) != "" {
			continue
		}
		serviceName := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		if isServiceSubResourceKey(serviceName) {
			continue
		}
		serviceNames = append(serviceNames, serviceName)
	}
	sort.Strings(serviceNames)
	return serviceNames
}

// isServiceSubResourceKey returns true if the name contains a sub-resource
// path segment (init, expose, probe, lifecycle, logging) that indicates it is
// a child key of a service definition, not a service itself.
func isServiceSubResourceKey(serviceName string) bool {
	subResourceSegments := []string{"/init/", "/expose/", "/probe/", "/lifecycle/", "/logging/"}
	for _, segment := range subResourceSegments {
		if strings.Contains(serviceName, segment) {
			return true
		}
	}
	return false
}

// collectServiceStatus builds each service definition from its desired facts
// and counts running instances to produce the service status list. A
// service with a corrupt definition is skipped, as before.
func collectServiceStatus(snapshot *statusFactSnapshot, sortedServiceNames []string, allInstances []types.Instance) []ServiceStatus {
	crashLoopCounters := readCrashLoopCounters(snapshot)
	runningCountByService := make(map[string]int)
	for _, instance := range allInstances {
		if instance.State == types.InstanceRunning {
			runningCountByService[instance.Service]++
		}
	}
	var serviceStatusList []ServiceStatus
	for _, serviceName := range sortedServiceNames {
		service, buildError := types.ServiceFromFacts(serviceName, snapshot.factsUnder(types.KeyDesiredService(serviceName)+"/"))
		if buildError != nil {
			continue
		}
		serviceStatus := ServiceStatus{
			Name: service.Name, Image: service.Image, DesiredCount: service.Instances,
			RunningCount: runningCountByService[serviceName], ExposedPorts: service.Ports,
			Tags: collectServiceTags(snapshot, serviceName),
		}
		if counter, hasCounter := crashLoopCounters[serviceName]; hasCounter {
			serviceStatus.CrashLoopCount = counter.count
			serviceStatus.CircuitBroken = counter.count >= defaultCircuitBreakerDisplayThreshold
		}
		serviceStatusList = append(serviceStatusList, serviceStatus)
	}
	return serviceStatusList
}

// defaultCircuitBreakerDisplayThreshold matches the controller's default
// CircuitBreakerThreshold for display purposes.
const defaultCircuitBreakerDisplayThreshold = 10

// crashLoopInfo is the parsed crash-loop counter of one service.
type crashLoopInfo struct {
	count int // count is the number of consecutive crash-loop restarts
}

// readCrashLoopCounters parses all crash-loop counter facts by service name.
func readCrashLoopCounters(snapshot *statusFactSnapshot) map[string]*crashLoopInfo {
	counters := make(map[string]*crashLoopInfo)
	for _, fact := range snapshot.factsUnder(types.ScanDerivedCrashLoops) {
		serviceName := strings.TrimPrefix(fact.Key, types.ScanDerivedCrashLoops)
		var parsed struct {
			Count int `json:"count"`
		}
		if json.Unmarshal(fact.Value, &parsed) == nil {
			counters[serviceName] = &crashLoopInfo{count: parsed.Count}
		}
	}
	return counters
}

// collectServiceTags returns a service's tag facts as a key-value map, or
// nil if the service has no tags.
func collectServiceTags(snapshot *statusFactSnapshot, serviceName string) map[string]string {
	tagPrefix := types.KeyDesiredServiceTagPrefix(serviceName)
	tagFacts := snapshot.factsUnder(tagPrefix)
	if len(tagFacts) == 0 {
		return nil
	}
	tags := make(map[string]string, len(tagFacts))
	for _, tagFact := range tagFacts {
		tagKey := strings.TrimPrefix(tagFact.Key, tagPrefix)
		if tagKey != "" {
			tags[tagKey] = string(tagFact.Value)
		}
	}
	return tags
}

// collectInstanceStatus builds an InstanceStatus for every instance,
// enriching it with placement, health, resource usage, init phase, restart
// count, and probe states. When includeAll is false, stopped instances are
// excluded from the result.
func collectInstanceStatus(snapshot *statusFactSnapshot, allInstances []types.Instance, includeAll bool) []InstanceStatus {
	var instanceStatusList []InstanceStatus
	for _, instance := range allInstances {
		if !includeAll && instance.State == types.InstanceStopped {
			continue
		}
		instanceStatusList = append(instanceStatusList, buildInstanceStatus(snapshot, instance))
	}
	return instanceStatusList
}

// buildInstanceStatus assembles one InstanceStatus from the instance and its
// placement, telemetry, probe and progress facts. Missing facts stay blank.
func buildInstanceStatus(snapshot *statusFactSnapshot, instance types.Instance) InstanceStatus {
	healthDisplay := string(instance.Health)
	if healthDisplay == "" {
		healthDisplay = "-"
	}
	instanceIPAddress := instance.IP
	if instanceIPAddress == "" {
		instanceIPAddress = "-"
	}
	instanceID := instance.ID
	return InstanceStatus{
		ID: instanceID, ServiceName: instance.Service, State: string(instance.State),
		NodeID:        snapshot.valueOrEmpty(types.KeyPlacementInstance(instanceID)),
		IPAddress:     instanceIPAddress,
		HealthState:   healthDisplay,
		CPUMillis:     snapshot.valueOrEmpty(types.KeyObservedInstanceCPU(instanceID)),
		MemoryBytes:   snapshot.valueOrEmpty(types.KeyObservedInstanceMemory(instanceID)),
		InitPhase:     snapshot.valueOrEmpty(types.KeyDerivedInstanceInitPhase(instanceID)),
		Restarts:      snapshot.valueOrEmpty(types.KeyObservedInstanceRestarts(instanceID)),
		Startup:       snapshot.valueOrEmpty(types.KeyObservedInstanceProbeState(instanceID, types.ProbeStartup)),
		Liveness:      snapshot.valueOrEmpty(types.KeyObservedInstanceProbeState(instanceID, types.ProbeLiveness)),
		Readiness:     snapshot.valueOrEmpty(types.KeyObservedInstanceProbeState(instanceID, types.ProbeReadiness)),
		StatusReason:  snapshot.valueOrEmpty(types.KeyObservedInstanceStatusReason(instanceID)),
		ExitReason:    snapshot.valueOrEmpty(types.KeyObservedInstanceExitReason(instanceID)),
		InitProgress:  snapshot.valueOrEmpty(types.KeyObservedInstanceInitProgress(instanceID)),
		ProbeProgress: snapshot.valueOrEmpty(types.KeyObservedInstanceProbeProgress(instanceID)),
		CreatedAt:     snapshot.valueOrEmpty(types.KeyObservedInstanceCreatedAt(instanceID)),
	}
}

// collectNetworkingStatus reads VIP assignments and assembles a sorted list
// of per-service network status entries.
func collectNetworkingStatus(snapshot *statusFactSnapshot) []NetworkStatus {
	vipByService := make(map[string]string)
	vipPortByService := make(map[string]int)
	for _, vipFact := range snapshot.factsUnder(types.ScanNetworkVIPs) {
		relativePath := strings.TrimPrefix(vipFact.Key, types.ScanNetworkVIPs)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 1 {
			vipByService[pathParts[0]] = string(vipFact.Value)
		} else if len(pathParts) == 2 && pathParts[1] == "port" {
			portValue := 0
			if _, scanErr := fmt.Sscanf(string(vipFact.Value), "%d", &portValue); scanErr != nil {
				logging.Default().Warn("corrupt VIP port fact", "service", pathParts[0], "value", string(vipFact.Value))
			}
			vipPortByService[pathParts[0]] = portValue
		}
	}
	var networkingStatusList []NetworkStatus
	for serviceName, vipAddress := range vipByService {
		networkingStatusList = append(networkingStatusList, NetworkStatus{
			ServiceName: serviceName,
			VIP:         vipAddress,
			Port:        vipPortByService[serviceName],
			DNS:         serviceName + "." + network.DefaultDNSDomain,
		})
	}
	sort.Slice(networkingStatusList, func(i, j int) bool {
		return networkingStatusList[i].ServiceName < networkingStatusList[j].ServiceName
	})
	return networkingStatusList
}

// collectVolumeStatus builds observed volumes and returns a sorted list of
// volume status entries.
func collectVolumeStatus(snapshot *statusFactSnapshot) []VolumeStatus {
	allVolumes := types.VolumesFromFields(types.GroupFactFieldsByID(types.ScanObservedVolumes, snapshot.factsUnder(types.ScanObservedVolumes), nil))
	sort.Slice(allVolumes, func(i, j int) bool { return allVolumes[i].Name < allVolumes[j].Name })
	var volumeStatusList []VolumeStatus
	for _, volume := range allVolumes {
		attachedNodeIDs, attachedInstanceIDs, attachedMountPaths := volume.AttachmentSummary()
		volumeStatusList = append(volumeStatusList, VolumeStatus{
			Name:          volume.Name,
			Size:          volume.Size,
			State:         string(volume.State),
			Node:          volume.Node,
			Instance:      volume.Instance,
			MountPath:     strings.Join(attachedMountPaths, ","),
			UsedBytes:     volume.UsedBytes,
			CapacityBytes: volume.CapacityBytes,
			Access:        string(volume.Access),
			Nodes:         attachedNodeIDs,
			Instances:     attachedInstanceIDs,
			Attachments:   volume.Attachments,
		})
	}
	return volumeStatusList
}

// collectSecretStatus lists secret names (never values) with the services
// granted each one, sorted by name. Empty when secrets/ was not readable.
func collectSecretStatus(snapshot *statusFactSnapshot, sortedServiceNames []string) []SecretStatus {
	secretFacts := snapshot.factsUnder(security.SecretStorePrefix)
	if len(secretFacts) == 0 {
		return nil
	}
	var secretStatusList []SecretStatus
	for _, secretFact := range secretFacts {
		secretName := strings.TrimPrefix(secretFact.Key, security.SecretStorePrefix)
		var grantedServiceNames []string
		for _, serviceName := range sortedServiceNames {
			if _, granted := snapshot.value(types.KeyDesiredServiceSecret(serviceName, secretName)); granted {
				grantedServiceNames = append(grantedServiceNames, serviceName)
			}
		}
		secretStatusList = append(secretStatusList, SecretStatus{
			Name:      secretName,
			GrantedTo: grantedServiceNames,
		})
	}
	sort.Slice(secretStatusList, func(i, j int) bool {
		return secretStatusList[i].Name < secretStatusList[j].Name
	})
	return secretStatusList
}

// collectConfigStatus collects every service's environment variable and file
// config entries, with sensitive values redacted.
func collectConfigStatus(snapshot *statusFactSnapshot, sortedServiceNames []string) []ConfigStatus {
	var configStatusList []ConfigStatus
	for _, serviceName := range sortedServiceNames {
		configPrefix := types.ScanDesiredServiceConfig(serviceName)
		for _, configFact := range snapshot.factsUnder(configPrefix) {
			relativePath := strings.TrimPrefix(configFact.Key, configPrefix)
			configType := "env"
			configKey := relativePath
			if strings.HasPrefix(relativePath, "env/") {
				configKey = strings.TrimPrefix(relativePath, "env/")
			} else if strings.HasPrefix(relativePath, "file/") {
				configType = "file"
				configKey = strings.TrimPrefix(relativePath, "file/")
			}
			configStatusList = append(configStatusList, ConfigStatus{
				Service: serviceName,
				Type:    configType,
				Key:     configKey,
				Value:   redactSensitiveConfigValue(configKey, string(configFact.Value)),
			})
		}
	}
	return configStatusList
}

// collectNodeStatus builds every registered node, counts the non-stopped
// instances placed on it, and adds CPU and memory utilization.
func collectNodeStatus(snapshot *statusFactSnapshot, allInstances []types.Instance) []NodeStatus {
	allNodes := types.NodesFromFields(types.GroupFactFieldsByID(types.ScanObservedNodes, snapshot.factsUnder(types.ScanObservedNodes), nil))
	sort.Slice(allNodes, func(i, j int) bool { return allNodes[i].ID < allNodes[j].ID })
	placedCountByNode := make(map[string]int)
	for _, instance := range allInstances {
		if instance.State == types.InstanceStopped {
			continue
		}
		if placedNodeID, placed := snapshot.value(types.KeyPlacementInstance(instance.ID)); placed {
			placedCountByNode[placedNodeID]++
		}
	}
	var nodeStatusList []NodeStatus
	for _, node := range allNodes {
		nodeStatusList = append(nodeStatusList, NodeStatus{
			ID: node.ID, State: string(node.State), PlacedInstances: placedCountByNode[node.ID],
			AvailableCPU: node.AvailableCPU, CapacityCPU: node.CapacityCPU,
			AvailableMemory: node.AvailableMemory, CapacityMemory: node.CapacityMemory,
			UtilizationCPU:    snapshot.valueOrEmpty(types.KeyObservedNodeUtilizationCPU(node.ID)),
			UtilizationMemory: snapshot.valueOrEmpty(types.KeyObservedNodeUtilizationMemory(node.ID)),
		})
	}
	return nodeStatusList
}

// collectCloudIdentityStatus lists cloud identity declarations with the
// services bound to each identity, sorted by name.
func collectCloudIdentityStatus(snapshot *statusFactSnapshot) []CloudIdentityStatus {
	identityMap := make(map[string]*CloudIdentityStatus)
	for _, fact := range snapshot.factsUnder(types.ScanDesiredCloudIdentities) {
		remainder := strings.TrimPrefix(fact.Key, types.ScanDesiredCloudIdentities)
		parts := strings.SplitN(remainder, "/", 2)
		identityName := parts[0]
		if _, exists := identityMap[identityName]; !exists {
			identityMap[identityName] = &CloudIdentityStatus{Name: identityName}
		}
		if len(parts) == 2 && parts[1] == "provider" {
			identityMap[identityName].Provider = string(fact.Value)
		}
	}
	for _, fact := range snapshot.factsUnder(types.ScanDesiredServices) {
		remainder := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		if cloudIdentityIndex := strings.Index(remainder, "/cloud_identity/"); cloudIdentityIndex > 0 {
			serviceName := remainder[:cloudIdentityIndex]
			identityPart := remainder[cloudIdentityIndex+len("/cloud_identity/"):]
			if !strings.Contains(identityPart, "/") {
				if identityStatus, exists := identityMap[identityPart]; exists {
					identityStatus.Services = appendUniqueString(identityStatus.Services, serviceName)
				}
			}
		}
	}
	var cloudIdentityStatusList []CloudIdentityStatus
	for _, identityStatus := range identityMap {
		cloudIdentityStatusList = append(cloudIdentityStatusList, *identityStatus)
	}
	sort.Slice(cloudIdentityStatusList, func(i, j int) bool {
		return cloudIdentityStatusList[i].Name < cloudIdentityStatusList[j].Name
	})
	return cloudIdentityStatusList
}

// collectEndpointStatus assembles a sorted list of endpoint status entries
// for display via `cca get endpoints`.
func collectEndpointStatus(snapshot *statusFactSnapshot) []EndpointStatus {
	var endpointStatusList []EndpointStatus
	for _, endpointFact := range snapshot.factsUnder(types.ScanEndpoints) {
		relativePath := strings.TrimPrefix(endpointFact.Key, types.ScanEndpoints)
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) != 3 {
			continue
		}
		portNumber := 0
		if _, parseError := fmt.Sscanf(pathParts[2], "%d", &portNumber); parseError != nil {
			continue
		}
		serviceName := pathParts[0]
		instanceID := pathParts[1]
		endpointAddress := string(endpointFact.Value)
		overlayIP := snapshot.valueOrEmpty(types.KeyObservedInstanceIP(instanceID))
		placedNodeID := snapshot.valueOrEmpty(types.KeyPlacementInstance(instanceID))
		nodeAddress := ""
		if placedNodeID != "" {
			nodeAddress = snapshot.valueOrEmpty(types.KeyObservedNodeAddress(placedNodeID))
		}
		endpointStatusList = append(endpointStatusList, EndpointStatus{
			Service:         serviceName,
			InstanceID:      instanceID,
			Port:            portNumber,
			Address:         endpointAddress,
			Hostname:        serviceName + "." + network.DefaultDNSDomain,
			InternalAddress: buildInternalAddress(overlayIP, portNumber),
			ExternalAddress: buildExternalAddress(overlayIP, nodeAddress, endpointAddress),
		})
	}
	sort.Slice(endpointStatusList, func(i, j int) bool {
		if endpointStatusList[i].Service != endpointStatusList[j].Service {
			return endpointStatusList[i].Service < endpointStatusList[j].Service
		}
		if endpointStatusList[i].InstanceID != endpointStatusList[j].InstanceID {
			return endpointStatusList[i].InstanceID < endpointStatusList[j].InstanceID
		}
		return endpointStatusList[i].Port < endpointStatusList[j].Port
	})
	return endpointStatusList
}

// buildInternalAddress formats the overlay IP:port string for an instance.
func buildInternalAddress(overlayIP string, port int) string {
	if overlayIP == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", overlayIP, port)
}

// buildExternalAddress determines the external (node IP:host-port) address for
// an endpoint. Returns empty when the endpoint already uses the overlay IP
// (no host port mapping) or when the node address is unknown.
func buildExternalAddress(overlayIP string, nodeAddress string, endpointAddress string) string {
	if overlayIP != "" && strings.HasPrefix(endpointAddress, overlayIP+":") {
		return ""
	}
	if nodeAddress != "" && strings.HasPrefix(endpointAddress, nodeAddress+":") {
		return endpointAddress
	}
	return ""
}

// appendUniqueString appends value to slice unless it is already present.
func appendUniqueString(slice []string, value string) []string {
	for _, existing := range slice {
		if existing == value {
			return slice
		}
	}
	return append(slice, value)
}
