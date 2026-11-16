// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Instance count approval for cca scale and cca apply (M217, Flow 31): the
// server decides; the CLI only supplies the approval and prints warnings.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/types"
)

// approveAboveLimitFlag approves a count above the limit by repeating it.
const approveAboveLimitFlag = "--approve-above-limit"

// simulatedApprovalHint tells a cca apply --simulate user how to approve.
const simulatedApprovalHint = "rerun cca apply --simulate with " + approveAboveLimitFlag

// parseApprovedInstanceCount reads the value of --approve-above-limit; it
// must be a positive count, otherwise the command exits.
func parseApprovedInstanceCount(flagValue string) int {
	approvedCount, parseError := strconv.Atoi(flagValue)
	if parseError != nil || approvedCount <= 0 {
		fmt.Fprintf(os.Stderr, "error: %s %q: want the instance count you approve, e.g. %s 700\n", approveAboveLimitFlag, flagValue, approveAboveLimitFlag)
		os.Exit(1)
	}
	return approvedCount
}

// printInstanceCountWarnings prints the server's instance count warnings to stderr.
func printInstanceCountWarnings(warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
}

// checkSimulatedInstanceCounts applies the limit rule to one rendered file
// for cca apply --simulate, which never reaches a server: no capability check,
// warnings printed, a count above the limit refused unless approved.
// Parse and compile errors are left to the apply step, which reports them.
func checkSimulatedInstanceCounts(rendered renderedDSLContent, approvedCount int) error {
	parsedFile, parseError := lang.Parse(rendered.content)
	if parseError != nil {
		return nil
	}
	parsedFile.BaseDir = filepath.Dir(rendered.filePath)
	facts, compileError := lang.Compile(parsedFile)
	if compileError != nil {
		return nil
	}
	for _, serviceCount := range lang.ServiceInstanceCountsFromFacts(facts) {
		decision := types.EvaluateServiceInstanceCount(serviceCount.InstanceCount, approvedCount)
		decisionText := types.DescribeInstanceCountDecision(decision, serviceCount.ServiceName, serviceCount.InstanceCount, simulatedApprovalHint)
		if decision.RefusesWithoutWrite() {
			return errors.New(decisionText)
		}
		if decision.Warns() {
			printInstanceCountWarnings([]string{decisionText})
		}
	}
	return nil
}
