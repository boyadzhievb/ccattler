// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package cloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

const (
	// awsManagedTagKey is the tag key applied to all CCattler-managed AWS resources.
	awsManagedTagKey = "ccattler:managed"
	// awsManagedTagValue is the expected value of the managed tag.
	awsManagedTagValue = "true"
	// awsServiceTagKey tags load balancer resources with their CCattler service name.
	awsServiceTagKey = "ccattler:service"
	// awsNodeTagKey tags EC2 instances with their CCattler node ID.
	awsNodeTagKey = "ccattler:node-id"
	// awsLoadBalancerNamePrefix is prepended to sanitized service names for NLB naming.
	awsLoadBalancerNamePrefix = "cca-"
	// awsTargetGroupNamePrefix is prepended to sanitized service names for target groups.
	awsTargetGroupNamePrefix = "cca-tg-"
	// awsResourceNameMaxLength is the AWS limit for LB and target group names.
	awsResourceNameMaxLength = 32
	// awsMaxInstancesPerRunRequest is the number of instances to launch in one RunInstances call.
	awsMaxInstancesPerRunRequest = 1
)

// AWSCloudProvider implements CloudProvider for Amazon Web Services using the
// AWS SDK v2. It manages EC2 instances, Network Load Balancers (NLB), and VPC
// route table entries. Configure provider-level defaults (VPC, subnets, AMI)
// via the Set* methods after construction.
type AWSCloudProvider struct {
	// region is the AWS region for all API calls.
	region string
	// vpcID is the VPC used for target group creation (required for LB operations).
	vpcID string
	// vpcRouteTableID is the route table for VPC route programming.
	vpcRouteTableID string
	// subnetIDs are the subnets used when creating load balancers.
	subnetIDs []string
	// defaultImageID is the AMI used when CreateInstance omits an ImageID.
	defaultImageID string
	// defaultSecurityGroupID is the security group applied to new instances.
	defaultSecurityGroupID string
	// ec2Client is the AWS EC2 API client.
	ec2Client *ec2.Client
	// elbv2Client is the AWS ELBv2 API client for NLB management.
	elbv2Client *elasticloadbalancingv2.Client
}

// NewAWSCloudProvider creates an AWSCloudProvider that uses the default AWS
// credential chain (environment variables, shared credentials, IAM role) for
// the given region. Returns an error if the AWS SDK config cannot be loaded.
func NewAWSCloudProvider(ctx context.Context, region string) (*AWSCloudProvider, error) {
	sdkConfig, loadError := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if loadError != nil {
		return nil, fmt.Errorf("aws: load SDK config: %w", loadError)
	}
	return &AWSCloudProvider{
		region:      region,
		ec2Client:   ec2.NewFromConfig(sdkConfig),
		elbv2Client: elasticloadbalancingv2.NewFromConfig(sdkConfig),
	}, nil
}

// SetVPCID configures the VPC ID used for target group creation.
func (awsProvider *AWSCloudProvider) SetVPCID(vpcID string) {
	awsProvider.vpcID = vpcID
}

// SetVPCRouteTableID configures the route table for VPC route operations.
func (awsProvider *AWSCloudProvider) SetVPCRouteTableID(routeTableID string) {
	awsProvider.vpcRouteTableID = routeTableID
}

// SetSubnetIDs configures the subnets used when creating load balancers.
func (awsProvider *AWSCloudProvider) SetSubnetIDs(subnetIDs []string) {
	awsProvider.subnetIDs = subnetIDs
}

// SetDefaultImageID configures the AMI used when creating instances.
func (awsProvider *AWSCloudProvider) SetDefaultImageID(imageID string) {
	awsProvider.defaultImageID = imageID
}

// SetDefaultSecurityGroupID configures the security group for new instances.
func (awsProvider *AWSCloudProvider) SetDefaultSecurityGroupID(securityGroupID string) {
	awsProvider.defaultSecurityGroupID = securityGroupID
}

