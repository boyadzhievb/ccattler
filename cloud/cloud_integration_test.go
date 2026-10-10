// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package cloud

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const (
	awsIntegrationTestRegion       = "us-east-1"
	awsIntegrationTestInstanceType = "t2.micro"
	awsIntegrationTestWaitTimeout  = 5 * time.Minute
	awsIntegrationTestPollInterval = 5 * time.Second
)

// skipWithoutAWSCredentials skips the test if AWS credentials are not available.
func skipWithoutAWSCredentials(testHandle *testing.T) {
	testHandle.Helper()
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		testHandle.Skip("skipping: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY not set")
	}
}

// skipOnAWSAuthorizationError skips the test if the error is an AWS
// authorization/permission failure (e.g. SCP deny, missing IAM policy).
func skipOnAWSAuthorizationError(testHandle *testing.T, operationName string, operationError error) {
	testHandle.Helper()
	if operationError == nil {
		return
	}
	errorMessage := operationError.Error()
	if strings.Contains(errorMessage, "UnauthorizedOperation") ||
		strings.Contains(errorMessage, "AccessDenied") ||
		strings.Contains(errorMessage, "not authorized") {
		testHandle.Skipf("skipping: %s returned authorization error: %v", operationName, operationError)
	}
}

// lookupLatestAmazonLinuxAMI finds the latest Amazon Linux 2023 AMI for the
// given region using DescribeImages with owner and name filters.
func lookupLatestAmazonLinuxAMI(testHandle *testing.T, ctx context.Context, region string) string {
	testHandle.Helper()
	sdkConfig, configError := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if configError != nil {
		testHandle.Fatalf("failed to load AWS config: %v", configError)
	}
	ec2Client := ec2.NewFromConfig(sdkConfig)
	describeOutput, describeError := ec2Client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []ec2types.Filter{
			{Name: aws.String("name"), Values: []string{"al2023-ami-2023*-x86_64"}},
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("architecture"), Values: []string{"x86_64"}},
		},
	})
	if describeError != nil {
		skipOnAWSAuthorizationError(testHandle, "DescribeImages", describeError)
		testHandle.Fatalf("DescribeImages failed: %v", describeError)
	}
	if len(describeOutput.Images) == 0 {
		testHandle.Fatal("no Amazon Linux 2023 AMI found")
	}
	latestImage := describeOutput.Images[0]
	for _, image := range describeOutput.Images[1:] {
		if aws.ToString(image.CreationDate) > aws.ToString(latestImage.CreationDate) {
			latestImage = image
		}
	}
	imageID := aws.ToString(latestImage.ImageId)
	testHandle.Logf("using AMI %s (%s)", imageID, aws.ToString(latestImage.Name))
	return imageID
}

