package lang

import (
	"context"
	"fmt"
	"strconv"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Fact is a key-value pair ready to be written to the store.
type Fact struct {
	Key   string // Key is the fact store key (e.g. "desired/service/web/image").
	Value string // Value is the serialized fact value (may be empty for marker facts).
}

// Compile converts a parsed AST into a list of facts.
func Compile(file *File) ([]Fact, error) {
	var facts []Fact
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
