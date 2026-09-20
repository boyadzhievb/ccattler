package cloud

import (
	"context"
	"fmt"
)

// AzureCloudProvider implements CloudProvider for Microsoft Azure.
// It manages Virtual Machines, Azure Load Balancer, and route tables.
// This is a stub implementation — real Azure API calls require the Azure SDK
// and valid credentials.
type AzureCloudProvider struct {
	// SubscriptionID is the Azure subscription ID.
	SubscriptionID string
	// ResourceGroup is the Azure resource group for all managed resources.
	ResourceGroup string
	// Region is the Azure region (e.g. "eastus").
	Region string
	// VNetName is the virtual network name for route management.
	VNetName string
}

// NewAzureCloudProvider creates an AzureCloudProvider for the given subscription and region.
func NewAzureCloudProvider(subscriptionID string, resourceGroup string, region string) *AzureCloudProvider {
	return &AzureCloudProvider{
		SubscriptionID: subscriptionID,
		ResourceGroup:  resourceGroup,
		Region:         region,
	}
}

// ProviderName returns "azure".
func (azureProvider *AzureCloudProvider) ProviderName() string {
	return "azure"
}

// ListInstances queries Azure for all VMs tagged as CCattler nodes.
func (azureProvider *AzureCloudProvider) ListInstances(_ context.Context) ([]CloudInstance, error) {
	return nil, fmt.Errorf("azure: ListInstances not yet implemented — requires Azure SDK compute.VirtualMachinesClient.List")
}

// CreateInstance launches a new Azure VM with the given configuration.
func (azureProvider *AzureCloudProvider) CreateInstance(_ context.Context, _ InstanceConfig) (string, error) {
	return "", fmt.Errorf("azure: CreateInstance not yet implemented — requires Azure SDK compute.VirtualMachinesClient.CreateOrUpdate")
}

// TerminateInstance deletes an Azure VM by its resource ID.
func (azureProvider *AzureCloudProvider) TerminateInstance(_ context.Context, _ string) error {
	return fmt.Errorf("azure: TerminateInstance not yet implemented — requires Azure SDK compute.VirtualMachinesClient.Delete")
}

// EnsureLoadBalancer creates or updates an Azure Load Balancer for the service.
func (azureProvider *AzureCloudProvider) EnsureLoadBalancer(_ context.Context, _ LoadBalancerConfig) (string, error) {
	return "", fmt.Errorf("azure: EnsureLoadBalancer not yet implemented — requires Azure SDK network.LoadBalancersClient.CreateOrUpdate")
}

// DeleteLoadBalancer removes the Azure Load Balancer for the named service.
func (azureProvider *AzureCloudProvider) DeleteLoadBalancer(_ context.Context, _ string) error {
	return fmt.Errorf("azure: DeleteLoadBalancer not yet implemented — requires Azure SDK network.LoadBalancersClient.Delete")
}

// ListLoadBalancers returns all CCattler-managed Azure Load Balancers.
func (azureProvider *AzureCloudProvider) ListLoadBalancers(_ context.Context) ([]LoadBalancerStatus, error) {
	return nil, fmt.Errorf("azure: ListLoadBalancers not yet implemented — requires Azure SDK network.LoadBalancersClient.List")
}

// EnsureRoute creates or updates an Azure route table entry.
func (azureProvider *AzureCloudProvider) EnsureRoute(_ context.Context, _ RouteConfig) error {
	return fmt.Errorf("azure: EnsureRoute not yet implemented — requires Azure SDK network.RoutesClient.CreateOrUpdate")
}

// DeleteRoute removes an Azure route table entry.
func (azureProvider *AzureCloudProvider) DeleteRoute(_ context.Context, _ string) error {
	return fmt.Errorf("azure: DeleteRoute not yet implemented — requires Azure SDK network.RoutesClient.Delete")
}

// ListRoutes returns all CCattler-managed Azure routes.
func (azureProvider *AzureCloudProvider) ListRoutes(_ context.Context) ([]RouteEntry, error) {
	return nil, fmt.Errorf("azure: ListRoutes not yet implemented — requires Azure SDK network.RoutesClient.List")
}
