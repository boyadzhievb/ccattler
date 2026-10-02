package cloud

import (
	"context"
	"fmt"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"
)

const (
	// gcpManagedLabelKey is the label key applied to all CCattler-managed GCP resources.
	gcpManagedLabelKey = "ccattler-managed"
	// gcpManagedLabelValue is the expected value of the managed label.
	gcpManagedLabelValue = "true"
	// gcpServiceLabelKey labels resources with their CCattler service name.
	gcpServiceLabelKey = "ccattler-service"
	// gcpNodeLabelKey labels instances with their CCattler node ID.
	gcpNodeLabelKey = "ccattler-node-id"
	// gcpResourceNamePrefix is prepended to sanitized service names for GCP resources.
	gcpResourceNamePrefix = "cca-"
	// gcpResourceNameMaxLength is the GCP limit for resource names.
	gcpResourceNameMaxLength = 63
	// gcpRoutePriority is the route priority for CCattler-managed VPC routes.
	gcpRoutePriority = 1000
)

// GCPCloudProvider implements CloudProvider for Google Cloud Platform using the
// Google Cloud Compute client library. It manages GCE instances, Network TCP
// Load Balancers (target pool + forwarding rule), and VPC routes.
type GCPCloudProvider struct {
	// projectID is the GCP project ID.
	projectID string
	// region is the GCP region (e.g. "us-central1").
	region string
	// networkName is the VPC network name for route management.
	networkName string
	// defaultZone is the default zone for instance creation when not specified.
	defaultZone string
	// defaultMachineImage is the GCE image for new instances (e.g. "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64").
	defaultMachineImage string
	// instancesClient manages GCE instance lifecycle.
	instancesClient *compute.InstancesClient
	// forwardingRulesClient manages forwarding rules for load balancing.
	forwardingRulesClient *compute.ForwardingRulesClient
	// targetPoolsClient manages target pools for load balancing.
	targetPoolsClient *compute.TargetPoolsClient
	// routesClient manages VPC routes.
	routesClient *compute.RoutesClient
}

// NewGCPCloudProvider creates a GCPCloudProvider that uses Application Default
// Credentials for authentication. Returns an error if any SDK client cannot be
// created.
func NewGCPCloudProvider(ctx context.Context, projectID string, region string) (*GCPCloudProvider, error) {
	instancesClient, instancesError := compute.NewInstancesRESTClient(ctx)
	if instancesError != nil {
		return nil, fmt.Errorf("gcp: create instances client: %w", instancesError)
	}
	forwardingRulesClient, forwardingError := compute.NewForwardingRulesRESTClient(ctx)
	if forwardingError != nil {
		return nil, fmt.Errorf("gcp: create forwarding rules client: %w", forwardingError)
	}
	targetPoolsClient, targetPoolsError := compute.NewTargetPoolsRESTClient(ctx)
	if targetPoolsError != nil {
		return nil, fmt.Errorf("gcp: create target pools client: %w", targetPoolsError)
	}
	routesClient, routesError := compute.NewRoutesRESTClient(ctx)
	if routesError != nil {
		return nil, fmt.Errorf("gcp: create routes client: %w", routesError)
	}
	return &GCPCloudProvider{
		projectID:             projectID,
		region:                region,
		instancesClient:       instancesClient,
		forwardingRulesClient: forwardingRulesClient,
		targetPoolsClient:     targetPoolsClient,
		routesClient:          routesClient,
	}, nil
}

// SetNetworkName configures the VPC network name for route management.
func (gcpProvider *GCPCloudProvider) SetNetworkName(networkName string) {
	gcpProvider.networkName = networkName
}

// SetDefaultZone configures the default zone for instance creation.
func (gcpProvider *GCPCloudProvider) SetDefaultZone(zone string) {
	gcpProvider.defaultZone = zone
}

// SetDefaultMachineImage configures the default GCE image for new instances.
func (gcpProvider *GCPCloudProvider) SetDefaultMachineImage(machineImage string) {
	gcpProvider.defaultMachineImage = machineImage
}

// ProviderName returns "gcp".
func (gcpProvider *GCPCloudProvider) ProviderName() string {
	return "gcp"
}

