// Package cloud defines the CloudProvider interface for managing cloud
// infrastructure resources. A CloudProvider can manage node lifecycle
// (provision/terminate instances), cloud load balancers for externally
// exposed services, and VPC routes for cross-node networking.
package cloud

import "context"

// CloudProvider manages cloud infrastructure for a CCattler cluster. It extends
// the basic node provisioning capability with cloud-native load balancer and
// route management. Each cloud (AWS, GCP, Azure) implements this interface.
type CloudProvider interface {
	// ProviderName returns the cloud provider identifier (e.g. "aws", "gcp", "azure").
	ProviderName() string

	// NodeLifecycle

	// ListInstances returns the IDs and states of all cloud instances managed
	// by this provider. The controller uses this to detect terminated instances.
	ListInstances(ctx context.Context) ([]CloudInstance, error)

	// CreateInstance provisions a new cloud instance with the given configuration.
	// Returns the instance ID assigned by the cloud provider.
	CreateInstance(ctx context.Context, config InstanceConfig) (string, error)

	// TerminateInstance shuts down and removes a cloud instance by its provider ID.
	TerminateInstance(ctx context.Context, providerInstanceID string) error

	// LoadBalancer

	// EnsureLoadBalancer creates or updates a cloud load balancer for the given
	// service configuration. Returns the external address (IP or hostname) of
	// the load balancer. Idempotent — calling with the same config is a no-op.
	EnsureLoadBalancer(ctx context.Context, config LoadBalancerConfig) (string, error)

	// DeleteLoadBalancer removes the cloud load balancer for the named service.
	DeleteLoadBalancer(ctx context.Context, serviceName string) error

	// ListLoadBalancers returns all load balancers managed by this provider.
	ListLoadBalancers(ctx context.Context) ([]LoadBalancerStatus, error)

	// Routes

	// EnsureRoute creates or updates a VPC route entry that directs traffic
	// for the given CIDR to the specified node. Idempotent.
	EnsureRoute(ctx context.Context, route RouteConfig) error

	// DeleteRoute removes the VPC route for the given destination CIDR.
	DeleteRoute(ctx context.Context, destinationCIDR string) error

	// ListRoutes returns all VPC routes managed by this provider.
	ListRoutes(ctx context.Context) ([]RouteEntry, error)
}

// CloudInstance represents a cloud provider instance and its current state.
type CloudInstance struct {
	// ProviderInstanceID is the cloud-assigned instance identifier (e.g. AWS i-xxx).
	ProviderInstanceID string
	// NodeID is the CCattler node ID mapped to this instance, if known.
	NodeID string
	// State is the cloud instance lifecycle state.
	State InstanceState
	// Region is the cloud region where the instance runs.
	Region string
	// InstanceType is the cloud instance type (e.g. "m5.large").
	InstanceType string
}

// InstanceState represents the lifecycle state of a cloud instance.
type InstanceState string

const (
	// InstanceStateRunning means the cloud instance is healthy and running.
	InstanceStateRunning InstanceState = "running"
	// InstanceStatePending means the cloud instance is being provisioned.
	InstanceStatePending InstanceState = "pending"
	// InstanceStateTerminated means the cloud instance has been shut down.
	InstanceStateTerminated InstanceState = "terminated"
	// InstanceStateStopped means the cloud instance exists but is not running.
	InstanceStateStopped InstanceState = "stopped"
)

// InstanceConfig holds parameters for provisioning a new cloud instance.
type InstanceConfig struct {
	// InstanceType is the cloud instance type to provision (e.g. "m5.large").
	InstanceType string
	// Region is the cloud region to provision in.
	Region string
	// Zone is the specific availability zone within the region.
	Zone string
	// Labels are key-value pairs applied to the instance as cloud tags.
	Labels map[string]string
}

// LoadBalancerConfig describes the desired state of a cloud load balancer.
type LoadBalancerConfig struct {
	// ServiceName is the CCattler service this load balancer exposes.
	ServiceName string
	// Port is the port the load balancer listens on externally.
	Port int
	// TargetPort is the port on backend nodes that receives traffic.
	TargetPort int
	// Protocol is the load balancer protocol ("tcp" or "http").
	Protocol string
	// Backends is the list of node addresses and ports to forward traffic to.
	Backends []LoadBalancerBackend
}

// LoadBalancerBackend is a single backend target for a cloud load balancer.
type LoadBalancerBackend struct {
	// NodeID is the CCattler node hosting this backend.
	NodeID string
	// Address is the routable IP address of the backend.
	Address string
	// Port is the port on the backend that accepts traffic.
	Port int
}

// LoadBalancerStatus represents the observed state of a cloud load balancer.
type LoadBalancerStatus struct {
	// ServiceName is the CCattler service this load balancer serves.
	ServiceName string
	// ExternalAddress is the public IP or hostname assigned by the cloud.
	ExternalAddress string
	// State is the load balancer's operational state.
	State LoadBalancerState
}

// LoadBalancerState represents the operational state of a cloud load balancer.
type LoadBalancerState string

const (
	// LoadBalancerStateActive means the load balancer is healthy and serving traffic.
	LoadBalancerStateActive LoadBalancerState = "active"
	// LoadBalancerStateProvisioning means the load balancer is being created.
	LoadBalancerStateProvisioning LoadBalancerState = "provisioning"
	// LoadBalancerStateDeleting means the load balancer is being torn down.
	LoadBalancerStateDeleting LoadBalancerState = "deleting"
)

// RouteConfig describes a desired VPC route entry.
type RouteConfig struct {
	// DestinationCIDR is the IP range this route covers (e.g. "10.244.1.0/24").
	DestinationCIDR string
	// TargetNodeID is the CCattler node ID that should receive this traffic.
	TargetNodeID string
	// TargetInstanceID is the cloud provider instance ID for the target node.
	TargetInstanceID string
}

// RouteEntry represents an observed VPC route.
type RouteEntry struct {
	// DestinationCIDR is the routed IP range.
	DestinationCIDR string
	// TargetNodeID is the CCattler node ID receiving this traffic.
	TargetNodeID string
	// TargetInstanceID is the cloud provider instance ID.
	TargetInstanceID string
}
