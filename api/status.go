// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"sort"
	"strings"

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
}

// ServiceStatus represents one service in the cluster status.
type ServiceStatus struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	DesiredCount int    `json:"desired"`
	RunningCount int    `json:"running"`
	ExposedPorts []int  `json:"ports,omitempty"`
}

// InstanceStatus represents one instance in the cluster status.
type InstanceStatus struct {
	ID          string `json:"id"`
	ServiceName string `json:"service"`
	State       string `json:"state"`
	NodeID      string `json:"node"`
	IPAddress   string `json:"ip"`
	HealthState string `json:"health"`
	CPUMillis   string `json:"cpu,omitempty"`
	MemoryBytes string `json:"memory,omitempty"`
	InitPhase   string `json:"init_phase,omitempty"`
	Restarts    string `json:"restarts,omitempty"`
	Startup     string `json:"startup,omitempty"`
	Liveness    string `json:"liveness,omitempty"`
	Readiness   string `json:"readiness,omitempty"`
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
	Name          string `json:"name"`
	Size          string `json:"size"`
	State         string `json:"state"`
	Node          string `json:"node,omitempty"`
	Instance      string `json:"instance,omitempty"`
	MountPath     string `json:"mount_path,omitempty"`
	UsedBytes     int64  `json:"used_bytes,omitempty"`
	CapacityBytes int64  `json:"capacity_bytes,omitempty"`
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
	Value   string `json:"value"`   // config value
}

// CloudIdentityStatus represents a cloud identity declaration with its
// provider, bound services, and per-instance credential state.
type CloudIdentityStatus struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Services []string `json:"services,omitempty"`
}

// buildStatusFromStore collects the full cluster state from the fact store
// and assembles it into a structured ClusterStatus by delegating to
// per-section builder functions. When a non-nil principal is provided,
// results are filtered to resources visible to that principal's tenant.
// Platform principals and nil principals see everything.
func buildStatusFromStore(ctx context.Context, factStore store.StateStore, principal *security.Principal) ClusterStatus {
	allInstances, _ := types.ListInstances(ctx, factStore)
	sort.Slice(allInstances, func(i, j int) bool { return allInstances[i].ID < allInstances[j].ID })

	sortedServiceNames := discoverSortedServiceNamesFromStore(ctx, factStore)

	if principal != nil && !tenant.IsPlatformPrincipal(*principal) {
		tenantName := tenant.ResolvePrincipalTenant(*principal)
		sortedServiceNames = tenant.FilterServiceNamesByTenant(ctx, factStore, sortedServiceNames, tenantName)
		allInstances = filterInstancesByServiceNames(allInstances, sortedServiceNames)
	}

	return ClusterStatus{
		Services:        collectServiceStatusFromStore(ctx, factStore, sortedServiceNames, allInstances),
		Instances:       collectInstanceStatusFromStore(ctx, factStore, allInstances),
		Nodes:           collectNodeStatusFromStore(ctx, factStore, allInstances),
		Networking:      collectNetworkingStatusFromStore(ctx, factStore),
		Volumes:         collectVolumeStatusFromStore(ctx, factStore),
		Secrets:         collectSecretStatusFromStore(ctx, factStore, sortedServiceNames),
		Config:          collectConfigStatusFromStore(ctx, factStore, sortedServiceNames),
		CloudIdentities: collectCloudIdentityStatusFromStore(ctx, factStore),
	}
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

// discoverSortedServiceNamesFromStore scans the desired-services prefix in the
// fact store and returns a deduplicated, alphabetically sorted list of service
// names.
func discoverSortedServiceNamesFromStore(ctx context.Context, factStore store.StateStore) []string {
	desiredFacts, _ := factStore.Scan(ctx, types.ScanDesiredServices)
	uniqueServiceNames := make(map[string]bool)
	for _, fact := range desiredFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		serviceName := strings.SplitN(relativePath, "/", 2)[0]
		uniqueServiceNames[serviceName] = true
	}
	sortedServiceNames := make([]string, 0, len(uniqueServiceNames))
	for serviceName := range uniqueServiceNames {
		sortedServiceNames = append(sortedServiceNames, serviceName)
	}
	sort.Strings(sortedServiceNames)
	return sortedServiceNames
}

// collectServiceStatusFromStore reads each service definition and counts
// running instances to produce the service status list.
func collectServiceStatusFromStore(ctx context.Context, factStore store.StateStore, sortedServiceNames []string, allInstances []types.Instance) []ServiceStatus {
	var serviceStatusList []ServiceStatus
	for _, serviceName := range sortedServiceNames {
		service, err := types.ReadService(ctx, factStore, serviceName)
		if err != nil {
			continue
		}
		runningInstanceCount := 0
		for _, instance := range allInstances {
			if instance.Service == serviceName && instance.State == types.InstanceRunning {
				runningInstanceCount++
			}
		}
		serviceStatusList = append(serviceStatusList, ServiceStatus{
			Name: service.Name, Image: service.Image, DesiredCount: service.Instances,
			RunningCount: runningInstanceCount, ExposedPorts: service.Ports,
		})
	}
	return serviceStatusList
}

