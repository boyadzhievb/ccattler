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
	awsIntegrationTestWaitTimeout  = 3 * time.Minute
	awsIntegrationTestPollInterval = 5 * time.Second
)

// skipWithoutAWSCredentials skips the test if AWS credentials are not available.
func skipWithoutAWSCredentials(testHandle *testing.T) {
	testHandle.Helper()
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		testHandle.Skip("skipping: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY not set")
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
	}
}