// ListInstances queries GCE for all instances labeled as CCattler-managed
// across all zones in the configured region.
func (gcpProvider *GCPCloudProvider) ListInstances(ctx context.Context) ([]CloudInstance, error) {
	if gcpProvider.projectID == "" {
		return nil, fmt.Errorf("gcp: project ID is required")
	}
	labelFilter := fmt.Sprintf("labels.%s=%s", gcpManagedLabelKey, gcpManagedLabelValue)
	listRequest := &computepb.AggregatedListInstancesRequest{
		Project: gcpProvider.projectID,
		Filter:  proto.String(labelFilter),
	}
	instanceIterator := gcpProvider.instancesClient.AggregatedList(ctx, listRequest)
	return collectGCEInstances(instanceIterator)
}

// CreateInstance launches a new GCE instance with the given configuration and
// labels it as CCattler-managed. Returns the instance name as the provider ID.
func (gcpProvider *GCPCloudProvider) CreateInstance(ctx context.Context, instanceConfig InstanceConfig) (string, error) {
	if gcpProvider.projectID == "" {
		return "", fmt.Errorf("gcp: project ID is required")
	}
	zone := instanceConfig.Zone
	if zone == "" {
		zone = gcpProvider.defaultZone
	}
	if zone == "" {
		return "", fmt.Errorf("gcp: no zone specified and no default zone configured")
	}
	machineImage := instanceConfig.ImageID
	if machineImage == "" {
		machineImage = gcpProvider.defaultMachineImage
	}
	if machineImage == "" {
		return "", fmt.Errorf("gcp: no machine image configured")
	}
	instanceResource := buildGCEInstanceResource(gcpProvider, zone, machineImage, instanceConfig)
	insertRequest := &computepb.InsertInstanceRequest{
		Project:          gcpProvider.projectID,
		Zone:             zone,
		InstanceResource: instanceResource,
	}
	insertOperation, insertError := gcpProvider.instancesClient.Insert(ctx, insertRequest)
	if insertError != nil {
		return "", fmt.Errorf("gcp: Insert instance: %w", insertError)
	}
	if insertError = insertOperation.Wait(ctx); insertError != nil {
		return "", fmt.Errorf("gcp: wait for instance insert: %w", insertError)
	}
	return instanceResource.GetName(), nil
}

// TerminateInstance deletes a GCE instance by its name. Requires the zone to be
// derivable from the instance's current placement.
func (gcpProvider *GCPCloudProvider) TerminateInstance(ctx context.Context, providerInstanceID string) error {
	if gcpProvider.projectID == "" {
		return fmt.Errorf("gcp: project ID is required")
	}
	zone, instanceName := parseGCPInstanceID(providerInstanceID)
	if zone == "" {
		zone = gcpProvider.defaultZone
	}
	if zone == "" {
		return fmt.Errorf("gcp: cannot determine zone for instance %s", providerInstanceID)
	}
	deleteRequest := &computepb.DeleteInstanceRequest{
		Project:  gcpProvider.projectID,
		Zone:     zone,
		Instance: instanceName,
	}
	deleteOperation, deleteError := gcpProvider.instancesClient.Delete(ctx, deleteRequest)
	if deleteError != nil {
		return fmt.Errorf("gcp: Delete instance(%s): %w", providerInstanceID, deleteError)
	}
	if deleteError = deleteOperation.Wait(ctx); deleteError != nil {
		return fmt.Errorf("gcp: wait for instance delete: %w", deleteError)
	}
	return nil
}

// EnsureLoadBalancer creates or updates a GCP Network TCP Load Balancer
// (target pool + forwarding rule) for the given service. Returns the external
// IP address of the forwarding rule.
func (gcpProvider *GCPCloudProvider) EnsureLoadBalancer(ctx context.Context, loadBalancerConfig LoadBalancerConfig) (string, error) {
	if gcpProvider.projectID == "" || gcpProvider.region == "" {
		return "", fmt.Errorf("gcp: project ID and region are required for load balancer")
	}
	targetPoolName := gcpSanitizeResourceName(gcpResourceNamePrefix+"tp-", loadBalancerConfig.ServiceName)
	if targetPoolError := gcpProvider.ensureTargetPool(ctx, targetPoolName, loadBalancerConfig); targetPoolError != nil {
		return "", targetPoolError
	}
	forwardingRuleName := gcpSanitizeResourceName(gcpResourceNamePrefix, loadBalancerConfig.ServiceName)
	externalAddress, forwardingError := gcpProvider.ensureForwardingRule(ctx, forwardingRuleName, targetPoolName, loadBalancerConfig)
	if forwardingError != nil {
		return "", forwardingError
	}
	return externalAddress, nil
}

