// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// command_status.go contains CLI commands for querying cluster status, events,
// logs, metrics, resource utilization, describe, get, watch, and scale.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// instanceIDDisplayWidth is the maximum number of characters shown for
	// instance IDs in CLI tables (instances and volumes).
	instanceIDDisplayWidth = 12

	// configValueDisplayWidth is the maximum number of characters shown for
	// config values in the "get config" CLI table.
	configValueDisplayWidth = 50

	// sseWatchReadBufferSize is the byte-buffer size used when streaming
	// SSE watch events from the API server.
	sseWatchReadBufferSize = 4096
)

// eventsCommandConfig holds parsed flags for the "events" command.
type eventsCommandConfig struct {
	// followMode enables real-time streaming of new events via SSE.
	followMode bool
	// serviceFilter limits output to events whose target contains this service name.
	serviceFilter string
}

// parseEventsCommandArgs extracts --follow and --service flags from the
// arguments following "events".
func parseEventsCommandArgs(args []string) eventsCommandConfig {
	parsedConfig := eventsCommandConfig{}
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--follow", "-f":
			parsedConfig.followMode = true
		case "--service", "-s":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.serviceFilter = args[argIndex]
			}
		}
	}
	return parsedConfig
}

// executeEventsCommand shows cluster events. In default mode it fetches recent
// events from the API. With --follow it connects to the SSE stream and prints
// new events in real time. The --service flag filters events by service name.
func executeEventsCommand(parsedConfig eventsCommandConfig) {
	if parsedConfig.followMode {
		executeEventsFollowMode(parsedConfig.serviceFilter)
		return
	}

	apiURL := statusAPIBaseURL() + "/api/logs"
	if parsedConfig.serviceFilter != "" {
		apiURL += "?target=" + parsedConfig.serviceFilter
	}
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)

	var events []struct {
		Timestamp time.Time `json:"timestamp"`
		Kind      string    `json:"kind"`
		Target    string    `json:"target"`
		Detail    string    `json:"detail"`
		Source    string    `json:"source"`
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(&events); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("no events")
		return
	}

	fmt.Printf("%-24s  %-22s  %-30s  %s\n", "TIMESTAMP", "KIND", "TARGET", "DETAIL")
	for _, event := range events {
		formattedTimestamp := event.Timestamp.Format("2006-01-02 15:04:05.000")
		displayTarget := stripEventTargetPrefix(event.Target)
		fmt.Printf("%-24s  %-22s  %-30s  %s\n", formattedTimestamp, event.Kind, displayTarget, event.Detail)
	}
}

// stripEventTargetPrefix removes the resource type prefix (instance/, node/,
// service/) from an event target for CLI display. The Kind column already
// conveys the resource type, so repeating it in the target is redundant.
func stripEventTargetPrefix(eventTarget string) string {
	if slashIndex := strings.Index(eventTarget, "/"); slashIndex >= 0 {
		return eventTarget[slashIndex+1:]
	}
	return eventTarget
}

// executeEventsFollowMode connects to the SSE event stream and prints new
// events as they arrive. Blocks until interrupted with Ctrl-C.
func executeEventsFollowMode(serviceFilter string) {
	apiURL := statusAPIBaseURL() + "/api/events/stream"
	if serviceFilter != "" {
		apiURL += "?service=" + serviceFilter
	}

	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.Header.Get("Content-Type") != "text/event-stream" {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "unexpected response: %s\n", string(responseBody))
		os.Exit(1)
	}

	fmt.Printf("%-24s  %-22s  %-30s  %s\n", "TIMESTAMP", "KIND", "TARGET", "DETAIL")

	scanner := bufio.NewScanner(httpResponse.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		eventJSON := strings.TrimPrefix(line, "data: ")
		var event struct {
			Timestamp time.Time `json:"timestamp"`
			Kind      string    `json:"kind"`
			Target    string    `json:"target"`
			Detail    string    `json:"detail"`
		}
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			continue
		}
		formattedTimestamp := event.Timestamp.Format("2006-01-02 15:04:05.000")
		displayTarget := stripEventTargetPrefix(event.Target)
		fmt.Printf("%-24s  %-22s  %-30s  %s\n", formattedTimestamp, event.Kind, displayTarget, event.Detail)
	}
	if scanError := scanner.Err(); scanError != nil {
		fmt.Fprintf(os.Stderr, "event stream error: %v\n", scanError)
		os.Exit(1)
	}
}

// logsCommandConfig holds parsed flags for the "logs" command.
type logsCommandConfig struct {
	serviceName string
	instanceID  string
	follow      bool
	tailLines   string
	since       string
	timestamps  bool
	previous    bool
	tagFilters  map[string]string
}

// parseContainerName checks if a string is a container name in the format
// <serviceName>-<instanceID> where instanceID is a 10-char hex string.
// Returns the service name, instance ID, and true if it matches.
func parseContainerName(name string) (string, string, bool) {
	lastDash := strings.LastIndex(name, "-")
	if lastDash < 1 || lastDash == len(name)-1 {
		return "", "", false
	}
	candidateID := name[lastDash+1:]
	if len(candidateID) != 10 {
		return "", "", false
	}
	for _, character := range candidateID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", "", false
		}
	}
	return name[:lastDash], candidateID, true
}

// parseLogsCommandArgs extracts flags from the arguments following "logs".
func parseLogsCommandArgs(args []string) logsCommandConfig {
	parsedConfig := logsCommandConfig{tagFilters: make(map[string]string)}
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--follow", "-f":
			parsedConfig.follow = true
		case "--instance":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.instanceID = args[argIndex]
			}
		case "--tail":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tailLines = args[argIndex]
			}
		case "--since":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.since = args[argIndex]
			}
		case "--timestamps":
			parsedConfig.timestamps = true
		case "--previous", "-p":
			parsedConfig.previous = true
		case "--tag":
			if argIndex+1 < len(args) {
				argIndex++
				tagExpression := args[argIndex]
				equalsIndex := strings.Index(tagExpression, "=")
				if equalsIndex > 0 {
					parsedConfig.tagFilters[tagExpression[:equalsIndex]] = tagExpression[equalsIndex+1:]
				}
			}
		default:
			if !strings.HasPrefix(currentArg, "-") && parsedConfig.serviceName == "" {
				if serviceName, instanceID, isContainer := parseContainerName(currentArg); isContainer {
					parsedConfig.serviceName = serviceName
					parsedConfig.instanceID = instanceID
				} else {
					parsedConfig.serviceName = currentArg
				}
			}
		}
	}
	return parsedConfig
}

// executeLogsCommand streams container stdout/stderr logs for a service or
// specific instance via the agent debug API. With --tag, resolves matching
// services and aggregates logs across all of them.
func executeLogsCommand(parsedConfig logsCommandConfig) {
	if parsedConfig.serviceName == "" && parsedConfig.instanceID == "" && len(parsedConfig.tagFilters) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cca logs <service|container-name> [-f] [--tail N] [--since TIME] [--timestamps] [--previous]")
		os.Exit(1)
	}

	if parsedConfig.instanceID != "" {
		streamContainerLogs(parsedConfig.serviceName, parsedConfig.instanceID, parsedConfig)
		return
	}

	serviceNames := resolveServiceNamesForLogs(parsedConfig)

	observedFacts := fetchObservedInstanceFacts()

	var allInstanceIDs []struct {
		serviceName string
		instanceID  string
	}
	for _, serviceName := range serviceNames {
		instanceIDs := findInstanceIDsForService(observedFacts, serviceName)
		for _, instanceID := range instanceIDs {
			allInstanceIDs = append(allInstanceIDs, struct {
				serviceName string
				instanceID  string
			}{serviceName, instanceID})
		}
	}

	if len(allInstanceIDs) == 0 {
		if len(parsedConfig.tagFilters) > 0 {
			fmt.Fprintln(os.Stderr, "no running instances found for matching tags")
		} else {
			fmt.Fprintf(os.Stderr, "no running instances found for service %q\n", parsedConfig.serviceName)
		}
		os.Exit(1)
	}

	if parsedConfig.follow {
		streamContainerLogs(allInstanceIDs[0].serviceName, allInstanceIDs[0].instanceID, parsedConfig)
		return
	}

	nonFollowConfig := parsedConfig
	nonFollowConfig.follow = false
	for _, entry := range allInstanceIDs {
		fmt.Printf("==> %s-%s <==\n", entry.serviceName, entry.instanceID)
		streamContainerLogs(entry.serviceName, entry.instanceID, nonFollowConfig)
		fmt.Println()
	}
}

