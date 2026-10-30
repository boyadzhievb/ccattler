// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"fmt"
	"strings"
	"unicode"
)

// indentUnit is the canonical indentation — 4 spaces per nesting level.
const indentUnit = "    "

// Format serializes an AST back to canonical .cca syntax. The output uses
// 4-space indentation, deterministic block ordering, and a trailing newline.
// Format(Parse(Format(Parse(input)))) == Format(Parse(input)) (idempotent).
func Format(file *File) string {
	var builder strings.Builder
	needsBlank := false

	for _, connectionDecl := range file.Connections {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatConnectionDecl(&builder, connectionDecl)
		needsBlank = true
	}
	for _, volumeDecl := range file.Volumes {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatVolumeDecl(&builder, volumeDecl)
		needsBlank = true
	}
	for _, tenantDecl := range file.Tenants {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatTenantDecl(&builder, tenantDecl)
		needsBlank = true
	}
	if file.CredentialBroker != nil {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatCredentialBrokerDecl(&builder, *file.CredentialBroker)
		needsBlank = true
	}
	for _, cloudIdentityDecl := range file.CloudIdentities {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatCloudIdentityDecl(&builder, cloudIdentityDecl)
		needsBlank = true
	}
	if file.Cloud != nil {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatCloudDecl(&builder, *file.Cloud)
		needsBlank = true
	}
	for _, roleDecl := range file.Roles {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatRoleDecl(&builder, roleDecl)
		needsBlank = true
	}
	for _, groupDecl := range file.Groups {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatGroupDecl(&builder, groupDecl)
		needsBlank = true
	}
	for _, grantDecl := range file.Grants {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatGrantDecl(&builder, grantDecl)
		needsBlank = true
	}
	for _, policyDecl := range file.Policies {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatPolicyDecl(&builder, policyDecl)
		needsBlank = true
	}
	for _, networkDecl := range file.Networks {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatNetworkDecl(&builder, networkDecl)
		needsBlank = true
	}
	for _, serviceGroupDecl := range file.ServiceGroups {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatServiceGroupDecl(&builder, serviceGroupDecl)
		needsBlank = true
	}
	for _, serviceDecl := range file.Services {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatServiceDecl(&builder, serviceDecl)
		needsBlank = true
	}
	for _, jobDecl := range file.Jobs {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatJobDecl(&builder, jobDecl)
		needsBlank = true
	}
	for _, cronJobDecl := range file.CronJobs {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatCronJobDecl(&builder, cronJobDecl)
		needsBlank = true
	}
	for _, routeDecl := range file.Routes {
		if needsBlank {
			builder.WriteString("\n")
		}
		formatRouteDecl(&builder, routeDecl)
		needsBlank = true
	}

	result := builder.String()
	if result != "" && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result
}

// quoteIfNeeded wraps a value in double quotes if it cannot be represented as
// a bare identifier in the DSL. A bare identifier must start with a letter or
// underscore and continue with letters, digits, underscore, hyphen, dot, or
// colon — matching the lexer's isIdentifierStartChar/isIdentifierContinuationChar.
func quoteIfNeeded(value string) string {
	if value == "" {
		return `""`
	}
	for charIndex, character := range value {
		if charIndex == 0 {
			if !unicode.IsLetter(character) && character != '_' {
				return fmt.Sprintf("%q", value)
			}
		} else {
			if !unicode.IsLetter(character) && !unicode.IsDigit(character) &&
				character != '_' && character != '-' && character != '.' && character != ':' {
				return fmt.Sprintf("%q", value)
			}
		}
	}
	return value
}

// formatConnectionDecl emits a connection block.
func formatConnectionDecl(builder *strings.Builder, connectionDecl ConnectionDecl) {
	fmt.Fprintf(builder, "connection %s {\n", connectionDecl.Name)
	if connectionDecl.Driver != "" {
		fmt.Fprintf(builder, "%sdriver %s\n", indentUnit, quoteIfNeeded(connectionDecl.Driver))
	}
	if connectionDecl.User != "" {
		fmt.Fprintf(builder, "%suser %s\n", indentUnit, quoteIfNeeded(connectionDecl.User))
	}
	if connectionDecl.Secret != "" {
		fmt.Fprintf(builder, "%ssecret %s\n", indentUnit, connectionDecl.Secret)
	}
	if connectionDecl.Host != "" {
		fmt.Fprintf(builder, "%shost %s\n", indentUnit, quoteIfNeeded(connectionDecl.Host))
	}
	if connectionDecl.Port != 0 {
		fmt.Fprintf(builder, "%sport %d\n", indentUnit, connectionDecl.Port)
	}
	if connectionDecl.Database != "" {
		fmt.Fprintf(builder, "%sdatabase %s\n", indentUnit, quoteIfNeeded(connectionDecl.Database))
	}
	builder.WriteString("}\n")
}

