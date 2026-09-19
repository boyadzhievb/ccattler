package api

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// ServiceDescribe is the detailed view of a single service, aggregating all
// related facts: instances, endpoints, networking, health, placement,
// autoscaling, config, secrets, init steps, rollout state, and recent events.
type ServiceDescribe struct {
	Name             string                    `json:"name"`
	Image            string                    `json:"image"`
	DesiredInstances int                       `json:"desired_instances"`
	RunningInstances int                       `json:"running_instances"`
	ExposedPorts     []int                     `json:"ports,omitempty"`
	Resources        *DescribeResources        `json:"resources,omitempty"`
	Placement        *DescribePlacement        `json:"placement,omitempty"`
	Health           *DescribeHealthConfig      `json:"health,omitempty"`
	Probes           []DescribeProbe           `json:"probes,omitempty"`
	InitSteps        []DescribeInitStep        `json:"init_steps,omitempty"`
	Autoscaling      *DescribeAutoscaling      `json:"autoscaling,omitempty"`
	UpdateStrategy   *DescribeUpdateStrategy   `json:"update_strategy,omitempty"`
	Rollout          *DescribeRollout          `json:"rollout,omitempty"`
	Networking       *DescribeServiceNetwork   `json:"networking,omitempty"`
	Config           []ConfigStatus            `json:"config,omitempty"`
	Secrets          []DescribeSecretGrant     `json:"secrets,omitempty"`
	Instances        []DescribeInstanceSummary `json:"instances"`
	Endpoints        []string                  `json:"endpoints,omitempty"`
	Events           []DescribeEvent           `json:"events,omitempty"`
}

// NodeDescribe is the detailed view of a single node, aggregating all related
// facts: capacity, utilization, labels, restrictions, placed instances, lease
// state, address, and recent events.
type NodeDescribe struct {
	ID                string                    `json:"id"`
	State             string                    `json:"state"`
	Address           string                    `json:"address,omitempty"`
	Architecture      string                    `json:"architecture,omitempty"`
	Zone              string                    `json:"zone,omitempty"`
	CapacityCPU       int64                     `json:"capacity_cpu"`
	CapacityMemory    int64                     `json:"capacity_memory"`
	AvailableCPU      int64                     `json:"available_cpu"`
	AvailableMemory   int64                     `json:"available_memory"`
	UtilizationCPU    string                    `json:"utilization_cpu,omitempty"`
	UtilizationMemory string                    `json:"utilization_memory,omitempty"`
	WorkloadCount     string                    `json:"workload_count,omitempty"`
	Subnet            string                    `json:"subnet,omitempty"`
	Labels            map[string]string         `json:"labels,omitempty"`
	Restrictions      []string                  `json:"restrictions,omitempty"`
	Instances         []DescribeInstanceSummary `json:"instances"`
	Events            []DescribeEvent           `json:"events,omitempty"`
}

// InstanceDescribe is the detailed view of a single instance, aggregating all
// related facts: service, node, state, health, probes, init phase, resources,
// networking, and recent events.
type InstanceDescribe struct {
	ID            string          `json:"id"`
	ServiceName   string          `json:"service"`
	NodeID        string          `json:"node"`
	State         string          `json:"state"`
	Image         string          `json:"image,omitempty"`
	IPAddress     string          `json:"ip,omitempty"`
	HostPort      string          `json:"host_port,omitempty"`
	HealthState   string          `json:"health,omitempty"`
	CPUMillis     string          `json:"cpu,omitempty"`
	MemoryBytes   string          `json:"memory,omitempty"`
	InitPhase     string          `json:"init_phase,omitempty"`
	Restarts      string          `json:"restarts,omitempty"`
	StartupProbe  string          `json:"startup,omitempty"`
	LivenessProbe string          `json:"liveness,omitempty"`
	ReadinessProbe string         `json:"readiness,omitempty"`
	Endpoint      string          `json:"endpoint,omitempty"`
	Events        []DescribeEvent `json:"events,omitempty"`
}