// TestAWSIntegrationInstanceLifecycle creates a real t2.micro instance,
// verifies it appears in ListInstances, then terminates it. Requires AWS
// credentials and runs against the real AWS API. Skipped in short mode.
func TestAWSIntegrationInstanceLifecycle(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping AWS integration test in short mode")
	}
	skipWithoutAWSCredentials(testHandle)

	ctx, cancel := context.WithTimeout(context.Background(), awsIntegrationTestWaitTimeout)
	defer cancel()

	awsProvider, constructionError := NewAWSCloudProvider(ctx, awsIntegrationTestRegion)
	if constructionError != nil {
		testHandle.Fatalf("NewAWSCloudProvider failed: %v", constructionError)
	}

	imageID := lookupLatestAmazonLinuxAMI(testHandle, ctx, awsIntegrationTestRegion)
	awsProvider.SetDefaultImageID(imageID)

	instanceID, createError := awsProvider.CreateInstance(ctx, InstanceConfig{
		InstanceType: awsIntegrationTestInstanceType,
		Labels: map[string]string{
			"ccattler:test": "integration",
		},
	})
	if createError != nil {
		skipOnAWSAuthorizationError(testHandle, "CreateInstance", createError)
		testHandle.Fatalf("CreateInstance failed: %v", createError)
	}
	testHandle.Logf("created instance %s", instanceID)

	testHandle.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateError := awsProvider.TerminateInstance(cleanupCtx, instanceID)
		if terminateError != nil {
			testHandle.Logf("WARNING: failed to terminate instance %s: %v", instanceID, terminateError)
		} else {
			testHandle.Logf("terminated instance %s", instanceID)
		}
	})

	instanceList, listError := awsProvider.ListInstances(ctx)
	if listError != nil {
		skipOnAWSAuthorizationError(testHandle, "ListInstances", listError)
		testHandle.Fatalf("ListInstances failed: %v", listError)
	}

	foundInstance := false
	for _, cloudInstance := range instanceList {
		if cloudInstance.ProviderInstanceID == instanceID {
			foundInstance = true
			if cloudInstance.State != InstanceStateRunning && cloudInstance.State != InstanceStatePending {
				testHandle.Errorf("expected instance state running or pending, got %q", cloudInstance.State)
			}
			if cloudInstance.InstanceType != awsIntegrationTestInstanceType {
				testHandle.Errorf("expected instance type %s, got %q", awsIntegrationTestInstanceType, cloudInstance.InstanceType)
			}
			testHandle.Logf("instance %s found in state %q", instanceID, cloudInstance.State)
			break
		}
	}
	if !foundInstance {
		testHandle.Fatalf("created instance %s not found in ListInstances", instanceID)
	}

	terminateError := awsProvider.TerminateInstance(ctx, instanceID)
	if terminateError != nil {
		testHandle.Fatalf("TerminateInstance failed: %v", terminateError)
	}
	testHandle.Logf("terminated instance %s", instanceID)

	terminatedList, terminatedListError := awsProvider.ListInstances(ctx)
	if terminatedListError != nil {
		testHandle.Fatalf("ListInstances after terminate failed: %v", terminatedListError)
	}

	for _, cloudInstance := range terminatedList {
		if cloudInstance.ProviderInstanceID == instanceID {
			if cloudInstance.State != InstanceStateTerminated && cloudInstance.State != InstanceStateStopped {
				testHandle.Logf("instance %s in state %q (may take time to reach terminated)", instanceID, cloudInstance.State)
			} else {
				testHandle.Logf("instance %s confirmed terminated", instanceID)
			}
			break
		}
	}
}

// TestAWSIntegrationListInstancesEmpty verifies that ListInstances works when
// no CCattler-managed instances exist. This is a lightweight connectivity test.
func TestAWSIntegrationListInstancesEmpty(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping AWS integration test in short mode")
	}
	skipWithoutAWSCredentials(testHandle)

	ctx := context.Background()
	awsProvider, constructionError := NewAWSCloudProvider(ctx, awsIntegrationTestRegion)
	if constructionError != nil {
		if strings.Contains(constructionError.Error(), "credentials") {
			testHandle.Skipf("skipping: %v", constructionError)
		}
		testHandle.Fatalf("NewAWSCloudProvider failed: %v", constructionError)
	}

	instanceList, listError := awsProvider.ListInstances(ctx)
	if listError != nil {
		skipOnAWSAuthorizationError(testHandle, "ListInstances", listError)
		testHandle.Fatalf("ListInstances failed: %v", listError)
	}
	testHandle.Logf("ListInstances returned %d CCattler-managed instances", len(instanceList))
}

// TestAWSIntegrationListRoutesRequiresConfig verifies that route operations
// fail gracefully when the route table is not configured.
func TestAWSIntegrationListRoutesRequiresConfig(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping AWS integration test in short mode")
	}
	skipWithoutAWSCredentials(testHandle)

	ctx := context.Background()
	awsProvider, constructionError := NewAWSCloudProvider(ctx, awsIntegrationTestRegion)
	if constructionError != nil {
		testHandle.Fatalf("NewAWSCloudProvider failed: %v", constructionError)
	}

	_, listError := awsProvider.ListRoutes(ctx)
	if listError == nil {
		testHandle.Error("expected error from ListRoutes without route table configured")
	} else {
		skipOnAWSAuthorizationError(testHandle, "ListRoutes", listError)
	}
}

