// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// jobStatusRunning indicates the job has active instances.
const jobStatusRunning = "running"

// jobStatusComplete indicates all completions succeeded.
const jobStatusComplete = "complete"

// jobStatusFailed indicates failures exceeded the backoff limit.
const jobStatusFailed = "failed"

// defaultJobCompletions is the default number of successful completions
// required for a job to finish.
const defaultJobCompletions = 1

// defaultJobParallelism is the default number of concurrently running
// instances for a job.
const defaultJobParallelism = 1

// defaultJobBackoffLimitController is the default maximum number of
// failures before the job is marked failed.
const defaultJobBackoffLimitController = 3

// jobSpec holds the desired configuration for a single job, extracted
// from store facts.
type jobSpec struct {
	name         string // unique job identifier
	image        string // container image reference
	command      string // container command to execute
	completions  int    // target successful completions
	parallelism  int    // max concurrent instances
	backoffLimit int    // max failures before marking failed
}

// jobState holds the current runtime state of a job, computed from
// observed and derived facts.
type jobState struct {
	status         string // current status: running, complete, or failed
	succeededCount int    // number of successfully exited instances
	failedCount    int    // number of failed instances
	activeCount    int    // number of currently active instances (running/pulling/starting/pending)
	totalCreated   int    // total instances ever created (succeeded + failed + active)
}

// JobController manages run-to-completion job workloads. It creates
// instances to meet the desired completion count, tracks successes and
// failures, and marks jobs as complete or failed.
type JobController struct {
	// NewID generates unique instance identifiers. Defaults to
	// types.NewInstanceID but can be overridden in tests.
	NewID types.IDFunc
}

// NewJobController returns a JobController wired to the default ID generator.
func NewJobController() *JobController {
	return &JobController{
		NewID: types.NewInstanceID,
	}
}

// Name returns "job", identifying this controller in logs.
func (jobController *JobController) Name() string { return "job" }

// Watch returns the fact prefixes this controller monitors: desired jobs,
// derived job status, and observed instances.
func (jobController *JobController) Watch() []string {
	return []string{
		types.ScanDesiredJobs,
		types.ScanDerivedJobs,
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
	}
}

// Reconcile examines job definitions and instance states, creating new
// instances or updating job status as needed.
func (jobController *JobController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredJobs := extractDesiredJobs(facts)
	if len(desiredJobs) == 0 {
		return nil, nil
	}

	instanceFields := parseInstanceFieldsFromFacts(facts)
	derivedJobFacts := extractDerivedJobFacts(facts)

	var changes []Change
	jobNames := sortedMapKeys(desiredJobs)
	for _, jobName := range jobNames {
		jobSpecification := desiredJobs[jobName]
		currentState := computeJobState(jobName, instanceFields, derivedJobFacts)
		jobChanges := jobController.reconcileSingleJob(jobSpecification, currentState)
		changes = append(changes, jobChanges...)
	}
	return changes, nil
}

// reconcileSingleJob produces changes for one job based on its spec and
// current state.
func (jobController *JobController) reconcileSingleJob(spec jobSpec, state jobState) []Change {
	if state.status == jobStatusComplete || state.status == jobStatusFailed {
		return nil
	}

	var changes []Change

	if state.succeededCount >= spec.completions {
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedJobStatus(spec.name),
			Value: []byte(jobStatusComplete),
		})
		return changes
	}

	if state.failedCount >= spec.backoffLimit {
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedJobStatus(spec.name),
			Value: []byte(jobStatusFailed),
		})
		return changes
	}

	instancesToCreate := computeInstancesToCreate(spec, state)
	for instanceIndex := 0; instanceIndex < instancesToCreate; instanceIndex++ {
		instanceID := fmt.Sprintf("%s-%d", spec.name, state.totalCreated+instanceIndex)
		instanceChanges := buildJobInstanceCreationChanges(spec, instanceID)
		changes = append(changes, instanceChanges...)
	}

	if state.status != jobStatusRunning && (state.activeCount > 0 || instancesToCreate > 0) {
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedJobStatus(spec.name),
			Value: []byte(jobStatusRunning),
		})
	}

	return changes
}

