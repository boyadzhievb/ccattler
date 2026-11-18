// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// rolloutInstanceInfo holds instance details needed for rolling update decisions.
type rolloutInstanceInfo struct {
	id           string              // id is the instance ID.
	service      string              // service is the owning service.
	state        types.InstanceState // state is the effective state (controller_stopped and node_failure applied).
	image        string              // image is the recorded image, else the observed one; "" when neither exists yet.
	restarts     int                 // restarts is observed/instance/{id}/restarts, 0 when absent.
	nodeFailure  bool                // nodeFailure is true when the instance is on a lost node.
	statusReason string              // statusReason is observed/instance/{id}/status-reason.
	exitReason   string              // exitReason is observed/instance/{id}/exit-reason.
	startedAt    time.Time           // startedAt is observed/instance/{id}/started_at; zero when absent or unreadable.
	createdAt    time.Time           // createdAt is observed/instance/{id}/created-at; zero when absent or unreadable (D13).
	lastExit     string              // lastExit is observed/instance/{id}/last_exit, kept across restarts.
	stopWritten  bool                // stopWritten is true when observed/instance/{id}/state already says stopped (B2).
	readiness    string              // readiness is observed/instance/{id}/probe/readiness; "" when absent.
	healthy      bool                // healthy is set by classification: running long enough, and ready when probed (D10).
	healthWait   string              // healthWait says what a running, not yet healthy instance waits for ("8s", "readiness not-ready").
}

// rolloutField names one rollout bookkeeping fact under
// derived/service/{svc}/rollout/ (M241 D5, D7, D9).
type rolloutField string

// Rollout bookkeeping fields.
const (
	rolloutFieldState           rolloutField = "state"             // rolling, complete, rollback, rolled-back, stalled
	rolloutFieldPreviousImage   rolloutField = "previous_image"    // the known-good image a rollback goes back to
	rolloutFieldTargetImage     rolloutField = "target_image"      // the image the rollout moves to
	rolloutFieldRevision        rolloutField = "revision"          // the rollout counter
	rolloutFieldFailures        rolloutField = "failures"          // new-image failures while rolling
	rolloutFieldSurgeAllowance  rolloutField = "surge_allowance"   // extra instances the instance controller may create
	rolloutFieldReason          rolloutField = "reason"            // what a rollout in progress waits for
	rolloutFieldRollbackReason  rolloutField = "rollback_reason"   // why the last rollback happened
	rolloutFieldProgressAt      rolloutField = "progress_at"       // RFC 3339 time of the last progress
	rolloutFieldRunningNew      rolloutField = "running_new"       // healthy target-image instances last seen (D10)
	rolloutFieldRolledBackImage rolloutField = "rolled_back_image" // the image the last rollback moved away from
	rolloutFieldRolledBackAt    rolloutField = "rolled_back_at"    // RFC 3339 time of the last rollback
	rolloutFieldStartedAt       rolloutField = "started_at"        // RFC 3339 time the rollout or rollback started (D13)
)

// rolloutFieldKeys maps every rollout field to its store key function.
var rolloutFieldKeys = map[rolloutField]func(string) string{
	rolloutFieldState:           types.KeyDerivedServiceRolloutState,
	rolloutFieldPreviousImage:   types.KeyDerivedServiceRolloutImage,
	rolloutFieldTargetImage:     types.KeyDerivedServiceRolloutTargetImage,
	rolloutFieldRevision:        types.KeyDerivedServiceRolloutRevision,
	rolloutFieldFailures:        types.KeyDerivedServiceRolloutFailures,
	rolloutFieldSurgeAllowance:  types.KeyDerivedServiceRolloutSurgeAllowance,
	rolloutFieldReason:          types.KeyDerivedServiceRolloutReason,
	rolloutFieldRollbackReason:  types.KeyDerivedServiceRolloutRollbackReason,
	rolloutFieldProgressAt:      types.KeyDerivedServiceRolloutProgressAt,
	rolloutFieldRunningNew:      types.KeyDerivedServiceRolloutRunningNew,
	rolloutFieldRolledBackImage: types.KeyDerivedServiceRolloutRolledBackImage,
	rolloutFieldRolledBackAt:    types.KeyDerivedServiceRolloutRolledBackAt,
	rolloutFieldStartedAt:       types.KeyDerivedServiceRolloutStartedAt,
}