// DeleteLoadBalancer removes the forwarding rule and target pool for the named
// service.
func (gcpProvider *GCPCloudProvider) DeleteLoadBalancer(ctx context.Context, serviceName string) error {
	if gcpProvider.projectID == "" || gcpProvider.region == "" {
		return fmt.Errorf("gcp: project ID and region are required")
	}
	forwardingRuleName := gcpSanitizeResourceName(gcpResourceNamePrefix, serviceName)
	if deleteError := gcpProvider.deleteForwardingRule(ctx, forwardingRuleName); deleteError != nil {
		return deleteError
	}
	targetPoolName := gcpSanitizeResourceName(gcpResourceNamePrefix+"tp-", serviceName)
	return gcpProvider.deleteTargetPool(ctx, targetPoolName)
}

// ListLoadBalancers returns all CCattler-managed forwarding rules as
// LoadBalancerStatus values.
func (gcpProvider *GCPCloudProvider) ListLoadBalancers(ctx context.Context) ([]LoadBalancerStatus, error) {
	if gcpProvider.projectID == "" || gcpProvider.region == "" {
		return nil, fmt.Errorf("gcp: project ID and region are required")
	}
	listRequest := &computepb.ListForwardingRulesRequest{
		Project: gcpProvider.projectID,
		Region:  gcpProvider.region,
	}
	ruleIterator := gcpProvider.forwardingRulesClient.List(ctx, listRequest)
	return collectManagedForwardingRules(ruleIterator)
}

// EnsureRoute creates a VPC route that directs traffic for the given CIDR to
// the target instance.
func (gcpProvider *GCPCloudProvider) EnsureRoute(ctx context.Context, routeConfig RouteConfig) error {
	if gcpProvider.projectID == "" || gcpProvider.networkName == "" {
		return fmt.Errorf("gcp: project ID and network name are required for routes")
	}
	routeName := gcpRouteNameFromCIDR(routeConfig.DestinationCIDR)
	_, zone := parseGCPInstanceID(routeConfig.TargetInstanceID)
	if zone == "" {
		zone = gcpProvider.defaultZone
	}
	nextHopURL := fmt.Sprintf("projects/%s/zones/%s/instances/%s",
		gcpProvider.projectID, zone, routeConfig.TargetInstanceID)
	networkURL := fmt.Sprintf("projects/%s/global/networks/%s",
		gcpProvider.projectID, gcpProvider.networkName)
	routeResource := &computepb.Route{
		Name:             proto.String(routeName),
		Network:          proto.String(networkURL),
		DestRange:        proto.String(routeConfig.DestinationCIDR),
		NextHopInstance:  proto.String(nextHopURL),
		Priority:         proto.Uint32(gcpRoutePriority),
		Description:      proto.String("CCattler node route for " + routeConfig.TargetNodeID),
	}
	insertRequest := &computepb.InsertRouteRequest{
		Project:       gcpProvider.projectID,
		RouteResource: routeResource,
	}
	insertOperation, insertError := gcpProvider.routesClient.Insert(ctx, insertRequest)
	if insertError != nil {
		if strings.Contains(insertError.Error(), "alreadyExists") {
			return gcpProvider.replaceRoute(ctx, routeName, routeResource)
		}
		return fmt.Errorf("gcp: Insert route(%s): %w", routeConfig.DestinationCIDR, insertError)
	}
	if insertError = insertOperation.Wait(ctx); insertError != nil {
		return fmt.Errorf("gcp: wait for route insert: %w", insertError)
	}
	return nil
}

