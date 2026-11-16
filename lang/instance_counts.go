// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/types"
)

// ServiceInstanceCount is the largest instance count one compiled service can
// reach: its instances, or its autoscaling min or max, whichever is largest.
type ServiceInstanceCount struct {
	ServiceName   string // ServiceName is the declared service.
	InstanceCount int    // InstanceCount is the largest of instances, scale min and scale max.
}

// ServiceInstanceCountsFromFacts returns the largest count of every service
// declared in facts, sorted by name, for the M217 limit. A service is a
// declaration marker with an image. Every value of a key counts, not the
// last: a hierarchical name can compile to another service's count key.
func ServiceInstanceCountsFromFacts(facts []Fact) []ServiceInstanceCount {
	valuesByKey := make(map[string][]string, len(facts))
	for _, fact := range facts {
		valuesByKey[fact.Key] = append(valuesByKey[fact.Key], fact.Value)
	}
	servicePrefix := types.KeyDesiredService("")
	var counts []ServiceInstanceCount
	for _, fact := range facts {
		serviceName, isServiceKey := strings.CutPrefix(fact.Key, servicePrefix)
		if !isServiceKey || serviceName == "" || fact.Value != "" {
			continue
		}
		if _, hasImage := valuesByKey[types.KeyDesiredServiceImage(serviceName)]; !hasImage {
			continue
		}
		largestCount := 0
		for _, countKey := range []string{
			types.KeyDesiredServiceInstances(serviceName),
			types.KeyDesiredServiceScaleHorizontalMin(serviceName),
			types.KeyDesiredServiceScaleHorizontalMax(serviceName),
		} {
			for _, countValue := range valuesByKey[countKey] {
				if parsedCount, parseError := strconv.Atoi(countValue); parseError == nil && parsedCount > largestCount {
					largestCount = parsedCount
				}
			}
		}
		counts = append(counts, ServiceInstanceCount{ServiceName: serviceName, InstanceCount: largestCount})
	}
	sort.Slice(counts, func(indexA, indexB int) bool { return counts[indexA].ServiceName < counts[indexB].ServiceName })
	return counts
}
