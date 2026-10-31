// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// extractTenantFromHierarchicalName derives the owning tenant from a
// hierarchical service name like "payments/checkout" → "payments".
// Returns empty string for flat names without a slash.
func extractTenantFromHierarchicalName(serviceName string) string {
	if slashIndex := strings.IndexByte(serviceName, '/'); slashIndex > 0 {
		return serviceName[:slashIndex]
	}
	return ""
}

// Fact is a key-value pair ready to be written to the store.
type Fact struct {
	Key   string // Key is the fact store key (e.g. "desired/service/web/image").
	Value string // Value is the serialized fact value (may be empty for marker facts).
}

// Compile converts a parsed AST into a list of facts.
func Compile(file *File) ([]Fact, error) {
	return CompileWithSource(file, nil)
}

// CompileWithSource converts a parsed AST into a list of facts, using the
// provided source lines for richer error context in diagnostics.
func CompileWithSource(file *File, sourceLines []string) ([]Fact, error) {
	var facts []Fact
	infrastructureFacts, infrastructureErr := compileInfrastructureDeclarations(file, sourceLines)
	if infrastructureErr != nil {
		return nil, infrastructureErr
	}
	facts = append(facts, infrastructureFacts...)

	connectionDeclsByName := buildConnectionDeclMap(file.Connections)
	for _, serviceDecl := range file.Services {
		serviceFacts, serviceErr := compileServiceDeclaration(serviceDecl, sourceLines, file.BaseDir, connectionDeclsByName)
		if serviceErr != nil {
			return nil, serviceErr
		}
		facts = append(facts, serviceFacts...)
	}

	for _, jobDecl := range file.Jobs {
		jobFacts, jobErr := compileJobDeclaration(jobDecl, sourceLines)
		if jobErr != nil {
			return nil, jobErr
		}
		facts = append(facts, jobFacts...)
	}

	for _, cronJobDecl := range file.CronJobs {
		cronJobFacts, cronJobErr := compileCronJobDeclaration(cronJobDecl, sourceLines)
		if cronJobErr != nil {
			return nil, cronJobErr
		}
		facts = append(facts, cronJobFacts...)
	}

	accessControlFacts, accessControlErr := compileAccessControlDeclarations(file, sourceLines)
	if accessControlErr != nil {
		return nil, accessControlErr
	}
	facts = append(facts, accessControlFacts...)

	return facts, nil
}

// compileInfrastructureDeclarations compiles tenant, volume, cloud identity,
// credential broker, and cloud declarations into their corresponding facts.
func compileInfrastructureDeclarations(file *File, sourceLines []string) ([]Fact, error) {
	var facts []Fact
	for _, tenantDecl := range file.Tenants {
		tenantFacts, err := compileTenantDeclaration(tenantDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, tenantFacts...)
	}
	for _, volumeDecl := range file.Volumes {
		volumeFacts, err := compileVolumeDeclaration(volumeDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, volumeFacts...)
	}
	for _, cloudIdentityDecl := range file.CloudIdentities {
		identityFacts, err := compileCloudIdentityDeclaration(cloudIdentityDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, identityFacts...)
	}
	if file.CredentialBroker != nil {
		brokerFacts, err := compileCredentialBrokerDeclaration(*file.CredentialBroker, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, brokerFacts...)
	}
	if file.Cloud != nil {
		cloudFacts, err := compileCloudDeclaration(*file.Cloud, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, cloudFacts...)
	}
	for _, connectionDecl := range file.Connections {
		connectionFacts, err := compileConnectionDeclaration(connectionDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, connectionFacts...)
	}
	for _, routeDecl := range file.Routes {
		routeFacts, err := compileRouteDeclaration(routeDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, routeFacts...)
	}
	return facts, nil
}

// compileAccessControlDeclarations compiles role, grant, group, service group,
// policy, and network declarations into their corresponding facts.
func compileAccessControlDeclarations(file *File, sourceLines []string) ([]Fact, error) {
	var facts []Fact
	for _, roleDecl := range file.Roles {
		roleFacts, err := compileRoleDeclaration(roleDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, roleFacts...)
	}
	for _, grantDecl := range file.Grants {
		grantFacts, err := compileGrantDeclaration(grantDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, grantFacts...)
	}
	for _, groupDecl := range file.Groups {
		groupFacts, err := compileGroupDeclaration(groupDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, groupFacts...)
	}
	for _, serviceGroupDecl := range file.ServiceGroups {
		serviceGroupFacts, err := compileServiceGroupDeclaration(serviceGroupDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, serviceGroupFacts...)
	}
	for _, policyDecl := range file.Policies {
		policyFacts, err := compilePolicyDeclaration(policyDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, policyFacts...)
	}
	for _, networkDecl := range file.Networks {
		networkFacts, err := compileNetworkDeclaration(networkDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, networkFacts...)
	}
	return facts, nil
}

// compileTenantDeclaration converts a TenantDecl into its corresponding facts.
func compileTenantDeclaration(tenantDecl TenantDecl, sourceLines []string) ([]Fact, error) {
	if tenantDecl.Name == "" {
		return nil, &ParseError{
			Line:       tenantDecl.Line,
			Message:    "tenant name is required",
			SourceLine: sourceLineAt(sourceLines, tenantDecl.Line),
		}
	}

	facts := []Fact{
		{Key: types.KeyDesiredTenant(tenantDecl.Name), Value: ""},
	}

	if tenantDecl.Weight > 0 {
		facts = append(facts, Fact{
			Key: types.KeyDesiredTenantWeight(tenantDecl.Name), Value: strconv.Itoa(tenantDecl.Weight),
		})
	}

	if tenantDecl.Quota != nil {
		if tenantDecl.Quota.CPU > 0 {
			facts = append(facts, Fact{
				Key: types.KeyDesiredTenantQuotaCPU(tenantDecl.Name), Value: strconv.Itoa(tenantDecl.Quota.CPU),
			})
		}
		if tenantDecl.Quota.Memory != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredTenantQuotaMemory(tenantDecl.Name), Value: tenantDecl.Quota.Memory,
			})
		}
		if tenantDecl.Quota.Instances > 0 {
			facts = append(facts, Fact{
				Key: types.KeyDesiredTenantQuotaInstances(tenantDecl.Name), Value: strconv.Itoa(tenantDecl.Quota.Instances),
			})
		}
		if tenantDecl.Quota.Volumes > 0 {
			facts = append(facts, Fact{
				Key: types.KeyDesiredTenantQuotaVolumes(tenantDecl.Name), Value: strconv.Itoa(tenantDecl.Quota.Volumes),
			})
		}
		if tenantDecl.Quota.Storage != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredTenantQuotaStorage(tenantDecl.Name), Value: tenantDecl.Quota.Storage,
			})
		}
	}

	return facts, nil
}

// compileVolumeDeclaration converts a single VolumeDecl into its corresponding facts.
func compileVolumeDeclaration(volumeDecl VolumeDecl, sourceLines []string) ([]Fact, error) {
	if volumeDecl.Name == "" {
		return nil, &ParseError{
			Line:       volumeDecl.Line,
			Message:    "volume name is required",
			SourceLine: sourceLineAt(sourceLines, volumeDecl.Line),
		}
	}

	persistentValue := "false"
	if volumeDecl.Persistent {
		persistentValue = "true"
	}

	facts := []Fact{
		{Key: types.KeyDesiredVolume(volumeDecl.Name), Value: ""},
		{Key: types.KeyDesiredVolumeSize(volumeDecl.Name), Value: volumeDecl.Size},
		{Key: types.KeyDesiredVolumePersistent(volumeDecl.Name), Value: persistentValue},
	}
	if volumeDecl.StorageType != "" {
		facts = append(facts, Fact{Key: types.KeyDesiredVolumeType(volumeDecl.Name), Value: volumeDecl.StorageType})
	}
	return facts, nil
}

