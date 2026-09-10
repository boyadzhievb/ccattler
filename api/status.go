package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// ClusterStatus is the structured representation of the full cluster state.
type ClusterStatus struct {
	Services   []ServiceStatus  `json:"services"`
	Instances  []InstanceStatus `json:"instances"`
	Nodes      []NodeStatus     `json:"nodes"`
	Networking []NetworkStatus  `json:"networking,omitempty"`
	Volumes    []VolumeStatus   `json:"volumes,omitempty"`
	Secrets    []SecretStatus   `json:"secrets,omitempty"`
	Config     []ConfigStatus   `json:"config,omitempty"`
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
	Name      string `json:"name"`
	Size      string `json:"size"`
	State     string `json:"state"`
	Node      string `json:"node,omitempty"`
	Instance  string `json:"instance,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
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

// buildStatusFromStore collects the full cluster state from the fact store
// and assembles it into a structured ClusterStatus.
func buildStatusFromStore(ctx context.Context, factStore store.StateStore) ClusterStatus {
	var clusterStatus ClusterStatus

	allInstances, _ := types.ListInstances(ctx, factStore)
	sort.Slice(allInstances, func(i, j int) bool { return allInstances[i].ID < allInstances[j].ID })

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
		clusterStatus.Services = append(clusterStatus.Services, ServiceStatus{
			Name: service.Name, Image: service.Image, DesiredCount: service.Instances,
			RunningCount: runningInstanceCount, ExposedPorts: service.Ports,
		})
	}

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
		if initFact, initErr := factStore.Get(ctx, types.KeyObservedInstanceInitPhase(instance.ID)); initErr == nil {
			initPhase = string(initFact.Value)
		}
		restartCount := ""
		if restartFact, restartErr := factStore.Get(ctx, types.KeyObservedInstanceRestarts(instance.ID)); restartErr == nil {
			restartCount = string(restartFact.Value)
		}
		startupProbe := ""
		if startupFact, startupErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instance.ID, "startup")); startupErr == nil {
			startupProbe = string(startupFact.Value)
		}
		livenessProbe := ""
		if livenessFact, livenessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instance.ID, "liveness")); livenessErr == nil {
			livenessProbe = string(livenessFact.Value)
		}
		readinessProbe := ""
		if readinessFact, readinessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instance.ID, "readiness")); readinessErr == nil {
			readinessProbe = string(readinessFact.Value)
		}
		clusterStatus.Instances = append(clusterStatus.Instances, InstanceStatus{
			ID: instance.ID, ServiceName: instance.Service, State: string(instance.State),
			NodeID: placedNodeID, IPAddress: instanceIPAddress, HealthState: healthDisplay,
			CPUMillis: instanceCPU, MemoryBytes: instanceMemory,
			InitPhase: initPhase, Restarts: restartCount,
			Startup: startupProbe, Liveness: livenessProbe, Readiness: readinessProbe,
		})
	}

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
			portValue := 0
			fmt.Sscanf(string(vipFact.Value), "%d", &portValue)
			vipPortByService[pathParts[0]] = portValue
		}
	}
	for serviceName, vipAddress := range vipByService {
		dnsName := serviceName + "." + network.DefaultDNSDomain
		clusterStatus.Networking = append(clusterStatus.Networking, NetworkStatus{
			ServiceName: serviceName,
			VIP:         vipAddress,
			Port:        vipPortByService[serviceName],
			DNS:         dnsName,
		})
	}
	sort.Slice(clusterStatus.Networking, func(i, j int) bool {
		return clusterStatus.Networking[i].ServiceName < clusterStatus.Networking[j].ServiceName
	})

	allVolumes, _ := types.ListObservedVolumes(ctx, factStore)
	sort.Slice(allVolumes, func(i, j int) bool { return allVolumes[i].Name < allVolumes[j].Name })
	for _, volume := range allVolumes {
		clusterStatus.Volumes = append(clusterStatus.Volumes, VolumeStatus{
			Name:      volume.Name,
			Size:      volume.Size,
			State:     string(volume.State),
			Node:      volume.Node,
			Instance:  volume.Instance,
			MountPath: volume.MountPath,
		})
	}

	// Collect secrets: scan secret store for names, then find grants per secret.
	secretFacts, _ := factStore.Scan(ctx, security.SecretStorePrefix)
	if len(secretFacts) > 0 {
		for _, secretFact := range secretFacts {
			secretName := strings.TrimPrefix(secretFact.Key, security.SecretStorePrefix)
			var grantedServiceNames []string
			for _, serviceName := range sortedServiceNames {
				grantKey := types.KeyDesiredServiceSecret(serviceName, secretName)
				if _, err := factStore.Get(ctx, grantKey); err == nil {
					grantedServiceNames = append(grantedServiceNames, serviceName)
				}
			}
			clusterStatus.Secrets = append(clusterStatus.Secrets, SecretStatus{
				Name:      secretName,
				GrantedTo: grantedServiceNames,
			})
		}
		sort.Slice(clusterStatus.Secrets, func(i, j int) bool {
			return clusterStatus.Secrets[i].Name < clusterStatus.Secrets[j].Name
		})
	}

	// Collect config entries (env vars and files) for each service.
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
			clusterStatus.Config = append(clusterStatus.Config, ConfigStatus{
				Service: serviceName,
				Type:    configType,
				Key:     configKey,
				Value:   string(configFact.Value),
			})
		}
	}

	allNodes, _ := types.ListNodes(ctx, factStore)
	sort.Slice(allNodes, func(i, j int) bool { return allNodes[i].ID < allNodes[j].ID })
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
		clusterStatus.Nodes = append(clusterStatus.Nodes, NodeStatus{
			ID: node.ID, State: string(node.State), PlacedInstances: placedInstanceCount,
			AvailableCPU: node.AvailableCPU, CapacityCPU: node.CapacityCPU,
			AvailableMemory: node.AvailableMemory, CapacityMemory: node.CapacityMemory,
			UtilizationCPU: utilizationCPU, UtilizationMemory: utilizationMemory,
		})
	}

	return clusterStatus
}