// DeleteRoute removes the VPC route for the given destination CIDR.
func (gcpProvider *GCPCloudProvider) DeleteRoute(ctx context.Context, destinationCIDR string) error {
	if gcpProvider.projectID == "" {
		return fmt.Errorf("gcp: project ID is required")
	}
	routeName := gcpRouteNameFromCIDR(destinationCIDR)
	deleteRequest := &computepb.DeleteRouteRequest{
		Project: gcpProvider.projectID,
		Route:   routeName,
	}
	deleteOperation, deleteError := gcpProvider.routesClient.Delete(ctx, deleteRequest)
	if deleteError != nil {
		return fmt.Errorf("gcp: Delete route(%s): %w", destinationCIDR, deleteError)
	}
	if deleteError = deleteOperation.Wait(ctx); deleteError != nil {
		return fmt.Errorf("gcp: wait for route delete: %w", deleteError)
	}
	return nil
}

// ListRoutes returns all CCattler-managed VPC routes in the project.
func (gcpProvider *GCPCloudProvider) ListRoutes(ctx context.Context) ([]RouteEntry, error) {
	if gcpProvider.projectID == "" {
		return nil, fmt.Errorf("gcp: project ID is required")
	}
	listRequest := &computepb.ListRoutesRequest{
		Project: gcpProvider.projectID,
		Filter:  proto.String("description:\"CCattler node route\""),
	}
	routeIterator := gcpProvider.routesClient.List(ctx, listRequest)
	return collectManagedRoutes(routeIterator)
}

// collectGCEInstances iterates over an aggregated instance list and maps each
// GCE instance to a CloudInstance value.
func collectGCEInstances(instanceIterator *compute.InstancesScopedListPairIterator) ([]CloudInstance, error) {
	var cloudInstances []CloudInstance
	for {
		scopedPair, iterError := instanceIterator.Next()
		if iterError == iterator.Done {
			break
		}
		if iterError != nil {
			return nil, fmt.Errorf("gcp: list instances: %w", iterError)
		}
		for _, gceInstance := range scopedPair.Value.GetInstances() {
			cloudInstances = append(cloudInstances, mapGCEInstanceToCloudInstance(gceInstance))
		}
	}
	return cloudInstances, nil
}

// mapGCEInstanceToCloudInstance converts a single GCE instance to a
// CloudInstance value.
func mapGCEInstanceToCloudInstance(gceInstance *computepb.Instance) CloudInstance {
	cloudInstance := CloudInstance{
		ProviderInstanceID: gceInstance.GetName(),
		State:              mapGCEStatusToInstanceState(gceInstance.GetStatus()),
		InstanceType:       extractGCEMachineTypeName(gceInstance.GetMachineType()),
		Region:             extractGCEZoneFromURL(gceInstance.GetZone()),
	}
	if nodeID, hasLabel := gceInstance.GetLabels()[gcpNodeLabelKey]; hasLabel {
		cloudInstance.NodeID = nodeID
	}
	return cloudInstance
}

// mapGCEStatusToInstanceState converts a GCE instance status string to a
// CCattler InstanceState enum value.
func mapGCEStatusToInstanceState(gceStatus string) InstanceState {
	switch gceStatus {
	case "RUNNING":
		return InstanceStateRunning
	case "PROVISIONING", "STAGING":
		return InstanceStatePending
	case "TERMINATED":
		return InstanceStateTerminated
	case "STOPPED", "SUSPENDED", "SUSPENDING", "STOPPING":
		return InstanceStateStopped
	default:
		return InstanceStatePending
	}
}

// extractGCEMachineTypeName extracts the machine type name from a full GCE URL.
func extractGCEMachineTypeName(machineTypeURL string) string {
	lastSlashIndex := strings.LastIndex(machineTypeURL, "/")
	if lastSlashIndex >= 0 {
		return machineTypeURL[lastSlashIndex+1:]
	}
	return machineTypeURL
}

// extractGCEZoneFromURL extracts the zone name from a full GCE zone URL.
func extractGCEZoneFromURL(zoneURL string) string {
	lastSlashIndex := strings.LastIndex(zoneURL, "/")
	if lastSlashIndex >= 0 {
		return zoneURL[lastSlashIndex+1:]
	}
	return zoneURL
}

