package cloud

import (
	"context"
	"fmt"
)

// GCPCloudProvider implements CloudProvider for Google Cloud Platform.
// It manages GCE instances, Cloud Load Balancing, and VPC routes. This is
// a stub implementation — real GCP API calls require the Google Cloud SDK
// and valid credentials.
type GCPCloudProvider struct {
	// ProjectID is the GCP project ID.
	ProjectID string
	// Region is the GCP region (e.g. "us-central1").
	Region string
	// NetworkName is the VPC network name for route management.
	NetworkName string
}

// NewGCPCloudProvider creates a GCPCloudProvider for the given project and region.
func NewGCPCloudProvider(projectID string, region string) *GCPCloudProvider {
	return &GCPCloudProvider{ProjectID: projectID, Region: region}
}

// ProviderName returns "gcp".
func (gcpProvider *GCPCloudProvider) ProviderName() string {
	return "gcp"
}

// ListInstances queries GCE for all instances tagged as CCattler nodes.
func (gcpProvider *GCPCloudProvider) ListInstances(_ context.Context) ([]CloudInstance, error) {
	return nil, fmt.Errorf("gcp: ListInstances not yet implemented — requires Google Cloud compute.InstancesService.List")
}

// CreateInstance launches a new GCE instance with the given configuration.
func (gcpProvider *GCPCloudProvider) CreateInstance(_ context.Context, _ InstanceConfig) (string, error) {
	return "", fmt.Errorf("gcp: CreateInstance not yet implemented — requires Google Cloud compute.InstancesService.Insert")
}

// TerminateInstance deletes a GCE instance by its instance name.
func (gcpProvider *GCPCloudProvider) TerminateInstance(_ context.Context, _ string) error {
	return fmt.Errorf("gcp: TerminateInstance not yet implemented — requires Google Cloud compute.InstancesService.Delete")
}

// EnsureLoadBalancer creates or updates a Cloud Load Balancer for the service.
func (gcpProvider *GCPCloudProvider) EnsureLoadBalancer(_ context.Context, _ LoadBalancerConfig) (string, error) {
	return "", fmt.Errorf("gcp: EnsureLoadBalancer not yet implemented — requires Google Cloud compute forwarding rules")
}

// DeleteLoadBalancer removes the Cloud Load Balancer for the named service.
func (gcpProvider *GCPCloudProvider) DeleteLoadBalancer(_ context.Context, _ string) error {
	return fmt.Errorf("gcp: DeleteLoadBalancer not yet implemented — requires Google Cloud compute forwarding rules delete")
}

// ListLoadBalancers returns all CCattler-managed Cloud Load Balancers.
func (gcpProvider *GCPCloudProvider) ListLoadBalancers(_ context.Context) ([]LoadBalancerStatus, error) {
	return nil, fmt.Errorf("gcp: ListLoadBalancers not yet implemented — requires Google Cloud compute forwarding rules list")
}

// EnsureRoute creates or updates a VPC route.
func (gcpProvider *GCPCloudProvider) EnsureRoute(_ context.Context, _ RouteConfig) error {
	return fmt.Errorf("gcp: EnsureRoute not yet implemented — requires Google Cloud compute.RoutesService.Insert")
}

// DeleteRoute removes a VPC route.
func (gcpProvider *GCPCloudProvider) DeleteRoute(_ context.Context, _ string) error {
	return fmt.Errorf("gcp: DeleteRoute not yet implemented — requires Google Cloud compute.RoutesService.Delete")
}

// ListRoutes returns all CCattler-managed VPC routes.
func (gcpProvider *GCPCloudProvider) ListRoutes(_ context.Context) ([]RouteEntry, error) {
	return nil, fmt.Errorf("gcp: ListRoutes not yet implemented — requires Google Cloud compute.RoutesService.List")
}
