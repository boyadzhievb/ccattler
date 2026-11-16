// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// Instance count limits for one service (M217, Flow 31). The limit stops
// mistakes such as 100000 for 100; it is not a capacity promise.
const (
	// ServiceInstanceWarningThreshold is the count from which a scale or apply
	// is accepted with a warning.
	ServiceInstanceWarningThreshold = 500
	// ServiceInstanceApprovalLimit is the largest count accepted without an
	// approval; above it the request must repeat the count and come from
	// cluster.admin. There is no absolute ceiling.
	ServiceInstanceApprovalLimit = 600
)

// InstanceCountDecision is the outcome of checking a requested instance count.
type InstanceCountDecision string

const (
	// InstanceCountAccepted is a count below the warning threshold.
	InstanceCountAccepted InstanceCountDecision = "accepted"
	// InstanceCountAcceptedWithWarning is a count from the threshold up to the limit.
	InstanceCountAcceptedWithWarning InstanceCountDecision = "accepted_with_warning"
	// InstanceCountApproved is a count above the limit whose approval repeats
	// it; the caller must still require cluster.admin.
	InstanceCountApproved InstanceCountDecision = "approved"
	// InstanceCountNeedsApproval is a count above the limit without approval.
	InstanceCountNeedsApproval InstanceCountDecision = "needs_approval"
	// InstanceCountApprovalMismatch is a count above the limit whose approval
	// names a different count (a typo guard).
	InstanceCountApprovalMismatch InstanceCountDecision = "approval_mismatch"
)

// EvaluateServiceInstanceCount checks requestedCount against the warning
// threshold and the approval limit. approvedCount is the count the caller
// approved, 0 for none; it matters only above the limit.
func EvaluateServiceInstanceCount(requestedCount int, approvedCount int) InstanceCountDecision {
	switch {
	case requestedCount < ServiceInstanceWarningThreshold:
		return InstanceCountAccepted
	case requestedCount <= ServiceInstanceApprovalLimit:
		return InstanceCountAcceptedWithWarning
	case approvedCount == 0:
		return InstanceCountNeedsApproval
	case approvedCount != requestedCount:
		return InstanceCountApprovalMismatch
	default:
		return InstanceCountApproved
	}
}

// RefusesWithoutWrite reports whether the decision refuses the request.
func (decision InstanceCountDecision) RefusesWithoutWrite() bool {
	return decision == InstanceCountNeedsApproval || decision == InstanceCountApprovalMismatch
}

// Warns reports whether an accepted request carries a warning.
func (decision InstanceCountDecision) Warns() bool {
	return decision == InstanceCountAcceptedWithWarning || decision == InstanceCountApproved
}

// DescribeInstanceCountDecision returns the text shown to the user for a
// warning or refusal of requestedCount for serviceName; "" when accepted
// without remark. approvalHint names how to approve on this path.
func DescribeInstanceCountDecision(decision InstanceCountDecision, serviceName string, requestedCount int, approvalHint string) string {
	switch decision {
	case InstanceCountAcceptedWithWarning, InstanceCountApproved:
		return fmt.Sprintf("%d instances for %s is at or above the warning threshold of %d; "+
			"this is a limit against mistakes, not a capacity promise", requestedCount, serviceName, ServiceInstanceWarningThreshold)
	case InstanceCountNeedsApproval:
		return fmt.Sprintf("%d instances for %s is above the approval limit of %d; to approve, %s %d as cluster.admin",
			requestedCount, serviceName, ServiceInstanceApprovalLimit, approvalHint, requestedCount)
	case InstanceCountApprovalMismatch:
		return fmt.Sprintf("approval does not match: %s requests %d instances; the approval must repeat that exact count",
			serviceName, requestedCount)
	default:
		return ""
	}
}
