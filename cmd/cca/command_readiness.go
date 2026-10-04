// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// command_readiness.go implements `cca readiness` — a production readiness
// validation command that runs the project's Go test suite selectively across
// 16 areas and prints a structured report showing pass/fail status, test
// counts, and durations for each area.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// readinessTestTimeoutDuration is the maximum time allowed for each go test invocation.
	readinessTestTimeoutDuration = "60s"
	// readinessTestCountFlag ensures tests run exactly once with no caching.
	readinessTestCountFlag = "1"
	// readinessStatusPass indicates all tests in an area passed.
	readinessStatusPass = "PASS"
	// readinessStatusFail indicates one or more tests in an area failed.
	readinessStatusFail = "FAIL"
	// readinessStatusSkip indicates an area was skipped because no tests matched.
	readinessStatusSkip = "SKIP"
	// readinessPassLinePrefix is the prefix Go test verbose output uses for passing tests.
	readinessPassLinePrefix = "--- PASS:"
	// readinessFailLinePrefix is the prefix Go test verbose output uses for failing tests.
	readinessFailLinePrefix = "--- FAIL:"
	// readinessAreaNameColumnWidth is the fixed column width for area names in terminal output.
	readinessAreaNameColumnWidth = 18
	// readinessReportSeparator is the visual separator line under the report title.
	readinessReportSeparator = "====================================="
	// readinessModuleFileName is the file that must exist in the project root.
	readinessModuleFileName = "go.mod"
)

// readinessTestTarget defines a single Go test invocation: a package path and
// a regex pattern for the -run flag. Each target becomes one `go test` call.
type readinessTestTarget struct {
	// testPackage is the Go package path to test (e.g., "./scheduler/...").
	testPackage string
	// testPattern is the regex pattern passed to the -run flag to select tests.
	testPattern string
}

// readinessArea describes one production readiness validation area, including
// its test targets and the results populated after execution.
type readinessArea struct {
	// name is the short identifier for this area (e.g., "scheduling").
	name string
	// description is a human-readable summary of what this area validates.
	description string
	// testTargets is the list of Go test invocations that cover this area.
	testTargets []readinessTestTarget
	// status is the result after running: PASS, FAIL, or SKIP.
	status string
	// duration is the total wall-clock time spent running this area's tests.
	duration time.Duration
	// passedTestCount is the number of individual tests that passed.
	passedTestCount int
	// failedTestCount is the number of individual tests that failed.
	failedTestCount int
	// gapDescription notes any known gaps or issues for this area.
	gapDescription string
}

// readinessConfig holds parsed command-line options for the readiness command.
type readinessConfig struct {
	// jsonOutput enables JSON array output instead of the human-readable table.
	jsonOutput bool
}

// readinessAreaJSONResult is the JSON-serializable representation of a single
// area's results, used when --json output is requested.
type readinessAreaJSONResult struct {
	// Name is the area identifier.
	Name string `json:"name"`
	// Description summarizes what this area validates.
	Description string `json:"description"`
	// Status is PASS, FAIL, or SKIP.
	Status string `json:"status"`
	// TestsPassed is the number of individual tests that passed.
	TestsPassed int `json:"tests_passed"`
	// TestsFailed is the number of individual tests that failed.
	TestsFailed int `json:"tests_failed"`
	// DurationMilliseconds is the total wall-clock time in milliseconds.
	DurationMilliseconds int64 `json:"duration_ms"`
	// Gap describes known coverage gaps, if any.
	Gap string `json:"gap,omitempty"`
}

// readinessAreaDefinitions returns all 16 production readiness areas with their
// test targets mapped to existing packages and test function patterns.
func readinessAreaDefinitions() []readinessArea {
	return append(readinessCoreAreaDefinitions(), readinessExtendedAreaDefinitions()...)
}

