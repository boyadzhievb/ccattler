// command_status.go contains CLI commands for querying cluster status, events,
// logs, metrics, resource utilization, describe, get, watch, and scale.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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

	apiURL := "http://" + statusAPIListenAddress + "/api/logs"
	if parsedConfig.serviceFilter != "" {
		apiURL += "?target=" + parsedConfig.serviceFilter
	}
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

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

	fmt.Printf("%-24s  %-22s  %-20s  %s\n", "TIMESTAMP", "KIND", "TARGET", "DETAIL")
	for _, event := range events {
		formattedTimestamp := event.Timestamp.Format("2006-01-02 15:04:05.000")
		fmt.Printf("%-24s  %-22s  %-20s  %s\n", formattedTimestamp, event.Kind, event.Target, event.Detail)
	}
}

// executeEventsFollowMode connects to the SSE event stream and prints new
// events as they arrive. Blocks until interrupted with Ctrl-C.
func executeEventsFollowMode(serviceFilter string) {
	apiURL := "http://" + statusAPIListenAddress + "/api/events/stream"
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

	fmt.Printf("%-24s  %-22s  %-20s  %s\n", "TIMESTAMP", "KIND", "TARGET", "DETAIL")

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
		fmt.Printf("%-24s  %-22s  %-20s  %s\n", formattedTimestamp, event.Kind, event.Target, event.Detail)
	}
}

// logsCommandConfig holds parsed flags for the "logs" command.
type logsCommandConfig struct {
	serviceName string
	instanceID  string
	follow      bool
}

// parseLogsCommandArgs extracts flags from the arguments following "logs".
func parseLogsCommandArgs(args []string) logsCommandConfig {
	parsedConfig := logsCommandConfig{}
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
		default:
			if !strings.HasPrefix(currentArg, "-") && parsedConfig.serviceName == "" {
				parsedConfig.serviceName = currentArg
			}
		}
	}
	return parsedConfig
}

// executeLogsCommand streams container stdout/stderr logs for a service or
// specific instance. Uses nerdctl/docker logs on the local machine.
func executeLogsCommand(parsedConfig logsCommandConfig) {
	if parsedConfig.serviceName == "" && parsedConfig.instanceID == "" {
		fmt.Fprintln(os.Stderr, "usage: cca logs <service> [--follow] [--instance <id>]")
		os.Exit(1)
	}

	if parsedConfig.instanceID != "" {
		streamContainerLogs(parsedConfig.serviceName, parsedConfig.instanceID, parsedConfig.follow)
		return
	}

	apiURL := "http://" + statusAPIListenAddress + "/api/state?prefix=" + "observed/instance/"
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run', 'server', or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var facts []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&facts); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeError)
		os.Exit(1)
	}

	instanceIDs := findInstanceIDsForService(facts, parsedConfig.serviceName)
	if len(instanceIDs) == 0 {
		fmt.Fprintf(os.Stderr, "no running instances found for service %q\n", parsedConfig.serviceName)
		os.Exit(1)
	}

	if parsedConfig.follow {
		streamContainerLogs(parsedConfig.serviceName, instanceIDs[0], true)
		return
	}

	for _, instanceID := range instanceIDs {
		fmt.Printf("==> %s <==\n", instanceID)
		streamContainerLogs(parsedConfig.serviceName, instanceID, false)
		fmt.Println()
	}
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

// streamContainerLogs runs nerdctl logs for the given instance and streams
// output to stdout. Falls back to docker if nerdctl is not available.
// The container name is built from the service name and instance ID.
func streamContainerLogs(serviceName string, instanceID string, follow bool) {
	containerTool := "nerdctl"
	if _, lookupErr := exec.LookPath("nerdctl"); lookupErr != nil {
		containerTool = "docker"
	}

	logsArgs := []string{"logs"}
	if follow {
		logsArgs = append(logsArgs, "--follow")
	}
	containerName := serviceName + "-" + instanceID
	if serviceName == "" {
		containerName = "cca-" + instanceID
	}
	logsArgs = append(logsArgs, containerName)

	logsCommand := exec.Command(containerTool, logsArgs...)
	logsCommand.Stdout = os.Stdout
	logsCommand.Stderr = os.Stderr
	if runError := logsCommand.Run(); runError != nil {
		fmt.Fprintf(os.Stderr, "logs for %s: %v\n", instanceID, runError)
	}
}