// DescribeResources shows a service's CPU and memory resource requirements.
type DescribeResources struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// DescribePlacement shows a service's placement constraints and preferences.
type DescribePlacement struct {
	Architecture string            `json:"architecture,omitempty"`
	ZonePolicy   string            `json:"zone_policy,omitempty"`
	Require      map[string]string `json:"require,omitempty"`
	Prefer       map[string]string `json:"prefer,omitempty"`
	Accept       []string          `json:"accept,omitempty"`
}

// DescribeHealthConfig shows a service's legacy health check configuration.
type DescribeHealthConfig struct {
	Method   string `json:"method"`
	Path     string `json:"path,omitempty"`
	Interval string `json:"interval,omitempty"`
}

// DescribeProbe shows one probe type's configuration for a service.
type DescribeProbe struct {
	Type             string `json:"type"`
	Method           string `json:"method"`
	Path             string `json:"path,omitempty"`
	Port             string `json:"port,omitempty"`
	Interval         string `json:"interval,omitempty"`
	Timeout          string `json:"timeout,omitempty"`
	FailureThreshold string `json:"failure_threshold,omitempty"`
	SuccessThreshold string `json:"success_threshold,omitempty"`
	InitialDelay     string `json:"initial_delay,omitempty"`
}

// DescribeInitStep shows one init step's configuration for a service.
type DescribeInitStep struct {
	Index   int    `json:"index"`
	Exec    string `json:"exec"`
	Timeout string `json:"timeout,omitempty"`
	Retry   string `json:"retry,omitempty"`
}

// DescribeAutoscaling shows a service's autoscaling configuration.
type DescribeAutoscaling struct {
	Min     int                    `json:"min"`
	Max     int                    `json:"max"`
	Targets []DescribeScaleTarget  `json:"targets,omitempty"`
}

// DescribeScaleTarget shows one autoscaling metric target.
type DescribeScaleTarget struct {
	Metric string `json:"metric"`
	Value  int    `json:"value"`
}

// DescribeUpdateStrategy shows rolling update parameters.
type DescribeUpdateStrategy struct {
	MaxUnavailable string `json:"max_unavailable"`
	MaxExtra       string `json:"max_extra"`
}

// DescribeRollout shows the current rollout state for a service.
type DescribeRollout struct {
	State         string `json:"state"`
	PreviousImage string `json:"previous_image,omitempty"`
	Failures      string `json:"failures,omitempty"`
}

// DescribeServiceNetwork shows networking info for a service.
type DescribeServiceNetwork struct {
	VIP  string `json:"vip,omitempty"`
	Port int    `json:"port,omitempty"`
	DNS  string `json:"dns,omitempty"`
}

// DescribeSecretGrant shows a secret grant for a service.
type DescribeSecretGrant struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
}

// DescribeInstanceSummary is a compact instance view used in service and node describe.
type DescribeInstanceSummary struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	NodeID    string `json:"node,omitempty"`
	Service   string `json:"service,omitempty"`
	IPAddress string `json:"ip,omitempty"`
	Health    string `json:"health,omitempty"`
	Restarts  string `json:"restarts,omitempty"`
}