// ProviderName returns "aws".
func (awsProvider *AWSCloudProvider) ProviderName() string {
	return "aws"
}

// ListInstances queries EC2 for all instances tagged as CCattler-managed and
// maps them to CloudInstance values. Terminated instances are included so the
// node lifecycle controller can detect them.
func (awsProvider *AWSCloudProvider) ListInstances(ctx context.Context) ([]CloudInstance, error) {
	describeInput := &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + awsManagedTagKey), Values: []string{awsManagedTagValue}},
		},
	}
	describeOutput, describeError := awsProvider.ec2Client.DescribeInstances(ctx, describeInput)
	if describeError != nil {
		return nil, fmt.Errorf("aws: DescribeInstances: %w", describeError)
	}
	return mapEC2ReservationsToCloudInstances(describeOutput.Reservations), nil
}

// CreateInstance launches a new EC2 instance with the given configuration and
// tags it as CCattler-managed. Returns the provider-assigned instance ID.
func (awsProvider *AWSCloudProvider) CreateInstance(ctx context.Context, instanceConfig InstanceConfig) (string, error) {
	imageID := instanceConfig.ImageID
	if imageID == "" {
		imageID = awsProvider.defaultImageID
	}
	if imageID == "" {
		return "", fmt.Errorf("aws: no image ID configured (set ImageID in config or call SetDefaultImageID)")
	}
	runInput := buildEC2RunInstancesInput(awsProvider, imageID, instanceConfig)
	runOutput, runError := awsProvider.ec2Client.RunInstances(ctx, runInput)
	if runError != nil {
		return "", fmt.Errorf("aws: RunInstances: %w", runError)
	}
	if len(runOutput.Instances) == 0 {
		return "", fmt.Errorf("aws: RunInstances returned no instances")
	}
	return aws.ToString(runOutput.Instances[0].InstanceId), nil
}

// TerminateInstance terminates an EC2 instance by its provider instance ID.
func (awsProvider *AWSCloudProvider) TerminateInstance(ctx context.Context, providerInstanceID string) error {
	terminateInput := &ec2.TerminateInstancesInput{
		InstanceIds: []string{providerInstanceID},
	}
	_, terminateError := awsProvider.ec2Client.TerminateInstances(ctx, terminateInput)
	if terminateError != nil {
		return fmt.Errorf("aws: TerminateInstances(%s): %w", providerInstanceID, terminateError)
	}
	return nil
}

// EnsureLoadBalancer creates or updates a Network Load Balancer for the given
// service. Creates the NLB, target group, listener, and registers backends
// as needed. Returns the NLB DNS name as the external address.
func (awsProvider *AWSCloudProvider) EnsureLoadBalancer(ctx context.Context, loadBalancerConfig LoadBalancerConfig) (string, error) {
	if len(awsProvider.subnetIDs) == 0 {
		return "", fmt.Errorf("aws: no subnet IDs configured for load balancer creation")
	}
	if awsProvider.vpcID == "" {
		return "", fmt.Errorf("aws: no VPC ID configured for target group creation")
	}
	loadBalancerName := awsSanitizeResourceName(awsLoadBalancerNamePrefix, loadBalancerConfig.ServiceName)
	loadBalancerARN, dnsName, findError := awsProvider.findOrCreateNLB(ctx, loadBalancerName, loadBalancerConfig.ServiceName)
	if findError != nil {
		return "", findError
	}
	targetGroupName := awsSanitizeResourceName(awsTargetGroupNamePrefix, loadBalancerConfig.ServiceName)
	targetGroupARN, targetGroupError := awsProvider.findOrCreateTargetGroup(ctx, targetGroupName, loadBalancerConfig)
	if targetGroupError != nil {
		return "", targetGroupError
	}
	if listenerError := awsProvider.ensureNLBListener(ctx, loadBalancerARN, targetGroupARN, loadBalancerConfig.Port); listenerError != nil {
		return "", listenerError
	}
	if syncError := awsProvider.syncTargetRegistrations(ctx, targetGroupARN, loadBalancerConfig.Backends); syncError != nil {
		return "", syncError
	}
	return dnsName, nil
}