// resolveServiceNamesForLogs determines which service names to stream logs for.
// With --tag, fetches cluster status and filters by tags. Otherwise uses the
// explicit service name from the command arguments.
func resolveServiceNamesForLogs(parsedConfig logsCommandConfig) []string {
	if len(parsedConfig.tagFilters) > 0 {
		matchedServices := fetchServicesMatchingTags(parsedConfig.tagFilters)
		if len(matchedServices) == 0 {
			fmt.Fprintln(os.Stderr, "no services match the specified tags")
			os.Exit(1)
		}
		return matchedServices
	}
	return []string{parsedConfig.serviceName}
}

// fetchObservedInstanceFacts retrieves all observed instance facts from the API.
func fetchObservedInstanceFacts() []struct {
	Key   string `json:"key"`
	Value string `json:"value"`
} {
	apiURL := statusAPIBaseURL() + "/api/state?prefix=" + "observed/instance/"
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run', 'server', or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)

	var facts []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&facts); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeError)
		os.Exit(1)
	}
	return facts
}

// fetchServicesMatchingTags queries the cluster status API and returns the names
// of services whose tags match all the provided filter criteria.
func fetchServicesMatchingTags(tagFilters map[string]string) []string {
	apiURL := statusAPIBaseURL() + "/api/status"
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run', 'server', or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)

	var clusterStatus api.ClusterStatus
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&clusterStatus); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeError)
		os.Exit(1)
	}

	var matchingServiceNames []string
	for _, service := range clusterStatus.Services {
		if serviceMatchesAllTags(service.Tags, tagFilters) {
			matchingServiceNames = append(matchingServiceNames, service.Name)
		}
	}
	sort.Strings(matchingServiceNames)
	return matchingServiceNames
}

// findInstanceIDsForService scans observed facts to find instance IDs belonging
// to the given service that are in a running state.
func findInstanceIDsForService(facts []struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}, serviceName string) []string {
	serviceMap := make(map[string]bool)
	stateMap := make(map[string]string)

	for _, fact := range facts {
		key := fact.Key
		if strings.HasSuffix(key, "/service") && fact.Value == serviceName {
			instanceID := strings.TrimPrefix(key, "observed/instance/")
			instanceID = strings.TrimSuffix(instanceID, "/service")
			serviceMap[instanceID] = true
		}
		if strings.HasSuffix(key, "/state") {
			instanceID := strings.TrimPrefix(key, "observed/instance/")
			instanceID = strings.TrimSuffix(instanceID, "/state")
			stateMap[instanceID] = fact.Value
		}
	}

	var instanceIDs []string
	for instanceID := range serviceMap {
		if stateMap[instanceID] == string(types.InstanceRunning) {
			instanceIDs = append(instanceIDs, instanceID)
		}
	}
	sort.Strings(instanceIDs)
	return instanceIDs
}

// findPreviousInstance queries observed facts for a terminated (stopped or
// failed) instance of the given service. Returns the instance ID if found,
// or an empty string if no previous instance exists.
func findPreviousInstance(serviceName string) string {
	observedFacts := fetchObservedInstanceFacts()

	serviceInstances := make(map[string]bool)
	stateMap := make(map[string]string)

	for _, factEntry := range observedFacts {
		if strings.HasSuffix(factEntry.Key, "/service") && factEntry.Value == serviceName {
			instanceID := strings.TrimPrefix(factEntry.Key, "observed/instance/")
			instanceID = strings.TrimSuffix(instanceID, "/service")
			serviceInstances[instanceID] = true
		}
		if strings.HasSuffix(factEntry.Key, "/state") {
			instanceID := strings.TrimPrefix(factEntry.Key, "observed/instance/")
			instanceID = strings.TrimSuffix(instanceID, "/state")
			stateMap[instanceID] = factEntry.Value
		}
	}

	for instanceID := range serviceInstances {
		instanceState := stateMap[instanceID]
		if instanceState == string(types.InstanceFailed) || instanceState == string(types.InstanceStopped) {
			return instanceID
		}
	}
	return ""
}

// lookupInstanceState queries the API for the current state of an instance.
func lookupInstanceState(instanceID string) string {
	stateURL := statusAPIBaseURL() + "/api/state?prefix=observed/instance/" + instanceID + "/state"
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(stateURL) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		return ""
	}
	defer func() { _ = httpResponse.Body.Close() }()
	var facts []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&facts); decodeError != nil || len(facts) == 0 {
		return ""
	}
	return facts[0].Value
}

// streamContainerLogs fetches logs for a container instance through the agent
// debug API. It discovers the instance's placement, connects to the node's
// debug endpoint, and streams the response to stdout.
func streamContainerLogs(serviceName string, instanceID string, logsConfig logsCommandConfig) {
	if logsConfig.previous {
		previousID := findPreviousInstance(serviceName)
		if previousID == "" {
			fmt.Fprintf(os.Stderr, "no previous (terminated) instance found for service %q\n", serviceName)
			return
		}
		instanceID = previousID
	}

	placementNodeID := discoverInstancePlacement(instanceID)
	debugAddress := discoverNodeDebugAddress(placementNodeID)

	endpointURL := "http://" + debugAddress + "/debug/logs"
	if strings.HasPrefix(debugAddress, "http://") || strings.HasPrefix(debugAddress, "https://") {
		endpointURL = debugAddress + "/debug/logs"
	}

	queryParams := url.Values{}
	queryParams.Set("instance", instanceID)
	if logsConfig.tailLines != "" {
		queryParams.Set("tail", logsConfig.tailLines)
	}
	if logsConfig.since != "" {
		queryParams.Set("since", logsConfig.since)
	}
	if logsConfig.timestamps {
		queryParams.Set("timestamps", "true")
	}
	if logsConfig.follow {
		queryParams.Set("follow", "true")
	}

	requestURL := endpointURL + "?" + queryParams.Encode()
	httpResponse, requestError := http.Get(requestURL) //nolint:gosec // CLI connects to agent debug API on discovered node
	if requestError != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to agent debug API at %s: %v\n", debugAddress, requestError)
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "logs error: %s\n", string(responseBody))
		if strings.Contains(string(responseBody), "not found") {
			instanceState := lookupInstanceState(instanceID)
			if instanceState != "" && instanceState != string(types.InstanceRunning) {
				fmt.Fprintf(os.Stderr, "hint: instance %s is in %q state — no workload container exists yet\n", instanceID, instanceState)
			}
		}
		return
	}

	_, _ = io.Copy(os.Stdout, httpResponse.Body)
}

// executeStatusCommand queries the status API of a running ccattler instance
// and prints the cluster status to stdout. Requires a running 'run' or 'demo' instance.
func executeStatusCommand() {
	statusRequest, _ := http.NewRequest("GET", statusAPIBaseURL()+"/status", nil)
	statusRequest.Header.Set("Accept", "text/plain")
	httpResponse, err := buildAuthenticatedHTTPClient().Do(statusRequest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "error: %s\n", responseBody)
		os.Exit(1)
	}
	fmt.Print(string(responseBody))
}

// executeMetricSetCommand sends a simulated metric value to a running ccattler instance
// via the status API. The metric is stored in the fact store at observed/metric/service/{service}/{metric}.
func executeMetricSetCommand(serviceName, metricName, metricValue string) {
	requestURL := fmt.Sprintf("%s/api/metric?service=%s&metric=%s&value=%s",
		statusAPIBaseURL(), serviceName, metricName, metricValue)
	httpResponse, err := buildAuthenticatedHTTPClient().Post(requestURL, "", nil) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, _ := io.ReadAll(httpResponse.Body)
	fmt.Print(string(responseBody))
}