// compileServiceDeclaration converts a single ServiceDecl into its corresponding facts.
// It validates the declaration and delegates to per-block sub-compilers for each
// DSL section (ports, resources, scale, placement, health, config, secrets, etc.).
func compileServiceDeclaration(serviceDecl ServiceDecl, sourceLines []string, baseDir string, connectionDeclsByName map[string]ConnectionDecl) ([]Fact, error) {
	if serviceDecl.Name == "" {
		return nil, &ParseError{
			Line: serviceDecl.Line, Message: "service name is required",
			SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
		}
	}
	if serviceDecl.Image == "" {
		return nil, &ParseError{
			Line: serviceDecl.Line, Message: fmt.Sprintf("service %q requires an image", serviceDecl.Name),
			SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
		}
	}
	if serviceDecl.Instances < 0 {
		return nil, &ParseError{
			Line: serviceDecl.Line, Message: fmt.Sprintf("service %q instances must be >= 0", serviceDecl.Name),
			SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
		}
	}

	isPerNode := serviceDecl.Placement != nil && serviceDecl.Placement.Mode == "per-node"

	facts := []Fact{
		{Key: types.KeyDesiredService(serviceDecl.Name), Value: ""},
		{Key: types.KeyDesiredServiceImage(serviceDecl.Name), Value: serviceDecl.Image},
		{Key: types.KeyDesiredServiceInstances(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Instances)},
		{Key: types.KeyIntentUserServiceInstances(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Instances)},
	}
	if !isPerNode {
		facts = append(facts, Fact{
			Key: types.KeyEffectiveServiceInstances(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Instances),
		})
	}

	if serviceDecl.Command != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceCommand(serviceDecl.Name), Value: serviceDecl.Command,
		})
	}

	ownerTenant := serviceDecl.Owner
	if ownerTenant == "" {
		ownerTenant = extractTenantFromHierarchicalName(serviceDecl.Name)
	}
	if ownerTenant != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceOwner(serviceDecl.Name), Value: ownerTenant,
		})
	}

	portFacts, portError := compileServicePortsFacts(serviceDecl.Name, serviceDecl.Ports, serviceDecl.ExternalPorts, serviceDecl.Line, sourceLines)
	if portError != nil {
		return nil, portError
	}
	facts = append(facts, portFacts...)
	facts = append(facts, compileServiceResourceFacts(serviceDecl.Name, serviceDecl.Resources)...)
	facts = append(facts, compileServiceVolumeMountFacts(serviceDecl.Name, serviceDecl.VolumeMounts)...)

	scaleFacts, scaleError := compileServiceScaleFacts(serviceDecl.Name, serviceDecl.Scale, serviceDecl.Line, sourceLines)
	if scaleError != nil {
		return nil, scaleError
	}
	facts = append(facts, scaleFacts...)
	facts = append(facts, compileServicePlacementFacts(serviceDecl.Name, serviceDecl.Placement)...)
	facts = append(facts, compileServiceUpdateFacts(serviceDecl.Name, serviceDecl.Update)...)
	facts = append(facts, compileServiceHealthProbeFacts(serviceDecl.Name, serviceDecl.Health, serviceDecl.Startup, serviceDecl.Liveness, serviceDecl.Readiness)...)
	configFacts, configError := compileServiceConfigFacts(serviceDecl.Name, serviceDecl.Config, baseDir)
	if configError != nil {
		return nil, configError
	}
	facts = append(facts, configFacts...)
	facts = append(facts, compileServiceSecretFacts(serviceDecl.Name, serviceDecl.Secrets)...)
	facts = append(facts, compileServiceConnectionRefFacts(serviceDecl.Name, serviceDecl.Connections, connectionDeclsByName, serviceDecl.Secrets)...)
	facts = append(facts, compileServiceCloudIdentityFacts(serviceDecl.Name, serviceDecl.CloudIdentities)...)
	facts = append(facts, compileServiceInitStepFacts(serviceDecl.Name, serviceDecl.InitSteps)...)
	facts = append(facts, compileServiceDisruptionFacts(serviceDecl.Name, serviceDecl.Disruption)...)
	facts = append(facts, compileServiceTagFacts(serviceDecl.Name, serviceDecl.Tags)...)
	facts = append(facts, compileServiceLoggingFacts(serviceDecl.Name, serviceDecl.Logging)...)
	facts = append(facts, compileServiceLifecycleFacts(serviceDecl.Name, serviceDecl.Lifecycle)...)

	if serviceDecl.PullPolicy != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServicePullPolicy(serviceDecl.Name), Value: serviceDecl.PullPolicy,
		})
	}

	if serviceDecl.ImagePullSecret != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceImagePullSecret(serviceDecl.Name), Value: serviceDecl.ImagePullSecret,
		})
	}

	if serviceDecl.Stateful {
		facts = append(facts,
			Fact{Key: types.KeyDesiredServiceStateful(serviceDecl.Name), Value: "true"},
			Fact{Key: types.KeyEffectiveServiceStateful(serviceDecl.Name), Value: "true"},
		)
	}

	if serviceDecl.TerminationGracePeriod != "" {
		facts = append(facts, Fact{
			Key:   types.KeyDesiredServiceTerminationGracePeriod(serviceDecl.Name),
			Value: serviceDecl.TerminationGracePeriod,
		})
	}

	if serviceDecl.Priority > 0 {
		priorityValue := strconv.Itoa(serviceDecl.Priority)
		facts = append(facts,
			Fact{Key: types.KeyDesiredServicePriority(serviceDecl.Name), Value: priorityValue},
			Fact{Key: types.KeyEffectiveServicePriority(serviceDecl.Name), Value: priorityValue},
		)
	}

	return facts, nil
}

// compileServicePortsFacts produces expose and external port facts for a service.
// Internal ports become simple expose facts; external ports additionally produce
// cloud load balancer exposure facts with their protocol.
func compileServicePortsFacts(serviceName string, ports []int, externalPorts []ExternalPortDecl, declarationLine int, sourceLines []string) ([]Fact, error) {
	var portFacts []Fact
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return nil, &ParseError{
				Line: declarationLine, Message: fmt.Sprintf("service %q port %d out of range (1-65535)", serviceName, port),
				SourceLine: sourceLineAt(sourceLines, declarationLine),
			}
		}
		portFacts = append(portFacts, Fact{
			Key: types.KeyDesiredServiceExpose(serviceName, port), Value: "",
		})
	}
	for _, externalPort := range externalPorts {
		if externalPort.Port < 1 || externalPort.Port > 65535 {
			return nil, &ParseError{
				Line: declarationLine, Message: fmt.Sprintf("service %q external port %d out of range (1-65535)", serviceName, externalPort.Port),
				SourceLine: sourceLineAt(sourceLines, declarationLine),
			}
		}
		portFacts = append(portFacts, Fact{
			Key: types.KeyDesiredServiceExpose(serviceName, externalPort.Port), Value: "",
		})
		portFacts = append(portFacts, Fact{
			Key: types.KeyDesiredServiceExposeExternal(serviceName, externalPort.Port), Value: externalPort.Protocol,
		})
	}
	return portFacts, nil
}

// compileServiceResourceFacts produces CPU and memory resource requirement facts
// for a service. Returns nil if no resource constraints are declared.
func compileServiceResourceFacts(serviceName string, resourcesDecl *ResourcesDecl) []Fact {
	if resourcesDecl == nil {
		return nil
	}
	var resourceFacts []Fact
	if resourcesDecl.CPU != "" {
		resourceFacts = append(resourceFacts, Fact{
			Key: types.KeyDesiredServiceResourcesCPU(serviceName), Value: resourcesDecl.CPU,
		})
	}
	if resourcesDecl.Memory != "" {
		resourceFacts = append(resourceFacts, Fact{
			Key: types.KeyDesiredServiceResourcesMemory(serviceName), Value: resourcesDecl.Memory,
		})
	}
	if resourcesDecl.CPULimit != "" {
		resourceFacts = append(resourceFacts, Fact{
			Key: types.KeyDesiredServiceResourcesCPULimit(serviceName), Value: resourcesDecl.CPULimit,
		})
	}
	if resourcesDecl.MemoryLimit != "" {
		resourceFacts = append(resourceFacts, Fact{
			Key: types.KeyDesiredServiceResourcesMemoryLimit(serviceName), Value: resourcesDecl.MemoryLimit,
		})
	}
	qosClass := classifyQoS(resourcesDecl)
	resourceFacts = append(resourceFacts, Fact{
		Key: types.KeyDerivedServiceQoSClass(serviceName), Value: qosClass,
	})
	return resourceFacts
}

