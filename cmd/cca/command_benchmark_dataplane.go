// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// command_benchmark_dataplane.go implements `cca benchmark dataplane` — a
// microbenchmark comparing nftables ruleset generation vs iptables command
// count at different service scales.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/network"
)

const (
	// dataplaneBenchmarkDefaultBackends is the number of backends per service.
	dataplaneBenchmarkDefaultBackends = 3
	// dataplaneBenchmarkDefaultIterations is the number of generation iterations.
	dataplaneBenchmarkDefaultIterations = 20
)

// dataplaneBenchmarkDefaultServiceCounts is the set of service counts to test.
var dataplaneBenchmarkDefaultServiceCounts = []int{10, 100, 500, 1000}

// dataplaneBenchmarkConfig holds parsed options for the dataplane benchmark.
type dataplaneBenchmarkConfig struct {
	// jsonOutput enables structured JSON output.
	jsonOutput bool
	// serviceCounts is the list of service counts to benchmark.
	serviceCounts []int
	// backendsPerService is the number of backends per service.
	backendsPerService int
	// iterations is the number of repeated generation runs per scale point.
	iterations int
}

// dataplaneBenchmarkScaleResult holds benchmark results for one scale point.
type dataplaneBenchmarkScaleResult struct {
	// ServiceCount is the number of services at this scale.
	ServiceCount int `json:"service_count"`
	// BackendsPerService is the number of backends each service has.
	BackendsPerService int `json:"backends_per_service"`
	// NftablesGenerationP50 is the median nftables ruleset generation time.
	NftablesGenerationP50 time.Duration `json:"nftables_generation_p50_ns"`
	// NftablesGenerationP99 is the 99th percentile generation time.
	NftablesGenerationP99 time.Duration `json:"nftables_generation_p99_ns"`
	// NftablesRulesetBytes is the size of the generated nftables ruleset.
	NftablesRulesetBytes uint64 `json:"nftables_ruleset_bytes"`
	// NftablesChainCount is the number of per-service chains.
	NftablesChainCount int `json:"nftables_chain_count"`
	// IptablesCommandCount is the number of sequential iptables exec calls.
	IptablesCommandCount int `json:"iptables_command_count"`
	// HeapAllocBytes is the heap memory used during generation.
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
}

// dataplaneBenchmarkReport holds the complete benchmark output.
type dataplaneBenchmarkReport struct {
	// Iterations is the number of iterations per scale point.
	Iterations int `json:"iterations"`
	// Results is one entry per service count.
	Results []dataplaneBenchmarkScaleResult `json:"results"`
}

// parseDataplaneBenchmarkArgs parses flags for `cca benchmark dataplane`.
func parseDataplaneBenchmarkArgs(args []string) dataplaneBenchmarkConfig {
	config := dataplaneBenchmarkConfig{
		serviceCounts:      dataplaneBenchmarkDefaultServiceCounts,
		backendsPerService: dataplaneBenchmarkDefaultBackends,
		iterations:         dataplaneBenchmarkDefaultIterations,
	}

	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--json":
			config.jsonOutput = true
		case "--backends":
			if index+1 < len(args) {
				index++
				parsedValue, parseError := strconv.Atoi(args[index])
				if parseError != nil || parsedValue < 1 {
					fmt.Fprintf(os.Stderr, "invalid --backends value %q: must be a positive integer\n", args[index])
					os.Exit(1)
				}
				config.backendsPerService = parsedValue
			}
		case "--iterations":
			if index+1 < len(args) {
				index++
				parsedValue, parseError := strconv.Atoi(args[index])
				if parseError != nil || parsedValue < 1 {
					fmt.Fprintf(os.Stderr, "invalid --iterations value %q: must be a positive integer\n", args[index])
					os.Exit(1)
				}
				config.iterations = parsedValue
			}
		case "--services":
			if index+1 < len(args) {
				index++
				config.serviceCounts = parseServiceCountList(args[index])
			}
		}
	}

	return config
}

// parseServiceCountList parses a comma-separated list of service counts.
func parseServiceCountList(serviceCountInput string) []int {
	parts := strings.Split(serviceCountInput, ",")
	serviceCounts := make([]int, 0, len(parts))
	for _, part := range parts {
		parsedValue, parseError := strconv.Atoi(strings.TrimSpace(part))
		if parseError != nil || parsedValue < 1 {
			fmt.Fprintf(os.Stderr, "invalid service count %q in --services\n", part)
			os.Exit(1)
		}
		serviceCounts = append(serviceCounts, parsedValue)
	}
	sort.Ints(serviceCounts)
	return serviceCounts
}

// executeDataplaneBenchmarkCommand runs the dataplane benchmark and prints
// results as a human-readable table or JSON.
func executeDataplaneBenchmarkCommand(config dataplaneBenchmarkConfig) {
	if !config.jsonOutput {
		fmt.Println("=== CCattler Data Plane Benchmark ===")
		fmt.Printf("Backends per service: %d | Iterations: %d\n\n", config.backendsPerService, config.iterations)
	}

	report := dataplaneBenchmarkReport{
		Iterations: config.iterations,
		Results:    make([]dataplaneBenchmarkScaleResult, 0, len(config.serviceCounts)),
	}

	for _, serviceCount := range config.serviceCounts {
		result := benchmarkSingleScale(serviceCount, config.backendsPerService, config.iterations)
		report.Results = append(report.Results, result)

		if !config.jsonOutput {
			printDataplaneScaleResult(result)
		}
	}

	if config.jsonOutput {
		outputDataplaneBenchmarkJSON(report)
	} else {
		printDataplaneSummary(report)
	}
}