// executeTopCommand queries the cluster API and displays resource utilization
// in a tabular format, similar to `kubectl top`.
func executeTopCommand(resourceType string, extraArgs []string) {
	apiBaseURL := statusAPIBaseURL() + "/api/status"
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiBaseURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)

	var clusterStatus api.ClusterStatus
	if err := json.NewDecoder(httpResponse.Body).Decode(&clusterStatus); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	switch resourceType {
	case "nodes", "node":
		fmt.Printf("%-14s  %-12s  %-16s  %-16s  %s\n", "NODE", "STATUS", "CPU", "MEMORY", "WORKLOADS")
		for _, nodeStatus := range clusterStatus.Nodes {
			cpuDisplay := formatResourceUsage(nodeStatus.CapacityCPU-nodeStatus.AvailableCPU, nodeStatus.CapacityCPU, "m")
			memoryDisplay := formatResourceUsage(nodeStatus.CapacityMemory-nodeStatus.AvailableMemory, nodeStatus.CapacityMemory, "Mi")
			fmt.Printf("%-14s  %-12s  %-16s  %-16s  %d\n",
				nodeStatus.ID, nodeStatus.State, cpuDisplay, memoryDisplay, nodeStatus.PlacedInstances)
		}
	case "workloads", "workload", "instances", "inst":
		tagFilters := parseTagFilterArgs(extraArgs)
		filteredInstances := filterInstancesByServiceTags(clusterStatus.Instances, clusterStatus.Services, tagFilters)
		fmt.Printf("%-24s  %-14s  %-10s  %-10s  %-10s  %s\n", "WORKLOAD", "NODE", "STATUS", "CPU", "MEMORY", "HEALTH")
		for _, instanceStatus := range filteredInstances {
			healthDisplay := instanceStatus.HealthState
			if healthDisplay == "" || healthDisplay == "-" {
				healthDisplay = "-"
			}
			nodeDisplay := instanceStatus.NodeID
			if nodeDisplay == "" {
				nodeDisplay = "-"
			}
			cpuDisplay := instanceStatus.CPUMillis
			if cpuDisplay == "" {
				cpuDisplay = "-"
			} else {
				cpuDisplay += "m"
			}
			memDisplay := instanceStatus.MemoryBytes
			if memDisplay == "" {
				memDisplay = "-"
			}
			workloadName := instanceStatus.ServiceName + "/" + instanceStatus.ID
			fmt.Printf("%-24s  %-14s  %-10s  %-10s  %-10s  %s\n",
				workloadName, nodeDisplay, instanceStatus.State, cpuDisplay, memDisplay, healthDisplay)
		}
	case "volumes", "volume", "vol":
		fmt.Printf("%-16s  %-10s  %-12s  %-14s  %-16s  %s\n", "VOLUME", "STATE", "SIZE", "NODE", "USAGE", "INSTANCE")
		for _, volumeStatus := range clusterStatus.Volumes {
			nodeDisplay := volumeStatus.Node
			if nodeDisplay == "" {
				nodeDisplay = "-"
			}
			instanceDisplay := volumeStatus.Instance
			if instanceDisplay == "" {
				instanceDisplay = "-"
			}
			usageDisplay := "-"
			if volumeStatus.CapacityBytes > 0 {
				usageDisplay = formatResourceUsage(volumeStatus.UsedBytes, volumeStatus.CapacityBytes, "B")
			}
			fmt.Printf("%-16s  %-10s  %-12s  %-14s  %-16s  %s\n",
				volumeStatus.Name, volumeStatus.State, volumeStatus.Size, nodeDisplay, usageDisplay, instanceDisplay)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s (use 'nodes', 'workloads', or 'volumes')\n", resourceType)
		os.Exit(1)
	}
}

// formatResourceUsage formats a used/capacity pair as "used/capacity unit" or
// "used/capacity unit (pct%)" for display in `cca top`.
func formatResourceUsage(used, capacity int64, unit string) string {
	if capacity <= 0 {
		return fmt.Sprintf("%d%s", used, unit)
	}
	percentage := (used * 100) / capacity
	return fmt.Sprintf("%d/%d%s (%d%%)", used, capacity, unit, percentage)
}

// executeDescribeCommand queries the describe API for a single resource and
// prints a detailed human-readable view of all related facts, health state,
// placement, networking, and recent events.
func executeDescribeCommand(resourceType, resourceName string) {
	var normalizedType string
	switch resourceType {
	case "service", "svc":
		normalizedType = "service"
	case "node":
		normalizedType = "node"
	case "instance", "inst":
		normalizedType = "instance"
	default:
		fmt.Fprintf(os.Stderr, "unknown resource type: %s (use service, node, or instance)\n", resourceType)
		os.Exit(1)
	}

	if normalizedType == "instance" {
		if _, instanceID, isContainer := parseContainerName(resourceName); isContainer {
			resourceName = instanceID
		}
	}

	apiURL := fmt.Sprintf("%s/api/describe?type=%s&name=%s",
		statusAPIBaseURL(), normalizedType, resourceName)
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode == http.StatusNotFound {
		var errorBody map[string]string
		_ = json.NewDecoder(httpResponse.Body).Decode(&errorBody)
		fmt.Fprintf(os.Stderr, "%s\n", errorBody["error"])
		os.Exit(1)
	}
	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", string(responseBody))
		os.Exit(1)
	}

	switch normalizedType {
	case "service":
		var serviceDetail api.ServiceDescribe
		if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&serviceDetail); decodeErr != nil {
			fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
			os.Exit(1)
		}
		printServiceDescribe(serviceDetail)
	case "node":
		var nodeDetail api.NodeDescribe
		if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&nodeDetail); decodeErr != nil {
			fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
			os.Exit(1)
		}
		printNodeDescribe(nodeDetail)
	case "instance":
		var instanceDetail api.InstanceDescribe
		if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&instanceDetail); decodeErr != nil {
			fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
			os.Exit(1)
		}
		printInstanceDescribe(instanceDetail)
	}
}

// printServiceDescribe renders a ServiceDescribe as human-readable text output,
// delegating to per-section printer functions for each detail category.
func printServiceDescribe(serviceDetail api.ServiceDescribe) {
	printServiceDescribeBasicInfo(serviceDetail)
	printServiceDescribePlacement(serviceDetail)
	printServiceDescribeHealthAndProbes(serviceDetail)
	printServiceDescribeInitSteps(serviceDetail)
	printServiceDescribeScalingAndRollout(serviceDetail)
	printServiceDescribeNetworkingAndConfig(serviceDetail)
	printServiceDescribeInstances(serviceDetail)
	printDescribeEvents(serviceDetail.Events)
}

// printServiceDescribeBasicInfo prints the name, image, instance counts, ports,
// and resource requirements for a service describe output.
func printServiceDescribeBasicInfo(serviceDetail api.ServiceDescribe) {
	fmt.Printf("Name:       %s\n", serviceDetail.Name)
	fmt.Printf("Image:      %s\n", serviceDetail.Image)
	if serviceDetail.Command != "" {
		fmt.Printf("Command:    %s\n", serviceDetail.Command)
	}
	fmt.Printf("Instances:  %d desired, %d running\n", serviceDetail.DesiredInstances, serviceDetail.RunningInstances)
	if len(serviceDetail.ExposedPorts) > 0 {
		portStrings := make([]string, len(serviceDetail.ExposedPorts))
		for portIndex, port := range serviceDetail.ExposedPorts {
			portStrings[portIndex] = fmt.Sprintf("%d", port)
		}
		fmt.Printf("Ports:      %s\n", strings.Join(portStrings, ", "))
	}

	if serviceDetail.Resources != nil {
		fmt.Println()
		fmt.Println("Resources:")
		if serviceDetail.Resources.CPU != "" {
			fmt.Printf("  CPU:      %s\n", serviceDetail.Resources.CPU)
		}
		if serviceDetail.Resources.Memory != "" {
			fmt.Printf("  Memory:   %s\n", serviceDetail.Resources.Memory)
		}
	}
}

// printServiceDescribePlacement prints the placement constraints section
// including architecture, zone policy, require, prefer, and accept labels.
func printServiceDescribePlacement(serviceDetail api.ServiceDescribe) {
	if serviceDetail.Placement == nil {
		return
	}
	fmt.Println()
	fmt.Println("Placement:")
	if serviceDetail.Placement.Architecture != "" {
		fmt.Printf("  Architecture:  %s\n", serviceDetail.Placement.Architecture)
	}
	if serviceDetail.Placement.ZonePolicy != "" {
		fmt.Printf("  Zone policy:   %s\n", serviceDetail.Placement.ZonePolicy)
	}
	for label, value := range serviceDetail.Placement.Require {
		fmt.Printf("  Require:       %s = %s\n", label, value)
	}
	for label, value := range serviceDetail.Placement.Prefer {
		fmt.Printf("  Prefer:        %s = %s\n", label, value)
	}
	for _, label := range serviceDetail.Placement.Accept {
		fmt.Printf("  Accept:        %s\n", label)
	}
}