// readinessCoreAreaDefinitions returns the first 8 readiness areas: scheduling,
// control-plane, network, nodes, storage, upgrades, reconciliation, and security.
func readinessCoreAreaDefinitions() []readinessArea {
	return []readinessArea{
		{
			name:        "scheduling",
			description: "Scheduler placement, spread, resource fit, co-scheduling",
			testTargets: []readinessTestTarget{
				{testPackage: "./scheduler/...", testPattern: "Test"},
			},
		},
		{
			name:        "control-plane",
			description: "HA restart recovery, leader election, rolling upgrade",
			testTargets: []readinessTestTarget{
				{testPackage: "./integration/...",
					testPattern: "TestControlPlaneRestart|TestRollingControlPlaneUpgrade|TestLeaderElection"},
			},
		},
		{
			name:        "network",
			description: "IP allocation, VIPs, DNS, load balancing, network policies",
			testTargets: []readinessTestTarget{
				{testPackage: "./network/...", testPattern: "Test"},
				{testPackage: "./controllers/...", testPattern: "TestNetworkController|TestNftables"},
				{testPackage: "./integration/...",
					testPattern: "TestInstancesGetUnique|TestEndpointsReflect|TestServiceGetsVIP|" +
						"TestDNSResolves|TestLoadBalancing|TestNodeFailureUpdatesNet|" +
						"TestScaleUpAdds|TestServiceReachable|TestMultipleServicesIndependent|" +
						"TestInstanceIPReleased|TestNetworkPolicy"},
			},
		},
		{
			name:        "nodes",
			description: "Node failure handling, etcd unavailability, controller isolation",
			testTargets: []readinessTestTarget{
				{testPackage: "./controllers/...", testPattern: "TestNodeFailure|TestNodeLifecycle"},
				{testPackage: "./integration/...",
					testPattern: "TestEtcdUnavailable|TestControllerIsolation|TestConcurrentController"},
			},
		},
		{
			name:        "storage",
			description: "Volume create, attach, detach, force-detach, node failure survival",
			testTargets: []readinessTestTarget{
				{testPackage: "./storage/...", testPattern: "Test"},
				{testPackage: "./integration/...", testPattern: "TestVolume|TestServiceWithoutVolume"},
			},
		},
		{
			name:        "upgrades",
			description: "Rolling updates, health-based rollback",
			testTargets: []readinessTestTarget{
				{testPackage: "./integration/...", testPattern: "TestRollingUpdate|TestRollback"},
			},
		},
		{
			name:        "reconciliation",
			description: "Instance controller, endpoint controller, invariants, drain, init, stateful",
			testTargets: []readinessTestTarget{
				{testPackage: "./controllers/...",
					testPattern: "TestScaleUp|TestAlreadySatisfied|TestScaleDown|" +
						"TestStoppedInstances|TestMultipleServices|TestDesiredZero|" +
						"TestEndpoint|TestStaleEndpoint|TestDrain|TestInit|" +
						"TestClassify|TestStateful|TestWarmZero|TestMetrics"},
				{testPackage: "./integration/...",
					testPattern: "TestDesiredToPlaced|TestScaleUpPlaces|TestNoPlacement|" +
						"TestInvariant|TestStateful|TestServiceGroup"},
			},
		},
		{
			name:        "security",
			description: "RBAC, OIDC, mTLS, certificates, ABAC conditions, auth controller",
			testTargets: []readinessTestTarget{
				{testPackage: "./security/...", testPattern: "Test"},
				{testPackage: "./controllers/...", testPattern: "TestAuthController"},
				{testPackage: "./integration/...",
					testPattern: "TestCompromised|TestSecretNeverInPlaintext|" +
						"TestCertificateRotation|TestTenantIsolation|TestAuthDSL"},
			},
		},
	}
}