// formatVolumeDecl emits a volume block.
func formatVolumeDecl(builder *strings.Builder, volumeDecl VolumeDecl) {
	fmt.Fprintf(builder, "volume %s {\n", volumeDecl.Name)
	if volumeDecl.Size != "" {
		fmt.Fprintf(builder, "%ssize %s\n", indentUnit, volumeDecl.Size)
	}
	if volumeDecl.Persistent {
		fmt.Fprintf(builder, "%spersistent true\n", indentUnit)
	}
	builder.WriteString("}\n")
}

// formatTenantDecl emits a tenant block with optional quota sub-block.
func formatTenantDecl(builder *strings.Builder, tenantDecl TenantDecl) {
	fmt.Fprintf(builder, "tenant %s {\n", tenantDecl.Name)
	if tenantDecl.Quota != nil {
		formatQuotaDecl(builder, *tenantDecl.Quota)
	}
	if tenantDecl.Weight != 0 {
		fmt.Fprintf(builder, "%sweight %d\n", indentUnit, tenantDecl.Weight)
	}
	builder.WriteString("}\n")
}

// formatQuotaDecl emits the quota sub-block inside a tenant.
func formatQuotaDecl(builder *strings.Builder, quotaDecl QuotaDecl) {
	fmt.Fprintf(builder, "%squota {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if quotaDecl.CPU != 0 {
		fmt.Fprintf(builder, "%scpu %d\n", indent2, quotaDecl.CPU)
	}
	if quotaDecl.Memory != "" {
		fmt.Fprintf(builder, "%smemory %s\n", indent2, quotaDecl.Memory)
	}
	if quotaDecl.Instances != 0 {
		fmt.Fprintf(builder, "%sinstances %d\n", indent2, quotaDecl.Instances)
	}
	if quotaDecl.Volumes != 0 {
		fmt.Fprintf(builder, "%svolumes %d\n", indent2, quotaDecl.Volumes)
	}
	if quotaDecl.Storage != "" {
		fmt.Fprintf(builder, "%sstorage %s\n", indent2, quotaDecl.Storage)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatCredentialBrokerDecl emits a credential_broker block.
func formatCredentialBrokerDecl(builder *strings.Builder, brokerDecl CredentialBrokerDecl) {
	builder.WriteString("credential_broker {\n")
	if brokerDecl.OIDCIssuer != "" {
		fmt.Fprintf(builder, "%soidc_issuer %s\n", indentUnit, quoteIfNeeded(brokerDecl.OIDCIssuer))
	}
	if brokerDecl.CredentialTTL != "" {
		fmt.Fprintf(builder, "%scredential_ttl %s\n", indentUnit, quoteIfNeeded(brokerDecl.CredentialTTL))
	}
	if brokerDecl.RefreshBefore != "" {
		fmt.Fprintf(builder, "%srefresh_before %s\n", indentUnit, quoteIfNeeded(brokerDecl.RefreshBefore))
	}
	builder.WriteString("}\n")
}

// formatCloudIdentityDecl emits a top-level cloud_identity block.
func formatCloudIdentityDecl(builder *strings.Builder, identityDecl CloudIdentityDecl) {
	fmt.Fprintf(builder, "cloud_identity %s {\n", identityDecl.Name)
	if identityDecl.Provider != "" {
		fmt.Fprintf(builder, "%sprovider %s\n", indentUnit, identityDecl.Provider)
	}
	if identityDecl.Role != "" {
		fmt.Fprintf(builder, "%srole %s\n", indentUnit, quoteIfNeeded(identityDecl.Role))
	}
	if identityDecl.ServiceAccount != "" {
		fmt.Fprintf(builder, "%sservice_account %s\n", indentUnit, quoteIfNeeded(identityDecl.ServiceAccount))
	}
	if identityDecl.Pool != "" {
		fmt.Fprintf(builder, "%spool %s\n", indentUnit, quoteIfNeeded(identityDecl.Pool))
	}
	if identityDecl.ClientID != "" {
		fmt.Fprintf(builder, "%sclient_id %s\n", indentUnit, quoteIfNeeded(identityDecl.ClientID))
	}
	if identityDecl.TenantID != "" {
		fmt.Fprintf(builder, "%stenant_id %s\n", indentUnit, quoteIfNeeded(identityDecl.TenantID))
	}
	builder.WriteString("}\n")
}

// formatCloudDecl emits a cloud provider configuration block.
func formatCloudDecl(builder *strings.Builder, cloudDecl CloudDecl) {
	builder.WriteString("cloud {\n")
	if cloudDecl.Provider != "" {
		fmt.Fprintf(builder, "%sprovider %s\n", indentUnit, cloudDecl.Provider)
	}
	if cloudDecl.Region != "" {
		fmt.Fprintf(builder, "%sregion %s\n", indentUnit, quoteIfNeeded(cloudDecl.Region))
	}
	if cloudDecl.InstanceType != "" {
		fmt.Fprintf(builder, "%sinstance_type %s\n", indentUnit, quoteIfNeeded(cloudDecl.InstanceType))
	}
	if cloudDecl.Credentials != "" {
		fmt.Fprintf(builder, "%scredentials %s\n", indentUnit, cloudDecl.Credentials)
	}
	if cloudDecl.ProjectID != "" {
		fmt.Fprintf(builder, "%sproject_id %s\n", indentUnit, quoteIfNeeded(cloudDecl.ProjectID))
	}
	if cloudDecl.ResourceGroup != "" {
		fmt.Fprintf(builder, "%sresource_group %s\n", indentUnit, quoteIfNeeded(cloudDecl.ResourceGroup))
	}
	if cloudDecl.VPCNetwork != "" {
		fmt.Fprintf(builder, "%svpc_network %s\n", indentUnit, quoteIfNeeded(cloudDecl.VPCNetwork))
	}
	if cloudDecl.RouteTable != "" {
		fmt.Fprintf(builder, "%sroute_table %s\n", indentUnit, quoteIfNeeded(cloudDecl.RouteTable))
	}
	builder.WriteString("}\n")
}

// formatRoleDecl emits a role block with capabilities and scopes.
func formatRoleDecl(builder *strings.Builder, roleDecl RoleDecl) {
	fmt.Fprintf(builder, "role %s {\n", roleDecl.Name)
	for _, capability := range roleDecl.Capabilities {
		fmt.Fprintf(builder, "%sallow %s\n", indentUnit, capability)
	}
	for _, scope := range roleDecl.Scopes {
		fmt.Fprintf(builder, "%sscope %s\n", indentUnit, scope)
	}
	builder.WriteString("}\n")
}

// formatGroupDecl emits an auth group block with member entries.
func formatGroupDecl(builder *strings.Builder, groupDecl GroupDecl) {
	fmt.Fprintf(builder, "group %s {\n", groupDecl.Name)
	for _, member := range groupDecl.Members {
		fmt.Fprintf(builder, "%smember %s\n", indentUnit, quoteIfNeeded(member))
	}
	builder.WriteString("}\n")
}

// formatGrantDecl emits a single-line grant statement.
func formatGrantDecl(builder *strings.Builder, grantDecl GrantDecl) {
	fmt.Fprintf(builder, "grant %s to %s %s\n", grantDecl.RoleName, grantDecl.PrincipalKind, quoteIfNeeded(grantDecl.PrincipalName))
}

// formatPolicyDecl emits a policy block with conditions.
func formatPolicyDecl(builder *strings.Builder, policyDecl PolicyDecl) {
	fmt.Fprintf(builder, "policy %s {\n", policyDecl.Name)
	if policyDecl.Capability != "" {
		fmt.Fprintf(builder, "%sallow %s\n", indentUnit, policyDecl.Capability)
	}
	for _, condition := range policyDecl.Conditions {
		fmt.Fprintf(builder, "%swhen %s %s %s\n", indentUnit, condition.Field, condition.Operator, condition.Value)
	}
	builder.WriteString("}\n")
}

// formatNetworkDecl emits a network block with allow/deny rules.
func formatNetworkDecl(builder *strings.Builder, networkDecl NetworkDecl) {
	builder.WriteString("network {\n")
	for _, rule := range networkDecl.Rules {
		if rule.Port != 0 {
			fmt.Fprintf(builder, "%s%s %s -> %s port %d\n", indentUnit, rule.Action, rule.Source, rule.Target, rule.Port)
		} else {
			fmt.Fprintf(builder, "%s%s %s -> %s\n", indentUnit, rule.Action, rule.Source, rule.Target)
		}
	}
	builder.WriteString("}\n")
}

// formatServiceGroupDecl emits a service group block.
func formatServiceGroupDecl(builder *strings.Builder, serviceGroupDecl ServiceGroupDecl) {
	fmt.Fprintf(builder, "group %s {\n", serviceGroupDecl.Name)
	for _, processName := range serviceGroupDecl.Processes {
		fmt.Fprintf(builder, "%sprocess %s\n", indentUnit, processName)
	}
	if serviceGroupDecl.ShareNetwork {
		fmt.Fprintf(builder, "%sshare network\n", indentUnit)
	}
	for _, volumeName := range serviceGroupDecl.SharedVolumes {
		fmt.Fprintf(builder, "%sshare volume %s\n", indentUnit, volumeName)
	}
	builder.WriteString("}\n")
}

// formatServiceDecl emits a service block with all sub-blocks in canonical order.
func formatServiceDecl(builder *strings.Builder, serviceDecl ServiceDecl) {
	fmt.Fprintf(builder, "service %s {\n", serviceDecl.Name)
	formatServiceIdentityFields(builder, serviceDecl)
	formatServiceNetworkFields(builder, serviceDecl)
	formatServiceInitFields(builder, serviceDecl)
	formatServiceProbeFields(builder, serviceDecl)
	formatServiceResourceFields(builder, serviceDecl)
	formatServiceScaleFields(builder, serviceDecl)
	formatServicePlacementFields(builder, serviceDecl)
	formatServiceUpdateFields(builder, serviceDecl)
	formatServiceDisruptionFields(builder, serviceDecl)
	formatServiceConfigFields(builder, serviceDecl)
	formatServiceMountFields(builder, serviceDecl)
	formatServiceTagFields(builder, serviceDecl)
	formatServicePullPolicyField(builder, serviceDecl)
	formatServiceImagePullSecretField(builder, serviceDecl)
	formatServiceLoggingFields(builder, serviceDecl)
	formatServiceLifecycleFields(builder, serviceDecl)
	formatServiceTerminationGracePeriodField(builder, serviceDecl)
	builder.WriteString("}\n")
}

// formatJobDecl emits a job block with its fields.
func formatJobDecl(builder *strings.Builder, jobDecl JobDecl) {
	fmt.Fprintf(builder, "job %s {\n", quoteIfNeeded(jobDecl.Name))
	if jobDecl.Image != "" {
		fmt.Fprintf(builder, "%simage %s\n", indentUnit, quoteIfNeeded(jobDecl.Image))
	}
	if jobDecl.Command != "" {
		fmt.Fprintf(builder, "%scommand %s\n", indentUnit, quoteIfNeeded(jobDecl.Command))
	}
	if jobDecl.Completions > 0 {
		fmt.Fprintf(builder, "%scompletions %d\n", indentUnit, jobDecl.Completions)
	}
	if jobDecl.Parallelism > 0 {
		fmt.Fprintf(builder, "%sparallelism %d\n", indentUnit, jobDecl.Parallelism)
	}
	if jobDecl.BackoffLimit > 0 {
		fmt.Fprintf(builder, "%sbackoff_limit %d\n", indentUnit, jobDecl.BackoffLimit)
	}
	if jobDecl.ImagePullSecret != "" {
		fmt.Fprintf(builder, "%simage_pull_secret %s\n", indentUnit, quoteIfNeeded(jobDecl.ImagePullSecret))
	}
	builder.WriteString("}\n")
}

// formatCronJobDecl emits a cronjob block with its fields.
func formatCronJobDecl(builder *strings.Builder, cronJobDecl CronJobDecl) {
	fmt.Fprintf(builder, "cronjob %s {\n", quoteIfNeeded(cronJobDecl.Name))
	fmt.Fprintf(builder, "%sschedule %s\n", indentUnit, quoteIfNeeded(cronJobDecl.Schedule))
	if cronJobDecl.Image != "" {
		fmt.Fprintf(builder, "%simage %s\n", indentUnit, quoteIfNeeded(cronJobDecl.Image))
	}
	if cronJobDecl.Command != "" {
		fmt.Fprintf(builder, "%scommand %s\n", indentUnit, quoteIfNeeded(cronJobDecl.Command))
	}
	if cronJobDecl.Completions > 0 {
		fmt.Fprintf(builder, "%scompletions %d\n", indentUnit, cronJobDecl.Completions)
	}
	if cronJobDecl.Parallelism > 0 {
		fmt.Fprintf(builder, "%sparallelism %d\n", indentUnit, cronJobDecl.Parallelism)
	}
	if cronJobDecl.BackoffLimit > 0 {
		fmt.Fprintf(builder, "%sbackoff_limit %d\n", indentUnit, cronJobDecl.BackoffLimit)
	}
	builder.WriteString("}\n")
}

// formatServiceIdentityFields emits image, command, instances, owner, stateful.
func formatServiceIdentityFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Image != "" {
		fmt.Fprintf(builder, "%simage %s\n", indentUnit, quoteIfNeeded(serviceDecl.Image))
	}
	if serviceDecl.Command != "" {
		fmt.Fprintf(builder, "%scommand %s\n", indentUnit, quoteIfNeeded(serviceDecl.Command))
	}
	fmt.Fprintf(builder, "%sinstances %d\n", indentUnit, serviceDecl.Instances)
	if serviceDecl.Owner != "" {
		fmt.Fprintf(builder, "%sowner %s\n", indentUnit, serviceDecl.Owner)
	}
	if serviceDecl.Stateful {
		fmt.Fprintf(builder, "%sstateful\n", indentUnit)
	}
}

// formatServiceNetworkFields emits expose and expose external declarations.
func formatServiceNetworkFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	for _, port := range serviceDecl.Ports {
		fmt.Fprintf(builder, "%sexpose %d\n", indentUnit, port)
	}
	for _, externalPort := range serviceDecl.ExternalPorts {
		if externalPort.Protocol != "" && externalPort.Protocol != "tcp" {
			fmt.Fprintf(builder, "%sexpose external %d %s\n", indentUnit, externalPort.Port, externalPort.Protocol)
		} else {
			fmt.Fprintf(builder, "%sexpose external %d\n", indentUnit, externalPort.Port)
		}
	}
}