// classifyQoS determines the QoS class based on resource requests and limits.
// Guaranteed: both CPU and memory have requests equal to limits.
// Burstable: at least one resource request is set.
// BestEffort: no resource requests or limits set.
func classifyQoS(resourcesDecl *ResourcesDecl) string {
	hasCPU := resourcesDecl.CPU != ""
	hasMemory := resourcesDecl.Memory != ""
	hasCPULimit := resourcesDecl.CPULimit != ""
	hasMemoryLimit := resourcesDecl.MemoryLimit != ""

	if !hasCPU && !hasMemory && !hasCPULimit && !hasMemoryLimit {
		return "BestEffort"
	}

	cpuGuaranteed := hasCPU && hasCPULimit && resourcesDecl.CPU == resourcesDecl.CPULimit
	memoryGuaranteed := hasMemory && hasMemoryLimit && resourcesDecl.Memory == resourcesDecl.MemoryLimit
	if hasCPU && cpuGuaranteed && hasMemory && memoryGuaranteed {
		return "Guaranteed"
	}

	if hasCPU && !hasCPULimit && hasMemory && !hasMemoryLimit {
		return "Guaranteed"
	}

	return "Burstable"
}

// compileServiceVolumeMountFacts produces volume mount binding facts that connect
// named volumes to filesystem paths inside the service's instances.
func compileServiceVolumeMountFacts(serviceName string, volumeMounts []VolumeMountDecl) []Fact {
	var volumeFacts []Fact
	for _, volumeMount := range volumeMounts {
		volumeFacts = append(volumeFacts, Fact{
			Key:   types.KeyDesiredServiceVolume(serviceName, volumeMount.VolumeName),
			Value: volumeMount.MountPath,
		})
	}
	return volumeFacts
}

// compileServiceScaleFacts produces horizontal and vertical autoscaling facts
// for a service. Returns nil if no scaling policy is declared. Delegates to
// compileServiceHorizontalScaleFacts and compileServiceVerticalScaleFacts for
// each scaling dimension.
func compileServiceScaleFacts(serviceName string, scaleDecl *ScaleDecl, declarationLine int, sourceLines []string) ([]Fact, error) {
	if scaleDecl == nil {
		return nil, nil
	}
	var scaleFacts []Fact
	if scaleDecl.Horizontal != nil {
		horizontalFacts, horizontalError := compileServiceHorizontalScaleFacts(serviceName, scaleDecl.Horizontal, declarationLine, sourceLines)
		if horizontalError != nil {
			return nil, horizontalError
		}
		scaleFacts = append(scaleFacts, horizontalFacts...)
	}
	if scaleDecl.Vertical != nil {
		scaleFacts = append(scaleFacts, compileServiceVerticalScaleFacts(serviceName, scaleDecl.Vertical)...)
	}
	return scaleFacts, nil
}

// compileServiceHorizontalScaleFacts produces horizontal autoscaling facts including
// min/max bounds, metric targets, event-driven sources, scheduled scaling, and
// stabilization windows.
func compileServiceHorizontalScaleFacts(serviceName string, horizontalDecl *HorizontalScaleDecl, declarationLine int, sourceLines []string) ([]Fact, error) {
	if horizontalDecl.Min < 0 {
		return nil, &ParseError{
			Line: declarationLine, Message: fmt.Sprintf("service %q scale min must be >= 0", serviceName),
			SourceLine: sourceLineAt(sourceLines, declarationLine),
		}
	}
	if horizontalDecl.Max < horizontalDecl.Min {
		return nil, &ParseError{
			Line: declarationLine, Message: fmt.Sprintf("service %q scale max must be >= min", serviceName),
			SourceLine: sourceLineAt(sourceLines, declarationLine),
		}
	}
	horizontalFacts := []Fact{
		{Key: types.KeyDesiredServiceScaleHorizontalMin(serviceName), Value: strconv.Itoa(horizontalDecl.Min)},
		{Key: types.KeyDesiredServiceScaleHorizontalMax(serviceName), Value: strconv.Itoa(horizontalDecl.Max)},
	}
	for _, target := range horizontalDecl.Targets {
		if target.Value <= 0 {
			return nil, &ParseError{
				Line: declarationLine, Message: fmt.Sprintf("service %q scale target %q must be > 0", serviceName, target.Metric),
				SourceLine: sourceLineAt(sourceLines, declarationLine),
			}
		}
		horizontalFacts = append(horizontalFacts, Fact{
			Key:   types.KeyDesiredServiceScaleHorizontalTarget(serviceName, target.Metric),
			Value: strconv.Itoa(target.Value),
		})
	}
	for _, eventSource := range horizontalDecl.Events {
		if eventSource.Target <= 0 {
			return nil, &ParseError{
				Line: declarationLine, Message: fmt.Sprintf("service %q event target for %q must be > 0", serviceName, eventSource.Source),
				SourceLine: sourceLineAt(sourceLines, declarationLine),
			}
		}
		horizontalFacts = append(horizontalFacts, Fact{
			Key:   types.KeyDesiredServiceScaleHorizontalEvent(serviceName, eventSource.Source),
			Value: strconv.Itoa(eventSource.Target),
		})
	}
	if horizontalDecl.Schedule != nil {
		horizontalFacts = append(horizontalFacts,
			Fact{Key: types.KeyDesiredServiceScaleScheduleDays(serviceName), Value: horizontalDecl.Schedule.Days},
			Fact{Key: types.KeyDesiredServiceScaleScheduleStart(serviceName), Value: horizontalDecl.Schedule.Start},
			Fact{Key: types.KeyDesiredServiceScaleScheduleEnd(serviceName), Value: horizontalDecl.Schedule.End},
			Fact{Key: types.KeyDesiredServiceScaleScheduleMinimum(serviceName), Value: strconv.Itoa(horizontalDecl.Schedule.Minimum)},
		)
	}
	if horizontalDecl.Stabilization != nil {
		if horizontalDecl.Stabilization.ScaleUp != "" {
			horizontalFacts = append(horizontalFacts, Fact{
				Key: types.KeyDesiredServiceScaleStabilizationUp(serviceName), Value: horizontalDecl.Stabilization.ScaleUp,
			})
		}
		if horizontalDecl.Stabilization.ScaleDown != "" {
			horizontalFacts = append(horizontalFacts, Fact{
				Key: types.KeyDesiredServiceScaleStabilizationDown(serviceName), Value: horizontalDecl.Stabilization.ScaleDown,
			})
		}
	}
	if horizontalDecl.IdleTimeout != "" {
		horizontalFacts = append(horizontalFacts, Fact{
			Key: types.KeyDesiredServiceScaleIdleTimeout(serviceName), Value: horizontalDecl.IdleTimeout,
		})
	}
	if horizontalDecl.ActivationTimeout != "" {
		horizontalFacts = append(horizontalFacts, Fact{
			Key: types.KeyDesiredServiceScaleActivationTimeout(serviceName), Value: horizontalDecl.ActivationTimeout,
		})
	}
	return horizontalFacts, nil
}

// compileServiceVerticalScaleFacts produces vertical autoscaling resource bound
// facts (CPU and memory min/max limits).
func compileServiceVerticalScaleFacts(serviceName string, verticalDecl *VerticalScaleDecl) []Fact {
	var verticalFacts []Fact
	if verticalDecl.CPUMin != "" {
		verticalFacts = append(verticalFacts, Fact{Key: types.KeyDesiredServiceScaleVerticalCPUMin(serviceName), Value: verticalDecl.CPUMin})
	}
	if verticalDecl.CPUMax != "" {
		verticalFacts = append(verticalFacts, Fact{Key: types.KeyDesiredServiceScaleVerticalCPUMax(serviceName), Value: verticalDecl.CPUMax})
	}
	if verticalDecl.MemoryMin != "" {
		verticalFacts = append(verticalFacts, Fact{Key: types.KeyDesiredServiceScaleVerticalMemoryMin(serviceName), Value: verticalDecl.MemoryMin})
	}
	if verticalDecl.MemoryMax != "" {
		verticalFacts = append(verticalFacts, Fact{Key: types.KeyDesiredServiceScaleVerticalMemoryMax(serviceName), Value: verticalDecl.MemoryMax})
	}
	return verticalFacts
}

