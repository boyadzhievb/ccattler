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
	var facts []Fact
	for _, tenantDecl := range file.Tenants {
		tenantFacts, err := compileTenantDeclaration(tenantDecl)
		if err != nil {
			return nil, err
		}
		facts = append(facts, tenantFacts...)
	}
	for _, volumeDecl := range file.Volumes {
		volumeFacts, err := compileVolumeDeclaration(volumeDecl)
		if err != nil {
			return nil, err
		}
		facts = append(facts, volumeFacts...)
	}
	for _, serviceDecl := range file.Services {
		serviceFacts, err := compileServiceDeclaration(serviceDecl)
		if err != nil {
			return nil, err
		}
		facts = append(facts, serviceFacts...)
	}
	return facts, nil
}

// compileTenantDeclaration converts a TenantDecl into its corresponding facts.
func compileTenantDeclaration(tenantDecl TenantDecl) ([]Fact, error) {
	if tenantDecl.Name == "" {
		return nil, fmt.Errorf("line %d: tenant name is required", tenantDecl.Line)
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
func compileVolumeDeclaration(volumeDecl VolumeDecl) ([]Fact, error) {
	if volumeDecl.Name == "" {
		return nil, fmt.Errorf("line %d: volume name is required", volumeDecl.Line)
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
func compileServiceDeclaration(serviceDecl ServiceDecl) ([]Fact, error) {
	if serviceDecl.Name == "" {
		return nil, fmt.Errorf("line %d: service name is required", serviceDecl.Line)
	}
	if serviceDecl.Image == "" {
		return nil, fmt.Errorf("line %d: service %q requires an image", serviceDecl.Line, serviceDecl.Name)
	}
	if serviceDecl.Instances < 0 {
		return nil, fmt.Errorf("line %d: service %q instances must be >= 0", serviceDecl.Line, serviceDecl.Name)
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
			return nil, fmt.Errorf("line %d: service %q port %d out of range", serviceDecl.Line, serviceDecl.Name, port)
		}
		facts = append(facts, Fact{
			Key: types.KeyDesiredServiceExpose(serviceDecl.Name, port), Value: "",
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
			return nil, fmt.Errorf("line %d: service %q scale min must be >= 0", serviceDecl.Line, serviceDecl.Name)
		}
		if horizontal.Max < horizontal.Min {
			return nil, fmt.Errorf("line %d: service %q scale max must be >= min", serviceDecl.Line, serviceDecl.Name)
		}
		facts = append(facts,
			Fact{Key: types.KeyDesiredServiceScaleHorizontalMin(serviceDecl.Name), Value: strconv.Itoa(horizontal.Min)},
			Fact{Key: types.KeyDesiredServiceScaleHorizontalMax(serviceDecl.Name), Value: strconv.Itoa(horizontal.Max)},
		)
		for _, target := range horizontal.Targets {
			if target.Value <= 0 {
				return nil, fmt.Errorf("line %d: service %q scale target %q must be > 0", serviceDecl.Line, serviceDecl.Name, target.Metric)
			}
			facts = append(facts, Fact{
				Key:   types.KeyDesiredServiceScaleHorizontalTarget(serviceDecl.Name, target.Metric),
				Value: strconv.Itoa(target.Value),
			})
		}
		for _, event := range horizontal.Events {
			if event.Target <= 0 {
				return nil, fmt.Errorf("line %d: service %q event target for %q must be > 0", serviceDecl.Line, serviceDecl.Name, event.Source)
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

// Apply parses a DSL string and writes all resulting facts to the store.
func Apply(ctx context.Context, stateStore store.StateStore, input string) error {
	file, err := Parse(input)
	if err != nil {
		return err
	}
	facts, err := Compile(file)
	if err != nil {
		return err
	}
	for _, fact := range facts {
		if _, err := stateStore.Put(ctx, fact.Key, []byte(fact.Value)); err != nil {
			return fmt.Errorf("writing %s: %w", fact.Key, err)
		}
	}
	return nil
}