// formatServiceInitFields emits init step blocks.
func formatServiceInitFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	for _, initStep := range serviceDecl.InitSteps {
		fmt.Fprintf(builder, "%sinit {\n", indentUnit)
		indent2 := indentUnit + indentUnit
		if initStep.Exec != "" {
			fmt.Fprintf(builder, "%sexec %s\n", indent2, quoteIfNeeded(initStep.Exec))
		}
		if initStep.Connection != "" {
			fmt.Fprintf(builder, "%sconnection %s\n", indent2, initStep.Connection)
		}
		if initStep.Timeout != "" {
			fmt.Fprintf(builder, "%stimeout %s\n", indent2, initStep.Timeout)
		}
		if initStep.Retry != 0 {
			fmt.Fprintf(builder, "%sretry %d\n", indent2, initStep.Retry)
		}
		fmt.Fprintf(builder, "%s}\n", indentUnit)
	}
}

// formatServiceProbeFields emits health, startup, liveness, readiness blocks.
func formatServiceProbeFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Health != nil {
		formatHealthBlock(builder, *serviceDecl.Health)
	}
	if serviceDecl.Startup != nil {
		formatProbeBlock(builder, "startup", *serviceDecl.Startup)
	}
	if serviceDecl.Liveness != nil {
		formatProbeBlock(builder, "liveness", *serviceDecl.Liveness)
	}
	if serviceDecl.Readiness != nil {
		formatProbeBlock(builder, "readiness", *serviceDecl.Readiness)
	}
}