// executeStatusCommand queries the status API of a running ccattler instance
// and prints the cluster status to stdout. Requires a running 'run' or 'demo' instance.
func executeStatusCommand() {
	statusRequest, _ := http.NewRequest("GET", "http://"+statusAPIListenAddress+"/status", nil)
	statusRequest.Header.Set("Accept", "text/plain")
	httpResponse, err := buildAuthenticatedHTTPClient().Do(statusRequest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, _ := io.ReadAll(httpResponse.Body)
	fmt.Print(string(responseBody))
}

// executeMetricSetCommand sends a simulated metric value to a running ccattler instance
// via the status API. The metric is stored in the fact store at observed/metric/service/{service}/{metric}.
func executeMetricSetCommand(serviceName, metricName, metricValue string) {
	requestURL := fmt.Sprintf("http://%s/api/metric?service=%s&metric=%s&value=%s",
		statusAPIListenAddress, serviceName, metricName, metricValue)
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
func executeTopCommand(resourceType string) {
	apiBaseURL := "http://" + statusAPIListenAddress + "/api/status"
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiBaseURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

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
		fmt.Printf("%-24s  %-14s  %-10s  %-10s  %-10s  %s\n", "WORKLOAD", "NODE", "STATUS", "CPU", "MEMORY", "HEALTH")
		for _, instanceStatus := range clusterStatus.Instances {
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
	normalizedType := resourceType
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

	apiURL := fmt.Sprintf("http://%s/api/describe?type=%s&name=%s",
		statusAPIListenAddress, normalizedType, resourceName)
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

// printServiceDescribeInstances prints the instance table showing ID, state,
// node, IP, health, and restart count for each instance of the service.
func printServiceDescribeInstances(serviceDetail api.ServiceDescribe) {
	if len(serviceDetail.Instances) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Instances:")
	fmt.Printf("  %-20s  %-10s  %-14s  %-16s  %-10s  %s\n",
		"ID", "STATE", "NODE", "IP", "HEALTH", "RESTARTS")
	for _, instanceSummary := range serviceDetail.Instances {
		restartDisplay := instanceSummary.Restarts
		if restartDisplay == "" {
			restartDisplay = "0"
		}
		fmt.Printf("  %-20s  %-10s  %-14s  %-16s  %-10s  %s\n",
			instanceSummary.ID, instanceSummary.State, instanceSummary.NodeID,
			instanceSummary.IPAddress, instanceSummary.Health, restartDisplay)
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
		fmt.Printf("  %-20s  %-14s  %-10s  %-16s  %-10s  %s\n",
			"ID", "SERVICE", "STATE", "IP", "HEALTH", "RESTARTS")
		for _, instanceSummary := range nodeDetail.Instances {
			restartDisplay := instanceSummary.Restarts
			if restartDisplay == "" {
				restartDisplay = "0"
			}
			fmt.Printf("  %-20s  %-14s  %-10s  %-16s  %-10s  %s\n",
				instanceSummary.ID, instanceSummary.Service, instanceSummary.State,
				instanceSummary.IPAddress, instanceSummary.Health, restartDisplay)
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
	if instanceDetail.StartupProbe != "" {
		fmt.Printf("  Startup:    %s\n", instanceDetail.StartupProbe)
	}
	if instanceDetail.LivenessProbe != "" {
		fmt.Printf("  Liveness:   %s\n", instanceDetail.LivenessProbe)
	}
	if instanceDetail.ReadinessProbe != "" {
		fmt.Printf("  Readiness:  %s\n", instanceDetail.ReadinessProbe)
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
func executeGetCommand(resourceType string) {
	apiBaseURL := "http://" + statusAPIListenAddress + "/api/status"
	httpResponse, err := buildAuthenticatedHTTPClient().Get(apiBaseURL) //nolint:gosec // CLI connects to user-configured API server
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var status api.ClusterStatus
	if err := json.NewDecoder(httpResponse.Body).Decode(&status); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	switch resourceType {
	case "services", "svc":
		printServicesTable(status.Services)
	case "instances", "inst":
		printInstancesTable(status.Instances)
	case "nodes":
		printNodesTable(status.Nodes)
	case "volumes", "vol":
		printVolumesTable(status.Volumes)
	case "networking", "net":
		printNetworkingTable(status.Networking)
	case "secrets", "secret":
		printSecretsTable(status.Secrets)
	case "config", "cfg":
		printConfigTable(status.Config)
	case "cloud-identities", "cloud-identity", "identities":
		printCloudIdentitiesTable(status.CloudIdentities)
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s (use services, instances, nodes, volumes, networking, secrets, config, cloud-identities)\n", resourceType)
		os.Exit(1)
	}
}

// printAlignedTable renders rows as an aligned table with a header row. Column
// widths are computed from the maximum cell width in each column.
func printAlignedTable(header []string, rows [][]string) {
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

	fmt.Println(formatLine(header))
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

func printServicesTable(services []api.ServiceStatus) {
	header := []string{"NAME", "IMAGE", "DESIRED", "RUNNING", "PORTS"}
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
		rows = append(rows, []string{
			service.Name,
			truncateValue(service.Image, 40),
			fmt.Sprintf("%d", service.DesiredCount),
			fmt.Sprintf("%d", service.RunningCount),
			portsDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printInstancesTable(instances []api.InstanceStatus) {
	header := []string{"ID", "SERVICE", "STATE", "NODE", "IP", "HEALTH"}
	var rows [][]string
	for _, instance := range instances {
		healthDisplay := instance.HealthState
		if healthDisplay == "" {
			healthDisplay = "-"
		}
		ipDisplay := instance.IPAddress
		if ipDisplay == "" {
			ipDisplay = "-"
		}
		rows = append(rows, []string{
			truncateValue(instance.ID, instanceIDDisplayWidth),
			instance.ServiceName,
			instance.State,
			instance.NodeID,
			ipDisplay,
			healthDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printNodesTable(nodes []api.NodeStatus) {
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
	printAlignedTable(header, rows)
}

func printVolumesTable(volumes []api.VolumeStatus) {
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
	printAlignedTable(header, rows)
}

func printNetworkingTable(networking []api.NetworkStatus) {
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
	printAlignedTable(header, rows)
}

func printSecretsTable(secrets []api.SecretStatus) {
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
	printAlignedTable(header, rows)
}

func printConfigTable(configEntries []api.ConfigStatus) {
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
	printAlignedTable(header, rows)
}

func printCloudIdentitiesTable(cloudIdentities []api.CloudIdentityStatus) {
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
	printAlignedTable(header, rows)
}

// executeScaleCommand sends a scale request to the API to change a service's
// desired instance count.
func executeScaleCommand(serviceName, countStr string) {
	requestBody := fmt.Sprintf(`{"service":%q,"instances":%s}`, serviceName, countStr)
	apiURL := "http://" + statusAPIListenAddress + "/api/scale"
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
	apiURL := "http://" + statusAPIListenAddress + "/api/node/drain"
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
	apiURL := "http://" + statusAPIListenAddress + "/api/node/disable"
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
	apiURL := "http://" + statusAPIListenAddress + "/api/node/enable"
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
	apiURL := fmt.Sprintf("http://%s/api/watch?prefix=%s", statusAPIListenAddress, prefix)
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
	default:
		fmt.Fprintln(os.Stderr, "usage: cca secret <set|get|list|delete> [name] [value]")
		os.Exit(1)
	}
}

// executeSecretSet stores an encrypted secret via the API.
func executeSecretSet(secretName, secretValue string) {
	requestURL := fmt.Sprintf("http://%s/api/secret?name=%s", statusAPIListenAddress, secretName)
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
	requestURL := fmt.Sprintf("http://%s/api/secret?name=%s", statusAPIListenAddress, secretName)
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
	requestURL := fmt.Sprintf("http://%s/api/secret", statusAPIListenAddress)
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
	requestURL := fmt.Sprintf("http://%s/api/secret?name=%s", statusAPIListenAddress, secretName)
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