// rolloutFactWriter proposes the rollout changes of one service. It writes a
// field only when its value changes and deletes only fields that exist, so a
// settled rollout proposes nothing; all its changes share one group, and a
// later change of a key replaces the earlier one (one change per key).
type rolloutFactWriter struct {
	serviceName      string            // serviceName is the service the facts belong to.
	storedFields     map[string]string // storedFields holds the rollout fields as stored, updated by this pass.
	changes          []Change          // changes are the proposed changes so far.
	changeIndexByKey map[string]int    // changeIndexByKey maps a proposed key to its index in changes.
}

// value returns a rollout field as stored or as written earlier in this pass.
func (writer *rolloutFactWriter) value(field rolloutField) string {
	return writer.storedFields[string(field)]
}

// put proposes writing a rollout field when its value differs.
func (writer *rolloutFactWriter) put(field rolloutField, fieldValue string) {
	if storedValue, isStored := writer.storedFields[string(field)]; isStored && storedValue == fieldValue {
		return
	}
	writer.storedFields[string(field)] = fieldValue
	writer.putKey(rolloutFieldKeys[field](writer.serviceName), fieldValue)
}

// remove proposes deleting a rollout field when it exists.
func (writer *rolloutFactWriter) remove(field rolloutField) {
	if _, isStored := writer.storedFields[string(field)]; !isStored {
		return
	}
	delete(writer.storedFields, string(field))
	writer.propose(Change{Type: store.OpDelete, Key: rolloutFieldKeys[field](writer.serviceName)})
}

// putKey proposes writing any key in the service's rollout group (history
// entries, the reverted desired image).
func (writer *rolloutFactWriter) putKey(factKey string, factValue string) {
	writer.propose(Change{Type: store.OpPut, Key: factKey, Value: []byte(factValue)})
}

// propose adds a change to the service's rollout group, replacing an earlier
// change of the same key so a transaction never holds a key twice.
func (writer *rolloutFactWriter) propose(change Change) {
	change.Group = "rollout/" + writer.serviceName
	if writer.changeIndexByKey == nil {
		writer.changeIndexByKey = make(map[string]int)
	}
	if existingIndex, alreadyProposed := writer.changeIndexByKey[change.Key]; alreadyProposed {
		writer.changes[existingIndex] = change
		return
	}
	writer.changeIndexByKey[change.Key] = len(writer.changes)
	writer.changes = append(writer.changes, change)
}

// rolloutServiceInput is everything one pass of one service's rollout needs.
type rolloutServiceInput struct {
	serviceName           string                          // serviceName is the service.
	desiredImage          string                          // desiredImage is desired/service/{svc}/image.
	instances             []rolloutInstanceInfo           // instances are the service's instances, sorted by ID.
	writer                *rolloutFactWriter              // writer holds the stored rollout fields and collects changes.
	maxUnavailable        int                             // maxUnavailable is the effective budget (update policy or default, disruption budget; D12).
	maxExtra              int                             // maxExtra is the surge allowance of a new rollout (update policy or default; D12).
	desiredInstanceCount  int                             // desiredInstanceCount drives the failure threshold.
	expectedInstanceCount int                             // expectedInstanceCount is how many must run for completion.
	paused                bool                            // paused is desired/service/{svc}/rollout/paused.
	progressDeadline      time.Duration                   // progressDeadline is the time without progress before rollback.
	healthRule            rolloutHealthRule               // healthRule decides when a running instance counts as healthy (D10).
	unplacedInstances     map[string]*unplacedDemandEntry // unplacedInstances holds the scheduler's unplaced reasons by instance ID.
	now                   time.Time                       // now is the controller clock for this pass.
}