// printServiceDescribeHealthAndProbes prints the health check configuration
// and probe definitions (startup, liveness, readiness) for a service.
func printServiceDescribeHealthAndProbes(serviceDetail api.ServiceDescribe) {
	if serviceDetail.Health != nil {
		fmt.Println()
		fmt.Println("Health Check:")
		fmt.Printf("  Method:    %s\n", serviceDetail.Health.Method)
		if serviceDetail.Health.Path != "" {
			fmt.Printf("  Path:      %s\n", serviceDetail.Health.Path)
		}
		if serviceDetail.Health.Interval != "" {
			fmt.Printf("  Interval:  %s\n", serviceDetail.Health.Interval)
		}
	}

	if len(serviceDetail.Probes) > 0 {
		fmt.Println()
		fmt.Println("Probes:")
		for _, probe := range serviceDetail.Probes {
			fmt.Printf("  %s:\n", probe.Type)
			fmt.Printf("    Method:    %s\n", probe.Method)
			if probe.Path != "" {
				fmt.Printf("    Path:      %s\n", probe.Path)
			}
			if probe.Port != "" {
				fmt.Printf("    Port:      %s\n", probe.Port)
			}
			if probe.Interval != "" {
				fmt.Printf("    Interval:  %s\n", probe.Interval)
			}
			if probe.Timeout != "" {
				fmt.Printf("    Timeout:   %s\n", probe.Timeout)
			}
		}
	}
}

// printServiceDescribeInitSteps prints the ordered initialization steps that
// run before the main workload starts.
func printServiceDescribeInitSteps(serviceDetail api.ServiceDescribe) {
	if len(serviceDetail.InitSteps) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Init Steps:")
	for _, step := range serviceDetail.InitSteps {
		fmt.Printf("  [%d] exec %q", step.Index, step.Exec)
		if step.Timeout != "" {
			fmt.Printf("  timeout=%s", step.Timeout)
		}
		if step.Retry != "" {
			fmt.Printf("  retry=%s", step.Retry)
		}
		fmt.Println()
	}
}

// printServiceDescribeScalingAndRollout prints autoscaling targets, update
// strategy constraints, and active rollout state for a service.
func printServiceDescribeScalingAndRollout(serviceDetail api.ServiceDescribe) {
	if serviceDetail.Autoscaling != nil {
		fmt.Println()
		fmt.Println("Autoscaling:")
		fmt.Printf("  Min: %d  Max: %d\n", serviceDetail.Autoscaling.Min, serviceDetail.Autoscaling.Max)
		for _, target := range serviceDetail.Autoscaling.Targets {
			fmt.Printf("  Target:  %s = %d\n", target.Metric, target.Value)
		}
	}

	if serviceDetail.UpdateStrategy != nil {
		fmt.Println()
		fmt.Println("Update Strategy:")
		if serviceDetail.UpdateStrategy.MaxUnavailable != "" {
			fmt.Printf("  Max unavailable:  %s\n", serviceDetail.UpdateStrategy.MaxUnavailable)
		}
		if serviceDetail.UpdateStrategy.MaxExtra != "" {
			fmt.Printf("  Max extra:        %s\n", serviceDetail.UpdateStrategy.MaxExtra)
		}
	}

	if serviceDetail.Rollout != nil {
		fmt.Println()
		fmt.Println("Rollout:")
		fmt.Printf("  State:  %s\n", serviceDetail.Rollout.State)
		if serviceDetail.Rollout.PreviousImage != "" {
			fmt.Printf("  Previous image:  %s\n", serviceDetail.Rollout.PreviousImage)
		}
		if serviceDetail.Rollout.Failures != "" {
			fmt.Printf("  Failures:        %s\n", serviceDetail.Rollout.Failures)
		}
	}
}

// printServiceDescribeNetworkingAndConfig prints the networking configuration
// (VIP, port, DNS), config entries, secrets, and endpoints for a service.
func printServiceDescribeNetworkingAndConfig(serviceDetail api.ServiceDescribe) {
	if serviceDetail.Networking != nil {
		fmt.Println()
		fmt.Println("Networking:")
		if serviceDetail.Networking.VIP != "" {
			fmt.Printf("  VIP:   %s\n", serviceDetail.Networking.VIP)
		}
		if serviceDetail.Networking.Port > 0 {
			fmt.Printf("  Port:  %d\n", serviceDetail.Networking.Port)
		}
		if serviceDetail.Networking.DNS != "" {
			fmt.Printf("  DNS:   %s\n", serviceDetail.Networking.DNS)
		}
	}

	if len(serviceDetail.Config) > 0 {
		fmt.Println()
		fmt.Println("Config:")
		for _, configEntry := range serviceDetail.Config {
			fmt.Printf("  [%s] %s = %s\n", configEntry.Type, configEntry.Key, configEntry.Value)
		}
	}

	if len(serviceDetail.Secrets) > 0 {
		fmt.Println()
		fmt.Println("Secrets:")
		for _, secretGrant := range serviceDetail.Secrets {
			fmt.Printf("  %s -> %s\n", secretGrant.Name, secretGrant.MountPath)
		}
	}

	if len(serviceDetail.Endpoints) > 0 {
		fmt.Println()
		fmt.Println("Endpoints:")
		for _, endpoint := range serviceDetail.Endpoints {
			fmt.Printf("  %s\n", endpoint)
		}
	}
}

// printServiceDescribeInstances prints the instance table showing ID, ready,
// status, node, IP, and restart count for each instance of the service.
func printServiceDescribeInstances(serviceDetail api.ServiceDescribe) {
	if len(serviceDetail.Instances) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Instances:")
	fmt.Printf("  %-20s  %-6s  %-10s  %-10s  %-8s  %-14s  %-16s  %s\n",
		"ID", "READY", "STATUS", "RESTARTS", "AGE", "NODE", "IP", "REASON")
	for _, instanceSummary := range serviceDetail.Instances {
		restartDisplay := instanceSummary.Restarts
		if restartDisplay == "" {
			restartDisplay = "0"
		}
		readyDisplay := "0/1"
		if instanceSummary.State == "running" && instanceSummary.Health != "unhealthy" {
			readyDisplay = "1/1"
		}
		reasonDisplay := instanceSummary.StatusReason
		if reasonDisplay == "" || reasonDisplay == "running" {
			reasonDisplay = "-"
		}
		ageDisplay := formatRelativeAge(instanceSummary.CreatedAt)
		fmt.Printf("  %-20s  %-6s  %-10s  %-10s  %-8s  %-14s  %-16s  %s\n",
			instanceSummary.ID, readyDisplay, instanceSummary.State,
			restartDisplay, ageDisplay, instanceSummary.NodeID,
			instanceSummary.IPAddress, reasonDisplay)
	}
}

// printNodeDescribe renders a NodeDescribe as human-readable text output.
func printNodeDescribe(nodeDetail api.NodeDescribe) {
	fmt.Printf("ID:            %s\n", nodeDetail.ID)
	fmt.Printf("State:         %s\n", nodeDetail.State)
	if nodeDetail.Address != "" {
		fmt.Printf("Address:       %s\n", nodeDetail.Address)
	}
	if nodeDetail.Architecture != "" {
		fmt.Printf("Architecture:  %s\n", nodeDetail.Architecture)
	}
	if nodeDetail.Zone != "" {
		fmt.Printf("Zone:          %s\n", nodeDetail.Zone)
	}
	if nodeDetail.Subnet != "" {
		fmt.Printf("Subnet:        %s\n", nodeDetail.Subnet)
	}

	fmt.Println()
	fmt.Println("Resources:")
	fmt.Printf("  CPU:     %d/%dm available\n", nodeDetail.AvailableCPU, nodeDetail.CapacityCPU)
	fmt.Printf("  Memory:  %d/%dMi available\n", nodeDetail.AvailableMemory, nodeDetail.CapacityMemory)
	if nodeDetail.UtilizationCPU != "" {
		fmt.Printf("  CPU utilization:     %s%%\n", nodeDetail.UtilizationCPU)
	}
	if nodeDetail.UtilizationMemory != "" {
		fmt.Printf("  Memory utilization:  %s%%\n", nodeDetail.UtilizationMemory)
	}
	if nodeDetail.WorkloadCount != "" {
		fmt.Printf("  Workloads:           %s\n", nodeDetail.WorkloadCount)
	}

	if len(nodeDetail.Labels) > 0 {
		fmt.Println()
		fmt.Println("Labels:")
		labelNames := make([]string, 0, len(nodeDetail.Labels))
		for labelName := range nodeDetail.Labels {
			labelNames = append(labelNames, labelName)
		}
		sort.Strings(labelNames)
		for _, labelName := range labelNames {
			fmt.Printf("  %s = %s\n", labelName, nodeDetail.Labels[labelName])
		}
	}

	if len(nodeDetail.Restrictions) > 0 {
		fmt.Println()
		fmt.Println("Restrictions:")
		for _, restriction := range nodeDetail.Restrictions {
			fmt.Printf("  %s\n", restriction)
		}
	}

	if len(nodeDetail.Instances) > 0 {
		fmt.Println()
		fmt.Println("Instances:")
		fmt.Printf("  %-28s  %-6s  %-10s  %-10s  %-8s  %-16s  %s\n",
			"CONTAINER", "READY", "STATUS", "RESTARTS", "AGE", "IP", "REASON")
		for _, instanceSummary := range nodeDetail.Instances {
			restartDisplay := instanceSummary.Restarts
			if restartDisplay == "" {
				restartDisplay = "0"
			}
			readyDisplay := "0/1"
			if instanceSummary.State == "running" && instanceSummary.Health != "unhealthy" {
				readyDisplay = "1/1"
			}
			containerName := instanceSummary.Service + "-" + instanceSummary.ID
			ageDisplay := formatRelativeAge(instanceSummary.CreatedAt)
			reasonDisplay := instanceSummary.StatusReason
			if reasonDisplay == "" || reasonDisplay == "running" {
				reasonDisplay = "-"
			}
			fmt.Printf("  %-28s  %-6s  %-10s  %-10s  %-8s  %-16s  %s\n",
				containerName, readyDisplay, instanceSummary.State,
				restartDisplay, ageDisplay, instanceSummary.IPAddress, reasonDisplay)
		}
	}

	printDescribeEvents(nodeDetail.Events)
}