// buildGCEInstanceResource constructs the GCE Instance protobuf resource for
// creating a new instance.
func buildGCEInstanceResource(gcpProvider *GCPCloudProvider, zone string, machineImage string, instanceConfig InstanceConfig) *computepb.Instance {
	instanceName := fmt.Sprintf("cca-%s-%s", sanitizeForGCPLabel(zone), fmt.Sprintf("%d", len(instanceConfig.Labels)))
	if nodeID, hasNodeID := instanceConfig.Labels[gcpNodeLabelKey]; hasNodeID {
		instanceName = gcpSanitizeResourceName("cca-", nodeID)
	}
	labels := map[string]string{
		gcpManagedLabelKey: gcpManagedLabelValue,
	}
	for labelKey, labelValue := range instanceConfig.Labels {
		labels[sanitizeForGCPLabel(labelKey)] = sanitizeForGCPLabel(labelValue)
	}
	machineType := fmt.Sprintf("zones/%s/machineTypes/%s", zone, instanceConfig.InstanceType)
	sourceImage := machineImage
	return &computepb.Instance{
		Name:        proto.String(instanceName),
		MachineType: proto.String(machineType),
		Labels:      labels,
		Disks: []*computepb.AttachedDisk{
			{
				Boot:             proto.Bool(true),
				AutoDelete:       proto.Bool(true),
				InitializeParams: &computepb.AttachedDiskInitializeParams{SourceImage: proto.String(sourceImage)},
			},
		},
		NetworkInterfaces: []*computepb.NetworkInterface{
			{
				AccessConfigs: []*computepb.AccessConfig{
					{Name: proto.String("External NAT"), Type: proto.String("ONE_TO_ONE_NAT")},
				},
			},
		},
	}
}

// parseGCPInstanceID splits a "zone/instance-name" provider ID into zone and
// name components. If no slash is present, returns empty zone and the full ID
// as the name.
func parseGCPInstanceID(providerInstanceID string) (string, string) {
	slashIndex := strings.Index(providerInstanceID, "/")
	if slashIndex >= 0 {
		return providerInstanceID[:slashIndex], providerInstanceID[slashIndex+1:]
	}
	return "", providerInstanceID
}

// ensureTargetPool creates a target pool if it does not exist, then syncs its
// instance list to match the desired backends.
func (gcpProvider *GCPCloudProvider) ensureTargetPool(ctx context.Context, targetPoolName string, loadBalancerConfig LoadBalancerConfig) error {
	getRequest := &computepb.GetTargetPoolRequest{
		Project:    gcpProvider.projectID,
		Region:     gcpProvider.region,
		TargetPool: targetPoolName,
	}
	_, getError := gcpProvider.targetPoolsClient.Get(ctx, getRequest)
	if getError != nil {
		return gcpProvider.createTargetPool(ctx, targetPoolName, loadBalancerConfig)
	}
	return nil
}

// createTargetPool creates a new target pool with the given backends.
func (gcpProvider *GCPCloudProvider) createTargetPool(ctx context.Context, targetPoolName string, loadBalancerConfig LoadBalancerConfig) error {
	var instanceURLs []string
	for _, backend := range loadBalancerConfig.Backends {
		instanceURLs = append(instanceURLs, backend.Address)
	}
	targetPoolResource := &computepb.TargetPool{
		Name:      proto.String(targetPoolName),
		Instances: instanceURLs,
	}
	insertRequest := &computepb.InsertTargetPoolRequest{
		Project:            gcpProvider.projectID,
		Region:             gcpProvider.region,
		TargetPoolResource: targetPoolResource,
	}
	insertOperation, insertError := gcpProvider.targetPoolsClient.Insert(ctx, insertRequest)
	if insertError != nil {
		return fmt.Errorf("gcp: Insert target pool(%s): %w", targetPoolName, insertError)
	}
	if insertError = insertOperation.Wait(ctx); insertError != nil {
		return fmt.Errorf("gcp: wait for target pool insert: %w", insertError)
	}
	return nil
}

