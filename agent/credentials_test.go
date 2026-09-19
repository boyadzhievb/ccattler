package agent

import (
	"strings"
	"testing"
	"time"
)

func TestAWSCredentialFileFormat(t *testing.T) {
	credential := &MaterializedCredential{
		Provider:     "aws",
		AccessKeyID:  "AKIAIOSFODNN7EXAMPLE",
		SecretKey:    "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		SessionToken: "FwoGZXIvYXdzEBYaDH...",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		DeliverMode:  "credentials",
		MountPath:    "/var/run/cloud-creds",
	}

	content, filename, err := credential.CredentialFileContent()
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if filename != "credentials" {
		t.Errorf("filename: got %q, want credentials", filename)
	}
	if !strings.Contains(content, "[default]") {
		t.Error("expected [default] section header")
	}
	if !strings.Contains(content, "aws_access_key_id = AKIAIOSFODNN7EXAMPLE") {
		t.Error("expected access key in content")
	}
	if !strings.Contains(content, "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY") {
		t.Error("expected secret key in content")
	}
	if !strings.Contains(content, "aws_session_token = FwoGZXIvYXdzEBYaDH...") {
		t.Error("expected session token in content")
	}
}

func TestGCPCredentialFileFormat(t *testing.T) {
	credential := &MaterializedCredential{
		Provider:     "gcp",
		SessionToken: "ya29.test-access-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		DeliverMode:  "credentials",
		MountPath:    "/var/run/cloud-creds",
	}

	content, filename, err := credential.CredentialFileContent()
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if filename != "application_default_credentials.json" {
		t.Errorf("filename: got %q, want application_default_credentials.json", filename)
	}
	if !strings.Contains(content, "external_account") {
		t.Error("expected external_account type in content")
	}
	if !strings.Contains(content, "/var/run/cloud-creds/token") {
		t.Error("expected token file path in credential source")
	}
}

func TestAzureCredentialFileFormat(t *testing.T) {
	credential := &MaterializedCredential{
		Provider:     "azure",
		SessionToken: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		DeliverMode:  "credentials",
		MountPath:    "/var/run/cloud-creds",
	}

	content, filename, err := credential.CredentialFileContent()
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if filename != "azure-token" {
		t.Errorf("filename: got %q, want azure-token", filename)
	}
	if content != "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test" {
		t.Errorf("expected raw token content")
	}
}

func TestTokenProjectionMode(t *testing.T) {
	credential := &MaterializedCredential{
		Provider:     "aws",
		AccessKeyID:  "AKIATEST",
		SecretKey:    "secret",
		SessionToken: "raw-jwt-token-here",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		DeliverMode:  "token",
		MountPath:    "/var/run/cloud-creds",
	}

	content, filename, err := credential.CredentialFileContent()
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if filename != "token" {
		t.Errorf("filename: got %q, want token", filename)
	}
	if content != "raw-jwt-token-here" {
		t.Errorf("token mode should return raw session token, got %q", content)
	}
}

func TestUnknownProviderError(t *testing.T) {
	credential := &MaterializedCredential{
		Provider:    "oracle",
		DeliverMode: "credentials",
	}

	_, _, err := credential.CredentialFileContent()
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestTrackedCredentialFields(t *testing.T) {
	tracked := TrackedCredential{
		InstanceID:   "inst-1",
		IdentityName: "payments_s3",
		MountPath:    "/var/run/cloud-creds",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}
	if tracked.InstanceID != "inst-1" {
		t.Error("instance ID mismatch")
	}
	if tracked.IdentityName != "payments_s3" {
		t.Error("identity name mismatch")
	}
}
