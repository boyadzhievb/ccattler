//go:build cloudprovider

// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// awsDefaultSTSRegion is the region used for STS calls when none is specified.
const awsDefaultSTSRegion = "us-east-1"

// awsSTSSessionNamePrefix is prepended to instance IDs for STS session names.
const awsSTSSessionNamePrefix = "ccattler-"

// awsSTSCredentialDurationSeconds is the default requested credential lifetime.
const awsSTSCredentialDurationSeconds = 3600

// AWSSTSAdapter exchanges CCattler JWTs for AWS temporary credentials using
// the STS AssumeRoleWithWebIdentity API via the AWS SDK v2.
type AWSSTSAdapter struct {
	stsClient *sts.Client // stsClient is the AWS STS API client.
	region    string      // region is the AWS region for STS calls.
}

// NewAWSSTSAdapter creates an AWS STS adapter. The region defaults to
// "us-east-1" if empty. Loads credentials from the default AWS credential
// chain. Returns an error if the SDK config cannot be loaded.
func NewAWSSTSAdapter(ctx context.Context, region string, stsEndpoint string) (*AWSSTSAdapter, error) {
	if region == "" {
		region = awsDefaultSTSRegion
	}
	sdkConfig, loadError := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if loadError != nil {
		return nil, fmt.Errorf("aws-sts: load SDK config: %w", loadError)
	}
	var stsOptions []func(*sts.Options)
	if stsEndpoint != "" {
		stsOptions = append(stsOptions, func(options *sts.Options) {
			options.BaseEndpoint = &stsEndpoint
		})
	}
	return &AWSSTSAdapter{
		stsClient: sts.NewFromConfig(sdkConfig, stsOptions...),
		region:    region,
	}, nil
}

// ExchangeToken calls AWS STS AssumeRoleWithWebIdentity to exchange the JWT
// for temporary AWS credentials bound to the configured IAM role. Returns a
// CloudCredential with the temporary access key, secret key, and session token.
func (adapter *AWSSTSAdapter) ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error) {
	if identityConfig.Role == "" {
		return nil, fmt.Errorf("aws: role ARN is required")
	}
	sessionName := awsSTSSessionNamePrefix + identityConfig.Name
	assumeInput := &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          &identityConfig.Role,
		RoleSessionName:  &sessionName,
		WebIdentityToken: &jwtToken,
		DurationSeconds:  intPtr(awsSTSCredentialDurationSeconds),
	}
	assumeOutput, assumeError := adapter.stsClient.AssumeRoleWithWebIdentity(ctx, assumeInput)
	if assumeError != nil {
		return nil, fmt.Errorf("aws: AssumeRoleWithWebIdentity(role=%s): %w", identityConfig.Role, assumeError)
	}
	if assumeOutput.Credentials == nil {
		return nil, fmt.Errorf("aws: AssumeRoleWithWebIdentity returned nil credentials")
	}
	return &CloudCredential{
		Provider:     "aws",
		AccessKeyID:  derefString(assumeOutput.Credentials.AccessKeyId),
		SecretKey:    derefString(assumeOutput.Credentials.SecretAccessKey),
		SessionToken: derefString(assumeOutput.Credentials.SessionToken),
		ExpiresAt:    derefTime(assumeOutput.Credentials.Expiration),
	}, nil
}

// ProviderName returns "aws".
func (adapter *AWSSTSAdapter) ProviderName() string {
	return "aws"
}

// intPtr returns a pointer to the given int32 value.
func intPtr(value int32) *int32 {
	return &value
}

// derefString returns the string value of a pointer, or empty string if nil.
func derefString(stringPtr *string) string {
	if stringPtr == nil {
		return ""
	}
	return *stringPtr
}

// derefTime returns the time value of a pointer, or zero time if nil.
func derefTime(timePtr *time.Time) time.Time {
	if timePtr == nil {
		return time.Time{}
	}
	return *timePtr
}