// computeInstancesToCreate determines how many new instances to launch
// based on parallelism, completions, and current active/succeeded counts.
func computeInstancesToCreate(spec jobSpec, state jobState) int {
	remainingCompletions := spec.completions - state.succeededCount - state.activeCount
	if remainingCompletions <= 0 {
		return 0
	}
	parallelismSlots := spec.parallelism - state.activeCount
	if parallelismSlots <= 0 {
		return 0
	}
	if remainingCompletions < parallelismSlots {
		return remainingCompletions
	}
	return parallelismSlots
}

// buildJobInstanceCreationChanges creates the store changes to launch a
// new job instance with the given ID.
func buildJobInstanceCreationChanges(spec jobSpec, instanceID string) []Change {
	changes := []Change{
		{Type: store.OpPut, Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
		{Type: store.OpPut, Key: types.KeyObservedInstanceService(instanceID), Value: []byte(spec.name)},
		{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstancePending))},
		{Type: store.OpPut, Key: types.KeyDerivedJobInstanceOwner(instanceID), Value: []byte(spec.name)},
	}
	return changes
}

// extractDesiredJobs scans facts for desired/job/{name}/... keys and
// groups them into jobSpec values keyed by job name.
func extractDesiredJobs(facts []store.Fact) map[string]jobSpec {
	jobs := make(map[string]jobSpec)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredJobs) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredJobs)
		pathParts := strings.SplitN(relativePath, "/", 2)
		jobName := pathParts[0]

		existing := jobs[jobName]
		existing.name = jobName

		if len(pathParts) == 2 {
			switch pathParts[1] {
			case "image":
				existing.image = string(fact.Value)
			case "command":
				existing.command = string(fact.Value)
			case "completions":
				existing.completions, _ = strconv.Atoi(string(fact.Value))
			case "parallelism":
				existing.parallelism, _ = strconv.Atoi(string(fact.Value))
			case "backoff_limit":
				existing.backoffLimit, _ = strconv.Atoi(string(fact.Value))
			}
		}
		jobs[jobName] = existing
	}

	for jobName, spec := range jobs {
		if spec.completions == 0 {
			spec.completions = defaultJobCompletions
		}
		if spec.parallelism == 0 {
			spec.parallelism = defaultJobParallelism
		}
		if spec.backoffLimit == 0 {
			spec.backoffLimit = defaultJobBackoffLimitController
		}
		jobs[jobName] = spec
	}
	return jobs
}

// extractDerivedJobFacts reads derived/job/{name}/{field} facts into a
// map[jobName]map[field]value structure.
func extractDerivedJobFacts(facts []store.Fact) map[string]map[string]string {
	derivedJobs := make(map[string]map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedJobs) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedJobs)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		jobName := pathParts[0]
		if derivedJobs[jobName] == nil {
			derivedJobs[jobName] = make(map[string]string)
		}
		derivedJobs[jobName][pathParts[1]] = string(fact.Value)
	}
	return derivedJobs
}

// computeJobState aggregates instance states and derived counters into
// a jobState for reconciliation decisions.
func computeJobState(jobName string, instanceFields map[string]map[string]string, derivedJobFacts map[string]map[string]string) jobState {
	state := jobState{}

	if derived, hasDerived := derivedJobFacts[jobName]; hasDerived {
		state.status = derived["status"]
		state.succeededCount, _ = strconv.Atoi(derived["succeeded"])
		state.failedCount, _ = strconv.Atoi(derived["failed"])
	}

	for _, fields := range instanceFields {
		if fields["job_owner"] != jobName {
			continue
		}
		instanceState := effectiveInstanceState(fields)
		switch instanceState {
		case types.InstanceRunning, types.InstancePending, types.InstancePulling, types.InstanceStarting,
			types.InstanceImagePullBackoff:
			state.activeCount++
		case types.InstanceFailed:
			state.failedCount++
		case types.InstanceStopped:
			state.succeededCount++
		}
	}

	state.totalCreated = state.succeededCount + state.failedCount + state.activeCount
	return state
}