// DeleteLoadBalancer removes the NLB and its target group for the named service.
func (awsProvider *AWSCloudProvider) DeleteLoadBalancer(ctx context.Context, serviceName string) error {
	loadBalancerName := awsSanitizeResourceName(awsLoadBalancerNamePrefix, serviceName)
	loadBalancerARN, deleteError := awsProvider.deleteNLBByName(ctx, loadBalancerName)
	if deleteError != nil {
		return deleteError
	}
	if loadBalancerARN == "" {
		return nil
	}
	targetGroupName := awsSanitizeResourceName(awsTargetGroupNamePrefix, serviceName)
	return awsProvider.deleteTargetGroupByName(ctx, targetGroupName)
}

// ListLoadBalancers returns all CCattler-managed NLBs by querying for the
// managed tag.
func (awsProvider *AWSCloudProvider) ListLoadBalancers(ctx context.Context) ([]LoadBalancerStatus, error) {
	describeInput := &elasticloadbalancingv2.DescribeLoadBalancersInput{}
	describeOutput, describeError := awsProvider.elbv2Client.DescribeLoadBalancers(ctx, describeInput)
	if describeError != nil {
		return nil, fmt.Errorf("aws: DescribeLoadBalancers: %w", describeError)
	}
	return awsProvider.filterManagedLoadBalancers(ctx, describeOutput.LoadBalancers)
}

// EnsureRoute creates or replaces a VPC route entry that directs traffic for
// the given CIDR to the target EC2 instance.
func (awsProvider *AWSCloudProvider) EnsureRoute(ctx context.Context, routeConfig RouteConfig) error {
	if awsProvider.vpcRouteTableID == "" {
		return fmt.Errorf("aws: no VPC route table ID configured")
	}
	createInput := &ec2.CreateRouteInput{
		RouteTableId:         aws.String(awsProvider.vpcRouteTableID),
		DestinationCidrBlock: aws.String(routeConfig.DestinationCIDR),
		InstanceId:           aws.String(routeConfig.TargetInstanceID),
	}
	_, createError := awsProvider.ec2Client.CreateRoute(ctx, createInput)
	if createError != nil && strings.Contains(createError.Error(), "RouteAlreadyExists") {
		return awsProvider.replaceExistingRoute(ctx, routeConfig)
	}
	if createError != nil {
		return fmt.Errorf("aws: CreateRoute(%s): %w", routeConfig.DestinationCIDR, createError)
	}
	return nil
}

// DeleteRoute removes a VPC route table entry for the given destination CIDR.
func (awsProvider *AWSCloudProvider) DeleteRoute(ctx context.Context, destinationCIDR string) error {
	if awsProvider.vpcRouteTableID == "" {
		return fmt.Errorf("aws: no VPC route table ID configured")
	}
	deleteInput := &ec2.DeleteRouteInput{
		RouteTableId:         aws.String(awsProvider.vpcRouteTableID),
		DestinationCidrBlock: aws.String(destinationCIDR),
	}
	_, deleteError := awsProvider.ec2Client.DeleteRoute(ctx, deleteInput)
	if deleteError != nil {
		return fmt.Errorf("aws: DeleteRoute(%s): %w", destinationCIDR, deleteError)
	}
	return nil
}

// ListRoutes returns all routes in the configured VPC route table that target
// EC2 instances (i.e., CCattler node routes, not gateway or local routes).
func (awsProvider *AWSCloudProvider) ListRoutes(ctx context.Context) ([]RouteEntry, error) {
	if awsProvider.vpcRouteTableID == "" {
		return nil, fmt.Errorf("aws: no VPC route table ID configured")
	}
	describeInput := &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{awsProvider.vpcRouteTableID},
	}
	describeOutput, describeError := awsProvider.ec2Client.DescribeRouteTables(ctx, describeInput)
	if describeError != nil {
		return nil, fmt.Errorf("aws: DescribeRouteTables: %w", describeError)
	}
	return extractInstanceRoutesFromTables(describeOutput.RouteTables), nil
}

