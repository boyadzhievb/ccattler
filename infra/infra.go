// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Package infra defines the InfrastructureProvider interface for cluster
// autoscaling. An InfrastructureProvider can provision and decommission nodes
// in response to unsatisfied scheduling demand.
package infra

import "context"

// CapacityRequestRequirements describes the resource and constraint requirements
// for a capacity request. The infrastructure provider uses these to select an
// appropriate node type and configuration.
type CapacityRequestRequirements struct {
	// CPU is the minimum CPU capacity needed in millicores.
	CPU int64
	// Memory is the minimum memory capacity needed in MiB.
	Memory int64
	// Architecture is the required CPU architecture (e.g. "amd64", "arm64").
	Architecture string
}

// InfrastructureProvider provisions and decommissions cluster nodes. The cluster
// autoscaler calls RequestNodeWithRequirements when scheduling demand cannot be
// satisfied by existing nodes, and RemoveNode when nodes are idle and can be drained.
type InfrastructureProvider interface {
	// RequestNodeWithRequirements provisions a new node matching the given
	// requirements and returns its ID. The requestID enables provider-level
	// idempotency — repeated calls with the same requestID return the same
	// node without provisioning a duplicate.
	RequestNodeWithRequirements(ctx context.Context, requestID string, requirements CapacityRequestRequirements) (string, error)

	// RemoveNode decommissions an existing node by ID. The provider should
	// drain and terminate the node.
	RemoveNode(ctx context.Context, nodeID string) error

	// NodeCount returns the current number of provider-managed nodes.
	NodeCount(ctx context.Context) (int, error)
}
