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

	for _, port := range serviceDecl.Ports {
		if port < 1 || port > 65535 {
			return nil, &ParseError{
				Line: serviceDecl.Line, Message: fmt.Sprintf("service %q port %d out of range (1-65535)", serviceDecl.Name, port),
				SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
			}
		}
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceExpose(serviceDecl.Name, port), Value: "",
		})
	}

	for _, externalPort := range serviceDecl.ExternalPorts {
		if externalPort.Port < 1 || externalPort.Port > 65535 {
			return nil, &ParseError{
				Line: serviceDecl.Line, Message: fmt.Sprintf("service %q external port %d out of range (1-65535)", serviceDecl.Name, externalPort.Port),
				SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
			}
		}
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceExpose(serviceDecl.Name, externalPort.Port), Value: "",
		})
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceExposeExternal(serviceDecl.Name, externalPort.Port), Value: externalPort.Protocol,
		})
	}

	if serviceDecl.Resources != nil {
		if serviceDecl.Resources.CPU != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceResourcesCPU(serviceDecl.Name), Value: serviceDecl.Resources.CPU,
			})
		}
		if serviceDecl.Resources.Memory != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceResourcesMemory(serviceDecl.Name), Value: serviceDecl.Resources.Memory,
			})
		}
	}

	for _, volumeMount := range serviceDecl.VolumeMounts {
		facts = append(facts, Fact{
			Key:   types.KeyDesiredServiceVolume(serviceDecl.Name, volumeMount.VolumeName),
			Value: volumeMount.MountPath,
		})
	}

	if serviceDecl.Scale != nil && serviceDecl.Scale.Horizontal != nil {
		horizontal := serviceDecl.Scale.Horizontal
		if horizontal.Min < 0 {
			return nil, &ParseError{
				Line: serviceDecl.Line, Message: fmt.Sprintf("service %q scale min must be >= 0", serviceDecl.Name),
				SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
			}
		}
		if horizontal.Max < horizontal.Min {
			return nil, &ParseError{
				Line: serviceDecl.Line, Message: fmt.Sprintf("service %q scale max must be >= min", serviceDecl.Name),
				SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
			}
		}
		facts = append(facts,
			Fact{Key: types.KeyDesiredServiceScaleHorizontalMin(serviceDecl.Name), Value: strconv.Itoa(horizontal.Min)},
			Fact{Key: types.KeyDesiredServiceScaleHorizontalMax(serviceDecl.Name), Value: strconv.Itoa(horizontal.Max)},
		)
		for _, target := range horizontal.Targets {
			if target.Value <= 0 {
				return nil, &ParseError{
					Line: serviceDecl.Line, Message: fmt.Sprintf("service %q scale target %q must be > 0", serviceDecl.Name, target.Metric),
					SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
				}
			}
			facts = append(facts, Fact{
				Key:   types.KeyDesiredServiceScaleHorizontalTarget(serviceDecl.Name, target.Metric),
				Value: strconv.Itoa(target.Value),
			})
		}
		for _, event := range horizontal.Events {
			if event.Target <= 0 {
				return nil, &ParseError{
					Line: serviceDecl.Line, Message: fmt.Sprintf("service %q event target for %q must be > 0", serviceDecl.Name, event.Source),
					SourceLine: sourceLineAt(sourceLines, serviceDecl.Line),
				}
			}
			facts = append(facts, Fact{
				Key:   types.KeyDesiredServiceScaleHorizontalEvent(serviceDecl.Name, event.Source),
				Value: strconv.Itoa(event.Target),
			})
		}
		if horizontal.Schedule != nil {
			facts = append(facts,
				Fact{Key: types.KeyDesiredServiceScaleScheduleDays(serviceDecl.Name), Value: horizontal.Schedule.Days},
				Fact{Key: types.KeyDesiredServiceScaleScheduleStart(serviceDecl.Name), Value: horizontal.Schedule.Start},
				Fact{Key: types.KeyDesiredServiceScaleScheduleEnd(serviceDecl.Name), Value: horizontal.Schedule.End},
				Fact{Key: types.KeyDesiredServiceScaleScheduleMinimum(serviceDecl.Name), Value: strconv.Itoa(horizontal.Schedule.Minimum)},
			)
		}
		if horizontal.Stabilization != nil {
			if horizontal.Stabilization.ScaleUp != "" {
				facts = append(facts, Fact{
					Key: types.KeyDesiredServiceScaleStabilizationUp(serviceDecl.Name), Value: horizontal.Stabilization.ScaleUp,
				})
			}
			if horizontal.Stabilization.ScaleDown != "" {
				facts = append(facts, Fact{
					Key: types.KeyDesiredServiceScaleStabilizationDown(serviceDecl.Name), Value: horizontal.Stabilization.ScaleDown,
				})
			}
		}
		if horizontal.IdleTimeout != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceScaleIdleTimeout(serviceDecl.Name), Value: horizontal.IdleTimeout,
			})
		}
		if horizontal.ActivationTimeout != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceScaleActivationTimeout(serviceDecl.Name), Value: horizontal.ActivationTimeout,
			})
		}
	}

	if serviceDecl.Scale != nil && serviceDecl.Scale.Vertical != nil {
		vertical := serviceDecl.Scale.Vertical
		if vertical.CPUMin != "" {
			facts = append(facts, Fact{Key: types.KeyDesiredServiceScaleVerticalCPUMin(serviceDecl.Name), Value: vertical.CPUMin})
		}
		if vertical.CPUMax != "" {
			facts = append(facts, Fact{Key: types.KeyDesiredServiceScaleVerticalCPUMax(serviceDecl.Name), Value: vertical.CPUMax})
		}
		if vertical.MemoryMin != "" {
			facts = append(facts, Fact{Key: types.KeyDesiredServiceScaleVerticalMemoryMin(serviceDecl.Name), Value: vertical.MemoryMin})
		}
		if vertical.MemoryMax != "" {
			facts = append(facts, Fact{Key: types.KeyDesiredServiceScaleVerticalMemoryMax(serviceDecl.Name), Value: vertical.MemoryMax})
		}
	}

	if serviceDecl.Placement != nil {
		if serviceDecl.Placement.Architecture != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServicePlacementArchitecture(serviceDecl.Name), Value: serviceDecl.Placement.Architecture,
			})
		}
		if serviceDecl.Placement.ZonePolicy != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServicePlacementZonePolicy(serviceDecl.Name), Value: serviceDecl.Placement.ZonePolicy,
			})
		}
		for _, requireRule := range serviceDecl.Placement.Require {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServicePlacementRequire(serviceDecl.Name, requireRule.Label), Value: requireRule.Value,
			})
		}
		for _, preferRule := range serviceDecl.Placement.Prefer {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServicePlacementPrefer(serviceDecl.Name, preferRule.Label), Value: preferRule.Value,
			})
		}
		for _, acceptLabel := range serviceDecl.Placement.Accept {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServicePlacementAccept(serviceDecl.Name, acceptLabel), Value: "",
			})
		}
	}

	if serviceDecl.Update != nil {
		facts = append(facts,
			Fact{Key: types.KeyDesiredServiceUpdateMaxUnavailable(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Update.MaxUnavailable)},
			Fact{Key: types.KeyDesiredServiceUpdateMaxExtra(serviceDecl.Name), Value: strconv.Itoa(serviceDecl.Update.MaxExtra)},
		)
	}

	if serviceDecl.Health != nil {
		if serviceDecl.Health.Method != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceHealthMethod(serviceDecl.Name), Value: serviceDecl.Health.Method,
			})
		}
		if serviceDecl.Health.Path != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceHealthPath(serviceDecl.Name), Value: serviceDecl.Health.Path,
			})
		}
		if serviceDecl.Health.Interval != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceHealthInterval(serviceDecl.Name), Value: serviceDecl.Health.Interval,
			})
		}
	}

	if serviceDecl.Config != nil {
		for _, envVar := range serviceDecl.Config.EnvVars {
			facts = append(facts, Fact{
				Key:   types.KeyDesiredServiceConfigEnv(serviceDecl.Name, envVar.Name),
				Value: envVar.Value,
			})
		}
		for _, configFile := range serviceDecl.Config.ConfigFiles {
			facts = append(facts, Fact{
				Key:   types.KeyDesiredServiceConfigFile(serviceDecl.Name, configFile.Path),
				Value: configFile.Content,
			})
		}
	}

	for _, secretDecl := range serviceDecl.Secrets {
		facts = append(facts, Fact{
			Key:   types.KeyDesiredServiceSecret(serviceDecl.Name, secretDecl.Name),
			Value: secretDecl.MountPath,
		})
	}

	for _, cloudIdentityBinding := range serviceDecl.CloudIdentities {
		facts = append(facts, Fact{
			Key:   types.KeyDesiredServiceCloudIdentity(serviceDecl.Name, cloudIdentityBinding.IdentityName),
			Value: "",
		})
		if cloudIdentityBinding.MountPath != "" {
			facts = append(facts, Fact{
				Key:   types.KeyDesiredServiceCloudIdentityMountPath(serviceDecl.Name, cloudIdentityBinding.IdentityName),
				Value: cloudIdentityBinding.MountPath,
			})
		}
		if cloudIdentityBinding.DeliverMode != "" {
			facts = append(facts, Fact{
				Key:   types.KeyDesiredServiceCloudIdentityDeliverMode(serviceDecl.Name, cloudIdentityBinding.IdentityName),
				Value: cloudIdentityBinding.DeliverMode,
			})
		}
	}

	if serviceDecl.Startup != nil {
		facts = append(facts, compileProbeDeclaration(serviceDecl.Name, "startup", serviceDecl.Startup)...)
	}
	if serviceDecl.Liveness != nil {
		facts = append(facts, compileProbeDeclaration(serviceDecl.Name, "liveness", serviceDecl.Liveness)...)
	}
	if serviceDecl.Readiness != nil {
		facts = append(facts, compileProbeDeclaration(serviceDecl.Name, "readiness", serviceDecl.Readiness)...)
	}

	for stepIndex, initStep := range serviceDecl.InitSteps {
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceInitStep(serviceDecl.Name, stepIndex), Value: "",
		})
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceInitStepExec(serviceDecl.Name, stepIndex), Value: initStep.Exec,
		})
		if initStep.Timeout != "" {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceInitStepTimeout(serviceDecl.Name, stepIndex), Value: initStep.Timeout,
			})
		}
		if initStep.Retry > 0 {
			facts = append(facts, Fact{
				Key: types.KeyDesiredServiceInitStepRetry(serviceDecl.Name, stepIndex), Value: strconv.Itoa(initStep.Retry),
			})
		}
	}

	return facts, nil
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
func compileProbeDeclaration(serviceName string, probeType string, probeDecl *ProbeDecl) []Fact {
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
		if getError != nil {
			changes = append(changes, FactChange{
				Key:      fact.Key,
				NewValue: fact.Value,
				Type:     "add",
			})
		} else if string(existing.Value) != fact.Value {
			changes = append(changes, FactChange{
				Key:      fact.Key,
				OldValue: string(existing.Value),
				NewValue: fact.Value,
				Type:     "modify",
			})
		} else {
			changes = append(changes, FactChange{
				Key:      fact.Key,
				NewValue: fact.Value,
				Type:     "unchanged",
			})
		}
	}
	return changes, nil
}
