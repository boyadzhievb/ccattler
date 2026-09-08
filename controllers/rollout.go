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
		types.ScanObservedServices,
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

		var oldImageInstances, newImageInstances []rolloutInstanceInfo
		var newImageFailed int
		for _, instanceInfo := range instances {
			if instanceInfo.state == types.InstanceStopped {
				continue
			}
			if instanceInfo.image == "" || instanceInfo.image == desiredImage {
				newImageInstances = append(newImageInstances, instanceInfo)
				if instanceInfo.state == types.InstanceFailed {
					newImageFailed++
				}
			} else {
				oldImageInstances = append(oldImageInstances, instanceInfo)
			}
		}

		if len(oldImageInstances) == 0 {
			if rolloutState[serviceName] == "rolling" {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyObservedServiceRolloutState(serviceName),
					Value: []byte("complete"),
				})
			}
			continue
		}

		previousImage := rolloutState[serviceName+"/previous_image"]
		if previousImage == "" {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedServiceRolloutImage(serviceName),
				Value: []byte(oldImageInstances[0].image),
			})
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedServiceRolloutState(serviceName),
				Value: []byte("rolling"),
			})
		}

		rollbackThreshold := 3
		if newImageFailed >= rollbackThreshold && previousImage != "" {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyDesiredServiceImage(serviceName),
				Value: []byte(previousImage),
			})
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedServiceRolloutState(serviceName),
				Value: []byte("rollback"),
			})
			continue
		}

		policy := updatePolicies[serviceName]
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
			continue
		}

		if healthyNewCount == 0 && len(newImageInstances) > 0 {
			continue
		}

		stopped := 0
		for _, instanceInfo := range oldImageInstances {
			if stopped >= canStop {
				break
			}
			if instanceInfo.state == types.InstanceRunning || instanceInfo.state == types.InstancePending {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyObservedInstanceState(instanceInfo.id),
					Value: []byte(string(types.InstanceStopped)),
				})
				stopped++
			}
		}
	}

	return changes, nil
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
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
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
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
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

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedInstances) {
			continue
		}
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

// extractRolloutState returns rollout tracking facts.
func extractRolloutState(facts []store.Fact) map[string]string {
	state := make(map[string]string)
	prefix := types.PrefixObserved + "/service/"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, prefix)
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