// readinessExtendedAreaDefinitions returns the last 8 readiness areas:
// multi-tenancy, autoscaling, topology, runtime, observability, recovery, api,
// and ecosystem.
func readinessExtendedAreaDefinitions() []readinessArea {
	return []readinessArea{
		{
			name:        "multi-tenancy",
			description: "Tenant lifecycle, quotas, policy gates, garbage collection, visibility",
			testTargets: []readinessTestTarget{
				{testPackage: "./tenant/...", testPattern: "Test"},
				{testPackage: "./integration/...", testPattern: "TestTenantVisibility|TestTenantAudit"},
			},
		},
		{
			name:        "autoscaling",
			description: "Horizontal, vertical, event-driven, scheduled, stabilization",
			testTargets: []readinessTestTarget{
				{testPackage: "./controllers/...", testPattern: "TestAutoscale|TestComputeP95|TestVertical"},
				{testPackage: "./integration/...",
					testPattern: "TestStabilizationWindow|TestEventDrivenScaling|" +
						"TestScheduledScaling|TestVerticalAutoscaling|" +
						"TestQuotaAwareScaling|TestClusterAutoscale"},
			},
		},
		{
			name:        "topology",
			description: "Zone spread, placement constraints, architecture filtering",
			testTargets: []readinessTestTarget{
				{testPackage: "./scheduler/...",
					testPattern: "TestZoneSpread|TestRequireLabel|TestMultipleRequire|" +
						"TestPrefer|TestRestrict|TestAccept|TestRequireAnd|TestArchitecture"},
				{testPackage: "./integration/...", testPattern: "TestPlacementConstraint"},
			},
		},
		{
			name:        "runtime",
			description: "Simulator, process, and container runtime conformance",
			testTargets: []readinessTestTarget{
				{testPackage: "./runtime/...", testPattern: "Test"},
				{testPackage: "./integration/...", testPattern: "TestContainerRuntime|TestContainerGets"},
			},
		},
		{
			name:        "observability",
			description: "Metrics, structured logging, distributed tracing",
			testTargets: []readinessTestTarget{
				{testPackage: "./metrics/...", testPattern: "Test"},
				{testPackage: "./logging/...", testPattern: "Test"},
				{testPackage: "./tracing/...", testPattern: "Test"},
				{testPackage: "./integration/...", testPattern: "TestObservability"},
			},
		},
		{
			name:        "recovery",
			description: "Disaster recovery, split brain healing, network partition, storage failure",
			testTargets: []readinessTestTarget{
				{testPackage: "./integration/...",
					testPattern: "TestDisasterRecovery|TestSplitBrain|TestNetworkPartition|TestStorageFailure"},
			},
		},
		{
			name:        "api",
			description: "API server endpoints, watch SSE, rate limiting, response cache",
			testTargets: []readinessTestTarget{
				{testPackage: "./api/...", testPattern: "Test"},
				{testPackage: "./integration/...", testPattern: "TestAPIVersioning|TestAPIStability"},
			},
		},
		{
			name:        "ecosystem",
			description: "DSL parser, lexer, compiler, templates, examples, controller SDK",
			testTargets: []readinessTestTarget{
				{testPackage: "./lang/...", testPattern: "Test"},
				{testPackage: "./integration/...",
					testPattern: "TestEcosystem|TestAllExamples|TestTemplate|TestDSLPhase10"},
			},
		},
	}
}

// parseReadinessCommandArgs parses command-line arguments for the readiness
// command. Currently supports the --json flag for JSON output.
func parseReadinessCommandArgs(arguments []string) readinessConfig {
	parsedConfig := readinessConfig{}
	for _, argument := range arguments {
		if argument == "--json" {
			parsedConfig.jsonOutput = true
		}
	}
	return parsedConfig
}

// executeReadinessCommand runs all 16 readiness areas sequentially, executing
// their Go test targets and collecting results, then prints either a
// human-readable table or JSON report depending on the configuration.
func executeReadinessCommand(parsedConfig readinessConfig) {
	rootVerificationError := verifyProjectRootDirectory()
	if rootVerificationError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", rootVerificationError)
		fmt.Fprintln(os.Stderr, "hint: run `cca readiness` from the CCattler project root directory")
		os.Exit(1)
	}

	allReadinessAreas := readinessAreaDefinitions()

	if !parsedConfig.jsonOutput {
		fmt.Fprintf(os.Stderr, "Running production readiness tests across %d areas...\n", len(allReadinessAreas))
	}

	for areaIndex := range allReadinessAreas {
		if !parsedConfig.jsonOutput {
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s...\n",
				areaIndex+1, len(allReadinessAreas), allReadinessAreas[areaIndex].name)
		}
		runReadinessAreaTests(&allReadinessAreas[areaIndex])
	}

	if parsedConfig.jsonOutput {
		printReadinessJSONReport(allReadinessAreas)
	} else {
		fmt.Fprintln(os.Stderr, "")
		printReadinessTerminalReport(allReadinessAreas)
	}
}

// runReadinessAreaTests executes all test targets for a single readiness area,
// aggregating pass/fail counts and total duration into the area struct.
func runReadinessAreaTests(area *readinessArea) {
	totalPassedCount := 0
	totalFailedCount := 0
	totalElapsedDuration := time.Duration(0)
	allTargetsSucceeded := true

	for _, testTarget := range area.testTargets {
		passedCount, failedCount, targetDuration, targetSucceeded := executeGoTestForTarget(testTarget)
		totalPassedCount += passedCount
		totalFailedCount += failedCount
		totalElapsedDuration += targetDuration
		if !targetSucceeded {
			allTargetsSucceeded = false
		}
	}

	area.passedTestCount = totalPassedCount
	area.failedTestCount = totalFailedCount
	area.duration = totalElapsedDuration

	switch {
	case !allTargetsSucceeded || totalFailedCount > 0:
		area.status = readinessStatusFail
	case totalPassedCount == 0:
		area.status = readinessStatusSkip
		area.gapDescription = "no tests matched"
	default:
		area.status = readinessStatusPass
	}
}

