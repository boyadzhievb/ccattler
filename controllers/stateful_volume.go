// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// StatefulVolumeController creates per-ordinal volume declarations for
// stateful services. When a stateful service mounts a volume named "pgdata"
// and has 3 instances, this controller ensures volumes named
// "postgres-0-pgdata", "postgres-1-pgdata", "postgres-2-pgdata" exist in
// desired state, each inheriting the original volume's size and persistence
// settings. The storage controller then materializes them.
type StatefulVolumeController struct{}

// NewStatefulVolumeController returns a ready-to-use StatefulVolumeController.
func NewStatefulVolumeController() *StatefulVolumeController {
	return &StatefulVolumeController{}
}

// Name returns "stateful-volume", identifying this controller in logs and
// runner bookkeeping.
func (statefulVolumeController *StatefulVolumeController) Name() string { return "stateful-volume" }

// Watch returns the fact prefixes the stateful volume controller monitors:
// effective services (for stateful flag and instance count), desired services
// (for volume mount bindings), and desired volumes (for size and persistence).
func (statefulVolumeController *StatefulVolumeController) Watch() []string {
	return []string{
		types.ScanEffectiveServices,
		types.ScanDesiredServices,
		types.ScanDesiredVolumes,
	}
}

// Reconcile examines stateful service volume mounts and effective instance
// counts, then emits changes to create per-ordinal volume declarations.
func (statefulVolumeController *StatefulVolumeController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredCounts, statefulServices := parseEffectiveServiceFacts(facts)
	serviceVolumeMounts := parseServiceVolumeMountsWithPaths(facts)
	volumeTemplates := parseVolumeTemplates(facts)
	existingDesiredVolumes := parseExistingDesiredVolumeNames(facts)

	var changes []Change
	sortedServiceNames := sortedMapKeys(statefulServices)

	for _, serviceName := range sortedServiceNames {
		if !statefulServices[serviceName] {
			continue
		}
		instanceCount := desiredCounts[serviceName]
		mounts := serviceVolumeMounts[serviceName]
		changes = append(changes, reconcilePerOrdinalVolumes(
			serviceName, instanceCount, mounts, volumeTemplates, existingDesiredVolumes,
		)...)
	}

	return changes, nil
}

// volumeTemplate holds the size and persistence flag for a volume declaration.
type volumeTemplate struct {
	size       string // declared storage size (e.g. "100Gi")
	persistent string // "true" or "false"
}

// volumeMountBinding holds a volume name and its mount path inside the service.
type volumeMountBinding struct {
	volumeName string // name of the volume template (e.g. "pgdata")
	mountPath  string // filesystem path inside the instance (e.g. "/var/lib/postgresql/data")
}

// parseServiceVolumeMountsWithPaths extracts volume mount bindings and their
// mount paths from desired service facts. Returns a map from service name to
// volume mount bindings sorted by volume name.
func parseServiceVolumeMountsWithPaths(facts []store.Fact) map[string][]volumeMountBinding {
	mounts := make(map[string][]volumeMountBinding)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 3 && pathParts[1] == "volume" {
			serviceName := pathParts[0]
			volumeName := pathParts[2]
			mounts[serviceName] = append(mounts[serviceName], volumeMountBinding{
				volumeName: volumeName,
				mountPath:  string(fact.Value),
			})
		}
	}
	for serviceName := range mounts {
		sort.Slice(mounts[serviceName], func(i, j int) bool {
			return mounts[serviceName][i].volumeName < mounts[serviceName][j].volumeName
		})
	}
	return mounts
}

// parseVolumeTemplates extracts size and persistence from desired volume
// declarations, providing templates for per-ordinal volume creation.
func parseVolumeTemplates(facts []store.Fact) map[string]volumeTemplate {
	templates := make(map[string]volumeTemplate)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredVolumes) {
		volumeName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredVolumes)
		if !hasSuffix {
			continue
		}
		current := templates[volumeName]
		switch suffix {
		case "size":
			current.size = string(fact.Value)
		case "persistent":
			current.persistent = string(fact.Value)
		}
		templates[volumeName] = current
	}
	return templates
}

// parseExistingDesiredVolumeNames returns the set of volume names that already
// have desired-state root marker keys.
func parseExistingDesiredVolumeNames(facts []store.Fact) map[string]bool {
	existing := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredVolumes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredVolumes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 1 {
			existing[pathParts[0]] = true
		}
	}
	return existing
}

// reconcilePerOrdinalVolumes generates changes to create per-ordinal volume
// declarations for a stateful service. Each ordinal gets a volume named
// "{service}-{ordinal}-{volumeName}" inheriting the template's properties.
func reconcilePerOrdinalVolumes(
	serviceName string, instanceCount int,
	mounts []volumeMountBinding,
	volumeTemplates map[string]volumeTemplate,
	existingDesiredVolumes map[string]bool,
) []Change {
	var changes []Change

	for ordinal := range instanceCount {
		for _, mount := range mounts {
			perOrdinalVolumeName := fmt.Sprintf("%s-%d-%s", serviceName, ordinal, mount.volumeName)
			if existingDesiredVolumes[perOrdinalVolumeName] {
				continue
			}
			template := volumeTemplates[mount.volumeName]
			changes = append(changes,
				Change{Type: store.OpPut, Key: types.KeyDesiredVolume(perOrdinalVolumeName), Value: []byte("")},
			)
			if template.size != "" {
				changes = append(changes,
					Change{Type: store.OpPut, Key: types.KeyDesiredVolumeSize(perOrdinalVolumeName), Value: []byte(template.size)},
				)
			}
			persistentValue := template.persistent
			if persistentValue == "" {
				persistentValue = "true"
			}
			changes = append(changes,
				Change{Type: store.OpPut, Key: types.KeyDesiredVolumePersistent(perOrdinalVolumeName), Value: []byte(persistentValue)},
			)
		}
	}

	return changes
}