// rolloutClusterFacts holds the facts of all services the rollout controller reads.
type rolloutClusterFacts struct {
	desiredImages      map[string]string                  // desiredImages maps service to desired image.
	updatePolicies     map[string]extractedUpdatePolicy   // updatePolicies maps service to update policy.
	instancesByService map[string][]rolloutInstanceInfo   // instancesByService maps service to its sorted instances.
	rolloutFields      map[string]map[string]string       // rolloutFields maps service to rollout field to value.
	disruptionBudgets  map[string]rolloutDisruptionBudget // disruptionBudgets maps service to disruption budget.
	desiredCounts      map[string]int                     // desiredCounts maps service to desired instance count.
	effectiveCounts    map[string]int                     // effectiveCounts maps service to effective instance count.
	pausedServices     map[string]bool                    // pausedServices holds services with a paused rollout.
	readinessProbed    map[string]bool                    // readinessProbed holds services that declare a readiness probe.
	unplacedInstances  map[string]*unplacedDemandEntry    // unplacedInstances maps instance ID to the scheduler's unplaced reason.
}

// extractRolloutClusterFacts parses every fact the rollout controller reads.
func extractRolloutClusterFacts(facts []store.Fact) rolloutClusterFacts {
	return rolloutClusterFacts{
		desiredImages:      extractDesiredImages(facts),
		updatePolicies:     extractUpdatePolicies(facts),
		instancesByService: extractInstancesByService(facts),
		rolloutFields:      extractRolloutFields(facts),
		disruptionBudgets:  extractRolloutDisruptionBudgets(facts),
		desiredCounts:      extractDesiredInstanceCounts(facts),
		effectiveCounts:    collectIntValuesBySuffix(facts, types.ScanEffectiveServices, "instances"),
		pausedServices:     extractRolloutPausedServices(facts),
		readinessProbed:    extractReadinessProbedServices(facts),
		unplacedInstances:  extractUnplacedDemand(facts),
	}
}

// buildServiceInput assembles one service's rollout input. The expected count
// (completion, budget, defaults) is the effective instance count, else the
// desired one; the pace is resolved from it (D12).
func (clusterFacts rolloutClusterFacts) buildServiceInput(serviceName string, now time.Time) rolloutServiceInput {
	updatePolicy, hasPolicy := clusterFacts.updatePolicies[serviceName]
	if !hasPolicy {
		updatePolicy = newDefaultUpdatePolicy()
	}
	expectedCount, hasEffectiveCount := clusterFacts.effectiveCounts[serviceName]
	if !hasEffectiveCount {
		expectedCount = clusterFacts.desiredCounts[serviceName]
	}
	disruptionBudget := clusterFacts.disruptionBudgets[serviceName]
	pacing := types.ResolveRolloutPacing(expectedCount, updatePolicy.explicitMaxUnavailable, updatePolicy.explicitMaxExtra).
		CappedByDisruptionBudget(disruptionBudget.minAvailable, disruptionBudget.maxUnavailable, clusterFacts.desiredCounts[serviceName])
	healthRule := rolloutHealthRule{
		minHealthyTime:   updatePolicy.minHealthyTime,
		requireReadiness: clusterFacts.readinessProbed[serviceName],
		now:              now,
	}
	storedFields := clusterFacts.rolloutFields[serviceName]
	if storedFields == nil {
		storedFields = make(map[string]string)
	}
	return rolloutServiceInput{
		serviceName:           serviceName,
		desiredImage:          clusterFacts.desiredImages[serviceName],
		instances:             clusterFacts.instancesByService[serviceName],
		writer:                &rolloutFactWriter{serviceName: serviceName, storedFields: storedFields},
		maxUnavailable:        pacing.MaxUnavailable,
		maxExtra:              pacing.MaxExtra,
		desiredInstanceCount:  clusterFacts.desiredCounts[serviceName],
		expectedInstanceCount: expectedCount,
		paused:                clusterFacts.pausedServices[serviceName],
		progressDeadline:      updatePolicy.progressDeadline,
		healthRule:            healthRule,
		unplacedInstances:     clusterFacts.unplacedInstances,
		now:                   now,
	}
}

// extractDesiredImages returns a map of service name to desired container image.
func extractDesiredImages(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanDesiredServices, "image")
}