// benchmarkSingleScale runs the nftables generation benchmark at one scale
// point and collects timing, size, and memory statistics.
func benchmarkSingleScale(serviceCount int, backendsPerService int, iterations int) dataplaneBenchmarkScaleResult {
	serviceConfigs := network.GenerateSyntheticServiceConfigs(serviceCount, backendsPerService)
	nftablesDataPlane := network.NewNftablesDataPlane()

	durations := make([]time.Duration, iterations)
	var lastRuleset string

	// Warm up
	_ = nftablesDataPlane.GenerateVIPRuleset(serviceConfigs)

	runtime.GC()
	var memStatsBefore runtime.MemStats
	runtime.ReadMemStats(&memStatsBefore)

	for iteration := range durations {
		startTime := time.Now()
		lastRuleset = nftablesDataPlane.GenerateVIPRuleset(serviceConfigs)
		durations[iteration] = time.Since(startTime)
	}

	var memStatsAfter runtime.MemStats
	runtime.ReadMemStats(&memStatsAfter)

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	chainCount := strings.Count(lastRuleset, "\n    chain svc_")
	iptablesCommandCount := network.CountIptablesVIPCommands(serviceConfigs)

	return dataplaneBenchmarkScaleResult{
		ServiceCount:          serviceCount,
		BackendsPerService:    backendsPerService,
		NftablesGenerationP50: durations[len(durations)/2],
		NftablesGenerationP99: durations[len(durations)*99/100],
		NftablesRulesetBytes:  uint64(len(lastRuleset)), //nolint:gosec // len is always non-negative
		NftablesChainCount:    chainCount,
		IptablesCommandCount:  iptablesCommandCount,
		HeapAllocBytes:        memStatsAfter.HeapAlloc - memStatsBefore.HeapAlloc,
	}
}

// printDataplaneScaleResult prints one scale point result as a table row.
func printDataplaneScaleResult(result dataplaneBenchmarkScaleResult) {
	fmt.Printf("--- %d services (%d backends each) ---\n", result.ServiceCount, result.BackendsPerService)
	fmt.Printf("  nftables generation p50: %v\n", result.NftablesGenerationP50.Round(time.Microsecond))
	fmt.Printf("  nftables generation p99: %v\n", result.NftablesGenerationP99.Round(time.Microsecond))
	fmt.Printf("  nftables ruleset size:   %s (%d chains)\n",
		formatBytesUint64(result.NftablesRulesetBytes), result.NftablesChainCount)
	fmt.Printf("  iptables commands:       %d (sequential exec calls)\n", result.IptablesCommandCount)
	fmt.Printf("  heap allocated:          %s\n\n", formatBytesUint64(result.HeapAllocBytes))
}

// printDataplaneSummary prints the final comparison summary.
func printDataplaneSummary(report dataplaneBenchmarkReport) {
	if len(report.Results) < 2 {
		return
	}

	first := report.Results[0]
	last := report.Results[len(report.Results)-1]

	scaleRatio := float64(last.ServiceCount) / float64(first.ServiceCount)
	nftablesTimeRatio := float64(last.NftablesGenerationP50) / float64(first.NftablesGenerationP50)
	iptablesCommandRatio := float64(last.IptablesCommandCount) / float64(first.IptablesCommandCount)

	fmt.Printf("=== Summary (%dx service scale) ===\n", int(scaleRatio))
	fmt.Printf("  nftables generation time: %.1fx (ruleset rebuild is O(N) but atomic — 1 nft call)\n", nftablesTimeRatio)
	fmt.Printf("  iptables command count:   %.1fx (each command is a separate exec)\n", iptablesCommandRatio)
	fmt.Printf("  nftables lookup:          O(1) verdict map hash\n")
	fmt.Printf("  iptables lookup:          O(N) linear chain walk\n")
}

// outputDataplaneBenchmarkJSON writes the report as JSON to stdout.
func outputDataplaneBenchmarkJSON(report dataplaneBenchmarkReport) {
	jsonBytes, marshalError := json.MarshalIndent(report, "", "  ")
	if marshalError != nil {
		fmt.Fprintf(os.Stderr, "json marshal error: %v\n", marshalError)
		os.Exit(1)
	}
	fmt.Println(string(jsonBytes))
}

// formatBytesUint64 formats a byte count as a human-readable string.
func formatBytesUint64(byteCount uint64) string {
	switch {
	case byteCount >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(byteCount)/(1024*1024))
	case byteCount >= 1024:
		return fmt.Sprintf("%.1f KB", float64(byteCount)/1024)
	default:
		return fmt.Sprintf("%d B", byteCount)
	}
}