// compileServicePlacementFacts produces placement constraint facts including
// architecture requirements, zone spread policy, required/preferred node labels,
// and accepted node restrictions.
func compileServicePlacementFacts(serviceName string, placementDecl *PlacementDecl) []Fact {
	if placementDecl == nil {
		return nil
	}
	var placementFacts []Fact
	if placementDecl.Mode != "" {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementMode(serviceName), Value: placementDecl.Mode,
		})
	}
	if placementDecl.Architecture != "" {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementArchitecture(serviceName), Value: placementDecl.Architecture,
		})
	}
	if placementDecl.ZonePolicy != "" {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementZonePolicy(serviceName), Value: placementDecl.ZonePolicy,
		})
		if placementDecl.ZoneMaxSkew > 0 {
			placementFacts = append(placementFacts, Fact{
				Key:   types.KeyDesiredServicePlacementZoneMaxSkew(serviceName),
				Value: strconv.Itoa(placementDecl.ZoneMaxSkew),
			})
		}
	}
	for _, requireRule := range placementDecl.Require {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementRequire(serviceName, requireRule.Label), Value: requireRule.Value,
		})
	}
	for _, preferRule := range placementDecl.Prefer {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementPrefer(serviceName, preferRule.Label), Value: preferRule.Value,
		})
	}
	for _, acceptLabel := range placementDecl.Accept {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementAccept(serviceName, acceptLabel), Value: "",
		})
	}
	for _, targetService := range placementDecl.Colocate {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementColocate(serviceName, targetService), Value: "",
		})
	}
	for _, targetService := range placementDecl.Separate {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementSeparate(serviceName, targetService), Value: "",
		})
	}
	return placementFacts
}

// compileServiceUpdateFacts produces rolling update strategy facts (max unavailable
// and max extra instance counts). Returns nil if no update strategy is declared.
func compileServiceUpdateFacts(serviceName string, updateDecl *UpdateDecl) []Fact {
	if updateDecl == nil {
		return nil
	}
	return []Fact{
		{Key: types.KeyDesiredServiceUpdateMaxUnavailable(serviceName), Value: strconv.Itoa(updateDecl.MaxUnavailable)},
		{Key: types.KeyDesiredServiceUpdateMaxExtra(serviceName), Value: strconv.Itoa(updateDecl.MaxExtra)},
	}
}

// compileServiceDisruptionFacts produces disruption budget facts for a service.
// The budget constrains how many instances may be taken down simultaneously
// during node drains and rolling updates.
func compileServiceDisruptionFacts(serviceName string, disruptionDecl *DisruptionDecl) []Fact {
	if disruptionDecl == nil {
		return nil
	}
	var disruptionFacts []Fact
	if disruptionDecl.MinAvailable != 0 {
		disruptionFacts = append(disruptionFacts, Fact{
			Key:   types.KeyDesiredServiceDisruptionMinAvailable(serviceName),
			Value: strconv.Itoa(disruptionDecl.MinAvailable),
		})
	}
	if disruptionDecl.MaxUnavailable != 0 {
		disruptionFacts = append(disruptionFacts, Fact{
			Key:   types.KeyDesiredServiceDisruptionMaxUnavailable(serviceName),
			Value: strconv.Itoa(disruptionDecl.MaxUnavailable),
		})
	}
	return disruptionFacts
}

// compileServiceTagFacts produces user-defined tag facts for a service.
// Each tag becomes a fact at desired/service/{name}/tag/{key} = {value}.
func compileServiceTagFacts(serviceName string, tagDecls []TagDecl) []Fact {
	if len(tagDecls) == 0 {
		return nil
	}
	tagFacts := make([]Fact, 0, len(tagDecls))
	for _, tagDecl := range tagDecls {
		tagFacts = append(tagFacts, Fact{
			Key:   types.KeyDesiredServiceTag(serviceName, tagDecl.Key),
			Value: tagDecl.Value,
		})
	}
	return tagFacts
}

// compileServiceLoggingFacts produces log rotation configuration facts.
func compileServiceLoggingFacts(serviceName string, loggingDecl *LoggingDecl) []Fact {
	if loggingDecl == nil {
		return nil
	}
	var loggingFacts []Fact
	if loggingDecl.MaxSize != "" {
		loggingFacts = append(loggingFacts, Fact{
			Key: types.KeyDesiredServiceLoggingMaxSize(serviceName), Value: loggingDecl.MaxSize,
		})
	}
	if loggingDecl.MaxFiles > 0 {
		loggingFacts = append(loggingFacts, Fact{
			Key: types.KeyDesiredServiceLoggingMaxFiles(serviceName), Value: strconv.Itoa(loggingDecl.MaxFiles),
		})
	}
	return loggingFacts
}

// compileServiceLifecycleFacts produces lifecycle hook facts.
func compileServiceLifecycleFacts(serviceName string, lifecycleDecl *LifecycleDecl) []Fact {
	if lifecycleDecl == nil {
		return nil
	}
	var lifecycleFacts []Fact
	if lifecycleDecl.PreStop != "" {
		lifecycleFacts = append(lifecycleFacts, Fact{
			Key: types.KeyDesiredServiceLifecyclePreStop(serviceName), Value: lifecycleDecl.PreStop,
		})
	}
	if lifecycleDecl.PostStart != "" {
		lifecycleFacts = append(lifecycleFacts, Fact{
			Key: types.KeyDesiredServiceLifecyclePostStart(serviceName), Value: lifecycleDecl.PostStart,
		})
	}
	return lifecycleFacts
}

// compileServiceHealthProbeFacts produces health check configuration facts for a
// service. This includes the legacy health block (method, path, interval) and the
// startup, liveness, and readiness probe declarations.
func compileServiceHealthProbeFacts(serviceName string, healthDecl *HealthDecl, startupProbe *ProbeDecl, livenessProbe *ProbeDecl, readinessProbe *ProbeDecl) []Fact {
	var healthFacts []Fact
	if healthDecl != nil {
		if healthDecl.Method != "" {
			healthFacts = append(healthFacts, Fact{
				Key: types.KeyDesiredServiceHealthMethod(serviceName), Value: healthDecl.Method,
			})
		}
		if healthDecl.Path != "" {
			healthFacts = append(healthFacts, Fact{
				Key: types.KeyDesiredServiceHealthPath(serviceName), Value: healthDecl.Path,
			})
		}
		if healthDecl.Interval != "" {
			healthFacts = append(healthFacts, Fact{
				Key: types.KeyDesiredServiceHealthInterval(serviceName), Value: healthDecl.Interval,
			})
		}
		if healthDecl.Timeout != "" {
			healthFacts = append(healthFacts, Fact{
				Key: types.KeyDesiredServiceHealthTimeout(serviceName), Value: healthDecl.Timeout,
			})
		}
	}
	if startupProbe != nil {
		healthFacts = append(healthFacts, compileProbeDeclaration(serviceName, types.ProbeStartup, startupProbe)...)
	}
	if livenessProbe != nil {
		healthFacts = append(healthFacts, compileProbeDeclaration(serviceName, types.ProbeLiveness, livenessProbe)...)
	}
	if readinessProbe != nil {
		healthFacts = append(healthFacts, compileProbeDeclaration(serviceName, types.ProbeReadiness, readinessProbe)...)
	}
	return healthFacts
}