// mapEC2ReservationsToCloudInstances converts EC2 reservations into a flat list
// of CloudInstance values.
func mapEC2ReservationsToCloudInstances(reservations []ec2types.Reservation) []CloudInstance {
	var cloudInstances []CloudInstance
	for _, reservation := range reservations {
		for _, ec2Instance := range reservation.Instances {
			cloudInstance := CloudInstance{
				ProviderInstanceID: aws.ToString(ec2Instance.InstanceId),
				State:              mapEC2StateToInstanceState(ec2Instance.State),
				InstanceType:       string(ec2Instance.InstanceType),
			}
			if ec2Instance.Placement != nil {
				cloudInstance.Region = aws.ToString(ec2Instance.Placement.AvailabilityZone)
			}
			cloudInstance.NodeID = findEC2TagValue(ec2Instance.Tags, awsNodeTagKey)
			cloudInstances = append(cloudInstances, cloudInstance)
		}
	}
	return cloudInstances
}

// mapEC2StateToInstanceState converts an EC2 instance state to a CCattler
// InstanceState enum value.
func mapEC2StateToInstanceState(ec2State *ec2types.InstanceState) InstanceState {
	if ec2State == nil {
		return InstanceStatePending
	}
	switch ec2State.Name {
	case ec2types.InstanceStateNameRunning:
		return InstanceStateRunning
	case ec2types.InstanceStateNamePending:
		return InstanceStatePending
	case ec2types.InstanceStateNameTerminated, ec2types.InstanceStateNameShuttingDown:
		return InstanceStateTerminated
	case ec2types.InstanceStateNameStopped, ec2types.InstanceStateNameStopping:
		return InstanceStateStopped
	default:
		return InstanceStatePending
	}
}

