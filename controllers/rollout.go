package controllers

import (
	"context"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// RolloutController manages rolling updates when a service's desired image
// changes. It gradually stops old-image instances at a rate controlled by
// max_unavailable, allowing the InstanceController to create replacements
// with the new image. If new-image instances fail beyond a threshold, it
// triggers a rollback by reverting the desired image.
type RolloutController struct{}

// NewRolloutController returns a new RolloutController.
func NewRolloutController() *RolloutController {
	return &RolloutController{}
}

// Name returns "rollout", identifying this controller in logs.
func (rolloutController *RolloutController) Name() string { return "rollout" }

// Watch returns the fact prefixes that drive rolling updates.
func (rolloutController *RolloutController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanObservedInstances,
		types.ScanDerivedServices,
		types.ScanPlacements,
	}
}

// Reconcile detects image mismatches between the desired service image and
// running instances, then manages the rolling update by stopping old-image
// instances at a controlled rate. If too many new-image instances fail,
// it reverts the desired image for rollback.
func (rolloutController *RolloutController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredImages := extractDesiredImages(facts)
	updatePolicies := extractUpdatePolicies(facts)
	instancesByService := extractInstancesByService(facts)
	rolloutState := extractRolloutState(facts)

	var changes []Change

	for serviceName, desiredImage := range desiredImages {
		instances := instancesByService[serviceName]
		if len(instances) == 0 {
			continue
		}

		oldImageInstances, newImageInstances, newImageFailedCount := classifyInstancesByImage(instances, desiredImage)

		if len(oldImageInstances) == 0 {
			if rolloutState[serviceName] == "rolling" {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedServiceRolloutState(serviceName),
					Value: []byte("complete"),
				})
			}
			continue
		}

		previousImage := rolloutState[serviceName+"/previous_image"]
		trackingChanges := buildRolloutTrackingChanges(serviceName, oldImageInstances, previousImage)
		changes = append(changes, trackingChanges...)

		if shouldTriggerRollback(newImageFailedCount, previousImage) {
			rollbackChanges := buildRollbackChanges(serviceName, previousImage)
			changes = append(changes, rollbackChanges...)
			continue
		}

		stopChanges := buildOldInstanceStopChanges(
			oldImageInstances, newImageInstances, updatePolicies[serviceName],
		)
		changes = append(changes, stopChanges...)
	}

	return changes, nil
}

// classifyInstancesByImage partitions a service's instances into those running
// the old image and those running the new (desired) image, skipping stopped
// instances. It also returns the count of new-image instances in the failed
// state, which is used for rollback threshold checks.
func classifyInstancesByImage(
	instances []rolloutInstanceInfo,
	desiredImage string,
) ([]rolloutInstanceInfo, []rolloutInstanceInfo, int) {
	var oldImageInstances, newImageInstances []rolloutInstanceInfo
	var newImageFailedCount int
	for _, instanceInfo := range instances {
		if instanceInfo.state == types.InstanceStopped {
			continue
		}
		if instanceInfo.image == "" || instanceInfo.image == desiredImage {
			newImageInstances = append(newImageInstances, instanceInfo)
			if instanceInfo.state == types.InstanceFailed {
				newImageFailedCount++
			}
		} else {
			oldImageInstances = append(oldImageInstances, instanceInfo)
		}
	}
	return oldImageInstances, newImageInstances, newImageFailedCount
}

// buildRolloutTrackingChanges returns changes that record the previous image
// and set the rollout state to "rolling" when a rollout is first detected
// (i.e. when no previous image has been recorded yet).
func buildRolloutTrackingChanges(serviceName string, oldImageInstances []rolloutInstanceInfo, previousImage string) []Change {
	if previousImage != "" {
		return nil
	}
	return []Change{
		{
			Type:  store.OpPut,
			Key:   types.KeyDerivedServiceRolloutImage(serviceName),
			Value: []byte(oldImageInstances[0].image),
		},
		{
			Type:  store.OpPut,
			Key:   types.KeyDerivedServiceRolloutState(serviceName),
			Value: []byte("rolling"),
		},
	}
}

// shouldTriggerRollback returns true when the number of failed new-image
// instances meets or exceeds the rollback threshold (3) and a previous image
// is available to revert to.
func shouldTriggerRollback(newImageFailedCount int, previousImage string) bool {
	const rollbackThreshold = 3
	return newImageFailedCount >= rollbackThreshold && previousImage != ""
}