// compileServiceConfigFacts produces environment variable and config file facts
// for a service. Returns nil if no config block is declared.
func compileServiceConfigFacts(serviceName string, configDecl *ConfigDecl, baseDir string) ([]Fact, error) {
	if configDecl == nil {
		return nil, nil
	}
	var configFacts []Fact
	for _, envVar := range configDecl.EnvVars {
		configFacts = append(configFacts, Fact{
			Key:   types.KeyDesiredServiceConfigEnv(serviceName, envVar.Name),
			Value: envVar.Value,
		})
	}
	for _, configFile := range configDecl.ConfigFiles {
		fileContent := configFile.Content
		if configFile.FromFile != "" {
			resolvedPath := configFile.FromFile
			if baseDir != "" && !filepath.IsAbs(resolvedPath) {
				resolvedPath = filepath.Join(baseDir, resolvedPath)
			}
			fileBytes, readErr := os.ReadFile(resolvedPath) //nolint:gosec // intentional: user-specified config file reference
			if readErr != nil {
				return nil, fmt.Errorf("config file %q: cannot read %q: %w", configFile.Path, configFile.FromFile, readErr)
			}
			fileContent = string(fileBytes)
		}
		configFacts = append(configFacts, Fact{
			Key:   types.KeyDesiredServiceConfigFile(serviceName, configFile.Path),
			Value: fileContent,
		})
	}
	return configFacts, nil
}

// compileServiceSecretFacts produces secret mount path facts for each secret
// bound to a service.
func compileServiceSecretFacts(serviceName string, secretDecls []SecretDecl) []Fact {
	var secretFacts []Fact
	for _, secretDecl := range secretDecls {
		secretFacts = append(secretFacts, Fact{
			Key:   types.KeyDesiredServiceSecret(serviceName, secretDecl.Name),
			Value: secretDecl.MountPath,
		})
	}
	return secretFacts
}

// compileServiceConnectionRefFacts produces connection mount path facts for
// each connection referenced by a service. When a referenced connection
// declares a secret, an implicit secret grant is emitted so the agent can
// decrypt the password at runtime. Explicit secret declarations take
// precedence — if the service already grants the same secret with a mount
// path, the auto-grant is skipped.
func compileServiceConnectionRefFacts(serviceName string, connectionRefs []ConnectionRef, connectionDeclsByName map[string]ConnectionDecl, explicitSecrets []SecretDecl) []Fact {
	explicitSecretNames := make(map[string]bool, len(explicitSecrets))
	for _, explicitSecret := range explicitSecrets {
		explicitSecretNames[explicitSecret.Name] = true
	}
	var connectionFacts []Fact
	for _, connectionRef := range connectionRefs {
		connectionFacts = append(connectionFacts, Fact{
			Key:   types.KeyDesiredServiceConnection(serviceName, connectionRef.ConnectionName),
			Value: connectionRef.MountPath,
		})
		connectionDecl, connectionDeclExists := connectionDeclsByName[connectionRef.ConnectionName]
		if connectionDeclExists && connectionDecl.Secret != "" && !explicitSecretNames[connectionDecl.Secret] {
			connectionFacts = append(connectionFacts, Fact{
				Key:   types.KeyDesiredServiceSecret(serviceName, connectionDecl.Secret),
				Value: "connection",
			})
		}
	}
	return connectionFacts
}

// buildConnectionDeclMap indexes top-level connection declarations by name
// so service compilation can look up a connection's secret field.
func buildConnectionDeclMap(connectionDecls []ConnectionDecl) map[string]ConnectionDecl {
	connectionDeclsByName := make(map[string]ConnectionDecl, len(connectionDecls))
	for _, connectionDecl := range connectionDecls {
		connectionDeclsByName[connectionDecl.Name] = connectionDecl
	}
	return connectionDeclsByName
}

// compileConnectionDeclaration validates and converts a top-level ConnectionDecl
// into a JSON-encoded fact containing the connection definition.
func compileConnectionDeclaration(connectionDecl ConnectionDecl, sourceLines []string) ([]Fact, error) {
	if connectionDecl.Name == "" {
		return nil, &ParseError{
			Line: connectionDecl.Line, Message: "connection name is required",
			SourceLine: sourceLineAt(sourceLines, connectionDecl.Line),
		}
	}

	connectionJSON := buildConnectionJSON(connectionDecl)

	return []Fact{
		{Key: types.KeyDesiredConnection(connectionDecl.Name), Value: connectionJSON},
	}, nil
}

