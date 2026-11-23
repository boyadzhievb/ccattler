// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import (
	"cmp"
	"slices"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
)

// ResourceKind names a requested resource: CPU or memory (M243). It is the
// typed key of every per-kind table, so no reader compares raw strings.
type ResourceKind string

const (
	// ResourceKindCPU is a CPU request, read in millicores.
	ResourceKindCPU ResourceKind = "cpu"
	// ResourceKindMemory is a memory request, read in bytes.
	ResourceKindMemory ResourceKind = "memory"
)

// ResourceKindSpec is how one resource kind is stored and read: the key
// suffix of its requests and recommendations, its parser and its keys.
type ResourceKindSpec struct {
	Kind             ResourceKind                // Kind is the resource this spec describes
	RequestKeySuffix string                      // RequestKeySuffix ends a request key, e.g. "/resources/cpu"
	Parse            func(string) (int64, error) // Parse reads a stored value (ParseCPUMillicores, ParseMemoryBytes)
	IntentKey        func(string) string         // IntentKey builds intent/autoscaler/service/{svc}/resources/{kind}
	EffectiveKey     func(string) string         // EffectiveKey builds effective/service/{svc}/resources/{kind}
}

// resourceKindSpecs lists the kinds in a fixed order, CPU first, so every
// per-kind loop is deterministic. A fixed-size array: it cannot grow.
var resourceKindSpecs = [...]ResourceKindSpec{
	{Kind: ResourceKindCPU, RequestKeySuffix: "/resources/cpu", Parse: ParseCPUMillicores,
		IntentKey: KeyIntentAutoscalerServiceResourcesCPU, EffectiveKey: KeyEffectiveServiceResourcesCPU},
	{Kind: ResourceKindMemory, RequestKeySuffix: "/resources/memory", Parse: ParseMemoryBytes,
		IntentKey: KeyIntentAutoscalerServiceResourcesMemory, EffectiveKey: KeyEffectiveServiceResourcesMemory},
}

// ResourceKindSpecs returns a copy of the spec of every resource kind, CPU
// first, so a caller cannot change the table (R5-N4).
func ResourceKindSpecs() []ResourceKindSpec {
	return slices.Clone(resourceKindSpecs[:])
}

// LookupResourceKindSpec returns the spec of kind; kind must be one of the
// ResourceKind constants.
func LookupResourceKindSpec(kind ResourceKind) ResourceKindSpec {
	for _, spec := range resourceKindSpecs {
		if spec.Kind == kind {
			return spec
		}
	}
	panic("types: unknown resource kind " + string(kind))
}

// ParseResourceKind returns the kind named by text ("cpu" or "memory"), and
// false for any other text.
func ParseResourceKind(text string) (ResourceKind, bool) {
	for _, spec := range resourceKindSpecs {
		if string(spec.Kind) == text {
			return spec.Kind, true
		}
	}
	return "", false
}

// ServiceResourceKey identifies one resource of one service; it is the map key
// of per-resource values instead of a "{svc}/{kind}" string (hierarchical
// service names contain slashes).
type ServiceResourceKey struct {
	ServiceName string       // ServiceName is the service, e.g. "web" or "team/web"
	Kind        ResourceKind // Kind is the resource
}

// SortedServiceResourceKeys returns the keys of values sorted by service name,
// then kind, for deterministic iteration.
func SortedServiceResourceKeys[Value any](values map[ServiceResourceKey]Value) []ServiceResourceKey {
	sortedKeys := make([]ServiceResourceKey, 0, len(values))
	for resourceKey := range values {
		sortedKeys = append(sortedKeys, resourceKey)
	}
	slices.SortFunc(sortedKeys, func(first ServiceResourceKey, second ServiceResourceKey) int {
		return cmp.Or(cmp.Compare(first.ServiceName, second.ServiceName), cmp.Compare(first.Kind, second.Kind))
	})
	return sortedKeys
}

// SplitServiceResourceKey returns the service and kind of a
// {prefix}{svc}/resources/{kind} key, matching the key suffix so hierarchical
// names (team/web) work (M243 B5); false for any other key.
func SplitServiceResourceKey(factKey string, prefix string) (ServiceResourceKey, bool) {
	relativePath, hasPrefix := strings.CutPrefix(factKey, prefix)
	if !hasPrefix {
		return ServiceResourceKey{}, false
	}
	for _, spec := range resourceKindSpecs {
		if serviceName, matched := strings.CutSuffix(relativePath, spec.RequestKeySuffix); matched && serviceName != "" {
			return ServiceResourceKey{ServiceName: serviceName, Kind: spec.Kind}, true
		}
	}
	return ServiceResourceKey{}, false
}

// StoredResourceValue is one stored request value and the key it is under,
// so a reader can name both when the value cannot be parsed.
type StoredResourceValue struct {
	FactKey   string // FactKey is the store key, desired/ or effective/
	FactValue string // FactValue is the stored text, e.g. "500m" or "512Mi"
}

// CollectEffectiveFirstResourceValues returns each service's non-empty CPU and
// memory request: the effective value when one is stored, otherwise the
// desired one. It never falls back from an unreadable effective value, because
// the agent applies it (M243 D4, D6). The agent reads one key at a time by the
// same rule (agent.lookupResourceWithFallback).
func CollectEffectiveFirstResourceValues(facts []store.Fact) map[ServiceResourceKey]StoredResourceValue {
	chosenValues := make(map[ServiceResourceKey]StoredResourceValue)
	for _, servicePrefix := range []string{ScanDesiredServices, ScanEffectiveServices} { // effective last: it wins
		for _, fact := range store.FactsWithPrefix(facts, servicePrefix) {
			if resourceKey, matched := SplitServiceResourceKey(fact.Key, servicePrefix); matched && len(fact.Value) > 0 {
				chosenValues[resourceKey] = StoredResourceValue{FactKey: fact.Key, FactValue: string(fact.Value)}
			}
		}
	}
	return chosenValues
}