// buildRollbackChanges returns changes that revert the desired service image
// to the previous image and set the rollout state to "rollback".
func buildRollbackChanges(serviceName string, previousImage string) []Change {
	return []Change{
		{
			Type:  store.OpPut,
			Key:   types.KeyDesiredServiceImage(serviceName),
			Value: []byte(previousImage),
		},
		{
			Type:  store.OpPut,
			Key:   types.KeyDerivedServiceRolloutState(serviceName),
			Value: []byte("rollback"),
		},
	}
}

// buildOldInstanceStopChanges determines how many old-image instances can be
// stopped in this reconciliation cycle based on the update policy's
// max_unavailable setting, the number of already-failed old instances, and
// whether any healthy new-image instances exist. It returns stop changes for
// up to canStop old instances.
func buildOldInstanceStopChanges(
	oldImageInstances []rolloutInstanceInfo,
	newImageInstances []rolloutInstanceInfo,
	policy extractedUpdatePolicy,
) []Change {
	maxUnavailable := policy.maxUnavailable
	if maxUnavailable == 0 {
		maxUnavailable = 1
	}

	var healthyNewCount int
	for _, instanceInfo := range newImageInstances {
		if instanceInfo.state == types.InstanceRunning {
			healthyNewCount++
		}
	}

	currentUnavailable := 0
	for _, instanceInfo := range oldImageInstances {
		if instanceInfo.state == types.InstanceFailed {
			currentUnavailable++
		}
	}

	canStop := maxUnavailable - currentUnavailable
	if canStop <= 0 {
		return nil
	}

	if healthyNewCount == 0 && len(newImageInstances) > 0 {
		return nil
	}

	var changes []Change
	stopped := 0
	for _, instanceInfo := range oldImageInstances {
		if stopped >= canStop {
			break
		}
		if instanceInfo.state == types.InstanceRunning || instanceInfo.state == types.InstancePending || instanceInfo.state == types.InstanceStarting {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstanceState(instanceInfo.id),
				Value: []byte(string(types.InstanceStopped)),
			})
			stopped++
		}
	}
	return changes
}

// rolloutInstanceInfo holds instance details needed for rolling update decisions.
type rolloutInstanceInfo struct {
	id      string
	service string
	state   types.InstanceState
	image   string
}

// extractDesiredImages returns a map of service name to desired container image.
func extractDesiredImages(facts []store.Fact) map[string]string {
	images := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "image" {
			images[parts[0]] = string(fact.Value)
		}
	}
	return images
}

// extractedUpdatePolicy holds parsed update strategy from facts.
type extractedUpdatePolicy struct {
	maxUnavailable int
	maxExtra       int
}

// extractUpdatePolicies returns update policies for each service.
func extractUpdatePolicies(facts []store.Fact) map[string]extractedUpdatePolicy {
	policies := make(map[string]extractedUpdatePolicy)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		serviceName := parts[0]
		suffix := parts[1]
		policy := policies[serviceName]
		switch suffix {
		case "update/max_unavailable":
			policy.maxUnavailable, _ = strconv.Atoi(string(fact.Value))
		case "update/max_extra":
			policy.maxExtra, _ = strconv.Atoi(string(fact.Value))
		default:
			continue
		}
		policies[serviceName] = policy
	}
	return policies
}

// extractInstancesByService returns all instances grouped by service name.
func extractInstancesByService(facts []store.Fact) map[string][]rolloutInstanceInfo {
	instanceMap := make(map[string]*rolloutInstanceInfo)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		instanceID := parts[0]
		if instanceMap[instanceID] == nil {
			instanceMap[instanceID] = &rolloutInstanceInfo{id: instanceID}
		}
		switch parts[1] {
		case "service":
			instanceMap[instanceID].service = string(fact.Value)
		case "state":
			instanceMap[instanceID].state = types.InstanceState(fact.Value)
		case "image":
			instanceMap[instanceID].image = string(fact.Value)
		}
	}

	result := make(map[string][]rolloutInstanceInfo)
	for _, instanceInfo := range instanceMap {
		if instanceInfo.service != "" {
			result[instanceInfo.service] = append(result[instanceInfo.service], *instanceInfo)
		}
	}
	return result
}

// extractRolloutState returns rollout tracking facts from the derived prefix.
func extractRolloutState(facts []store.Fact) map[string]string {
	state := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "rollout/") {
			continue
		}
		serviceName := parts[0]
		rolloutField := strings.TrimPrefix(parts[1], "rollout/")
		switch rolloutField {
		case "state":
			state[serviceName] = string(fact.Value)
		case "previous_image":
			state[serviceName+"/previous_image"] = string(fact.Value)
		}
	}
	return state
}