// buildConnectionJSON serializes a ConnectionDecl's fields into a JSON string
// suitable for storage as a fact value.
func buildConnectionJSON(connectionDecl ConnectionDecl) string {
	var parts []string
	parts = append(parts, fmt.Sprintf(`"driver":%q`, connectionDecl.Driver))
	if connectionDecl.User != "" {
		parts = append(parts, fmt.Sprintf(`"user":%q`, connectionDecl.User))
	}
	if connectionDecl.Secret != "" {
		parts = append(parts, fmt.Sprintf(`"secret":%q`, connectionDecl.Secret))
	}
	parts = append(parts, fmt.Sprintf(`"host":%q`, connectionDecl.Host))
	if connectionDecl.Port > 0 {
		parts = append(parts, fmt.Sprintf(`"port":%d`, connectionDecl.Port))
	}
	if connectionDecl.Database != "" {
		parts = append(parts, fmt.Sprintf(`"database":%q`, connectionDecl.Database))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// compileRouteDeclaration converts a RouteDecl into its corresponding facts.
func compileRouteDeclaration(routeDecl RouteDecl, sourceLines []string) ([]Fact, error) {
	if routeDecl.Name == "" {
		return nil, &ParseError{
			Line: routeDecl.Line, Message: "gate name is required",
			SourceLine: sourceLineAt(sourceLines, routeDecl.Line),
		}
	}
	if routeDecl.ServiceName == "" {
		return nil, &ParseError{
			Line: routeDecl.Line, Message: "gate must specify a target service",
			SourceLine: sourceLineAt(sourceLines, routeDecl.Line),
		}
	}

	routeFacts := []Fact{
		{Key: types.KeyDesiredRoute(routeDecl.Name), Value: ""},
		{Key: types.KeyDesiredRouteService(routeDecl.Name), Value: routeDecl.ServiceName},
	}

	if routeDecl.Host != "" {
		routeFacts = append(routeFacts, Fact{
			Key: types.KeyDesiredRouteHost(routeDecl.Name), Value: routeDecl.Host,
		})
	}
	if routeDecl.Path != "" {
		routeFacts = append(routeFacts, Fact{
			Key: types.KeyDesiredRoutePath(routeDecl.Name), Value: routeDecl.Path,
		})
	}
	if routeDecl.ServicePort > 0 {
		routeFacts = append(routeFacts, Fact{
			Key:   types.KeyDesiredRoutePort(routeDecl.Name),
			Value: strconv.Itoa(routeDecl.ServicePort),
		})
	}

	if routeDecl.Sticky != nil {
		routeFacts = append(routeFacts, compileStickyFacts(routeDecl.Name, routeDecl.Sticky)...)
	}

	return routeFacts, nil
}

// compileStickyFacts produces facts for a route's sticky session configuration.
func compileStickyFacts(routeName string, stickyDecl *RouteStickyDecl) []Fact {
	stickyFacts := []Fact{
		{Key: types.KeyDesiredRouteSticky(routeName), Value: stickyDecl.Method},
	}

	if stickyDecl.Cookie != nil {
		cookieSettings := stickyDecl.Cookie
		if cookieSettings.Name != "" {
			stickyFacts = append(stickyFacts, Fact{
				Key: types.KeyDesiredRouteCookieName(routeName), Value: cookieSettings.Name,
			})
		}
		if cookieSettings.Secure {
			stickyFacts = append(stickyFacts, Fact{
				Key: types.KeyDesiredRouteCookieSecure(routeName), Value: "true",
			})
		}
		if cookieSettings.HTTPOnly {
			stickyFacts = append(stickyFacts, Fact{
				Key: types.KeyDesiredRouteCookieHTTPOnly(routeName), Value: "true",
			})
		}
		if cookieSettings.SameSite != "" {
			stickyFacts = append(stickyFacts, Fact{
				Key: types.KeyDesiredRouteCookieSameSite(routeName), Value: cookieSettings.SameSite,
			})
		}
	}

	return stickyFacts
}

// compileServiceCloudIdentityFacts produces cloud identity binding facts for a
// service, including optional mount path and delivery mode configuration.
func compileServiceCloudIdentityFacts(serviceName string, cloudIdentityBindings []CloudIdentityBindingDecl) []Fact {
	var identityFacts []Fact
	for _, cloudIdentityBinding := range cloudIdentityBindings {
		identityFacts = append(identityFacts, Fact{
			Key:   types.KeyDesiredServiceCloudIdentity(serviceName, cloudIdentityBinding.IdentityName),
			Value: "",
		})
		if cloudIdentityBinding.MountPath != "" {
			identityFacts = append(identityFacts, Fact{
				Key:   types.KeyDesiredServiceCloudIdentityMountPath(serviceName, cloudIdentityBinding.IdentityName),
				Value: cloudIdentityBinding.MountPath,
			})
		}
		if cloudIdentityBinding.DeliverMode != "" {
			identityFacts = append(identityFacts, Fact{
				Key:   types.KeyDesiredServiceCloudIdentityDeliverMode(serviceName, cloudIdentityBinding.IdentityName),
				Value: cloudIdentityBinding.DeliverMode,
			})
		}
	}
	return identityFacts
}

// compileServiceInitStepFacts produces initialization step facts for a service,
// including the exec command, optional timeout, and retry count for each step.
func compileServiceInitStepFacts(serviceName string, initSteps []InitStepDecl) []Fact {
	var initFacts []Fact
	for stepIndex, initStep := range initSteps {
		initFacts = append(initFacts, Fact{
			Key: types.KeyDesiredServiceInitStep(serviceName, stepIndex), Value: "",
		})
		if initStep.Connection != "" {
			initFacts = append(initFacts, Fact{
				Key: types.KeyDesiredServiceInitStepConnection(serviceName, stepIndex), Value: initStep.Connection,
			})
		} else {
			initFacts = append(initFacts, Fact{
				Key: types.KeyDesiredServiceInitStepExec(serviceName, stepIndex), Value: initStep.Exec,
			})
		}
		if initStep.Timeout != "" {
			initFacts = append(initFacts, Fact{
				Key: types.KeyDesiredServiceInitStepTimeout(serviceName, stepIndex), Value: initStep.Timeout,
			})
		}
		if initStep.Retry > 0 {
			initFacts = append(initFacts, Fact{
				Key: types.KeyDesiredServiceInitStepRetry(serviceName, stepIndex), Value: strconv.Itoa(initStep.Retry),
			})
		}
	}
	return initFacts
}

// compileCloudIdentityDeclaration validates and converts a CloudIdentityDecl into facts.
func compileCloudIdentityDeclaration(cloudIdentityDecl CloudIdentityDecl, sourceLines []string) ([]Fact, error) {
	if validationErr := validateCloudIdentityRequiredFields(cloudIdentityDecl, sourceLines); validationErr != nil {
		return nil, validationErr
	}
	return buildCloudIdentityFactsFromDeclaration(cloudIdentityDecl), nil
}

// validateCloudIdentityRequiredFields checks that a cloud identity declaration
// has a name, a valid provider, and all provider-specific required fields.
func validateCloudIdentityRequiredFields(cloudIdentityDecl CloudIdentityDecl, sourceLines []string) error {
	if cloudIdentityDecl.Name == "" {
		return &ParseError{
			Line:       cloudIdentityDecl.Line,
			Message:    "cloud_identity name is required",
			SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
		}
	}
	if cloudIdentityDecl.Provider == "" {
		return &ParseError{
			Line:       cloudIdentityDecl.Line,
			Message:    fmt.Sprintf("cloud_identity %q requires a provider (aws, gcp, or azure)", cloudIdentityDecl.Name),
			SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
		}
	}

	switch cloudIdentityDecl.Provider {
	case "aws":
		if cloudIdentityDecl.Role == "" {
			return &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider aws requires a role", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
	case "gcp":
		if cloudIdentityDecl.ServiceAccount == "" {
			return &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider gcp requires a service_account", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
		if cloudIdentityDecl.Pool == "" {
			return &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider gcp requires a pool", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
	case "azure":
		if cloudIdentityDecl.ClientID == "" {
			return &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider azure requires a client_id", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
		if cloudIdentityDecl.TenantID == "" {
			return &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider azure requires a tenant_id", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
	default:
		return &ParseError{
			Line:       cloudIdentityDecl.Line,
			Message:    fmt.Sprintf("cloud_identity %q has unknown provider %q (must be aws, gcp, or azure)", cloudIdentityDecl.Name, cloudIdentityDecl.Provider),
			SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
		}
	}
	return nil
}

// buildCloudIdentityFactsFromDeclaration converts a validated CloudIdentityDecl
// into its corresponding fact entries for the store.
func buildCloudIdentityFactsFromDeclaration(cloudIdentityDecl CloudIdentityDecl) []Fact {
	facts := []Fact{
		{Key: types.KeyDesiredCloudIdentity(cloudIdentityDecl.Name), Value: ""},
		{Key: types.KeyDesiredCloudIdentityProvider(cloudIdentityDecl.Name), Value: cloudIdentityDecl.Provider},
	}

	if cloudIdentityDecl.Role != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudIdentityRole(cloudIdentityDecl.Name), Value: cloudIdentityDecl.Role,
		})
	}
	if cloudIdentityDecl.ServiceAccount != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudIdentityServiceAccount(cloudIdentityDecl.Name), Value: cloudIdentityDecl.ServiceAccount,
		})
	}
	if cloudIdentityDecl.Pool != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudIdentityPool(cloudIdentityDecl.Name), Value: cloudIdentityDecl.Pool,
		})
	}
	if cloudIdentityDecl.ClientID != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudIdentityClientID(cloudIdentityDecl.Name), Value: cloudIdentityDecl.ClientID,
		})
	}
	if cloudIdentityDecl.TenantID != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudIdentityTenantID(cloudIdentityDecl.Name), Value: cloudIdentityDecl.TenantID,
		})
	}

	return facts
}

// compileCredentialBrokerDeclaration validates and converts a CredentialBrokerDecl into facts.
func compileCredentialBrokerDeclaration(credentialBrokerDecl CredentialBrokerDecl, sourceLines []string) ([]Fact, error) {
	if credentialBrokerDecl.OIDCIssuer == "" {
		return nil, &ParseError{
			Line:       credentialBrokerDecl.Line,
			Message:    "credential_broker requires an oidc_issuer",
			SourceLine: sourceLineAt(sourceLines, credentialBrokerDecl.Line),
		}
	}

	facts := []Fact{
		{Key: types.KeyDesiredCredentialBrokerOIDCIssuer(), Value: credentialBrokerDecl.OIDCIssuer},
	}
	if credentialBrokerDecl.CredentialTTL != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCredentialBrokerCredentialTTL(), Value: credentialBrokerDecl.CredentialTTL,
		})
	}
	if credentialBrokerDecl.RefreshBefore != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCredentialBrokerRefreshBefore(), Value: credentialBrokerDecl.RefreshBefore,
		})
	}

	return facts, nil
}

// compileProbeDeclaration converts a ProbeDecl into facts for the given probe type.
func compileProbeDeclaration(serviceName string, probeType types.ProbeType, probeDecl *ProbeDecl) []Fact {
	var facts []Fact
	facts = append(facts, Fact{
		Key: types.KeyDesiredServiceProbeMethod(serviceName, probeType), Value: probeDecl.Method,
	})
	if probeDecl.Path != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceProbePath(serviceName, probeType), Value: probeDecl.Path,
		})
	}
	if probeDecl.Port > 0 {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceProbePort(serviceName, probeType), Value: strconv.Itoa(probeDecl.Port),
		})
	}
	if probeDecl.Interval != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceProbeInterval(serviceName, probeType), Value: probeDecl.Interval,
		})
	}
	if probeDecl.Timeout != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceProbeTimeout(serviceName, probeType), Value: probeDecl.Timeout,
		})
	}
	if probeDecl.FailureThreshold > 0 {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceProbeFailureThreshold(serviceName, probeType), Value: strconv.Itoa(probeDecl.FailureThreshold),
		})
	}
	if probeDecl.SuccessThreshold > 0 {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceProbeSuccessThreshold(serviceName, probeType), Value: strconv.Itoa(probeDecl.SuccessThreshold),
		})
	}
	if probeDecl.InitialDelay != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceProbeInitialDelay(serviceName, probeType), Value: probeDecl.InitialDelay,
		})
	}
	return facts
}

