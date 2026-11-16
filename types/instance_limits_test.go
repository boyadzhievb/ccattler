// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import (
	"strings"
	"testing"
)

// TestEvaluateServiceInstanceCount pins the boundaries of the M217 limit rule.
func TestEvaluateServiceInstanceCount(t *testing.T) {
	testCases := []struct {
		requestedCount int                   // requestedCount is the count asked for.
		approvedCount  int                   // approvedCount is the approval sent, 0 for none.
		wantDecision   InstanceCountDecision // wantDecision is the expected outcome.
	}{
		{0, 0, InstanceCountAccepted},
		{ServiceInstanceWarningThreshold - 1, 0, InstanceCountAccepted},
		{ServiceInstanceWarningThreshold, 0, InstanceCountAcceptedWithWarning},
		{ServiceInstanceApprovalLimit, 0, InstanceCountAcceptedWithWarning},
		{ServiceInstanceApprovalLimit, ServiceInstanceApprovalLimit - 1, InstanceCountAcceptedWithWarning},
		{ServiceInstanceApprovalLimit + 1, 0, InstanceCountNeedsApproval},
		{ServiceInstanceApprovalLimit + 1, ServiceInstanceApprovalLimit + 1, InstanceCountApproved},
		{100000, 10000, InstanceCountApprovalMismatch},
		{100000, 100000, InstanceCountApproved},
	}
	for _, testCase := range testCases {
		gotDecision := EvaluateServiceInstanceCount(testCase.requestedCount, testCase.approvedCount)
		if gotDecision != testCase.wantDecision {
			t.Errorf("EvaluateServiceInstanceCount(%d, %d) = %s, want %s",
				testCase.requestedCount, testCase.approvedCount, gotDecision, testCase.wantDecision)
		}
	}
}

// TestInstanceCountDecisionText checks that refusals say how to approve and
// that warnings name the threshold.
func TestInstanceCountDecisionText(t *testing.T) {
	refusalText := DescribeInstanceCountDecision(InstanceCountNeedsApproval, "web", 700, "resend with approved_instances")
	for _, wantFragment := range []string{"web", "700", "600", "approved_instances 700", "cluster.admin"} {
		if !strings.Contains(refusalText, wantFragment) {
			t.Errorf("refusal %q does not mention %q", refusalText, wantFragment)
		}
	}
	warningText := DescribeInstanceCountDecision(InstanceCountApproved, "web", 100000, "")
	if !strings.Contains(warningText, "500") || !strings.Contains(warningText, "not a capacity promise") {
		t.Errorf("warning %q does not name the threshold and the capacity caveat", warningText)
	}
	if acceptedText := DescribeInstanceCountDecision(InstanceCountAccepted, "web", 3, ""); acceptedText != "" {
		t.Errorf("accepted count has text %q", acceptedText)
	}
	if !InstanceCountNeedsApproval.RefusesWithoutWrite() || !InstanceCountApprovalMismatch.RefusesWithoutWrite() ||
		InstanceCountApproved.RefusesWithoutWrite() || !InstanceCountApproved.Warns() || InstanceCountAccepted.Warns() {
		t.Error("decision predicates disagree with the rule")
	}
}