// collectInstanceStatusFromStore builds an InstanceStatus for every non-stopped
// instance, enriching it with placement, health, resource usage, init phase,
// restart count, and probe states read from the fact store.
func collectInstanceStatusFromStore(ctx context.Context, factStore store.StateStore, allInstances []types.Instance) []InstanceStatus {
	var instanceStatusList []InstanceStatus
	for _, instance := range allInstances {
		if instance.State == types.InstanceStopped {
			continue
		}
		placedNodeID := ""
		if placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); err == nil {
			placedNodeID = string(placementFact.Value)
		}
		healthDisplay := string(instance.Health)
		if healthDisplay == "" {
			healthDisplay = "-"
		}
		instanceIPAddress := instance.IP
		if instanceIPAddress == "" {
			instanceIPAddress = "-"
		}
		instanceCPU := ""
		if cpuFact, cpuErr := factStore.Get(ctx, types.KeyObservedInstanceCPU(instance.ID)); cpuErr == nil {
			instanceCPU = string(cpuFact.Value)
		}
		instanceMemory := ""
		if memFact, memErr := factStore.Get(ctx, types.KeyObservedInstanceMemory(instance.ID)); memErr == nil {
			instanceMemory = string(memFact.Value)
		}
		initPhase := ""
		if initFact, initErr := factStore.Get(ctx, types.KeyDerivedInstanceInitPhase(instance.ID)); initErr == nil {
			initPhase = string(initFact.Value)
		}
		restartCount := ""
		if restartFact, restartErr := factStore.Get(ctx, types.KeyObservedInstanceRestarts(instance.ID)); restartErr == nil {
			restartCount = string(restartFact.Value)
		}
		startupProbe := ""
		if startupFact, startupErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instance.ID, types.ProbeStartup)); startupErr == nil {
			startupProbe = string(startupFact.Value)
		}
		livenessProbe := ""
		if livenessFact, livenessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instance.ID, types.ProbeLiveness)); livenessErr == nil {
			livenessProbe = string(livenessFact.Value)
		}
		readinessProbe := ""
		if readinessFact, readinessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instance.ID, types.ProbeReadiness)); readinessErr == nil {
			readinessProbe = string(readinessFact.Value)
		}
		instanceStatusList = append(instanceStatusList, InstanceStatus{
			ID: instance.ID, ServiceName: instance.Service, State: string(instance.State),
			NodeID: placedNodeID, IPAddress: instanceIPAddress, HealthState: healthDisplay,
			CPUMillis: instanceCPU, MemoryBytes: instanceMemory,
			InitPhase: initPhase, Restarts: restartCount,
			Startup: startupProbe, Liveness: livenessProbe, Readiness: readinessProbe,
		})
	}
	return instanceStatusList
}

// collectNetworkingStatusFromStore reads VIP assignments and DNS mappings from
// the fact store and assembles a sorted list of per-service network status
// entries.
func collectNetworkingStatusFromStore(ctx context.Context, factStore store.StateStore) []NetworkStatus {
	vipFacts, _ := factStore.Scan(ctx, types.ScanNetworkVIPs)
	dnsFacts, _ := factStore.Scan(ctx, types.ScanNetworkDNS)
	dnsMapping := make(map[string]string)
	for _, dnsFact := range dnsFacts {
		serviceName := strings.TrimPrefix(dnsFact.Key, types.ScanNetworkDNS)
		dnsMapping[serviceName] = string(dnsFact.Value)
	}
	vipByService := make(map[string]string)
	vipPortByService := make(map[string]int)
	for _, vipFact := range vipFacts {
		relativePath := strings.TrimPrefix(vipFact.Key, types.ScanNetworkVIPs)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 1 {
			vipByService[pathParts[0]] = string(vipFact.Value)
		} else if len(pathParts) == 2 && pathParts[1] == "port" {
			portValue, parsedOK := types.ParseFactInt(vipFact.Key, string(vipFact.Value))
			if !parsedOK {
				continue
			}
			vipPortByService[pathParts[0]] = portValue
		}
	}
	var networkingStatusList []NetworkStatus
	for serviceName, vipAddress := range vipByService {
		dnsName := serviceName + "." + network.DefaultDNSDomain
		networkingStatusList = append(networkingStatusList, NetworkStatus{
			ServiceName: serviceName,
			VIP:         vipAddress,
			Port:        vipPortByService[serviceName],
			DNS:         dnsName,
		})
	}
	sort.Slice(networkingStatusList, func(i, j int) bool {
		return networkingStatusList[i].ServiceName < networkingStatusList[j].ServiceName
	})
	return networkingStatusList
}