// DescribeEvent is a compact event representation for describe output.
type DescribeEvent struct {
	Timestamp string `json:"timestamp"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
	Source    string `json:"source,omitempty"`
}

// buildServiceDescribe aggregates all facts related to a single service.
func buildServiceDescribe(ctx context.Context, factStore store.StateStore, eventLog *types.EventLog, serviceName string) (*ServiceDescribe, error) {
	service, err := types.ReadService(ctx, factStore, serviceName)
	if err != nil {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}

	describe := &ServiceDescribe{
		Name:             service.Name,
		Image:            service.Image,
		DesiredInstances: service.Instances,
		ExposedPorts:     service.Ports,
	}

	if service.CPU != "" || service.Memory != "" {
		describe.Resources = &DescribeResources{CPU: service.CPU, Memory: service.Memory}
	}

	describe.Placement = buildServicePlacement(ctx, factStore, serviceName)
	describe.Health = buildServiceHealth(ctx, factStore, serviceName)
	describe.Probes = buildServiceProbes(ctx, factStore, serviceName)
	describe.InitSteps = buildServiceInitSteps(ctx, factStore, serviceName)
	describe.Autoscaling = buildServiceAutoscaling(ctx, factStore, serviceName)
	describe.UpdateStrategy = buildServiceUpdateStrategy(ctx, factStore, serviceName)
	describe.Rollout = buildServiceRollout(ctx, factStore, serviceName)
	describe.Networking = buildServiceNetworking(ctx, factStore, serviceName)
	describe.Config = buildServiceConfig(ctx, factStore, serviceName)
	describe.Secrets = buildServiceSecrets(ctx, factStore, serviceName)

	allInstances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range allInstances {
		if instance.Service != serviceName || instance.State == types.InstanceStopped {
			continue
		}
		if instance.State == types.InstanceRunning {
			describe.RunningInstances++
		}
		placedNode := ""
		if placementFact, placementErr := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); placementErr == nil {
			placedNode = string(placementFact.Value)
		}
		healthDisplay := string(instance.Health)
		if healthDisplay == "" {
			healthDisplay = "-"
		}
		restartCount := ""
		if restartFact, restartErr := factStore.Get(ctx, types.KeyObservedInstanceRestarts(instance.ID)); restartErr == nil {
			restartCount = string(restartFact.Value)
		}
		ipDisplay := instance.IP
		if ipDisplay == "" {
			ipDisplay = "-"
		}
		describe.Instances = append(describe.Instances, DescribeInstanceSummary{
			ID:        instance.ID,
			State:     string(instance.State),
			NodeID:    placedNode,
			IPAddress: ipDisplay,
			Health:    healthDisplay,
			Restarts:  restartCount,
		})
	}
	sort.Slice(describe.Instances, func(i, j int) bool {
		return describe.Instances[i].ID < describe.Instances[j].ID
	})

	endpointFacts, _ := factStore.Scan(ctx, fmt.Sprintf("%s/service/%s/", types.PrefixEndpoint, serviceName))
	for _, endpointFact := range endpointFacts {
		describe.Endpoints = append(describe.Endpoints, string(endpointFact.Value))
	}

	if eventLog != nil {
		recentEvents, _ := eventLog.ForTarget(ctx, serviceName, 10)
		for _, event := range recentEvents {
			describe.Events = append(describe.Events, DescribeEvent{
				Timestamp: event.Timestamp.Format("2006-01-02 15:04:05"),
				Kind:      event.Kind,
				Detail:    event.Detail,
				Source:    event.Source,
			})
		}
	}

	return describe, nil
}

// buildNodeDescribe aggregates all facts related to a single node.
func buildNodeDescribe(ctx context.Context, factStore store.StateStore, eventLog *types.EventLog, nodeID string) (*NodeDescribe, error) {
	node, err := types.ReadNode(ctx, factStore, nodeID)
	if err != nil {
		return nil, fmt.Errorf("node %q not found", nodeID)
	}

	describe := &NodeDescribe{
		ID:              node.ID,
		State:           string(node.State),
		Architecture:    node.Architecture,
		Zone:            node.Zone,
		CapacityCPU:     node.CapacityCPU,
		CapacityMemory:  node.CapacityMemory,
		AvailableCPU:    node.AvailableCPU,
		AvailableMemory: node.AvailableMemory,
	}

	if addressFact, addressErr := factStore.Get(ctx, types.KeyObservedNodeAddress(nodeID)); addressErr == nil {
		describe.Address = string(addressFact.Value)
	}
	if cpuUtilFact, cpuErr := factStore.Get(ctx, types.KeyObservedNodeUtilizationCPU(nodeID)); cpuErr == nil {
		describe.UtilizationCPU = string(cpuUtilFact.Value)
	}
	if memUtilFact, memErr := factStore.Get(ctx, types.KeyObservedNodeUtilizationMemory(nodeID)); memErr == nil {
		describe.UtilizationMemory = string(memUtilFact.Value)
	}
	if workloadFact, workloadErr := factStore.Get(ctx, types.KeyObservedNodeWorkloadCount(nodeID)); workloadErr == nil {
		describe.WorkloadCount = string(workloadFact.Value)
	}
	if subnetFact, subnetErr := factStore.Get(ctx, types.KeyNetworkNodeSubnet(nodeID)); subnetErr == nil {
		describe.Subnet = string(subnetFact.Value)
	}

	labelPrefix := fmt.Sprintf("%s/node/%s/label/", types.PrefixObserved, nodeID)
	labelFacts, _ := factStore.Scan(ctx, labelPrefix)
	if len(labelFacts) > 0 {
		describe.Labels = make(map[string]string)
		for _, labelFact := range labelFacts {
			labelName := strings.TrimPrefix(labelFact.Key, labelPrefix)
			describe.Labels[labelName] = string(labelFact.Value)
		}
	}

	restrictPrefix := fmt.Sprintf("%s/node/%s/restrict/", types.PrefixObserved, nodeID)
	restrictFacts, _ := factStore.Scan(ctx, restrictPrefix)
	for _, restrictFact := range restrictFacts {
		restrictLabel := strings.TrimPrefix(restrictFact.Key, restrictPrefix)
		describe.Restrictions = append(describe.Restrictions, restrictLabel)
	}

	allInstances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range allInstances {
		if instance.State == types.InstanceStopped {
			continue
		}
		placedNode := ""
		if placementFact, placementErr := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); placementErr == nil {
			placedNode = string(placementFact.Value)
		}
		if placedNode != nodeID {
			continue
		}
		healthDisplay := string(instance.Health)
		if healthDisplay == "" {
			healthDisplay = "-"
		}
		restartCount := ""
		if restartFact, restartErr := factStore.Get(ctx, types.KeyObservedInstanceRestarts(instance.ID)); restartErr == nil {
			restartCount = string(restartFact.Value)
		}
		ipDisplay := instance.IP
		if ipDisplay == "" {
			ipDisplay = "-"
		}
		describe.Instances = append(describe.Instances, DescribeInstanceSummary{
			ID:        instance.ID,
			State:     string(instance.State),
			Service:   instance.Service,
			IPAddress: ipDisplay,
			Health:    healthDisplay,
			Restarts:  restartCount,
		})
	}
	sort.Slice(describe.Instances, func(i, j int) bool {
		return describe.Instances[i].ID < describe.Instances[j].ID
	})

	if eventLog != nil {
		recentEvents, _ := eventLog.ForTarget(ctx, nodeID, 10)
		for _, event := range recentEvents {
			describe.Events = append(describe.Events, DescribeEvent{
				Timestamp: event.Timestamp.Format("2006-01-02 15:04:05"),
				Kind:      event.Kind,
				Detail:    event.Detail,
				Source:    event.Source,
			})
		}
	}

	return describe, nil
}

// buildInstanceDescribe aggregates all facts related to a single instance.
func buildInstanceDescribe(ctx context.Context, factStore store.StateStore, eventLog *types.EventLog, instanceID string) (*InstanceDescribe, error) {
	instance, err := types.ReadInstance(ctx, factStore, instanceID)
	if err != nil {
		return nil, fmt.Errorf("instance %q not found", instanceID)
	}

	placedNode := ""
	if placementFact, placementErr := factStore.Get(ctx, types.KeyPlacementInstance(instanceID)); placementErr == nil {
		placedNode = string(placementFact.Value)
	}

	describe := &InstanceDescribe{
		ID:          instance.ID,
		ServiceName: instance.Service,
		NodeID:      placedNode,
		State:       string(instance.State),
		Image:       instance.Image,
		IPAddress:   instance.IP,
		HealthState: string(instance.Health),
	}

	if hostPortFact, hostPortErr := factStore.Get(ctx, types.KeyObservedInstanceHostPort(instanceID)); hostPortErr == nil {
		describe.HostPort = string(hostPortFact.Value)
	}
	if cpuFact, cpuErr := factStore.Get(ctx, types.KeyObservedInstanceCPU(instanceID)); cpuErr == nil {
		describe.CPUMillis = string(cpuFact.Value)
	}
	if memFact, memErr := factStore.Get(ctx, types.KeyObservedInstanceMemory(instanceID)); memErr == nil {
		describe.MemoryBytes = string(memFact.Value)
	}
	if initFact, initErr := factStore.Get(ctx, types.KeyObservedInstanceInitPhase(instanceID)); initErr == nil {
		describe.InitPhase = string(initFact.Value)
	}
	if restartFact, restartErr := factStore.Get(ctx, types.KeyObservedInstanceRestarts(instanceID)); restartErr == nil {
		describe.Restarts = string(restartFact.Value)
	}
	if startupFact, startupErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "startup")); startupErr == nil {
		describe.StartupProbe = string(startupFact.Value)
	}
	if livenessFact, livenessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "liveness")); livenessErr == nil {
		describe.LivenessProbe = string(livenessFact.Value)
	}
	if readinessFact, readinessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "readiness")); readinessErr == nil {
		describe.ReadinessProbe = string(readinessFact.Value)
	}

	endpointKey := types.KeyEndpoint(instance.Service, instanceID)
	if endpointFact, endpointErr := factStore.Get(ctx, endpointKey); endpointErr == nil {
		describe.Endpoint = string(endpointFact.Value)
	}

	if eventLog != nil {
		recentEvents, _ := eventLog.ForTarget(ctx, instanceID, 10)
		for _, event := range recentEvents {
			describe.Events = append(describe.Events, DescribeEvent{
				Timestamp: event.Timestamp.Format("2006-01-02 15:04:05"),
				Kind:      event.Kind,
				Detail:    event.Detail,
				Source:    event.Source,
			})
		}
	}

	return describe, nil
}

// buildServicePlacement reads placement constraints for a service.
func buildServicePlacement(ctx context.Context, factStore store.StateStore, serviceName string) *DescribePlacement {
	placement := &DescribePlacement{}
	hasPlacement := false

	if archFact, err := factStore.Get(ctx, types.KeyDesiredServicePlacementArchitecture(serviceName)); err == nil {
		placement.Architecture = string(archFact.Value)
		hasPlacement = true
	}
	if zoneFact, err := factStore.Get(ctx, types.KeyDesiredServicePlacementZonePolicy(serviceName)); err == nil {
		placement.ZonePolicy = string(zoneFact.Value)
		hasPlacement = true
	}

	requirePrefix := fmt.Sprintf("%s/service/%s/placement/require/", types.PrefixDesired, serviceName)
	requireFacts, _ := factStore.Scan(ctx, requirePrefix)
	if len(requireFacts) > 0 {
		placement.Require = make(map[string]string)
		for _, fact := range requireFacts {
			label := strings.TrimPrefix(fact.Key, requirePrefix)
			placement.Require[label] = string(fact.Value)
		}
		hasPlacement = true
	}

	preferPrefix := fmt.Sprintf("%s/service/%s/placement/prefer/", types.PrefixDesired, serviceName)
	preferFacts, _ := factStore.Scan(ctx, preferPrefix)
	if len(preferFacts) > 0 {
		placement.Prefer = make(map[string]string)
		for _, fact := range preferFacts {
			label := strings.TrimPrefix(fact.Key, preferPrefix)
			placement.Prefer[label] = string(fact.Value)
		}
		hasPlacement = true
	}

	acceptPrefix := fmt.Sprintf("%s/service/%s/placement/accept/", types.PrefixDesired, serviceName)
	acceptFacts, _ := factStore.Scan(ctx, acceptPrefix)
	for _, fact := range acceptFacts {
		label := strings.TrimPrefix(fact.Key, acceptPrefix)
		placement.Accept = append(placement.Accept, label)
		hasPlacement = true
	}

	if !hasPlacement {
		return nil
	}
	return placement
}

// buildServiceHealth reads the legacy health check configuration.
func buildServiceHealth(ctx context.Context, factStore store.StateStore, serviceName string) *DescribeHealthConfig {
	methodFact, err := factStore.Get(ctx, types.KeyDesiredServiceHealthMethod(serviceName))
	if err != nil {
		return nil
	}
	health := &DescribeHealthConfig{Method: string(methodFact.Value)}
	if pathFact, pathErr := factStore.Get(ctx, types.KeyDesiredServiceHealthPath(serviceName)); pathErr == nil {
		health.Path = string(pathFact.Value)
	}
	if intervalFact, intervalErr := factStore.Get(ctx, types.KeyDesiredServiceHealthInterval(serviceName)); intervalErr == nil {
		health.Interval = string(intervalFact.Value)
	}
	return health
}

// buildServiceProbes reads startup, liveness, and readiness probe configurations.
func buildServiceProbes(ctx context.Context, factStore store.StateStore, serviceName string) []DescribeProbe {
	var probes []DescribeProbe
	for _, probeType := range []string{"startup", "liveness", "readiness"} {
		probeFacts, _ := factStore.Scan(ctx, types.ScanDesiredServiceProbe(serviceName, probeType))
		if len(probeFacts) == 0 {
			continue
		}
		probe := DescribeProbe{Type: probeType}
		for _, fact := range probeFacts {
			field := strings.TrimPrefix(fact.Key, types.ScanDesiredServiceProbe(serviceName, probeType))
			value := string(fact.Value)
			switch field {
			case "method":
				probe.Method = value
			case "path":
				probe.Path = value
			case "port":
				probe.Port = value
			case "interval":
				probe.Interval = value
			case "timeout":
				probe.Timeout = value
			case "failure_threshold":
				probe.FailureThreshold = value
			case "success_threshold":
				probe.SuccessThreshold = value
			case "initial_delay":
				probe.InitialDelay = value
			}
		}
		probes = append(probes, probe)
	}
	return probes
}

// buildServiceInitSteps reads init step configurations for a service.
func buildServiceInitSteps(ctx context.Context, factStore store.StateStore, serviceName string) []DescribeInitStep {
	initFacts, _ := factStore.Scan(ctx, types.ScanDesiredServiceInitSteps(serviceName))
	if len(initFacts) == 0 {
		return nil
	}

	stepMap := make(map[int]*DescribeInitStep)
	for _, fact := range initFacts {
		relative := strings.TrimPrefix(fact.Key, types.ScanDesiredServiceInitSteps(serviceName))
		parts := strings.SplitN(relative, "/", 2)
		stepIndex, indexErr := strconv.Atoi(parts[0])
		if indexErr != nil {
			continue
		}
		if _, exists := stepMap[stepIndex]; !exists {
			stepMap[stepIndex] = &DescribeInitStep{Index: stepIndex}
		}
		if len(parts) == 2 {
			value := string(fact.Value)
			switch parts[1] {
			case "exec":
				stepMap[stepIndex].Exec = value
			case "timeout":
				stepMap[stepIndex].Timeout = value
			case "retry":
				stepMap[stepIndex].Retry = value
			}
		}
	}

	steps := make([]DescribeInitStep, 0, len(stepMap))
	for _, step := range stepMap {
		steps = append(steps, *step)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Index < steps[j].Index })
	return steps
}

// buildServiceAutoscaling reads horizontal autoscaling configuration.
func buildServiceAutoscaling(ctx context.Context, factStore store.StateStore, serviceName string) *DescribeAutoscaling {
	scalePolicy, err := types.ReadScalePolicy(ctx, factStore, serviceName)
	if err != nil || scalePolicy == nil {
		return nil
	}
	autoscaling := &DescribeAutoscaling{
		Min: scalePolicy.Min,
		Max: scalePolicy.Max,
	}
	for _, target := range scalePolicy.Targets {
		autoscaling.Targets = append(autoscaling.Targets, DescribeScaleTarget{
			Metric: target.Metric,
			Value:  target.Value,
		})
	}
	return autoscaling
}

// buildServiceUpdateStrategy reads rolling update parameters.
func buildServiceUpdateStrategy(ctx context.Context, factStore store.StateStore, serviceName string) *DescribeUpdateStrategy {
	maxUnavailableFact, unavailableErr := factStore.Get(ctx, types.KeyDesiredServiceUpdateMaxUnavailable(serviceName))
	maxExtraFact, extraErr := factStore.Get(ctx, types.KeyDesiredServiceUpdateMaxExtra(serviceName))
	if unavailableErr != nil && extraErr != nil {
		return nil
	}
	strategy := &DescribeUpdateStrategy{}
	if unavailableErr == nil {
		strategy.MaxUnavailable = string(maxUnavailableFact.Value)
	}
	if extraErr == nil {
		strategy.MaxExtra = string(maxExtraFact.Value)
	}
	return strategy
}

// buildServiceRollout reads the current rollout state.
func buildServiceRollout(ctx context.Context, factStore store.StateStore, serviceName string) *DescribeRollout {
	stateFact, stateErr := factStore.Get(ctx, types.KeyObservedServiceRolloutState(serviceName))
	if stateErr != nil {
		return nil
	}
	rollout := &DescribeRollout{State: string(stateFact.Value)}
	if imageFact, imageErr := factStore.Get(ctx, types.KeyObservedServiceRolloutImage(serviceName)); imageErr == nil {
		rollout.PreviousImage = string(imageFact.Value)
	}
	if failFact, failErr := factStore.Get(ctx, types.KeyObservedServiceRolloutFailures(serviceName)); failErr == nil {
		rollout.Failures = string(failFact.Value)
	}
	return rollout
}

// buildServiceNetworking reads VIP, port, and DNS for a service.
func buildServiceNetworking(ctx context.Context, factStore store.StateStore, serviceName string) *DescribeServiceNetwork {
	serviceVIP, err := types.ReadServiceVIP(ctx, factStore, serviceName)
	if err != nil {
		return nil
	}
	serviceNetwork := &DescribeServiceNetwork{
		VIP:  serviceVIP.VIP,
		Port: serviceVIP.Port,
		DNS:  serviceName + "." + network.DefaultDNSDomain,
	}
	return serviceNetwork
}

// buildServiceConfig reads config entries (env vars and files) for a service.
func buildServiceConfig(ctx context.Context, factStore store.StateStore, serviceName string) []ConfigStatus {
	configFacts, _ := factStore.Scan(ctx, types.ScanDesiredServiceConfig(serviceName))
	if len(configFacts) == 0 {
		return nil
	}
	var configs []ConfigStatus
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
		configs = append(configs, ConfigStatus{
			Service: serviceName,
			Type:    configType,
			Key:     configKey,
			Value:   string(configFact.Value),
		})
	}
	return configs
}

// buildServiceSecrets reads secret grants for a service.
func buildServiceSecrets(ctx context.Context, factStore store.StateStore, serviceName string) []DescribeSecretGrant {
	secretFacts, _ := factStore.Scan(ctx, types.ScanDesiredServiceSecrets(serviceName))
	if len(secretFacts) == 0 {
		return nil
	}
	var grants []DescribeSecretGrant
	for _, secretFact := range secretFacts {
		secretName := strings.TrimPrefix(secretFact.Key, types.ScanDesiredServiceSecrets(serviceName))
		grants = append(grants, DescribeSecretGrant{
			Name:      secretName,
			MountPath: string(secretFact.Value),
		})
	}
	return grants
}
