// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package chaos

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// RecoveryReport captures the full results of a chaos benchmark run, including
// deployment timing, per-injection recovery metrics, and aggregate statistics.
type RecoveryReport struct {
	// NodeCount is the number of simulated nodes in the cluster.
	NodeCount int `json:"node_count"`
	// ServiceCount is the number of deployed services.
	ServiceCount int `json:"service_count"`
	// TotalInstances is the total desired instance count across all services.
	TotalInstances int `json:"total_instances"`
	// DeployConvergenceTime is how long initial deployment took to converge.
	DeployConvergenceTime time.Duration `json:"deploy_convergence_time_ns"`
	// ChaosEvents records every injection and its recovery outcome.
	ChaosEvents []ChaosEvent `json:"chaos_events"`
	// Metrics holds aggregate recovery statistics computed from ChaosEvents.
	Metrics RecoveryMetrics `json:"metrics"`
	// FinalConverged indicates whether the cluster was converged at test end.
	FinalConverged bool `json:"final_converged"`
	// FinalStatus describes the convergence state at test end.
	FinalStatus string `json:"final_status"`
}

// RecoveryMetrics holds aggregate statistics computed from chaos event outcomes.
type RecoveryMetrics struct {
	// TotalInjections is the number of chaos events injected.
	TotalInjections int `json:"total_injections"`
	// SuccessfulRecoveries is the number of injections the system recovered from.
	SuccessfulRecoveries int `json:"successful_recoveries"`
	// FailedRecoveries is the number of injections the system did not recover from.
	FailedRecoveries int `json:"failed_recoveries"`
	// RecoverySuccessRate is SuccessfulRecoveries / TotalInjections as a percentage.
	RecoverySuccessRate float64 `json:"recovery_success_rate_percent"`
	// MeanConvergenceTime is the average convergence time across successful recoveries.
	MeanConvergenceTime time.Duration `json:"mean_convergence_time_ns"`
	// P50ConvergenceTime is the median convergence time.
	P50ConvergenceTime time.Duration `json:"p50_convergence_time_ns"`
	// P95ConvergenceTime is the 95th percentile convergence time.
	P95ConvergenceTime time.Duration `json:"p95_convergence_time_ns"`
	// P99ConvergenceTime is the 99th percentile convergence time.
	P99ConvergenceTime time.Duration `json:"p99_convergence_time_ns"`
	// MaxConvergenceTime is the slowest successful convergence.
	MaxConvergenceTime time.Duration `json:"max_convergence_time_ns"`
}

// ComputeRecoveryMetrics calculates aggregate statistics from a slice of chaos
// events. Only events where Converged is true contribute to percentile calculations.
func ComputeRecoveryMetrics(events []ChaosEvent) RecoveryMetrics {
	metrics := RecoveryMetrics{
		TotalInjections: len(events),
	}

	var successfulDurations []time.Duration
	for _, event := range events {
		if event.Converged {
			metrics.SuccessfulRecoveries++
			successfulDurations = append(successfulDurations, event.ConvergenceTime)
		} else {
			metrics.FailedRecoveries++
		}
	}

	if metrics.TotalInjections > 0 {
		metrics.RecoverySuccessRate = float64(metrics.SuccessfulRecoveries) / float64(metrics.TotalInjections) * 100
	}

	if len(successfulDurations) == 0 {
		return metrics
	}

	sort.Slice(successfulDurations, func(i, j int) bool {
		return successfulDurations[i] < successfulDurations[j]
	})

	var totalNanoseconds int64
	for _, duration := range successfulDurations {
		totalNanoseconds += duration.Nanoseconds()
	}
	metrics.MeanConvergenceTime = time.Duration(totalNanoseconds / int64(len(successfulDurations)))

	metrics.P50ConvergenceTime = percentileDuration(successfulDurations, 50)
	metrics.P95ConvergenceTime = percentileDuration(successfulDurations, 95)
	metrics.P99ConvergenceTime = percentileDuration(successfulDurations, 99)
	metrics.MaxConvergenceTime = successfulDurations[len(successfulDurations)-1]

	return metrics
}

// percentileDuration returns the pth percentile from a sorted slice of durations.
func percentileDuration(sortedDurations []time.Duration, percentile float64) time.Duration {
	if len(sortedDurations) == 0 {
		return 0
	}
	index := int(math.Ceil(float64(len(sortedDurations))*percentile/100)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sortedDurations) {
		index = len(sortedDurations) - 1
	}
	return sortedDurations[index]
}

// ToJSON serializes the report as indented JSON.
func (recoveryReport *RecoveryReport) ToJSON() ([]byte, error) {
	return json.MarshalIndent(recoveryReport, "", "  ")
}