// discoverDefaultVPC finds the default VPC, its subnets, and main route table
// in the test region. Skips the test if no default VPC exists.
func discoverDefaultVPC(testHandle *testing.T, ctx context.Context, ec2Client *ec2.Client) (string, []string, string) {
	testHandle.Helper()

	vpcOutput, vpcError := ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("is-default"), Values: []string{"true"}},
		},
	})
	if vpcError != nil {
		skipOnAWSAuthorizationError(testHandle, "DescribeVpcs", vpcError)
		testHandle.Fatalf("DescribeVpcs failed: %v", vpcError)
	}
	if len(vpcOutput.Vpcs) == 0 {
		testHandle.Skip("skipping: no default VPC in region")
	}
	vpcID := aws.ToString(vpcOutput.Vpcs[0].VpcId)
	testHandle.Logf("default VPC: %s", vpcID)

	subnetOutput, subnetError := ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("default-for-az"), Values: []string{"true"}},
		},
	})
	if subnetError != nil {
		testHandle.Fatalf("DescribeSubnets failed: %v", subnetError)
	}
	if len(subnetOutput.Subnets) == 0 {
		testHandle.Skip("skipping: no default subnets found")
	}
	subnetIDs := make([]string, len(subnetOutput.Subnets))
	for index, subnet := range subnetOutput.Subnets {
		subnetIDs[index] = aws.ToString(subnet.SubnetId)
	}
	testHandle.Logf("subnets: %v", subnetIDs)

	routeTableOutput, routeTableError := ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("association.main"), Values: []string{"true"}},
		},
	})
	if routeTableError != nil {
		testHandle.Fatalf("DescribeRouteTables failed: %v", routeTableError)
	}
	if len(routeTableOutput.RouteTables) == 0 {
		testHandle.Skip("skipping: no main route table found")
	}
	routeTableID := aws.ToString(routeTableOutput.RouteTables[0].RouteTableId)
	testHandle.Logf("route table: %s", routeTableID)

	return vpcID, subnetIDs, routeTableID
}

// buildAWSProviderWithVPC creates an AWSCloudProvider configured with the
// default VPC, subnets, route table, and latest AMI. Skips if discovery fails.
func buildAWSProviderWithVPC(testHandle *testing.T, ctx context.Context) *AWSCloudProvider {
	testHandle.Helper()

	awsProvider, constructionError := NewAWSCloudProvider(ctx, awsIntegrationTestRegion)
	if constructionError != nil {
		testHandle.Fatalf("NewAWSCloudProvider failed: %v", constructionError)
	}

	sdkConfig, configError := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(awsIntegrationTestRegion))
	if configError != nil {
		testHandle.Fatalf("failed to load AWS config: %v", configError)
	}
	ec2Client := ec2.NewFromConfig(sdkConfig)

	vpcID, subnetIDs, routeTableID := discoverDefaultVPC(testHandle, ctx, ec2Client)
	awsProvider.SetVPCID(vpcID)
	awsProvider.SetSubnetIDs(subnetIDs)
	awsProvider.SetVPCRouteTableID(routeTableID)

	imageID := lookupLatestAmazonLinuxAMI(testHandle, ctx, awsIntegrationTestRegion)
	awsProvider.SetDefaultImageID(imageID)

	return awsProvider
}

// TestAWSIntegrationLoadBalancerLifecycle creates a real NLB, verifies it
// appears in ListLoadBalancers, then deletes it. Uses the default VPC.
func TestAWSIntegrationLoadBalancerLifecycle(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping AWS integration test in short mode")
	}
	skipWithoutAWSCredentials(testHandle)

	ctx, cancel := context.WithTimeout(context.Background(), awsIntegrationTestWaitTimeout)
	defer cancel()

	awsProvider := buildAWSProviderWithVPC(testHandle, ctx)

	serviceName := "cca-integ-test"
	externalAddress, ensureError := awsProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{
		ServiceName: serviceName,
		Port:        8080,
		Protocol:    "tcp",
		Backends:    []LoadBalancerBackend{},
	})
	if ensureError != nil {
		skipOnAWSAuthorizationError(testHandle, "EnsureLoadBalancer", ensureError)
		testHandle.Fatalf("EnsureLoadBalancer failed: %v", ensureError)
	}
	testHandle.Logf("load balancer external address: %s", externalAddress)

	testHandle.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		deleteError := awsProvider.DeleteLoadBalancer(cleanupCtx, serviceName)
		if deleteError != nil {
			testHandle.Logf("WARNING: failed to delete LB %s: %v", serviceName, deleteError)
		} else {
			testHandle.Logf("deleted load balancer for %s", serviceName)
		}
	})

	loadBalancerList, listError := awsProvider.ListLoadBalancers(ctx)
	if listError != nil {
		skipOnAWSAuthorizationError(testHandle, "ListLoadBalancers", listError)
		testHandle.Fatalf("ListLoadBalancers failed: %v", listError)
	}

	foundLoadBalancer := false
	for _, loadBalancer := range loadBalancerList {
		if loadBalancer.ServiceName == serviceName {
			foundLoadBalancer = true
			testHandle.Logf("LB for %s found in state %q", serviceName, loadBalancer.State)
			break
		}
	}
	if !foundLoadBalancer {
		testHandle.Fatalf("load balancer for service %s not found in ListLoadBalancers", serviceName)
	}

	deleteError := awsProvider.DeleteLoadBalancer(ctx, serviceName)
	if deleteError != nil {
		testHandle.Fatalf("DeleteLoadBalancer failed: %v", deleteError)
	}
	testHandle.Logf("deleted load balancer for %s", serviceName)
}