// findEC2TagValue returns the value of the tag with the given key, or empty
// string if the tag is not present.
func findEC2TagValue(tags []ec2types.Tag, tagKey string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == tagKey {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

// buildEC2RunInstancesInput constructs the RunInstances request with tags and
// placement configuration.
func buildEC2RunInstancesInput(awsProvider *AWSCloudProvider, imageID string, instanceConfig InstanceConfig) *ec2.RunInstancesInput {
	tags := []ec2types.Tag{
		{Key: aws.String(awsManagedTagKey), Value: aws.String(awsManagedTagValue)},
	}
	for labelKey, labelValue := range instanceConfig.Labels {
		tags = append(tags, ec2types.Tag{Key: aws.String(labelKey), Value: aws.String(labelValue)})
	}
	runInput := &ec2.RunInstancesInput{
		ImageId:      aws.String(imageID),
		InstanceType: ec2types.InstanceType(instanceConfig.InstanceType),
		MinCount:     aws.Int32(awsMaxInstancesPerRunRequest),
		MaxCount:     aws.Int32(awsMaxInstancesPerRunRequest),
		TagSpecifications: []ec2types.TagSpecification{
			{ResourceType: ec2types.ResourceTypeInstance, Tags: tags},
		},
	}
	if instanceConfig.Zone != "" {
		runInput.Placement = &ec2types.Placement{AvailabilityZone: aws.String(instanceConfig.Zone)}
	}
	if awsProvider.defaultSecurityGroupID != "" {
		runInput.SecurityGroupIds = []string{awsProvider.defaultSecurityGroupID}
	}
	return runInput
}

// findOrCreateNLB looks up an NLB by name and creates it if it does not exist.
// Returns the load balancer ARN and DNS name.
func (awsProvider *AWSCloudProvider) findOrCreateNLB(ctx context.Context, loadBalancerName string, serviceName string) (string, string, error) {
	describeInput := &elasticloadbalancingv2.DescribeLoadBalancersInput{
		Names: []string{loadBalancerName},
	}
	describeOutput, describeError := awsProvider.elbv2Client.DescribeLoadBalancers(ctx, describeInput)
	if describeError == nil && len(describeOutput.LoadBalancers) > 0 {
		existingLB := describeOutput.LoadBalancers[0]
		return aws.ToString(existingLB.LoadBalancerArn), aws.ToString(existingLB.DNSName), nil
	}
	createInput := &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name:    aws.String(loadBalancerName),
		Type:    elbv2types.LoadBalancerTypeEnumNetwork,
		Scheme:  elbv2types.LoadBalancerSchemeEnumInternetFacing,
		Subnets: awsProvider.subnetIDs,
		Tags: []elbv2types.Tag{
			{Key: aws.String(awsManagedTagKey), Value: aws.String(awsManagedTagValue)},
			{Key: aws.String(awsServiceTagKey), Value: aws.String(serviceName)},
		},
	}
	createOutput, createError := awsProvider.elbv2Client.CreateLoadBalancer(ctx, createInput)
	if createError != nil {
		return "", "", fmt.Errorf("aws: CreateLoadBalancer(%s): %w", loadBalancerName, createError)
	}
	if len(createOutput.LoadBalancers) == 0 {
		return "", "", fmt.Errorf("aws: CreateLoadBalancer returned no load balancers")
	}
	createdLB := createOutput.LoadBalancers[0]
	return aws.ToString(createdLB.LoadBalancerArn), aws.ToString(createdLB.DNSName), nil
}

// findOrCreateTargetGroup looks up a target group by name and creates it if it
// does not exist. Returns the target group ARN.
func (awsProvider *AWSCloudProvider) findOrCreateTargetGroup(ctx context.Context, targetGroupName string, loadBalancerConfig LoadBalancerConfig) (string, error) {
	describeInput := &elasticloadbalancingv2.DescribeTargetGroupsInput{
		Names: []string{targetGroupName},
	}
	describeOutput, describeError := awsProvider.elbv2Client.DescribeTargetGroups(ctx, describeInput)
	if describeError == nil && len(describeOutput.TargetGroups) > 0 {
		return aws.ToString(describeOutput.TargetGroups[0].TargetGroupArn), nil
	}
	createInput := &elasticloadbalancingv2.CreateTargetGroupInput{
		Name:       aws.String(targetGroupName),
		Protocol:   elbv2types.ProtocolEnumTcp,
		Port:       aws.Int32(safeIntToInt32(loadBalancerConfig.TargetPort)),
		TargetType: elbv2types.TargetTypeEnumIp,
		VpcId:      aws.String(awsProvider.vpcID),
		Tags: []elbv2types.Tag{
			{Key: aws.String(awsManagedTagKey), Value: aws.String(awsManagedTagValue)},
			{Key: aws.String(awsServiceTagKey), Value: aws.String(loadBalancerConfig.ServiceName)},
		},
	}
	createOutput, createError := awsProvider.elbv2Client.CreateTargetGroup(ctx, createInput)
	if createError != nil {
		return "", fmt.Errorf("aws: CreateTargetGroup(%s): %w", targetGroupName, createError)
	}
	if len(createOutput.TargetGroups) == 0 {
		return "", fmt.Errorf("aws: CreateTargetGroup returned no target groups")
	}
	return aws.ToString(createOutput.TargetGroups[0].TargetGroupArn), nil
}

// ensureNLBListener checks whether a listener already exists on the NLB for
// the given port and creates one if needed.
func (awsProvider *AWSCloudProvider) ensureNLBListener(ctx context.Context, loadBalancerARN string, targetGroupARN string, port int) error {
	describeInput := &elasticloadbalancingv2.DescribeListenersInput{
		LoadBalancerArn: aws.String(loadBalancerARN),
	}
	describeOutput, describeError := awsProvider.elbv2Client.DescribeListeners(ctx, describeInput)
	if describeError == nil {
		for _, existingListener := range describeOutput.Listeners {
			if aws.ToInt32(existingListener.Port) == safeIntToInt32(port) {
				return nil
			}
		}
	}
	createInput := &elasticloadbalancingv2.CreateListenerInput{
		LoadBalancerArn: aws.String(loadBalancerARN),
		Protocol:        elbv2types.ProtocolEnumTcp,
		Port:            aws.Int32(safeIntToInt32(port)),
		DefaultActions: []elbv2types.Action{
			{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: aws.String(targetGroupARN)},
		},
	}
	_, createError := awsProvider.elbv2Client.CreateListener(ctx, createInput)
	if createError != nil {
		return fmt.Errorf("aws: CreateListener(port=%d): %w", port, createError)
	}
	return nil
}

// syncTargetRegistrations updates the target group to match the desired backend
// set by registering new targets and deregistering removed ones.
func (awsProvider *AWSCloudProvider) syncTargetRegistrations(ctx context.Context, targetGroupARN string, desiredBackends []LoadBalancerBackend) error {
	desiredTargets := make(map[string]elbv2types.TargetDescription)
	for _, backend := range desiredBackends {
		targetKey := fmt.Sprintf("%s:%d", backend.Address, backend.Port)
		desiredTargets[targetKey] = elbv2types.TargetDescription{
			Id:   aws.String(backend.Address),
			Port: aws.Int32(safeIntToInt32(backend.Port)),
		}
	}
	currentTargets, currentError := awsProvider.describeCurrentTargets(ctx, targetGroupARN)
	if currentError != nil {
		return currentError
	}
	targetsToRegister := computeTargetsToRegister(desiredTargets, currentTargets)
	targetsToDeregister := computeTargetsToDeregister(desiredTargets, currentTargets)
	if len(targetsToRegister) > 0 {
		registerInput := &elasticloadbalancingv2.RegisterTargetsInput{
			TargetGroupArn: aws.String(targetGroupARN),
			Targets:        targetsToRegister,
		}
		if _, registerError := awsProvider.elbv2Client.RegisterTargets(ctx, registerInput); registerError != nil {
			return fmt.Errorf("aws: RegisterTargets: %w", registerError)
		}
	}
	if len(targetsToDeregister) > 0 {
		deregisterInput := &elasticloadbalancingv2.DeregisterTargetsInput{
			TargetGroupArn: aws.String(targetGroupARN),
			Targets:        targetsToDeregister,
		}
		if _, deregisterError := awsProvider.elbv2Client.DeregisterTargets(ctx, deregisterInput); deregisterError != nil {
			return fmt.Errorf("aws: DeregisterTargets: %w", deregisterError)
		}
	}
	return nil
}

// describeCurrentTargets returns the currently registered targets in a target
// group as a map keyed by "address:port".
func (awsProvider *AWSCloudProvider) describeCurrentTargets(ctx context.Context, targetGroupARN string) (map[string]elbv2types.TargetDescription, error) {
	healthInput := &elasticloadbalancingv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(targetGroupARN),
	}
	healthOutput, healthError := awsProvider.elbv2Client.DescribeTargetHealth(ctx, healthInput)
	if healthError != nil {
		return nil, fmt.Errorf("aws: DescribeTargetHealth: %w", healthError)
	}
	currentTargets := make(map[string]elbv2types.TargetDescription)
	for _, targetHealth := range healthOutput.TargetHealthDescriptions {
		targetKey := fmt.Sprintf("%s:%d", aws.ToString(targetHealth.Target.Id), aws.ToInt32(targetHealth.Target.Port))
		currentTargets[targetKey] = *targetHealth.Target
	}
	return currentTargets, nil
}

