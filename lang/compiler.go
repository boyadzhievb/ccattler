package lang

import (
	"context"
	"fmt"
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
	for _, serviceDecl := range file.Services {
		serviceFacts, err := compileServiceDeclaration(serviceDecl, sourceLines)
		if err != nil {
			return nil, err
		}
		facts = append(facts, serviceFacts...)
	}
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
	return facts, nil
}

// compileServiceDeclaration converts a single ServiceDecl into its corresponding facts.
// It validates the declaration and delegates to per-block sub-compilers for each
// DSL section (ports, resources, scale, placement, health, config, secrets, etc.).
func compileServiceDeclaration(serviceDecl ServiceDecl, sourceLines []string) ([]Fact, error) {
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

	facts := []Fact{
		{Key: types.KeyDesiredService(serviceDecl.Name), Value: ""},
		{Key: types.KeyDesiredServiceImage(serviceDecl.Name), Value: serviceDecl.Image},
		{Key: types.KeyDesiredServiceInstances(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Instances)},
		{Key: types.KeyIntentUserServiceInstances(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Instances)},
		{Key: types.KeyEffectiveServiceInstances(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Instances)},
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
	facts = append(facts, compileServiceConfigFacts(serviceDecl.Name, serviceDecl.Config)...)
	facts = append(facts, compileServiceSecretFacts(serviceDecl.Name, serviceDecl.Secrets)...)
	facts = append(facts, compileServiceCloudIdentityFacts(serviceDecl.Name, serviceDecl.CloudIdentities)...)
	facts = append(facts, compileServiceInitStepFacts(serviceDecl.Name, serviceDecl.InitSteps)...)
	facts = append(facts, compileServiceDisruptionFacts(serviceDecl.Name, serviceDecl.Disruption)...)

	if serviceDecl.Stateful {
		facts = append(facts,
			Fact{Key: types.KeyDesiredServiceStateful(serviceDecl.Name), Value: "true"},
			Fact{Key: types.KeyEffectiveServiceStateful(serviceDecl.Name), Value: "true"},
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
	return resourceFacts
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
	if placementDecl.Architecture != "" {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementArchitecture(serviceName), Value: placementDecl.Architecture,
		})
	}
	if placementDecl.ZonePolicy != "" {
		placementFacts = append(placementFacts, Fact{
			Key: types.KeyDesiredServicePlacementZonePolicy(serviceName), Value: placementDecl.ZonePolicy,
		})
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
func compileServiceConfigFacts(serviceName string, configDecl *ConfigDecl) []Fact {
	if configDecl == nil {
		return nil
	}
	var configFacts []Fact
	for _, envVar := range configDecl.EnvVars {
		configFacts = append(configFacts, Fact{
			Key:   types.KeyDesiredServiceConfigEnv(serviceName, envVar.Name),
			Value: envVar.Value,
		})
	}
	for _, configFile := range configDecl.ConfigFiles {
		configFacts = append(configFacts, Fact{
			Key:   types.KeyDesiredServiceConfigFile(serviceName, configFile.Path),
			Value: configFile.Content,
		})
	}
	return configFacts
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
		initFacts = append(initFacts, Fact{
			Key: types.KeyDesiredServiceInitStepExec(serviceName, stepIndex), Value: initStep.Exec,
		})
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
	if cloudIdentityDecl.Name == "" {
		return nil, &ParseError{
			Line:       cloudIdentityDecl.Line,
			Message:    "cloud_identity name is required",
			SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
		}
	}
	if cloudIdentityDecl.Provider == "" {
		return nil, &ParseError{
			Line:       cloudIdentityDecl.Line,
			Message:    fmt.Sprintf("cloud_identity %q requires a provider (aws, gcp, or azure)", cloudIdentityDecl.Name),
			SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
		}
	}

	switch cloudIdentityDecl.Provider {
	case "aws":
		if cloudIdentityDecl.Role == "" {
			return nil, &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider aws requires a role", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
	case "gcp":
		if cloudIdentityDecl.ServiceAccount == "" {
			return nil, &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider gcp requires a service_account", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
		if cloudIdentityDecl.Pool == "" {
			return nil, &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider gcp requires a pool", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
	case "azure":
		if cloudIdentityDecl.ClientID == "" {
			return nil, &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider azure requires a client_id", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
		if cloudIdentityDecl.TenantID == "" {
			return nil, &ParseError{
				Line:       cloudIdentityDecl.Line,
				Message:    fmt.Sprintf("cloud_identity %q with provider azure requires a tenant_id", cloudIdentityDecl.Name),
				SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
			}
		}
	default:
		return nil, &ParseError{
			Line:       cloudIdentityDecl.Line,
			Message:    fmt.Sprintf("cloud_identity %q has unknown provider %q (must be aws, gcp, or azure)", cloudIdentityDecl.Name, cloudIdentityDecl.Provider),
			SourceLine: sourceLineAt(sourceLines, cloudIdentityDecl.Line),
		}
	}

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

	return facts, nil
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
	file, parseError := Parse(input)
	if parseError != nil {
		return parseError
	}
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