// extractedUpdatePolicy holds parsed update strategy from facts.
type extractedUpdatePolicy struct {
	explicitMaxUnavailable *int          // explicitMaxUnavailable is update { max_unavailable }, nil when unset (default 25 %, D12).
	explicitMaxExtra       *int          // explicitMaxExtra is update { max_extra }, nil when unset (default 25 %, D12).
	progressDeadline       time.Duration // progressDeadline is update { progress_deadline }, the default when unset.
	minHealthyTime         time.Duration // minHealthyTime is update { min_healthy_time }, the default when unset; 0 is valid.
}

// newDefaultUpdatePolicy returns the update policy of a service that sets no
// update fields: default progress deadline and min healthy time.
func newDefaultUpdatePolicy() extractedUpdatePolicy {
	return extractedUpdatePolicy{progressDeadline: rolloutDefaultProgressDeadline, minHealthyTime: rolloutDefaultMinHealthyTime}
}

// extractUpdatePolicies returns update policies for each service. A corrupt or
// non-positive progress deadline, or a corrupt or negative min healthy time, is
// logged and the default is used.
func extractUpdatePolicies(facts []store.Fact) map[string]extractedUpdatePolicy {
	policies := make(map[string]extractedUpdatePolicy)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}
		policy, hasPolicy := policies[serviceName]
		if !hasPolicy {
			policy = newDefaultUpdatePolicy()
		}
		switch suffix {
		case "update/max_unavailable":
			policy.explicitMaxUnavailable = parseUpdatePolicyInteger(serviceName, suffix, fact.Value, policy.explicitMaxUnavailable)
		case "update/max_extra":
			policy.explicitMaxExtra = parseUpdatePolicyInteger(serviceName, suffix, fact.Value, policy.explicitMaxExtra)
		case "update/progress_deadline":
			parsedDeadline, parseError := time.ParseDuration(string(fact.Value))
			if parseError != nil || parsedDeadline <= 0 {
				logging.Default().Warn("corrupt rollout progress_deadline, using the default", "service", serviceName, "value", string(fact.Value))
				continue
			}
			policy.progressDeadline = parsedDeadline
		case "update/min_healthy_time":
			parsedMinHealthyTime, parseError := time.ParseDuration(string(fact.Value))
			if parseError != nil || parsedMinHealthyTime < 0 {
				logging.Default().Warn("corrupt rollout min_healthy_time, using the default", "service", serviceName, "value", string(fact.Value))
				continue
			}
			policy.minHealthyTime = parsedMinHealthyTime
		}
		policies[serviceName] = policy
	}
	return policies
}

// parseUpdatePolicyInteger parses an integer update policy fact, logging a
// corrupt value and keeping currentValue (nil: the default applies).
func parseUpdatePolicyInteger(serviceName string, suffix string, factValue []byte, currentValue *int) *int {
	parsedValue, parseError := strconv.Atoi(string(factValue))
	if parseError != nil {
		logging.Default().Warn("corrupt rollout "+strings.TrimPrefix(suffix, "update/"), "service", serviceName, "value", string(factValue))
		return currentValue
	}
	return &parsedValue
}

// extractInstancesByService returns all instances grouped by service name and
// sorted by ID, with effective state from observed and derived facts. The
// recorded image (derived/instance/{id}/image) shadows the observed image in
// the merged fields: it is the image the agent starts the instance with.
func extractInstancesByService(facts []store.Fact) map[string][]rolloutInstanceInfo {
	instanceFields := parseInstanceFieldsFromFacts(facts)
	result := make(map[string][]rolloutInstanceInfo)
	for _, instanceID := range sortedMapKeys(instanceFields) {
		fields := instanceFields[instanceID]
		serviceName := fields["service"]
		if serviceName == "" {
			continue
		}
		restartCount, _ := strconv.Atoi(fields["restarts"])
		result[serviceName] = append(result[serviceName], rolloutInstanceInfo{
			id:           instanceID,
			service:      serviceName,
			state:        effectiveInstanceState(fields),
			image:        fields["image"],
			restarts:     restartCount,
			nodeFailure:  fields["node_failure"] == "true",
			statusReason: fields["status-reason"],
			exitReason:   fields["exit-reason"],
			startedAt:    parseInstanceTimestamp(instanceID, "started_at", fields["started_at"]),
			createdAt:    parseInstanceTimestamp(instanceID, "created-at", fields["created-at"]),
			lastExit:     fields["last_exit"],
			stopWritten:  types.InstanceState(fields["state"]) == types.InstanceStopped,
			readiness:    fields["probe/"+string(types.ProbeReadiness)],
		})
	}
	return result
}