// printInstanceDescribe renders an InstanceDescribe as human-readable text output.
func printInstanceDescribe(instanceDetail api.InstanceDescribe) {
	fmt.Printf("ID:         %s\n", instanceDetail.ID)
	fmt.Printf("Service:    %s\n", instanceDetail.ServiceName)
	fmt.Printf("Node:       %s\n", instanceDetail.NodeID)
	fmt.Printf("State:      %s\n", instanceDetail.State)
	if instanceDetail.Image != "" {
		fmt.Printf("Image:      %s\n", instanceDetail.Image)
	}
	if instanceDetail.IPAddress != "" {
		fmt.Printf("IP:         %s\n", instanceDetail.IPAddress)
	}
	if instanceDetail.HostPort != "" {
		fmt.Printf("Host port:  %s\n", instanceDetail.HostPort)
	}
	if instanceDetail.Endpoint != "" {
		fmt.Printf("Endpoint:   %s\n", instanceDetail.Endpoint)
	}

	if instanceDetail.StatusReason != "" && instanceDetail.StatusReason != "running" {
		fmt.Printf("Reason:     %s\n", instanceDetail.StatusReason)
	}

	fmt.Println()
	fmt.Println("Health:")
	healthDisplay := instanceDetail.HealthState
	if healthDisplay == "" {
		healthDisplay = "-"
	}
	fmt.Printf("  Status:     %s\n", healthDisplay)
	if instanceDetail.InitPhase != "" {
		fmt.Printf("  Init:       %s\n", instanceDetail.InitPhase)
	}
	if instanceDetail.InitProgress != "" {
		fmt.Printf("  Init progress: %s\n", formatInitProgressCompact(instanceDetail.InitProgress))
	}
	if instanceDetail.StartupProbe != "" {
		fmt.Printf("  Startup:    %s\n", instanceDetail.StartupProbe)
	}
	if instanceDetail.LivenessProbe != "" {
		fmt.Printf("  Liveness:   %s\n", instanceDetail.LivenessProbe)
	}
	if instanceDetail.ReadinessProbe != "" {
		fmt.Printf("  Readiness:  %s\n", instanceDetail.ReadinessProbe)
	}
	if instanceDetail.ProbeProgress != "" {
		fmt.Printf("  Probe progress: %s\n", formatProbeProgressCompact(instanceDetail.ProbeProgress, true))
	}

	restartDisplay := instanceDetail.Restarts
	if restartDisplay == "" {
		restartDisplay = "0"
	}
	fmt.Printf("  Restarts:   %s\n", restartDisplay)

	if instanceDetail.CPUMillis != "" || instanceDetail.MemoryBytes != "" {
		fmt.Println()
		fmt.Println("Resources:")
		if instanceDetail.CPUMillis != "" {
			fmt.Printf("  CPU:     %sm\n", instanceDetail.CPUMillis)
		}
		if instanceDetail.MemoryBytes != "" {
			fmt.Printf("  Memory:  %s\n", instanceDetail.MemoryBytes)
		}
	}

	printDescribeEvents(instanceDetail.Events)
}

// printDescribeEvents renders the recent events section for any describe output.
func printDescribeEvents(events []api.DescribeEvent) {
	if len(events) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Events:")
	fmt.Printf("  %-20s  %-22s  %s\n", "TIMESTAMP", "KIND", "DETAIL")
	for _, event := range events {
		fmt.Printf("  %-20s  %-22s  %s\n", event.Timestamp, event.Kind, event.Detail)
	}
}

// executeGetCommand queries the API for a specific resource type and prints
// the result as formatted JSON.
func executeGetCommand(resourceType string, extraArgs []string) {
	outputOptions, remainingArgs := parseGetOutputOptions(extraArgs)

	apiBaseURL := statusAPIBaseURL() + "/api/status"
	if outputOptions.includeAll {
		apiBaseURL += "?all=true"
	}
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiBaseURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)

	var status api.ClusterStatus
	if err := json.NewDecoder(httpResponse.Body).Decode(&status); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	if outputOptions.outputFormat == "json" {
		printGetResourceJSON(resourceType, status, remainingArgs)
		return
	}

	switch resourceType {
	case "services", "svc":
		tagFilters := parseTagFilterArgs(remainingArgs)
		filteredServices := filterServicesByTags(status.Services, tagFilters)
		printServicesTableWithOptions(filteredServices, outputOptions)
	case "instances", "inst":
		printInstancesTableWithOptions(status.Instances, outputOptions)
	case "nodes":
		printNodesTableWithOptions(status.Nodes, outputOptions)
	case "volumes", "vol":
		printVolumesTableWithOptions(status.Volumes, outputOptions)
	case "networking", "net":
		printNetworkingTableWithOptions(status.Networking, outputOptions)
	case "secrets", "secret":
		printSecretsTableWithOptions(status.Secrets, outputOptions)
	case "config", "cfg":
		printConfigTableWithOptions(status.Config, outputOptions)
	case "cloud-identities", "cloud-identity", "identities":
		printCloudIdentitiesTableWithOptions(status.CloudIdentities, outputOptions)
	case "endpoints", "ep":
		printEndpointsTableWithOptions(status.Endpoints, outputOptions)
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s (use services, instances, nodes, volumes, networking, secrets, config, cloud-identities, endpoints)\n", resourceType)
		os.Exit(1)
	}
}

// printGetResourceJSON outputs the requested resource slice as JSON.
func printGetResourceJSON(resourceType string, status api.ClusterStatus, remainingArgs []string) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	switch resourceType {
	case "services", "svc":
		tagFilters := parseTagFilterArgs(remainingArgs)
		_ = encoder.Encode(filterServicesByTags(status.Services, tagFilters))
	case "instances", "inst":
		_ = encoder.Encode(status.Instances)
	case "nodes":
		_ = encoder.Encode(status.Nodes)
	case "volumes", "vol":
		_ = encoder.Encode(status.Volumes)
	case "networking", "net":
		_ = encoder.Encode(status.Networking)
	case "secrets", "secret":
		_ = encoder.Encode(status.Secrets)
	case "config", "cfg":
		_ = encoder.Encode(status.Config)
	case "cloud-identities", "cloud-identity", "identities":
		_ = encoder.Encode(status.CloudIdentities)
	case "endpoints", "ep":
		_ = encoder.Encode(status.Endpoints)
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s\n", resourceType)
		os.Exit(1)
	}
}

// parseTagFilterArgs extracts --tag key=value pairs from command arguments.
// Each --tag flag must be followed by a key=value argument.
func parseTagFilterArgs(args []string) map[string]string {
	tagFilters := make(map[string]string)
	for argIndex := 0; argIndex < len(args); argIndex++ {
		if args[argIndex] == "--tag" && argIndex+1 < len(args) {
			argIndex++
			tagExpression := args[argIndex]
			equalsIndex := strings.Index(tagExpression, "=")
			if equalsIndex > 0 {
				tagFilters[tagExpression[:equalsIndex]] = tagExpression[equalsIndex+1:]
			}
		}
	}
	return tagFilters
}

