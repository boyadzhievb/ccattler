// Package infra defines the InfrastructureProvider interface for cluster
// autoscaling. An InfrastructureProvider can provision and decommission nodes
// in response to unsatisfied scheduling demand.
package infra

import "context"

// InfrastructureProvider provisions and decommissions cluster nodes. The cluster
// autoscaler calls RequestNode when scheduling demand cannot be satisfied by
// existing nodes, and RemoveNode when nodes are idle and can be drained.
type InfrastructureProvider interface {
	// RequestNode provisions a new node and returns its ID. The node should
	// register itself with the fact store once it's ready.
	RequestNode(ctx context.Context) (string, error)

	// RemoveNode decommissions an existing node by ID. The provider should
	// drain and terminate the node.
	RemoveNode(ctx context.Context, nodeID string) error

	// NodeCount returns the current number of provider-managed nodes.
	NodeCount(ctx context.Context) (int, error)
}