// parseInstanceTimestamp parses an instance's started_at or created-at (RFC
// 3339, fractions allowed). A missing value is the zero time; so is a corrupt
// one, which is logged: the instance is then treated as having none (D10, D13).
func parseInstanceTimestamp(instanceID string, fieldName string, timestampText string) time.Time {
	if timestampText == "" {
		return time.Time{}
	}
	timestamp, parseError := time.Parse(time.RFC3339, timestampText)
	if parseError != nil {
		logging.Default().Warn("corrupt instance "+fieldName+", ignoring it", "instance", instanceID, "value", timestampText)
		return time.Time{}
	}
	return timestamp
}

// extractReadinessProbedServices returns the services that declare a readiness
// probe (any desired/service/{svc}/probe/readiness/* fact, M241 D10).
func extractReadinessProbedServices(facts []store.Fact) map[string]bool {
	readinessProbePrefix := "probe/" + string(types.ProbeReadiness) + "/"
	probedServices := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if hasSuffix && strings.HasPrefix(suffix, readinessProbePrefix) {
			probedServices[serviceName] = true
		}
	}
	return probedServices
}

// rolloutDisruptionBudget holds parsed disruption budget fields for a service
// used during rollout decisions.
type rolloutDisruptionBudget struct {
	minAvailable   int // minimum instances that must remain running
	maxUnavailable int // maximum instances that may be simultaneously unavailable
}

// extractRolloutDisruptionBudgets scans desired service facts and returns
// disruption budgets keyed by service name.
func extractRolloutDisruptionBudgets(facts []store.Fact) map[string]rolloutDisruptionBudget {
	budgets := make(map[string]rolloutDisruptionBudget)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(factEntry.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}
		budget := budgets[serviceName]
		switch suffix {
		case "disruption/min_available":
			parsedValue, parseError := strconv.Atoi(string(factEntry.Value))
			if parseError != nil {
				logging.Default().Warn("corrupt disruption min_available", "service", serviceName, "value", string(factEntry.Value))
				continue
			}
			budget.minAvailable = parsedValue
		case "disruption/max_unavailable":
			parsedValue, parseError := strconv.Atoi(string(factEntry.Value))
			if parseError != nil {
				logging.Default().Warn("corrupt disruption max_unavailable", "service", serviceName, "value", string(factEntry.Value))
				continue
			}
			budget.maxUnavailable = parsedValue
		default:
			continue
		}
		budgets[serviceName] = budget
	}
	return budgets
}

// extractDesiredInstanceCounts scans desired service facts and returns the
// desired instance count per service.
func extractDesiredInstanceCounts(facts []store.Fact) map[string]int {
	return collectIntValuesBySuffix(facts, types.ScanDesiredServices, "instances")
}

// extractRolloutFields returns the rollout bookkeeping facts per service
// (field name under rollout/ → value), history entries excluded.
func extractRolloutFields(facts []store.Fact) map[string]map[string]string {
	fieldsByService := make(map[string]map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDerivedServices)
		if !hasSuffix || !strings.HasPrefix(suffix, "rollout/") {
			continue
		}
		rolloutFieldName := strings.TrimPrefix(suffix, "rollout/")
		if _, isKnownField := rolloutFieldKeys[rolloutField(rolloutFieldName)]; !isKnownField {
			continue
		}
		if fieldsByService[serviceName] == nil {
			fieldsByService[serviceName] = make(map[string]string)
		}
		fieldsByService[serviceName][rolloutFieldName] = string(fact.Value)
	}
	return fieldsByService
}

// extractRolloutPausedServices returns a set of service names with paused rollouts.
func extractRolloutPausedServices(facts []store.Fact) map[string]bool {
	paused := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix || suffix != "rollout/paused" {
			continue
		}
		if string(fact.Value) == "true" {
			paused[serviceName] = true
		}
	}
	return paused
}