// computeTargetsToRegister returns targets that are desired but not currently
// registered.
func computeTargetsToRegister(desiredTargets map[string]elbv2types.TargetDescription, currentTargets map[string]elbv2types.TargetDescription) []elbv2types.TargetDescription {
	var targetsToRegister []elbv2types.TargetDescription
	for targetKey, targetDescription := range desiredTargets {
		if _, alreadyRegistered := currentTargets[targetKey]; !alreadyRegistered {
			targetsToRegister = append(targetsToRegister, targetDescription)
		}
	}
	return targetsToRegister
}

// computeTargetsToDeregister returns targets that are currently registered but
// no longer desired.
func computeTargetsToDeregister(desiredTargets map[string]elbv2types.TargetDescription, currentTargets map[string]elbv2types.TargetDescription) []elbv2types.TargetDescription {
	var targetsToDeregister []elbv2types.TargetDescription
	for targetKey, targetDescription := range currentTargets {
		if _, stillDesired := desiredTargets[targetKey]; !stillDesired {
			targetsToDeregister = append(targetsToDeregister, targetDescription)
		}
	}
	return targetsToDeregister
}

// deleteNLBByName finds an NLB by name and deletes it. Returns the ARN of the
// deleted LB (empty if not found).
func (awsProvider *AWSCloudProvider) deleteNLBByName(ctx context.Context, loadBalancerName string) (string, error) {
	describeInput := &elasticloadbalancingv2.DescribeLoadBalancersInput{
		Names: []string{loadBalancerName},
	}
	describeOutput, describeError := awsProvider.elbv2Client.DescribeLoadBalancers(ctx, describeInput)
	if describeError != nil || len(describeOutput.LoadBalancers) == 0 {
		return "", nil
	}
	loadBalancerARN := aws.ToString(describeOutput.LoadBalancers[0].LoadBalancerArn)
	deleteInput := &elasticloadbalancingv2.DeleteLoadBalancerInput{
		LoadBalancerArn: aws.String(loadBalancerARN),
	}
	if _, deleteError := awsProvider.elbv2Client.DeleteLoadBalancer(ctx, deleteInput); deleteError != nil {
		return "", fmt.Errorf("aws: DeleteLoadBalancer(%s): %w", loadBalancerName, deleteError)
	}
	return loadBalancerARN, nil
}