// compileCloudDeclaration converts a CloudDecl into its corresponding facts.
func compileCloudDeclaration(cloudDecl CloudDecl, sourceLines []string) ([]Fact, error) {
	if cloudDecl.Provider == "" {
		return nil, &ParseError{
			Line:       cloudDecl.Line,
			Message:    "cloud block requires a provider (aws, gcp, or azure)",
			SourceLine: sourceLineAt(sourceLines, cloudDecl.Line),
		}
	}

	validProviders := map[string]bool{"aws": true, "gcp": true, "azure": true}
	if !validProviders[cloudDecl.Provider] {
		return nil, &ParseError{
			Line:       cloudDecl.Line,
			Message:    fmt.Sprintf("unknown cloud provider %q (expected aws, gcp, or azure)", cloudDecl.Provider),
			SourceLine: sourceLineAt(sourceLines, cloudDecl.Line),
		}
	}

	facts := []Fact{
		{Key: types.KeyDesiredCloudProvider(), Value: cloudDecl.Provider},
	}

	if cloudDecl.Region != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudRegion(), Value: cloudDecl.Region,
		})
	}
	if cloudDecl.InstanceType != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudInstanceType(), Value: cloudDecl.InstanceType,
		})
	}
	if cloudDecl.Credentials != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudCredentials(), Value: cloudDecl.Credentials,
		})
	}
	if cloudDecl.ProjectID != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudProjectID(), Value: cloudDecl.ProjectID,
		})
	}
	if cloudDecl.ResourceGroup != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudResourceGroup(), Value: cloudDecl.ResourceGroup,
		})
	}
	if cloudDecl.VPCNetwork != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudVPCNetwork(), Value: cloudDecl.VPCNetwork,
		})
	}
	if cloudDecl.RouteTable != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCloudRouteTable(), Value: cloudDecl.RouteTable,
		})
	}

	return facts, nil
}

// compileRoleDeclaration converts a RoleDecl into auth/role/ prefix facts.
func compileRoleDeclaration(roleDecl RoleDecl, sourceLines []string) ([]Fact, error) {
	if roleDecl.Name == "" {
		return nil, &ParseError{
			Line:       roleDecl.Line,
			Message:    "role name is required",
			SourceLine: sourceLineAt(sourceLines, roleDecl.Line),
		}
	}
	if len(roleDecl.Capabilities) == 0 {
		return nil, &ParseError{
			Line:       roleDecl.Line,
			Message:    "role must have at least one capability",
			SourceLine: sourceLineAt(sourceLines, roleDecl.Line),
		}
	}

	var facts []Fact
	for _, capabilityName := range roleDecl.Capabilities {
		facts = append(facts, Fact{
			Key: types.KeyAuthRoleCapability(roleDecl.Name, capabilityName), Value: "true",
		})
	}
	for _, scopePath := range roleDecl.Scopes {
		facts = append(facts, Fact{
			Key: types.KeyAuthRoleScope(roleDecl.Name, scopePath), Value: "true",
		})
	}
	return facts, nil
}

// compileGrantDeclaration converts a GrantDecl into an auth/grant/ prefix fact.
func compileGrantDeclaration(grantDecl GrantDecl, sourceLines []string) ([]Fact, error) {
	if grantDecl.RoleName == "" {
		return nil, &ParseError{
			Line:       grantDecl.Line,
			Message:    "grant role name is required",
			SourceLine: sourceLineAt(sourceLines, grantDecl.Line),
		}
	}
	if grantDecl.PrincipalKind == "" {
		return nil, &ParseError{
			Line:       grantDecl.Line,
			Message:    "grant principal kind is required",
			SourceLine: sourceLineAt(sourceLines, grantDecl.Line),
		}
	}
	if grantDecl.PrincipalName == "" {
		return nil, &ParseError{
			Line:       grantDecl.Line,
			Message:    "grant principal name is required",
			SourceLine: sourceLineAt(sourceLines, grantDecl.Line),
		}
	}

	facts := []Fact{
		{Key: types.KeyAuthGrant(grantDecl.PrincipalKind, grantDecl.PrincipalName, grantDecl.RoleName), Value: "true"},
	}
	return facts, nil
}

// compileGroupDeclaration converts a GroupDecl into auth/group/ prefix facts.
func compileGroupDeclaration(groupDecl GroupDecl, sourceLines []string) ([]Fact, error) {
	if groupDecl.Name == "" {
		return nil, &ParseError{
			Line:       groupDecl.Line,
			Message:    "group name is required",
			SourceLine: sourceLineAt(sourceLines, groupDecl.Line),
		}
	}

	var facts []Fact
	for _, memberName := range groupDecl.Members {
		facts = append(facts, Fact{
			Key: types.KeyAuthGroupMember(groupDecl.Name, memberName), Value: "true",
		})
	}
	return facts, nil
}

// compileServiceGroupDeclaration converts a ServiceGroupDecl into
// desired/group/ prefix facts: a root marker, process memberships,
// optional shared network flag, and shared volume bindings.
func compileServiceGroupDeclaration(serviceGroupDecl ServiceGroupDecl, sourceLines []string) ([]Fact, error) {
	if serviceGroupDecl.Name == "" {
		return nil, &ParseError{
			Line:       serviceGroupDecl.Line,
			Message:    "service group name is required",
			SourceLine: sourceLineAt(sourceLines, serviceGroupDecl.Line),
		}
	}
	if len(serviceGroupDecl.Processes) == 0 {
		return nil, &ParseError{
			Line:       serviceGroupDecl.Line,
			Message:    "service group must have at least one process",
			SourceLine: sourceLineAt(sourceLines, serviceGroupDecl.Line),
		}
	}

	groupName := serviceGroupDecl.Name
	facts := []Fact{{Key: types.KeyDesiredGroup(groupName), Value: "true"}}

	for _, processName := range serviceGroupDecl.Processes {
		facts = append(facts, Fact{
			Key: types.KeyDesiredGroupProcess(groupName, processName), Value: "true",
		})
	}
	if serviceGroupDecl.ShareNetwork {
		facts = append(facts, Fact{
			Key: types.KeyDesiredGroupShareNetwork(groupName), Value: "true",
		})
	}
	for _, volumeName := range serviceGroupDecl.SharedVolumes {
		facts = append(facts, Fact{
			Key: types.KeyDesiredGroupShareVolume(groupName, volumeName), Value: "true",
		})
	}
	return facts, nil
}

// compilePolicyDeclaration converts a PolicyDecl into auth/policy/ prefix facts.
// Each policy produces a capability fact and per-condition field/operator/value facts.
func compilePolicyDeclaration(policyDecl PolicyDecl, sourceLines []string) ([]Fact, error) {
	if policyDecl.Name == "" {
		return nil, &ParseError{
			Line:       policyDecl.Line,
			Message:    "policy name is required",
			SourceLine: sourceLineAt(sourceLines, policyDecl.Line),
		}
	}
	if policyDecl.Capability == "" {
		return nil, &ParseError{
			Line:       policyDecl.Line,
			Message:    "policy must have an \"allow\" clause with a capability",
			SourceLine: sourceLineAt(sourceLines, policyDecl.Line),
		}
	}

	facts := []Fact{
		{Key: types.KeyAuthPolicyCapability(policyDecl.Name), Value: policyDecl.Capability},
	}

	for conditionIndex, conditionDecl := range policyDecl.Conditions {
		facts = append(facts,
			Fact{Key: types.KeyAuthPolicyConditionField(policyDecl.Name, conditionIndex), Value: conditionDecl.Field},
			Fact{Key: types.KeyAuthPolicyConditionOperator(policyDecl.Name, conditionIndex), Value: conditionDecl.Operator},
			Fact{Key: types.KeyAuthPolicyConditionValue(policyDecl.Name, conditionIndex), Value: conditionDecl.Value},
		)
	}

	return facts, nil
}