// ensureForwardingRule creates a forwarding rule if it does not exist. Returns
// the external IP address.
func (gcpProvider *GCPCloudProvider) ensureForwardingRule(ctx context.Context, forwardingRuleName string, targetPoolName string, loadBalancerConfig LoadBalancerConfig) (string, error) {
	getRequest := &computepb.GetForwardingRuleRequest{
		Project:        gcpProvider.projectID,
		Region:         gcpProvider.region,
		ForwardingRule: forwardingRuleName,
	}
	existingRule, getError := gcpProvider.forwardingRulesClient.Get(ctx, getRequest)
	if getError == nil {
		return existingRule.GetIPAddress(), nil
	}
	targetPoolURL := fmt.Sprintf("projects/%s/regions/%s/targetPools/%s",
		gcpProvider.projectID, gcpProvider.region, targetPoolName)
	portRange := fmt.Sprintf("%d-%d", loadBalancerConfig.Port, loadBalancerConfig.Port)
	forwardingRuleResource := &computepb.ForwardingRule{
		Name:                proto.String(forwardingRuleName),
		Target:              proto.String(targetPoolURL),
		PortRange:           proto.String(portRange),
		IPProtocol:          proto.String("TCP"),
		LoadBalancingScheme: proto.String("EXTERNAL"),
		Labels: map[string]string{
			gcpManagedLabelKey: gcpManagedLabelValue,
			gcpServiceLabelKey: sanitizeForGCPLabel(loadBalancerConfig.ServiceName),
		},
	}
	insertRequest := &computepb.InsertForwardingRuleRequest{
		Project:                gcpProvider.projectID,
		Region:                 gcpProvider.region,
		ForwardingRuleResource: forwardingRuleResource,
	}
	insertOperation, insertError := gcpProvider.forwardingRulesClient.Insert(ctx, insertRequest)
	if insertError != nil {
		return "", fmt.Errorf("gcp: Insert forwarding rule(%s): %w", forwardingRuleName, insertError)
	}
	if insertError = insertOperation.Wait(ctx); insertError != nil {
		return "", fmt.Errorf("gcp: wait for forwarding rule insert: %w", insertError)
	}
	createdRule, getError := gcpProvider.forwardingRulesClient.Get(ctx, getRequest)
	if getError != nil {
		return "", fmt.Errorf("gcp: get forwarding rule after create: %w", getError)
	}
	return createdRule.GetIPAddress(), nil
}

// deleteForwardingRule deletes a forwarding rule by name, ignoring not-found.
func (gcpProvider *GCPCloudProvider) deleteForwardingRule(ctx context.Context, forwardingRuleName string) error {
	deleteRequest := &computepb.DeleteForwardingRuleRequest{
		Project:        gcpProvider.projectID,
		Region:         gcpProvider.region,
		ForwardingRule: forwardingRuleName,
	}
	deleteOperation, deleteError := gcpProvider.forwardingRulesClient.Delete(ctx, deleteRequest)
	if deleteError != nil {
		if strings.Contains(deleteError.Error(), "notFound") {
			return nil
		}
		return fmt.Errorf("gcp: Delete forwarding rule(%s): %w", forwardingRuleName, deleteError)
	}
	if deleteError = deleteOperation.Wait(ctx); deleteError != nil {
		return fmt.Errorf("gcp: wait for forwarding rule delete: %w", deleteError)
	}
	return nil
}

// deleteTargetPool deletes a target pool by name, ignoring not-found.
func (gcpProvider *GCPCloudProvider) deleteTargetPool(ctx context.Context, targetPoolName string) error {
	deleteRequest := &computepb.DeleteTargetPoolRequest{
		Project:    gcpProvider.projectID,
		Region:     gcpProvider.region,
		TargetPool: targetPoolName,
	}
	deleteOperation, deleteError := gcpProvider.targetPoolsClient.Delete(ctx, deleteRequest)
	if deleteError != nil {
		if strings.Contains(deleteError.Error(), "notFound") {
			return nil
		}
		return fmt.Errorf("gcp: Delete target pool(%s): %w", targetPoolName, deleteError)
	}
	if deleteError = deleteOperation.Wait(ctx); deleteError != nil {
		return fmt.Errorf("gcp: wait for target pool delete: %w", deleteError)
	}
	return nil
}