// deleteTargetGroupByName finds a target group by name and deletes it.
func (awsProvider *AWSCloudProvider) deleteTargetGroupByName(ctx context.Context, targetGroupName string) error {
	describeInput := &elasticloadbalancingv2.DescribeTargetGroupsInput{
		Names: []string{targetGroupName},
	}
	describeOutput, describeError := awsProvider.elbv2Client.DescribeTargetGroups(ctx, describeInput)
	if describeError != nil || len(describeOutput.TargetGroups) == 0 {
		return nil
	}
	deleteInput := &elasticloadbalancingv2.DeleteTargetGroupInput{
		TargetGroupArn: describeOutput.TargetGroups[0].TargetGroupArn,
	}
	if _, deleteError := awsProvider.elbv2Client.DeleteTargetGroup(ctx, deleteInput); deleteError != nil {
		return fmt.Errorf("aws: DeleteTargetGroup(%s): %w", targetGroupName, deleteError)
	}
	return nil
}

// filterManagedLoadBalancers queries tags for each LB and returns only those
// with the CCattler managed tag, mapped to LoadBalancerStatus.
func (awsProvider *AWSCloudProvider) filterManagedLoadBalancers(ctx context.Context, allLoadBalancers []elbv2types.LoadBalancer) ([]LoadBalancerStatus, error) {
	var managedLoadBalancers []LoadBalancerStatus
	for _, loadBalancer := range allLoadBalancers {
		tagsInput := &elasticloadbalancingv2.DescribeTagsInput{
			ResourceArns: []string{aws.ToString(loadBalancer.LoadBalancerArn)},
		}
		tagsOutput, tagsError := awsProvider.elbv2Client.DescribeTags(ctx, tagsInput)
		if tagsError != nil {
			continue
		}
		serviceName, isManaged := extractServiceNameFromELBTags(tagsOutput)
		if !isManaged {
			continue
		}
		managedLoadBalancers = append(managedLoadBalancers, LoadBalancerStatus{
			ServiceName:     serviceName,
			ExternalAddress: aws.ToString(loadBalancer.DNSName),
			State:           mapELBStateToLoadBalancerState(loadBalancer.State),
		})
	}
	return managedLoadBalancers, nil
}

