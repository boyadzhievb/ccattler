package cloud

import (
	"context"
	"fmt"
)

// AWSCloudProvider implements CloudProvider for Amazon Web Services.
// It manages EC2 instances, Elastic Load Balancers (ELB/NLB), and VPC
// route table entries. This is a stub implementation — real AWS API calls
// require the AWS SDK and valid credentials configured via the credential
// broker or environment variables.
type AWSCloudProvider struct {
	// Region is the AWS region (e.g. "us-east-1").
	Region string
	// VPCRouteTableID is the VPC route table to manage routes in.
	VPCRouteTableID string
}

// NewAWSCloudProvider creates an AWSCloudProvider for the given region.
func NewAWSCloudProvider(region string) *AWSCloudProvider {
	return &AWSCloudProvider{Region: region}
}

// ProviderName returns "aws".
func (awsProvider *AWSCloudProvider) ProviderName() string {
	return "aws"
}

// ListInstances queries EC2 for all instances tagged as CCattler nodes.
func (awsProvider *AWSCloudProvider) ListInstances(_ context.Context) ([]CloudInstance, error) {
	return nil, fmt.Errorf("aws: ListInstances not yet implemented — requires AWS SDK ec2.DescribeInstances")
}

// CreateInstance launches a new EC2 instance with the given configuration.
func (awsProvider *AWSCloudProvider) CreateInstance(_ context.Context, _ InstanceConfig) (string, error) {
	return "", fmt.Errorf("aws: CreateInstance not yet implemented — requires AWS SDK ec2.RunInstances")
}

// TerminateInstance terminates an EC2 instance by its instance ID.
func (awsProvider *AWSCloudProvider) TerminateInstance(_ context.Context, _ string) error {
	return fmt.Errorf("aws: TerminateInstance not yet implemented — requires AWS SDK ec2.TerminateInstances")
}

// EnsureLoadBalancer creates or updates an ELB/NLB for the service.
func (awsProvider *AWSCloudProvider) EnsureLoadBalancer(_ context.Context, _ LoadBalancerConfig) (string, error) {
	return "", fmt.Errorf("aws: EnsureLoadBalancer not yet implemented — requires AWS SDK elbv2.CreateLoadBalancer")
}

// DeleteLoadBalancer removes the ELB/NLB for the named service.
func (awsProvider *AWSCloudProvider) DeleteLoadBalancer(_ context.Context, _ string) error {
	return fmt.Errorf("aws: DeleteLoadBalancer not yet implemented — requires AWS SDK elbv2.DeleteLoadBalancer")
}

// ListLoadBalancers returns all CCattler-managed ELB/NLB instances.
func (awsProvider *AWSCloudProvider) ListLoadBalancers(_ context.Context) ([]LoadBalancerStatus, error) {
	return nil, fmt.Errorf("aws: ListLoadBalancers not yet implemented — requires AWS SDK elbv2.DescribeLoadBalancers")
}

// EnsureRoute creates or updates a VPC route table entry.
func (awsProvider *AWSCloudProvider) EnsureRoute(_ context.Context, _ RouteConfig) error {
	return fmt.Errorf("aws: EnsureRoute not yet implemented — requires AWS SDK ec2.CreateRoute")
}

// DeleteRoute removes a VPC route table entry.
func (awsProvider *AWSCloudProvider) DeleteRoute(_ context.Context, _ string) error {
	return fmt.Errorf("aws: DeleteRoute not yet implemented — requires AWS SDK ec2.DeleteRoute")
}

// ListRoutes returns all CCattler-managed VPC routes.
func (awsProvider *AWSCloudProvider) ListRoutes(_ context.Context) ([]RouteEntry, error) {
	return nil, fmt.Errorf("aws: ListRoutes not yet implemented — requires AWS SDK ec2.DescribeRouteTables")
}