// collectManagedForwardingRules iterates forwarding rules and returns those
// with the CCattler managed label.
func collectManagedForwardingRules(ruleIterator *compute.ForwardingRuleIterator) ([]LoadBalancerStatus, error) {
	var managedLoadBalancers []LoadBalancerStatus
	for {
		forwardingRule, iterError := ruleIterator.Next()
		if iterError == iterator.Done {
			break
		}
		if iterError != nil {
			return nil, fmt.Errorf("gcp: list forwarding rules: %w", iterError)
		}
		labels := forwardingRule.GetLabels()
		if labels[gcpManagedLabelKey] != gcpManagedLabelValue {
			continue
		}
		serviceName := labels[gcpServiceLabelKey]
		if serviceName == "" {
			continue
		}
		managedLoadBalancers = append(managedLoadBalancers, LoadBalancerStatus{
			ServiceName:     serviceName,
			ExternalAddress: forwardingRule.GetIPAddress(),
			State:           LoadBalancerStateActive,
		})
	}
	return managedLoadBalancers, nil
}

// replaceRoute deletes an existing route and re-creates it with new parameters.
func (gcpProvider *GCPCloudProvider) replaceRoute(ctx context.Context, routeName string, routeResource *computepb.Route) error {
	deleteRequest := &computepb.DeleteRouteRequest{
		Project: gcpProvider.projectID,
		Route:   routeName,
	}
	deleteOperation, deleteError := gcpProvider.routesClient.Delete(ctx, deleteRequest)
	if deleteError != nil {
		return fmt.Errorf("gcp: Delete route for replace(%s): %w", routeName, deleteError)
	}
	if deleteError = deleteOperation.Wait(ctx); deleteError != nil {
		return fmt.Errorf("gcp: wait for route delete in replace: %w", deleteError)
	}
	insertRequest := &computepb.InsertRouteRequest{
		Project:       gcpProvider.projectID,
		RouteResource: routeResource,
	}
	insertOperation, insertError := gcpProvider.routesClient.Insert(ctx, insertRequest)
	if insertError != nil {
		return fmt.Errorf("gcp: re-Insert route(%s): %w", routeName, insertError)
	}
	if insertError = insertOperation.Wait(ctx); insertError != nil {
		return fmt.Errorf("gcp: wait for route re-insert: %w", insertError)
	}
	return nil
}

// collectManagedRoutes iterates routes and returns those with CCattler
// description markers.
func collectManagedRoutes(routeIterator *compute.RouteIterator) ([]RouteEntry, error) {
	var routeEntries []RouteEntry
	for {
		route, iterError := routeIterator.Next()
		if iterError == iterator.Done {
			break
		}
		if iterError != nil {
			return nil, fmt.Errorf("gcp: list routes: %w", iterError)
		}
		if !strings.Contains(route.GetDescription(), "CCattler node route") {
			continue
		}
		routeEntries = append(routeEntries, RouteEntry{
			DestinationCIDR:  route.GetDestRange(),
			TargetInstanceID: extractGCEInstanceFromURL(route.GetNextHopInstance()),
		})
	}
	return routeEntries, nil
}

// extractGCEInstanceFromURL extracts the instance name from a full GCE URL.
func extractGCEInstanceFromURL(instanceURL string) string {
	lastSlashIndex := strings.LastIndex(instanceURL, "/")
	if lastSlashIndex >= 0 {
		return instanceURL[lastSlashIndex+1:]
	}
	return instanceURL
}

// gcpRouteNameFromCIDR converts a CIDR notation into a valid GCP resource name.
func gcpRouteNameFromCIDR(cidr string) string {
	sanitized := strings.NewReplacer("/", "-", ".", "-").Replace(cidr)
	return gcpSanitizeResourceName("cca-route-", sanitized)
}

// gcpSanitizeResourceName builds a GCP resource name from a prefix and a name
// by replacing invalid characters and truncating to the GCP limit.
func gcpSanitizeResourceName(prefix string, resourceName string) string {
	sanitized := strings.ToLower(resourceName)
	sanitized = strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(sanitized)
	sanitized = strings.Trim(sanitized, "-")
	maxNamePartLength := gcpResourceNameMaxLength - len(prefix)
	if len(sanitized) > maxNamePartLength {
		sanitized = sanitized[:maxNamePartLength]
	}
	sanitized = strings.TrimRight(sanitized, "-")
	return prefix + sanitized
}

// sanitizeForGCPLabel converts a string to a valid GCP label value (lowercase,
// alphanumeric, hyphens, underscores).
func sanitizeForGCPLabel(value string) string {
	sanitized := strings.ToLower(value)
	sanitized = strings.NewReplacer("/", "-", ".", "-", ":", "-").Replace(sanitized)
	return sanitized
}