// TestAWSIntegrationRouteLifecycle creates a VPC route, verifies it appears
// in ListRoutes, then deletes it. Uses the default VPC's main route table.
func TestAWSIntegrationRouteLifecycle(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping AWS integration test in short mode")
	}
	skipWithoutAWSCredentials(testHandle)

	ctx, cancel := context.WithTimeout(context.Background(), awsIntegrationTestWaitTimeout)
	defer cancel()

	awsProvider := buildAWSProviderWithVPC(testHandle, ctx)

	imageID := lookupLatestAmazonLinuxAMI(testHandle, ctx, awsIntegrationTestRegion)
	awsProvider.SetDefaultImageID(imageID)

	instanceID, createError := awsProvider.CreateInstance(ctx, InstanceConfig{
		InstanceType: awsIntegrationTestInstanceType,
		Labels:       map[string]string{"ccattler:test": "route-integration"},
	})
	if createError != nil {
		skipOnAWSAuthorizationError(testHandle, "CreateInstance", createError)
		testHandle.Fatalf("CreateInstance for route target failed: %v", createError)
	}
	testHandle.Logf("created route target instance %s", instanceID)

	testHandle.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = awsProvider.DeleteRoute(cleanupCtx, "10.200.0.0/24")
		_ = awsProvider.TerminateInstance(cleanupCtx, instanceID)
		testHandle.Logf("cleaned up route and instance %s", instanceID)
	})

	testDestinationCIDR := "10.200.0.0/24"
	ensureError := awsProvider.EnsureRoute(ctx, RouteConfig{
		DestinationCIDR:  testDestinationCIDR,
		TargetNodeID:     "integ-test-node",
		TargetInstanceID: instanceID,
	})
	if ensureError != nil {
		skipOnAWSAuthorizationError(testHandle, "EnsureRoute", ensureError)
		testHandle.Fatalf("EnsureRoute failed: %v", ensureError)
	}
	testHandle.Logf("created route %s -> %s", testDestinationCIDR, instanceID)

	routeList, listError := awsProvider.ListRoutes(ctx)
	if listError != nil {
		testHandle.Fatalf("ListRoutes failed: %v", listError)
	}

	foundRoute := false
	for _, routeEntry := range routeList {
		if routeEntry.DestinationCIDR == testDestinationCIDR {
			foundRoute = true
			testHandle.Logf("route %s found targeting instance %s", testDestinationCIDR, routeEntry.TargetInstanceID)
			if routeEntry.TargetInstanceID != instanceID {
				testHandle.Errorf("expected target instance %s, got %s", instanceID, routeEntry.TargetInstanceID)
			}
			break
		}
	}
	if !foundRoute {
		testHandle.Fatalf("route %s not found in ListRoutes", testDestinationCIDR)
	}

	deleteError := awsProvider.DeleteRoute(ctx, testDestinationCIDR)
	if deleteError != nil {
		testHandle.Fatalf("DeleteRoute failed: %v", deleteError)
	}
	testHandle.Logf("deleted route %s", testDestinationCIDR)
}
