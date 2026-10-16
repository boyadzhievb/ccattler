//go:build cloudprovider

// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"strings"
	"testing"
)

func TestAWSSTSAdapterRequiresRole(t *testing.T) {
	ctx := context.Background()
	adapter, adapterError := NewAWSSTSAdapter(ctx, "us-east-1", "")
	if adapterError != nil {
		t.Fatalf("NewAWSSTSAdapter failed: %v", adapterError)
	}
	_, err := adapter.ExchangeToken(ctx, "jwt", CloudIdentityConfig{
		Provider: "aws",
	})
	if err == nil || !strings.Contains(err.Error(), "role ARN is required") {
		t.Fatalf("expected role required error, got: %v", err)
	}
}