// filterServicesByTags returns only services matching all specified tag filters.
// If no filters are specified, all services are returned.
func filterServicesByTags(services []api.ServiceStatus, tagFilters map[string]string) []api.ServiceStatus {
	if len(tagFilters) == 0 {
		return services
	}
	var matchingServices []api.ServiceStatus
	for _, service := range services {
		if serviceMatchesAllTags(service.Tags, tagFilters) {
			matchingServices = append(matchingServices, service)
		}
	}
	return matchingServices
}

// filterInstancesByServiceTags returns only instances belonging to services that
// match all specified tag filters. If no filters are specified, all instances
// are returned unchanged.
func filterInstancesByServiceTags(instances []api.InstanceStatus, services []api.ServiceStatus, tagFilters map[string]string) []api.InstanceStatus {
	if len(tagFilters) == 0 {
		return instances
	}
	matchingServiceNames := make(map[string]bool)
	for _, service := range services {
		if serviceMatchesAllTags(service.Tags, tagFilters) {
			matchingServiceNames[service.Name] = true
		}
	}
	var filteredInstances []api.InstanceStatus
	for _, instance := range instances {
		if matchingServiceNames[instance.ServiceName] {
			filteredInstances = append(filteredInstances, instance)
		}
	}
	return filteredInstances
}

// serviceMatchesAllTags checks whether a service's tags contain every filter entry.
func serviceMatchesAllTags(serviceTags map[string]string, tagFilters map[string]string) bool {
	for filterKey, filterValue := range tagFilters {
		if serviceTags[filterKey] != filterValue {
			return false
		}
	}
	return true
}

// getOutputOptions holds parsed output formatting flags for get commands.
type getOutputOptions struct {
	outputFormat string // outputFormat is "json" or "wide" or empty (compact default).
	noHeaders    bool   // noHeaders suppresses the table header row.
	includeAll   bool   // includeAll shows stopped/terminated resources when true.
}

// parseGetOutputOptions extracts -o and --no-headers flags from arguments,
// returning the options and the remaining arguments.
func parseGetOutputOptions(args []string) (getOutputOptions, []string) {
	var options getOutputOptions
	var remaining []string
	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "-o", "--output":
			if argIndex+1 < len(args) {
				argIndex++
				options.outputFormat = args[argIndex]
			}
		case "--no-headers":
			options.noHeaders = true
		case "--all", "-a":
			options.includeAll = true
		default:
			remaining = append(remaining, args[argIndex])
		}
	}
	return options, remaining
}

// printAlignedTableFiltered renders rows as an aligned table, optionally
// suppressing the header row when skipHeader is true.
func printAlignedTableFiltered(header []string, rows [][]string, skipHeader bool) {
	columnWidths := make([]int, len(header))
	for columnIndex, columnHeader := range header {
		columnWidths[columnIndex] = len(columnHeader)
	}
	for _, row := range rows {
		for columnIndex, cell := range row {
			if columnIndex < len(columnWidths) && len(cell) > columnWidths[columnIndex] {
				columnWidths[columnIndex] = len(cell)
			}
		}
	}

	formatLine := func(cells []string) string {
		var builder strings.Builder
		for columnIndex, cell := range cells {
			if columnIndex > 0 {
				builder.WriteString("  ")
			}
			if columnIndex < len(columnWidths) {
				fmt.Fprintf(&builder, "%-*s", columnWidths[columnIndex], cell)
			} else {
				builder.WriteString(cell)
			}
		}
		return builder.String()
	}

	if !skipHeader {
		fmt.Println(formatLine(header))
	}
	for _, row := range rows {
		fmt.Println(formatLine(row))
	}
}

// truncateValue shortens a string to maxLength, appending "..." if truncated.
func truncateValue(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return value[:maxLength-3] + "..."
}