// formatHealthBlock emits a legacy health block.
func formatHealthBlock(builder *strings.Builder, healthDecl HealthDecl) {
	fmt.Fprintf(builder, "%shealth {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if healthDecl.Method == "http" && healthDecl.Path != "" {
		fmt.Fprintf(builder, "%shttp %s\n", indent2, healthDecl.Path)
	} else if healthDecl.Method != "" {
		fmt.Fprintf(builder, "%s%s\n", indent2, healthDecl.Method)
	}
	if healthDecl.Interval != "" {
		fmt.Fprintf(builder, "%severy %s\n", indent2, healthDecl.Interval)
	}
	if healthDecl.Timeout != "" {
		fmt.Fprintf(builder, "%stimeout %s\n", indent2, healthDecl.Timeout)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatProbeBlock emits a startup, liveness, or readiness probe block.
func formatProbeBlock(builder *strings.Builder, probeName string, probeDecl ProbeDecl) {
	fmt.Fprintf(builder, "%s%s {\n", indentUnit, probeName)
	indent2 := indentUnit + indentUnit
	switch {
	case probeDecl.Method == "http" && probeDecl.Path != "":
		fmt.Fprintf(builder, "%shttp %s\n", indent2, probeDecl.Path)
	case probeDecl.Method == "exec" && probeDecl.Path != "":
		fmt.Fprintf(builder, "%sexec %s\n", indent2, quoteIfNeeded(probeDecl.Path))
	case probeDecl.Method != "":
		fmt.Fprintf(builder, "%s%s\n", indent2, probeDecl.Method)
	}
	if probeDecl.Port != 0 {
		fmt.Fprintf(builder, "%sport %d\n", indent2, probeDecl.Port)
	}
	if probeDecl.Interval != "" {
		fmt.Fprintf(builder, "%severy %s\n", indent2, probeDecl.Interval)
	}
	if probeDecl.Timeout != "" {
		fmt.Fprintf(builder, "%stimeout %s\n", indent2, probeDecl.Timeout)
	}
	if probeDecl.FailureThreshold != 3 {
		fmt.Fprintf(builder, "%sfailure_threshold %d\n", indent2, probeDecl.FailureThreshold)
	}
	if probeDecl.SuccessThreshold != 1 {
		fmt.Fprintf(builder, "%ssuccess_threshold %d\n", indent2, probeDecl.SuccessThreshold)
	}
	if probeDecl.InitialDelay != "" {
		fmt.Fprintf(builder, "%sinitial_delay %s\n", indent2, probeDecl.InitialDelay)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceResourceFields emits the resources block.
func formatServiceResourceFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Resources == nil {
		return
	}
	fmt.Fprintf(builder, "%sresources {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if serviceDecl.Resources.CPU != "" {
		fmt.Fprintf(builder, "%scpu %s\n", indent2, serviceDecl.Resources.CPU)
	}
	if serviceDecl.Resources.Memory != "" {
		fmt.Fprintf(builder, "%smemory %s\n", indent2, serviceDecl.Resources.Memory)
	}
	if serviceDecl.Resources.CPULimit != "" {
		fmt.Fprintf(builder, "%scpu_limit %s\n", indent2, serviceDecl.Resources.CPULimit)
	}
	if serviceDecl.Resources.MemoryLimit != "" {
		fmt.Fprintf(builder, "%smemory_limit %s\n", indent2, serviceDecl.Resources.MemoryLimit)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceScaleFields emits the scale block with horizontal/vertical sub-blocks.
func formatServiceScaleFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Scale == nil {
		return
	}
	fmt.Fprintf(builder, "%sscale {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if serviceDecl.Scale.Horizontal != nil {
		formatHorizontalScale(builder, *serviceDecl.Scale.Horizontal, indent2)
	}
	if serviceDecl.Scale.Vertical != nil {
		formatVerticalScale(builder, *serviceDecl.Scale.Vertical, indent2)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatHorizontalScale emits the horizontal scaling sub-block.
func formatHorizontalScale(builder *strings.Builder, horizontalDecl HorizontalScaleDecl, indent string) {
	fmt.Fprintf(builder, "%shorizontal {\n", indent)
	indent3 := indent + indentUnit
	if horizontalDecl.Min != 0 {
		fmt.Fprintf(builder, "%smin %d\n", indent3, horizontalDecl.Min)
	}
	if horizontalDecl.Max != 0 {
		fmt.Fprintf(builder, "%smax %d\n", indent3, horizontalDecl.Max)
	}
	for _, target := range horizontalDecl.Targets {
		fmt.Fprintf(builder, "%starget %s = %d\n", indent3, target.Metric, target.Value)
	}
	for _, event := range horizontalDecl.Events {
		fmt.Fprintf(builder, "%sevent %s = %d\n", indent3, event.Source, event.Target)
	}
	if horizontalDecl.IdleTimeout != "" {
		fmt.Fprintf(builder, "%sidle_timeout %s\n", indent3, horizontalDecl.IdleTimeout)
	}
	if horizontalDecl.ActivationTimeout != "" {
		fmt.Fprintf(builder, "%sactivation_timeout %s\n", indent3, horizontalDecl.ActivationTimeout)
	}
	if horizontalDecl.Schedule != nil {
		formatSchedule(builder, *horizontalDecl.Schedule, indent3)
	}
	if horizontalDecl.Stabilization != nil {
		formatStabilization(builder, *horizontalDecl.Stabilization, indent3)
	}
	fmt.Fprintf(builder, "%s}\n", indent)
}

// formatSchedule emits a schedule sub-block inside horizontal scaling.
func formatSchedule(builder *strings.Builder, scheduleDecl ScheduleDecl, indent string) {
	fmt.Fprintf(builder, "%sschedule {\n", indent)
	indent4 := indent + indentUnit
	if scheduleDecl.Days != "" {
		fmt.Fprintf(builder, "%sdays %s\n", indent4, scheduleDecl.Days)
	}
	if scheduleDecl.Start != "" {
		fmt.Fprintf(builder, "%sstart %s\n", indent4, quoteIfNeeded(scheduleDecl.Start))
	}
	if scheduleDecl.End != "" {
		fmt.Fprintf(builder, "%send %s\n", indent4, quoteIfNeeded(scheduleDecl.End))
	}
	if scheduleDecl.Minimum != 0 {
		fmt.Fprintf(builder, "%sminimum %d\n", indent4, scheduleDecl.Minimum)
	}
	fmt.Fprintf(builder, "%s}\n", indent)
}

// formatStabilization emits a stabilization sub-block inside horizontal scaling.
func formatStabilization(builder *strings.Builder, stabilizationDecl StabilizationDecl, indent string) {
	fmt.Fprintf(builder, "%sstabilization {\n", indent)
	indent4 := indent + indentUnit
	if stabilizationDecl.ScaleUp != "" {
		fmt.Fprintf(builder, "%sscale_up %s\n", indent4, stabilizationDecl.ScaleUp)
	}
	if stabilizationDecl.ScaleDown != "" {
		fmt.Fprintf(builder, "%sscale_down %s\n", indent4, stabilizationDecl.ScaleDown)
	}
	fmt.Fprintf(builder, "%s}\n", indent)
}

// formatVerticalScale emits the vertical scaling sub-block.
func formatVerticalScale(builder *strings.Builder, verticalDecl VerticalScaleDecl, indent string) {
	fmt.Fprintf(builder, "%svertical {\n", indent)
	indent3 := indent + indentUnit
	if verticalDecl.CPUMin != "" || verticalDecl.CPUMax != "" {
		fmt.Fprintf(builder, "%scpu { min %s max %s }\n", indent3, verticalDecl.CPUMin, verticalDecl.CPUMax)
	}
	if verticalDecl.MemoryMin != "" || verticalDecl.MemoryMax != "" {
		fmt.Fprintf(builder, "%smemory { min %s max %s }\n", indent3, verticalDecl.MemoryMin, verticalDecl.MemoryMax)
	}
	fmt.Fprintf(builder, "%s}\n", indent)
}

// formatServicePlacementFields emits the placement block.
func formatServicePlacementFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Placement == nil {
		return
	}
	fmt.Fprintf(builder, "%splacement {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	placementDecl := serviceDecl.Placement
	if placementDecl.Architecture != "" {
		fmt.Fprintf(builder, "%sarchitecture %s\n", indent2, placementDecl.Architecture)
	}
	if placementDecl.ZonePolicy != "" {
		if placementDecl.ZoneMaxSkew > 0 {
			fmt.Fprintf(builder, "%szone %s maxSkew %d\n", indent2, placementDecl.ZonePolicy, placementDecl.ZoneMaxSkew)
		} else {
			fmt.Fprintf(builder, "%szone %s\n", indent2, placementDecl.ZonePolicy)
		}
	}
	for _, requireRule := range placementDecl.Require {
		fmt.Fprintf(builder, "%srequire %s = %s\n", indent2, requireRule.Label, quoteIfNeeded(requireRule.Value))
	}
	for _, preferRule := range placementDecl.Prefer {
		fmt.Fprintf(builder, "%sprefer %s = %s\n", indent2, preferRule.Label, quoteIfNeeded(preferRule.Value))
	}
	for _, acceptLabel := range placementDecl.Accept {
		fmt.Fprintf(builder, "%saccept %s\n", indent2, acceptLabel)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceUpdateFields emits the update block.
func formatServiceUpdateFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Update == nil {
		return
	}
	fmt.Fprintf(builder, "%supdate {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if serviceDecl.Update.MaxUnavailable != 0 {
		fmt.Fprintf(builder, "%smax_unavailable %d\n", indent2, serviceDecl.Update.MaxUnavailable)
	}
	if serviceDecl.Update.MaxExtra != 0 {
		fmt.Fprintf(builder, "%smax_extra %d\n", indent2, serviceDecl.Update.MaxExtra)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceDisruptionFields emits the disruption block.
func formatServiceDisruptionFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Disruption == nil {
		return
	}
	fmt.Fprintf(builder, "%sdisruption {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if serviceDecl.Disruption.MinAvailable != 0 {
		fmt.Fprintf(builder, "%smin_available %d\n", indent2, serviceDecl.Disruption.MinAvailable)
	}
	if serviceDecl.Disruption.MaxUnavailable != 0 {
		fmt.Fprintf(builder, "%smax_unavailable %d\n", indent2, serviceDecl.Disruption.MaxUnavailable)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceConfigFields emits the config block with env and file entries.
func formatServiceConfigFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Config == nil {
		return
	}
	fmt.Fprintf(builder, "%sconfig {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	for _, envVar := range serviceDecl.Config.EnvVars {
		fmt.Fprintf(builder, "%senv %s %s\n", indent2, envVar.Name, quoteIfNeeded(envVar.Value))
	}
	for _, configFile := range serviceDecl.Config.ConfigFiles {
		if configFile.FromFile != "" {
			fmt.Fprintf(builder, "%sfile %s from %s\n", indent2, quoteIfNeeded(configFile.Path), quoteIfNeeded(configFile.FromFile))
		} else {
			fmt.Fprintf(builder, "%sfile %s %s\n", indent2, quoteIfNeeded(configFile.Path), quoteIfNeeded(configFile.Content))
		}
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceMountFields emits secret, connection, volume, and cloud_identity.
func formatServiceMountFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	for _, secretDecl := range serviceDecl.Secrets {
		if secretDecl.MountPath != "" {
			fmt.Fprintf(builder, "%ssecret %s %s\n", indentUnit, secretDecl.Name, quoteIfNeeded(secretDecl.MountPath))
		} else {
			fmt.Fprintf(builder, "%ssecret %s\n", indentUnit, secretDecl.Name)
		}
	}
	for _, connectionRef := range serviceDecl.Connections {
		if connectionRef.MountPath != "" {
			fmt.Fprintf(builder, "%sconnection %s %s\n", indentUnit, connectionRef.ConnectionName, quoteIfNeeded(connectionRef.MountPath))
		} else {
			fmt.Fprintf(builder, "%sconnection %s\n", indentUnit, connectionRef.ConnectionName)
		}
	}
	for _, volumeMount := range serviceDecl.VolumeMounts {
		fmt.Fprintf(builder, "%svolume %s %s\n", indentUnit, volumeMount.VolumeName, volumeMount.MountPath)
	}
	for _, cloudBinding := range serviceDecl.CloudIdentities {
		fmt.Fprintf(builder, "%scloud_identity %s {\n", indentUnit, cloudBinding.IdentityName)
		indent2 := indentUnit + indentUnit
		if cloudBinding.MountPath != "" {
			fmt.Fprintf(builder, "%smount_path %s\n", indent2, quoteIfNeeded(cloudBinding.MountPath))
		}
		if cloudBinding.DeliverMode != "" {
			fmt.Fprintf(builder, "%sdeliver %s\n", indent2, cloudBinding.DeliverMode)
		}
		fmt.Fprintf(builder, "%s}\n", indentUnit)
	}
}

// formatServiceTagFields emits the tags block.
func formatServiceTagFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if len(serviceDecl.Tags) == 0 {
		return
	}
	fmt.Fprintf(builder, "%stags {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	for _, tagDecl := range serviceDecl.Tags {
		fmt.Fprintf(builder, "%s%s %s\n", indent2, tagDecl.Key, quoteIfNeeded(tagDecl.Value))
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServicePullPolicyField emits the pull_policy field.
func formatServicePullPolicyField(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.PullPolicy != "" {
		fmt.Fprintf(builder, "%spull_policy %s\n", indentUnit, serviceDecl.PullPolicy)
	}
}

// formatServiceImagePullSecretField emits the image_pull_secret field.
func formatServiceImagePullSecretField(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.ImagePullSecret != "" {
		fmt.Fprintf(builder, "%simage_pull_secret %s\n", indentUnit, quoteIfNeeded(serviceDecl.ImagePullSecret))
	}
}

// formatServiceLoggingFields emits the logging block.
func formatServiceLoggingFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Logging == nil {
		return
	}
	fmt.Fprintf(builder, "%slogging {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if serviceDecl.Logging.MaxSize != "" {
		fmt.Fprintf(builder, "%smax_size %s\n", indent2, quoteIfNeeded(serviceDecl.Logging.MaxSize))
	}
	if serviceDecl.Logging.MaxFiles > 0 {
		fmt.Fprintf(builder, "%smax_files %d\n", indent2, serviceDecl.Logging.MaxFiles)
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceLifecycleFields emits the lifecycle block.
func formatServiceLifecycleFields(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.Lifecycle == nil {
		return
	}
	fmt.Fprintf(builder, "%slifecycle {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if serviceDecl.Lifecycle.PreStop != "" {
		fmt.Fprintf(builder, "%spre_stop %s\n", indent2, quoteIfNeeded(serviceDecl.Lifecycle.PreStop))
	}
	if serviceDecl.Lifecycle.PostStart != "" {
		fmt.Fprintf(builder, "%spost_start %s\n", indent2, quoteIfNeeded(serviceDecl.Lifecycle.PostStart))
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}

// formatServiceTerminationGracePeriodField emits the termination_grace_period field.
func formatServiceTerminationGracePeriodField(builder *strings.Builder, serviceDecl ServiceDecl) {
	if serviceDecl.TerminationGracePeriod != "" {
		fmt.Fprintf(builder, "%stermination_grace_period %s\n", indentUnit, serviceDecl.TerminationGracePeriod)
	}
}

// formatRouteDecl emits a gate block with optional sticky session config.
func formatRouteDecl(builder *strings.Builder, routeDecl RouteDecl) {
	fmt.Fprintf(builder, "gate %s {\n", routeDecl.Name)
	if routeDecl.Host != "" {
		fmt.Fprintf(builder, "%shost %s\n", indentUnit, quoteIfNeeded(routeDecl.Host))
	}
	if routeDecl.Path != "" {
		fmt.Fprintf(builder, "%spath %s\n", indentUnit, quoteIfNeeded(routeDecl.Path))
	}
	if routeDecl.ServicePort != 0 {
		fmt.Fprintf(builder, "%sservice %s port %d\n", indentUnit, routeDecl.ServiceName, routeDecl.ServicePort)
	} else {
		fmt.Fprintf(builder, "%sservice %s\n", indentUnit, routeDecl.ServiceName)
	}
	if routeDecl.Sticky != nil {
		formatRouteStickyDecl(builder, *routeDecl.Sticky)
	}
	builder.WriteString("}\n")
}

// formatRouteStickyDecl emits the sticky session configuration inside a route.
func formatRouteStickyDecl(builder *strings.Builder, stickyDecl RouteStickyDecl) {
	if stickyDecl.Method == "source-ip" {
		fmt.Fprintf(builder, "%ssticky source-ip\n", indentUnit)
		return
	}
	if stickyDecl.Cookie == nil {
		fmt.Fprintf(builder, "%ssticky cookie\n", indentUnit)
		return
	}
	fmt.Fprintf(builder, "%ssticky cookie {\n", indentUnit)
	indent2 := indentUnit + indentUnit
	if stickyDecl.Cookie.Name != "" {
		fmt.Fprintf(builder, "%sname %s\n", indent2, quoteIfNeeded(stickyDecl.Cookie.Name))
	}
	if stickyDecl.Cookie.Secure {
		fmt.Fprintf(builder, "%ssecure true\n", indent2)
	}
	if stickyDecl.Cookie.HTTPOnly {
		fmt.Fprintf(builder, "%shttp-only true\n", indent2)
	}
	if stickyDecl.Cookie.SameSite != "" {
		fmt.Fprintf(builder, "%ssame-site %s\n", indent2, quoteIfNeeded(stickyDecl.Cookie.SameSite))
	}
	fmt.Fprintf(builder, "%s}\n", indentUnit)
}