// executeGoTestForTarget runs a single `go test -v -run <pattern> <package>
// -count=1 -timeout 60s` invocation and returns the count of passed tests,
// failed tests, elapsed duration, and whether the command exited successfully.
func executeGoTestForTarget(target readinessTestTarget) (int, int, time.Duration, bool) {
	goTestArguments := []string{
		"test", "-v",
		"-run", target.testPattern,
		"-count", readinessTestCountFlag,
		"-timeout", readinessTestTimeoutDuration,
		target.testPackage,
	}

	goTestCommand := exec.Command("go", goTestArguments...) //nolint:gosec // arguments are hardcoded test targets, not user input
	goTestCommand.Dir, _ = os.Getwd()

	invocationStartTime := time.Now()
	combinedTestOutput, commandRunError := goTestCommand.CombinedOutput()
	invocationElapsedDuration := time.Since(invocationStartTime)

	passedCount, failedCount := countTestResultsFromOutput(combinedTestOutput)
	commandExitedCleanly := commandRunError == nil

	return passedCount, failedCount, invocationElapsedDuration, commandExitedCleanly
}

// countTestResultsFromOutput scans Go test verbose output line by line and
// counts occurrences of "--- PASS:" and "--- FAIL:" prefixes to determine
// how many individual tests passed and failed.
func countTestResultsFromOutput(testOutput []byte) (int, int) {
	passedCount := 0
	failedCount := 0

	outputLineScanner := bufio.NewScanner(bytes.NewReader(testOutput))
	for outputLineScanner.Scan() {
		trimmedOutputLine := strings.TrimSpace(outputLineScanner.Text())
		if strings.HasPrefix(trimmedOutputLine, readinessPassLinePrefix) {
			passedCount++
		} else if strings.HasPrefix(trimmedOutputLine, readinessFailLinePrefix) {
			failedCount++
		}
	}

	return passedCount, failedCount
}

// printReadinessTerminalReport prints the human-readable readiness report table
// to stdout, including a header row, one row per area, and a summary line
// showing how many areas passed out of the total.
func printReadinessTerminalReport(allAreas []readinessArea) {
	fmt.Println("CCattler Production Readiness Report")
	fmt.Println(readinessReportSeparator)
	fmt.Println()
	fmt.Printf("%-*s  %-6s  %5s  %9s  %s\n",
		readinessAreaNameColumnWidth, "Area", "Status", "Tests", "Duration", "Notes")

	passedAreaCount := 0
	for _, area := range allAreas {
		totalTestCount := area.passedTestCount + area.failedTestCount
		formattedDuration := formatReadinessDuration(area.duration)

		fmt.Printf("%-*s  %-6s  %5d  %9s  %s\n",
			readinessAreaNameColumnWidth, area.name,
			area.status, totalTestCount, formattedDuration, area.gapDescription)

		if area.status == readinessStatusPass {
			passedAreaCount++
		}
	}

	fmt.Println()
	fmt.Printf("Result: %d/%d areas PASS\n", passedAreaCount, len(allAreas))
}

// formatReadinessDuration formats a time.Duration for the readiness report,
// displaying seconds with one decimal place.
func formatReadinessDuration(elapsedDuration time.Duration) string {
	return fmt.Sprintf("%.1fs", elapsedDuration.Seconds())
}

// printReadinessJSONReport marshals all area results as a JSON array to stdout.
// Each area is represented as a readinessAreaJSONResult object.
func printReadinessJSONReport(allAreas []readinessArea) {
	jsonResultEntries := make([]readinessAreaJSONResult, len(allAreas))
	for areaIndex, area := range allAreas {
		jsonResultEntries[areaIndex] = readinessAreaJSONResult{
			Name:                 area.name,
			Description:          area.description,
			Status:               area.status,
			TestsPassed:          area.passedTestCount,
			TestsFailed:          area.failedTestCount,
			DurationMilliseconds: area.duration.Milliseconds(),
			Gap:                  area.gapDescription,
		}
	}

	encodedJSONBytes, marshalError := json.MarshalIndent(jsonResultEntries, "", "  ")
	if marshalError != nil {
		fmt.Fprintf(os.Stderr, "error marshaling readiness report to JSON: %v\n", marshalError)
		os.Exit(1)
	}
	fmt.Println(string(encodedJSONBytes))
}

// verifyProjectRootDirectory checks that the current working directory contains
// a go.mod file, indicating we are in a Go project root where tests can be run.
func verifyProjectRootDirectory() error {
	_, statError := os.Stat(readinessModuleFileName)
	if os.IsNotExist(statError) {
		return fmt.Errorf("no %s found in current directory — not a Go project root", readinessModuleFileName)
	}
	return statError
}