func printServicesTableWithOptions(services []api.ServiceStatus, options getOutputOptions) {
	anyServiceHasTags := false
	for _, service := range services {
		if len(service.Tags) > 0 {
			anyServiceHasTags = true
			break
		}
	}

	header := []string{"NAME", "IMAGE", "DESIRED", "RUNNING", "PORTS", "STATUS"}
	if anyServiceHasTags {
		header = append(header, "TAGS")
	}

	var rows [][]string
	for _, service := range services {
		portStrings := make([]string, len(service.ExposedPorts))
		for portIndex, port := range service.ExposedPorts {
			portStrings[portIndex] = fmt.Sprintf("%d", port)
		}
		portsDisplay := strings.Join(portStrings, ",")
		if portsDisplay == "" {
			portsDisplay = "-"
		}
		row := []string{
			service.Name,
			truncateValue(service.Image, 40),
			fmt.Sprintf("%d", service.DesiredCount),
			fmt.Sprintf("%d", service.RunningCount),
			portsDisplay,
			formatServiceCrashLoopStatus(service),
		}
		if anyServiceHasTags {
			row = append(row, formatServiceTags(service.Tags))
		}
		rows = append(rows, row)
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

// formatServiceCrashLoopStatus returns a compact status string for a service's
// crash-loop state: "OK" when healthy, "CrashLoop(N)" during backoff, or
// "CircuitBroken(N)" when the circuit breaker has tripped.
func formatServiceCrashLoopStatus(service api.ServiceStatus) string {
	if service.CrashLoopCount == 0 {
		return "OK"
	}
	if service.CircuitBroken {
		return fmt.Sprintf("CircuitBroken(%d)", service.CrashLoopCount)
	}
	return fmt.Sprintf("CrashLoop(%d)", service.CrashLoopCount)
}

// formatServiceTags renders a service's tags as a compact "key=value,..." string
// with deterministic key ordering.
func formatServiceTags(tags map[string]string) string {
	if len(tags) == 0 {
		return "-"
	}
	tagKeys := make([]string, 0, len(tags))
	for tagKey := range tags {
		tagKeys = append(tagKeys, tagKey)
	}
	sort.Strings(tagKeys)
	tagPairs := make([]string, len(tagKeys))
	for pairIndex, tagKey := range tagKeys {
		tagPairs[pairIndex] = tagKey + "=" + tags[tagKey]
	}
	return strings.Join(tagPairs, ",")
}

func printInstancesTableWithOptions(instances []api.InstanceStatus, options getOutputOptions) {
	sort.Slice(instances, func(first, second int) bool {
		nameFirst := instances[first].ServiceName + "-" + instances[first].ID
		nameSecond := instances[second].ServiceName + "-" + instances[second].ID
		return nameFirst < nameSecond
	})
	wideOutput := options.outputFormat == "wide"
	header := []string{"CONTAINER", "READY", "STATUS", "RESTARTS", "AGE", "NODE", "IP", "REASON"}
	var rows [][]string
	for _, instance := range instances {
		readyDisplay := formatReadyColumn(instance)
		ipDisplay := instance.IPAddress
		if ipDisplay == "" {
			ipDisplay = "-"
		}
		restartDisplay := instance.Restarts
		if restartDisplay == "" {
			restartDisplay = "0"
		}
		containerName := instance.ServiceName + "-" + instance.ID
		reasonDisplay := formatInstanceReason(instance, wideOutput)
		ageDisplay := formatRelativeAge(instance.CreatedAt)
		rows = append(rows, []string{
			containerName,
			readyDisplay,
			instance.State,
			restartDisplay,
			ageDisplay,
			instance.NodeID,
			ipDisplay,
			reasonDisplay,
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

// formatReadyColumn returns "1/1" when the instance is running and healthy
// (or has no probes configured), "0/1" otherwise. Mirrors kubectl READY.
func formatReadyColumn(instance api.InstanceStatus) string {
	if instance.State != "running" {
		return "0/1"
	}
	if instance.HealthState == "unhealthy" {
		return "0/1"
	}
	return "1/1"
}

// formatRelativeAge converts an RFC3339 timestamp to a kubectl-style relative
// age string like "5m", "2h", "3d". Returns "<unknown>" if the timestamp is
// empty or unparseable.
func formatRelativeAge(createdAt string) string {
	if createdAt == "" {
		return "<unknown>"
	}
	parsedTime, parseError := time.Parse(time.RFC3339, createdAt)
	if parseError != nil {
		return "<unknown>"
	}
	elapsed := time.Since(parsedTime)
	if elapsed < 0 {
		return "0s"
	}
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd", int(elapsed.Hours()/24))
	}
}

// formatInstanceReason builds a human-readable reason string from the
// instance's status reason, init progress, and probe progress facts.
// When wideOutput is false, long reasons are truncated to 80 characters.
func formatInstanceReason(instance api.InstanceStatus, wideOutput bool) string {
	if instance.State == "running" && instance.StatusReason == "running" {
		return "-"
	}
	var reason string
	switch {
	case instance.ExitReason != "":
		reason = instance.ExitReason
	case instance.InitProgress != "":
		reason = formatInitProgressCompact(instance.InitProgress)
	case instance.ProbeProgress != "":
		reason = formatProbeProgressCompact(instance.ProbeProgress, wideOutput)
	case instance.StatusReason != "" && instance.StatusReason != "running":
		reason = instance.StatusReason
	default:
		return "-"
	}
	if !wideOutput {
		return truncateReason(reason, 80)
	}
	return reason
}

// formatInitProgressCompact renders init progress JSON as a compact string
// like "init:0 conn:airflow-db 6/31".
func formatInitProgressCompact(initProgressJSON string) string {
	var progress types.InitProgress
	if err := json.Unmarshal([]byte(initProgressJSON), &progress); err != nil {
		return initProgressJSON
	}
	label := progress.StepLabel
	if len(label) > 30 {
		label = label[:30] + "..."
	}
	return fmt.Sprintf("init:%d/%d %s %d/%d",
		progress.Step, progress.TotalSteps,
		label,
		progress.Attempt, progress.MaxRetries)
}

// formatProbeProgressCompact renders probe progress JSON as a compact string
// like "startup:5/30" or "liveness:2/5". When wideOutput is true, the full
// error message is included untruncated.
func formatProbeProgressCompact(probeProgressJSON string, wideOutput bool) string {
	var progress types.ProbeProgress
	if err := json.Unmarshal([]byte(probeProgressJSON), &progress); err != nil {
		return probeProgressJSON
	}
	var parts []string
	if progress.StartupFailures > 0 {
		entry := fmt.Sprintf("startup:%d/%d", progress.StartupFailures, progress.StartupThreshold)
		if progress.StartupError != "" {
			entry += " " + progress.StartupError
		}
		parts = append(parts, entry)
	}
	if progress.LivenessFailures > 0 {
		entry := fmt.Sprintf("liveness:%d/%d", progress.LivenessFailures, progress.LivenessThreshold)
		if progress.LivenessError != "" {
			entry += " " + progress.LivenessError
		}
		parts = append(parts, entry)
	}
	if progress.ReadinessFailures > 0 {
		entry := fmt.Sprintf("readiness:%d/%d", progress.ReadinessFailures, progress.ReadinessThreshold)
		if progress.ReadinessError != "" {
			entry += " " + progress.ReadinessError
		}
		parts = append(parts, entry)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

// truncateReason shortens a reason string to maxLength characters, appending
// an ellipsis if truncated. Cuts at the last space before the limit to avoid
// splitting words mid-token.
func truncateReason(reason string, maxLength int) string {
	if len(reason) <= maxLength {
		return reason
	}
	truncated := reason[:maxLength]
	lastSpace := strings.LastIndex(truncated, " ")
	if lastSpace > maxLength/2 {
		truncated = truncated[:lastSpace]
	}
	return truncated + "..."
}

func printNodesTableWithOptions(nodes []api.NodeStatus, options getOutputOptions) {
	header := []string{"ID", "STATE", "INSTANCES", "CPU (avail/total)", "MEMORY (avail/total)"}
	var rows [][]string
	for _, node := range nodes {
		cpuDisplay := fmt.Sprintf("%dm/%dm", node.AvailableCPU, node.CapacityCPU)
		memoryDisplay := fmt.Sprintf("%dMi/%dMi", node.AvailableMemory, node.CapacityMemory)
		rows = append(rows, []string{
			node.ID,
			node.State,
			fmt.Sprintf("%d", node.PlacedInstances),
			cpuDisplay,
			memoryDisplay,
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

func printVolumesTableWithOptions(volumes []api.VolumeStatus, options getOutputOptions) {
	if len(volumes) == 0 {
		fmt.Println("No volumes found.")
		return
	}
	header := []string{"NAME", "SIZE", "STATE", "NODE", "INSTANCE", "MOUNT"}
	var rows [][]string
	for _, volume := range volumes {
		nodeDisplay := volume.Node
		if nodeDisplay == "" {
			nodeDisplay = "-"
		}
		instanceDisplay := volume.Instance
		if instanceDisplay == "" {
			instanceDisplay = "-"
		}
		mountDisplay := volume.MountPath
		if mountDisplay == "" {
			mountDisplay = "-"
		}
		rows = append(rows, []string{
			volume.Name,
			volume.Size,
			volume.State,
			nodeDisplay,
			truncateValue(instanceDisplay, instanceIDDisplayWidth),
			mountDisplay,
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

func printNetworkingTableWithOptions(networking []api.NetworkStatus, options getOutputOptions) {
	if len(networking) == 0 {
		fmt.Println("No networking entries found.")
		return
	}
	header := []string{"SERVICE", "VIP", "PORT", "DNS"}
	var rows [][]string
	for _, entry := range networking {
		dnsDisplay := entry.DNS
		if dnsDisplay == "" {
			dnsDisplay = "-"
		}
		rows = append(rows, []string{
			entry.ServiceName,
			entry.VIP,
			fmt.Sprintf("%d", entry.Port),
			dnsDisplay,
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

func printSecretsTableWithOptions(secrets []api.SecretStatus, options getOutputOptions) {
	if len(secrets) == 0 {
		fmt.Println("No secrets found.")
		return
	}
	header := []string{"NAME", "GRANTED TO"}
	var rows [][]string
	for _, secret := range secrets {
		grantedDisplay := strings.Join(secret.GrantedTo, ", ")
		if grantedDisplay == "" {
			grantedDisplay = "-"
		}
		rows = append(rows, []string{
			secret.Name,
			grantedDisplay,
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

func printConfigTableWithOptions(configEntries []api.ConfigStatus, options getOutputOptions) {
	if len(configEntries) == 0 {
		fmt.Println("No config entries found.")
		return
	}
	header := []string{"SERVICE", "TYPE", "KEY", "VALUE"}
	var rows [][]string
	for _, entry := range configEntries {
		rows = append(rows, []string{
			entry.Service,
			entry.Type,
			entry.Key,
			truncateValue(entry.Value, configValueDisplayWidth),
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

func printCloudIdentitiesTableWithOptions(cloudIdentities []api.CloudIdentityStatus, options getOutputOptions) {
	if len(cloudIdentities) == 0 {
		fmt.Println("No cloud identities found.")
		return
	}
	header := []string{"NAME", "PROVIDER", "SERVICES"}
	var rows [][]string
	for _, identity := range cloudIdentities {
		services := "-"
		if len(identity.Services) > 0 {
			services = strings.Join(identity.Services, ", ")
		}
		rows = append(rows, []string{
			identity.Name,
			identity.Provider,
			services,
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

// printEndpointsTableWithOptions prints endpoint status entries as an aligned table.
func printEndpointsTableWithOptions(endpoints []api.EndpointStatus, options getOutputOptions) {
	if len(endpoints) == 0 {
		fmt.Println("No endpoints found.")
		return
	}
	header := []string{"SERVICE", "INSTANCE", "PORT", "HOSTNAME", "INTERNAL", "EXTERNAL"}
	var rows [][]string
	for _, endpoint := range endpoints {
		externalDisplay := endpoint.ExternalAddress
		if externalDisplay == "" {
			externalDisplay = "-"
		}
		internalDisplay := endpoint.InternalAddress
		if internalDisplay == "" {
			internalDisplay = "-"
		}
		rows = append(rows, []string{
			endpoint.Service,
			endpoint.InstanceID,
			fmt.Sprintf("%d", endpoint.Port),
			endpoint.Hostname,
			internalDisplay,
			externalDisplay,
		})
	}
	printAlignedTableFiltered(header, rows, options.noHeaders)
}

// executeScaleCommand sends a scale request to the API to change a service's
// desired instance count.
func executeScaleCommand(serviceName, countStr string) {
	requestBody := fmt.Sprintf(`{"service":%q,"instances":%s}`, serviceName, countStr)
	apiURL := statusAPIBaseURL() + "/api/scale"
	httpResponse, err := buildAuthenticatedHTTPClient().Post(apiURL, "application/json", strings.NewReader(requestBody)) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "scale failed: %s\n", string(responseBody))
		os.Exit(1)
	}
	fmt.Printf("scaled %s to %s instances\n", serviceName, countStr)
}

// executeRestartCommand restarts a single container instance by setting its
// state to stopped. The instance controller will create a replacement.
func executeRestartCommand(target string) {
	instanceID := target
	if _, parsedID, isContainerName := parseContainerName(target); isContainerName {
		instanceID = parsedID
	}

	requestBody := fmt.Sprintf(`{"instance_id":%q}`, instanceID)
	apiURL := statusAPIBaseURL() + "/api/restart"
	httpResponse, err := buildAuthenticatedHTTPClient().Post(apiURL, "application/json", strings.NewReader(requestBody)) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "restart failed: %s\n", string(responseBody))
		os.Exit(1)
	}
	fmt.Printf("restarting instance %s\n", target)
}

// executeRolloutRestartCommand triggers a rolling restart of all instances
// belonging to a service.
func executeRolloutRestartCommand(serviceName string) {
	requestBody := fmt.Sprintf(`{"service":%q}`, serviceName)
	apiURL := statusAPIBaseURL() + "/api/rollout/restart"
	httpResponse, err := buildAuthenticatedHTTPClient().Post(apiURL, "application/json", strings.NewReader(requestBody)) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "rollout restart failed: %s\n", string(responseBody))
		os.Exit(1)
	}

	var result struct {
		Restarted int `json:"restarted"`
	}
	_ = json.Unmarshal(responseBody, &result)
	fmt.Printf("rolling restart of %s: %d instances restarting\n", serviceName, result.Restarted)
}

// defaultCLIDrainGracePeriodSeconds is the default grace period for the drain
// CLI command when --grace-period is not specified.
const defaultCLIDrainGracePeriodSeconds = 30

// parseDrainGracePeriod extracts the --grace-period flag from drain command
// arguments. Returns the default grace period if the flag is not present.
func parseDrainGracePeriod(args []string) int {
	for argIndex := 0; argIndex < len(args); argIndex++ {
		if args[argIndex] == "--grace-period" && argIndex+1 < len(args) {
			parsed, parseErr := strconv.Atoi(args[argIndex+1])
			if parseErr == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return defaultCLIDrainGracePeriodSeconds
}

// executeDrainCommand sends a drain request to the API to initiate graceful
// eviction of all instances from a node.
func executeDrainCommand(nodeID string, gracePeriod int) {
	requestBody := fmt.Sprintf(`{"node_id":%q,"grace_period":%d}`, nodeID, gracePeriod)
	apiURL := statusAPIBaseURL() + "/api/node/drain"
	httpResponse, err := buildAuthenticatedHTTPClient().Post(apiURL, "application/json", strings.NewReader(requestBody)) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'server' or 'run' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "drain failed: %s\n", string(responseBody))
		os.Exit(1)
	}
	fmt.Printf("draining node %s (grace period: %ds)\n", nodeID, gracePeriod)
}

// executeDisableNodeCommand sends a disable request to the API to exclude a node
// from new placements while keeping existing workloads running.
func executeDisableNodeCommand(nodeID string) {
	requestBody := fmt.Sprintf(`{"node_id":%q}`, nodeID)
	apiURL := statusAPIBaseURL() + "/api/node/disable"
	httpResponse, err := buildAuthenticatedHTTPClient().Post(apiURL, "application/json", strings.NewReader(requestBody)) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'server' or 'run' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "disable-node failed: %s\n", string(responseBody))
		os.Exit(1)
	}
	fmt.Printf("disabled node %s\n", nodeID)
}

// executeEnableNodeCommand sends an enable request to the API to return a
// disabled node to normal scheduling eligibility.
func executeEnableNodeCommand(nodeID string) {
	requestBody := fmt.Sprintf(`{"node_id":%q}`, nodeID)
	apiURL := statusAPIBaseURL() + "/api/node/enable"
	httpResponse, err := buildAuthenticatedHTTPClient().Post(apiURL, "application/json", strings.NewReader(requestBody)) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'server' or 'run' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "enable-node failed: %s\n", string(responseBody))
		os.Exit(1)
	}
	fmt.Printf("enabled node %s\n", nodeID)
}

// executeWatchCommand connects to the API's SSE watch endpoint and prints
// fact store changes as they occur.
func executeWatchCommand(prefix string) {
	apiURL := fmt.Sprintf("%s/api/watch?prefix=%s", statusAPIBaseURL(), prefix)
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	fmt.Printf("watching %s ...\n", prefix)
	buffer := make([]byte, sseWatchReadBufferSize)
	for {
		bytesRead, readErr := httpResponse.Body.Read(buffer)
		if bytesRead > 0 {
			fmt.Print(string(buffer[:bytesRead]))
		}
		if readErr != nil {
			return
		}
	}
}

// executeSecretCommand dispatches cca secret subcommands: set, get, list, delete.
func executeSecretCommand(args []string) {
	subcommand := args[0]
	switch subcommand {
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca secret set <name> <value>")
			os.Exit(1)
		}
		executeSecretSet(args[1], args[2])
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: cca secret get <name>")
			os.Exit(1)
		}
		executeSecretGet(args[1])
	case "list":
		executeSecretList()
	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: cca secret delete <name>")
			os.Exit(1)
		}
		executeSecretDelete(args[1])
	case "rotate":
		executeSecretRotate()
	default:
		fmt.Fprintln(os.Stderr, "usage: cca secret <set|get|list|delete|rotate> [name] [value]")
		os.Exit(1)
	}
}

// executeSecretSet stores an encrypted secret via the API.
func executeSecretSet(secretName, secretValue string) {
	requestURL := fmt.Sprintf("%s/api/secret?name=%s", statusAPIBaseURL(), secretName)
	httpResponse, err := buildAuthenticatedHTTPClient().Post(requestURL, "text/plain", strings.NewReader(secretValue)) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", responseBody)
		os.Exit(1)
	}
	fmt.Printf("secret %q stored\n", secretName)
}

// executeSecretGet retrieves and displays a decrypted secret via the API.
func executeSecretGet(secretName string) {
	requestURL := fmt.Sprintf("%s/api/secret?name=%s", statusAPIBaseURL(), secretName)
	httpResponse, err := buildAuthenticatedHTTPClient().Get(requestURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", responseBody)
		os.Exit(1)
	}

	var result map[string]string
	if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&result); decodeErr != nil {
		fmt.Fprintf(os.Stderr, "decode: %v\n", decodeErr)
		os.Exit(1)
	}
	fmt.Println(result["value"])
}

// executeSecretList displays all stored secret names.
func executeSecretList() {
	requestURL := fmt.Sprintf("%s/api/secret", statusAPIBaseURL())
	httpResponse, err := buildAuthenticatedHTTPClient().Get(requestURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", responseBody)
		os.Exit(1)
	}

	var result map[string]interface{}
	if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&result); decodeErr != nil {
		fmt.Fprintf(os.Stderr, "decode: %v\n", decodeErr)
		os.Exit(1)
	}

	secretNames, ok := result["secrets"].([]interface{})
	if !ok || len(secretNames) == 0 {
		fmt.Println("no secrets stored")
		return
	}
	for _, secretName := range secretNames {
		fmt.Println(secretName)
	}
}

// executeSecretDelete removes an encrypted secret via the API.
func executeSecretDelete(secretName string) {
	requestURL := fmt.Sprintf("%s/api/secret?name=%s", statusAPIBaseURL(), secretName)
	deleteRequest, _ := http.NewRequest(http.MethodDelete, requestURL, nil) //nolint:gosec // URL is local CLI → server, not user-controlled
	httpResponse, err := buildAuthenticatedHTTPClient().Do(deleteRequest)   //nolint:gosec // URL is local CLI → server, not user-controlled
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", responseBody)
		os.Exit(1)
	}
	fmt.Printf("secret %q deleted\n", secretName)
}

// executeSecretRotate rotates the master encryption key by re-wrapping all
// stored secret DEKs under a new key via the API.
func executeSecretRotate() {
	requestURL := fmt.Sprintf("%s/api/secret/rotate", statusAPIBaseURL())
	httpResponse, err := buildAuthenticatedHTTPClient().Post(requestURL, "application/json", nil) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", responseBody)
		os.Exit(1)
	}

	var rotateResult map[string]interface{}
	if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&rotateResult); decodeErr != nil {
		fmt.Fprintf(os.Stderr, "decode: %v\n", decodeErr)
		os.Exit(1)
	}
	rotatedValue, rotatedOK := rotateResult["rotated"].(float64)
	if !rotatedOK {
		fmt.Fprintln(os.Stderr, "unexpected response format")
		os.Exit(1)
	}
	rotatedCount := int(rotatedValue)
	fmt.Printf("master key rotated — %d secret(s) re-wrapped\n", rotatedCount)
}