// compileNetworkDeclaration converts a NetworkDecl into policy/network/ facts.
// Each rule is stored as "source:target:port:action" under a generated name
// derived from the source, target, and rule index within the block.
func compileNetworkDeclaration(networkDecl NetworkDecl, sourceLines []string) ([]Fact, error) {
	var facts []Fact
	for ruleIndex, rule := range networkDecl.Rules {
		if rule.Source == "" || rule.Target == "" {
			return nil, &ParseError{
				Line:       rule.Line,
				Message:    "network rule requires both source and target service names",
				SourceLine: sourceLineAt(sourceLines, rule.Line),
			}
		}
		ruleName := fmt.Sprintf("%s-to-%s-%d", sanitizeRuleName(rule.Source), sanitizeRuleName(rule.Target), ruleIndex)
		ruleValue := fmt.Sprintf("%s:%s:%d:%s", rule.Source, rule.Target, rule.Port, rule.Action)
		facts = append(facts, Fact{
			Key:   types.KeyNetworkPolicyRule(ruleName),
			Value: ruleValue,
		})
	}
	return facts, nil
}

// sanitizeRuleName replaces slashes in service names with dashes to produce
// valid fact key segments.
func sanitizeRuleName(serviceName string) string {
	return strings.ReplaceAll(serviceName, "/", "-")
}

// Apply parses a DSL string and writes all resulting facts to the store.
func Apply(ctx context.Context, stateStore store.StateStore, input string) error {
	return ApplyWithBaseDir(ctx, stateStore, input, "")
}

// ApplyWithBaseDir parses a DSL string and writes all resulting facts to the
// store. The baseDir is used to resolve relative "from" paths in config blocks.
func ApplyWithBaseDir(ctx context.Context, stateStore store.StateStore, input string, baseDir string) error {
	file, parseError := Parse(input)
	if parseError != nil {
		return parseError
	}
	file.BaseDir = baseDir
	sourceLines := splitSourceLines(input)
	facts, compileError := CompileWithSource(file, sourceLines)
	if compileError != nil {
		return compileError
	}
	for _, fact := range facts {
		if _, putError := stateStore.Put(ctx, fact.Key, []byte(fact.Value)); putError != nil {
			return fmt.Errorf("writing %s: %w", fact.Key, putError)
		}
	}
	return nil
}

// FactChange describes a single fact that would be added or modified by an apply.
type FactChange struct {
	Key      string `json:"key"`
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value"`
	Type     string `json:"type"` // "add", "modify", or "unchanged"
}

// Diff parses a DSL string, compiles it to facts, and compares each fact
// against the current store state. It returns a list of changes without
// writing anything. Facts that exist in the store but not in the compiled
// output are not reported — diff only shows what the apply would write.
func Diff(ctx context.Context, stateStore store.StateStore, input string) ([]FactChange, error) {
	file, parseError := Parse(input)
	if parseError != nil {
		return nil, parseError
	}
	sourceLines := splitSourceLines(input)
	facts, compileError := CompileWithSource(file, sourceLines)
	if compileError != nil {
		return nil, compileError
	}
	var changes []FactChange
	for _, fact := range facts {
		existing, getError := stateStore.Get(ctx, fact.Key)
		switch {
		case getError != nil:
			changes = append(changes, FactChange{
				Key:      fact.Key,
				NewValue: fact.Value,
				Type:     "add",
			})
		case string(existing.Value) != fact.Value:
			changes = append(changes, FactChange{
				Key:      fact.Key,
				OldValue: string(existing.Value),
				NewValue: fact.Value,
				Type:     "modify",
			})
		default:
			changes = append(changes, FactChange{
				Key:      fact.Key,
				NewValue: fact.Value,
				Type:     "unchanged",
			})
		}
	}
	return changes, nil
}

// compileJobDeclaration compiles a JobDecl into desired/job/{name}/ facts.
func compileJobDeclaration(jobDecl JobDecl, sourceLines []string) ([]Fact, error) {
	if jobDecl.Name == "" {
		return nil, &ParseError{
			Line: jobDecl.Line, Message: "job name is required",
			SourceLine: sourceLineAt(sourceLines, jobDecl.Line),
		}
	}
	if jobDecl.Image == "" {
		return nil, &ParseError{
			Line: jobDecl.Line, Message: fmt.Sprintf("job %q requires an image", jobDecl.Name),
			SourceLine: sourceLineAt(sourceLines, jobDecl.Line),
		}
	}

	completions := jobDecl.Completions
	if completions == 0 {
		completions = 1
	}
	parallelism := jobDecl.Parallelism
	if parallelism == 0 {
		parallelism = 1
	}
	backoffLimit := jobDecl.BackoffLimit
	if backoffLimit == 0 {
		backoffLimit = defaultJobBackoffLimit
	}

	facts := []Fact{
		{Key: types.KeyDesiredJob(jobDecl.Name), Value: ""},
		{Key: types.KeyDesiredJobImage(jobDecl.Name), Value: jobDecl.Image},
		{Key: types.KeyDesiredJobCompletions(jobDecl.Name), Value: strconv.Itoa(completions)},
		{Key: types.KeyDesiredJobParallelism(jobDecl.Name), Value: strconv.Itoa(parallelism)},
		{Key: types.KeyDesiredJobBackoffLimit(jobDecl.Name), Value: strconv.Itoa(backoffLimit)},
	}

	if jobDecl.Command != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredJobCommand(jobDecl.Name), Value: jobDecl.Command,
		})
	}

	if jobDecl.ImagePullSecret != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredJobImagePullSecret(jobDecl.Name), Value: jobDecl.ImagePullSecret,
		})
	}

	return facts, nil
}

// compileCronJobDeclaration validates a cronjob block and emits facts for its
// schedule, image, command, completions, and backoff limit.
func compileCronJobDeclaration(cronJobDecl CronJobDecl, sourceLines []string) ([]Fact, error) {
	if cronJobDecl.Name == "" {
		return nil, &ParseError{
			Line: cronJobDecl.Line, Message: "cronjob requires a name",
			SourceLine: sourceLineAt(sourceLines, cronJobDecl.Line),
		}
	}
	if cronJobDecl.Image == "" {
		return nil, &ParseError{
			Line: cronJobDecl.Line, Message: fmt.Sprintf("cronjob %q requires an image", cronJobDecl.Name),
			SourceLine: sourceLineAt(sourceLines, cronJobDecl.Line),
		}
	}
	if cronJobDecl.Schedule == "" {
		return nil, &ParseError{
			Line: cronJobDecl.Line, Message: fmt.Sprintf("cronjob %q requires a schedule", cronJobDecl.Name),
			SourceLine: sourceLineAt(sourceLines, cronJobDecl.Line),
		}
	}

	completions := cronJobDecl.Completions
	if completions == 0 {
		completions = 1
	}
	parallelism := cronJobDecl.Parallelism
	if parallelism == 0 {
		parallelism = 1
	}
	backoffLimit := cronJobDecl.BackoffLimit
	if backoffLimit == 0 {
		backoffLimit = defaultJobBackoffLimit
	}

	facts := []Fact{
		{Key: types.KeyDesiredCronJob(cronJobDecl.Name), Value: ""},
		{Key: types.KeyDesiredCronJobSchedule(cronJobDecl.Name), Value: cronJobDecl.Schedule},
		{Key: types.KeyDesiredCronJobImage(cronJobDecl.Name), Value: cronJobDecl.Image},
		{Key: types.KeyDesiredCronJobCompletions(cronJobDecl.Name), Value: strconv.Itoa(completions)},
		{Key: types.KeyDesiredCronJobParallelism(cronJobDecl.Name), Value: strconv.Itoa(parallelism)},
		{Key: types.KeyDesiredCronJobBackoffLimit(cronJobDecl.Name), Value: strconv.Itoa(backoffLimit)},
	}

	if cronJobDecl.Command != "" {
		facts = append(facts, Fact{
			Key: types.KeyDesiredCronJobCommand(cronJobDecl.Name), Value: cronJobDecl.Command,
		})
	}

	return facts, nil
}

// defaultJobBackoffLimit is the default number of failures before a job
// is marked failed, when no backoff_limit is specified in the DSL.
const defaultJobBackoffLimit = 3