// collectVolumeStatusFromStore reads observed volume facts from the store and
// returns a sorted list of volume status entries.
func collectVolumeStatusFromStore(ctx context.Context, factStore store.StateStore) []VolumeStatus {
	allVolumes, _ := types.ListObservedVolumes(ctx, factStore)
	sort.Slice(allVolumes, func(i, j int) bool { return allVolumes[i].Name < allVolumes[j].Name })
	var volumeStatusList []VolumeStatus
	for _, volume := range allVolumes {
		volumeStatusList = append(volumeStatusList, VolumeStatus{
			Name:          volume.Name,
			Size:          volume.Size,
			State:         string(volume.State),
			Node:          volume.Node,
			Instance:      volume.Instance,
			MountPath:     volume.MountPath,
			UsedBytes:     volume.UsedBytes,
			CapacityBytes: volume.CapacityBytes,
		})
	}
	return volumeStatusList
}

// collectSecretStatusFromStore scans the encrypted secret store for secret
// names and resolves per-secret service grants, returning a sorted list.
func collectSecretStatusFromStore(ctx context.Context, factStore store.StateStore, sortedServiceNames []string) []SecretStatus {
	secretFacts, _ := factStore.Scan(ctx, security.SecretStorePrefix)
	if len(secretFacts) == 0 {
		return nil
	}
	var secretStatusList []SecretStatus
	for _, secretFact := range secretFacts {
		secretName := strings.TrimPrefix(secretFact.Key, security.SecretStorePrefix)
		var grantedServiceNames []string
		for _, serviceName := range sortedServiceNames {
			grantKey := types.KeyDesiredServiceSecret(serviceName, secretName)
			if _, err := factStore.Get(ctx, grantKey); err == nil {
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

// collectConfigStatusFromStore iterates over all known services and scans
// their config prefixes to collect environment variable and file config
// entries.
func collectConfigStatusFromStore(ctx context.Context, factStore store.StateStore, sortedServiceNames []string) []ConfigStatus {
	var configStatusList []ConfigStatus
	for _, serviceName := range sortedServiceNames {
		configFacts, _ := factStore.Scan(ctx, types.ScanDesiredServiceConfig(serviceName))
		for _, configFact := range configFacts {
			relativePath := strings.TrimPrefix(configFact.Key, types.ScanDesiredServiceConfig(serviceName))
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
				Value:   string(configFact.Value),
			})
		}
	}
	return configStatusList
}

// collectNodeStatusFromStore reads all registered nodes, counts their placed
// instances, and enriches each node entry with CPU and memory utilization
// from observed facts.
func collectNodeStatusFromStore(ctx context.Context, factStore store.StateStore, allInstances []types.Instance) []NodeStatus {
	allNodes, _ := types.ListNodes(ctx, factStore)
	sort.Slice(allNodes, func(i, j int) bool { return allNodes[i].ID < allNodes[j].ID })
	var nodeStatusList []NodeStatus
	for _, node := range allNodes {
		placedInstanceCount := 0
		for _, instance := range allInstances {
			if instance.State == types.InstanceStopped {
				continue
			}
			if placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); err == nil && string(placementFact.Value) == node.ID {
				placedInstanceCount++
			}
		}
		utilizationCPU := ""
		if cpuUtilFact, cpuErr := factStore.Get(ctx, types.KeyObservedNodeUtilizationCPU(node.ID)); cpuErr == nil {
			utilizationCPU = string(cpuUtilFact.Value)
		}
		utilizationMemory := ""
		if memUtilFact, memErr := factStore.Get(ctx, types.KeyObservedNodeUtilizationMemory(node.ID)); memErr == nil {
			utilizationMemory = string(memUtilFact.Value)
		}
		nodeStatusList = append(nodeStatusList, NodeStatus{
			ID: node.ID, State: string(node.State), PlacedInstances: placedInstanceCount,
			AvailableCPU: node.AvailableCPU, CapacityCPU: node.CapacityCPU,
			AvailableMemory: node.AvailableMemory, CapacityMemory: node.CapacityMemory,
			UtilizationCPU: utilizationCPU, UtilizationMemory: utilizationMemory,
		})
	}
	return nodeStatusList
}

// collectCloudIdentityStatusFromStore scans cloud identity declarations and
// resolves which services are bound to each identity, returning a sorted list.
func collectCloudIdentityStatusFromStore(ctx context.Context, factStore store.StateStore) []CloudIdentityStatus {
	identityFacts, _ := factStore.Scan(ctx, types.ScanDesiredCloudIdentities)
	identityMap := make(map[string]*CloudIdentityStatus)
	for _, fact := range identityFacts {
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
	serviceFacts, _ := factStore.Scan(ctx, "desired/service/")
	for _, fact := range serviceFacts {
		remainder := strings.TrimPrefix(fact.Key, "desired/service/")
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

func appendUniqueString(slice []string, value string) []string {
	for _, existing := range slice {
		if existing == value {
			return slice
		}
	}
	return append(slice, value)
}