// extractServiceNameFromELBTags checks tag descriptions for the managed tag
// and returns the service name if found.
func extractServiceNameFromELBTags(tagsOutput *elasticloadbalancingv2.DescribeTagsOutput) (string, bool) {
	for _, tagDescription := range tagsOutput.TagDescriptions {
		isManaged := false
		serviceName := ""
		for _, tag := range tagDescription.Tags {
			if aws.ToString(tag.Key) == awsManagedTagKey && aws.ToString(tag.Value) == awsManagedTagValue {
				isManaged = true
			}
			if aws.ToString(tag.Key) == awsServiceTagKey {
				serviceName = aws.ToString(tag.Value)
			}
		}
		if isManaged && serviceName != "" {
			return serviceName, true
		}
	}
	return "", false
}

// mapELBStateToLoadBalancerState converts an ELBv2 state to CCattler state.
func mapELBStateToLoadBalancerState(elbState *elbv2types.LoadBalancerState) LoadBalancerState {
	if elbState == nil {
		return LoadBalancerStateProvisioning
	}
	switch elbState.Code {
	case elbv2types.LoadBalancerStateEnumActive:
		return LoadBalancerStateActive
	case elbv2types.LoadBalancerStateEnumProvisioning:
		return LoadBalancerStateProvisioning
	default:
		return LoadBalancerStateProvisioning
	}
}

// replaceExistingRoute replaces a VPC route when CreateRoute returns
// RouteAlreadyExists.
func (awsProvider *AWSCloudProvider) replaceExistingRoute(ctx context.Context, routeConfig RouteConfig) error {
	replaceInput := &ec2.ReplaceRouteInput{
		RouteTableId:         aws.String(awsProvider.vpcRouteTableID),
		DestinationCidrBlock: aws.String(routeConfig.DestinationCIDR),
		InstanceId:           aws.String(routeConfig.TargetInstanceID),
	}
	_, replaceError := awsProvider.ec2Client.ReplaceRoute(ctx, replaceInput)
	if replaceError != nil {
		return fmt.Errorf("aws: ReplaceRoute(%s): %w", routeConfig.DestinationCIDR, replaceError)
	}
	return nil
}

// extractInstanceRoutesFromTables extracts routes that target EC2 instances
// from route table descriptions.
func extractInstanceRoutesFromTables(routeTables []ec2types.RouteTable) []RouteEntry {
	var routeEntries []RouteEntry
	for _, routeTable := range routeTables {
		for _, route := range routeTable.Routes {
			if route.InstanceId == nil || route.DestinationCidrBlock == nil {
				continue
			}
			routeEntries = append(routeEntries, RouteEntry{
				DestinationCIDR:  aws.ToString(route.DestinationCidrBlock),
				TargetInstanceID: aws.ToString(route.InstanceId),
			})
		}
	}
	return routeEntries
}

// safeIntToInt32 converts an int to int32, clamping to math.MaxInt32 if the
// value overflows. Port numbers are always within int32 range in practice.
func safeIntToInt32(value int) int32 {
	const maxInt32 = 1<<31 - 1
	if value > maxInt32 {
		return maxInt32
	}
	return int32(value) //nolint:gosec // clamped above
}

// awsSanitizeResourceName builds an AWS resource name from a prefix and a
// CCattler name by replacing invalid characters with hyphens and truncating
// to the AWS name limit.
func awsSanitizeResourceName(prefix string, resourceName string) string {
	sanitized := strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(resourceName)
	sanitized = strings.Trim(sanitized, "-")
	maxNamePartLength := awsResourceNameMaxLength - len(prefix)
	if len(sanitized) > maxNamePartLength {
		sanitized = sanitized[:maxNamePartLength]
	}
	sanitized = strings.TrimRight(sanitized, "-")
	return prefix + sanitized
}
