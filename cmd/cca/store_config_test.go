// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"testing"
)

func TestResolveStoreDefaultsFromEnvVar(testContext *testing.T) {
	testContext.Setenv(endpointsEnvVariable, "http://10.0.0.1:2379")
	defaults := resolveStoreDefaults()
	if defaults.storeBackend != "etcd" {
		testContext.Errorf("expected store backend 'etcd', got %q", defaults.storeBackend)
	}
	if defaults.etcdEndpoints != "http://10.0.0.1:2379" {
		testContext.Errorf("expected endpoints 'http://10.0.0.1:2379', got %q", defaults.etcdEndpoints)
	}
}

func TestResolveStoreDefaultsFallsBackToMemory(testContext *testing.T) {
	testContext.Setenv(endpointsEnvVariable, "")
	defaults := resolveStoreDefaults()
	if os.Getenv(endpointsEnvVariable) != "" {
		testContext.Skip("CCA_ENDPOINTS is set in environment")
	}
	if defaults.storeBackend != "memory" && !fileExists(endpointsConfigPath) {
		testContext.Errorf("expected store backend 'memory' when no endpoints configured, got %q", defaults.storeBackend)
	}
}

func TestResolveStoreDefaultsForDaemonAlwaysEtcd(testContext *testing.T) {
	testContext.Setenv(endpointsEnvVariable, "")
	defaults := resolveStoreDefaultsForDaemon()
	if defaults.storeBackend != "etcd" {
		testContext.Errorf("expected daemon defaults to always be 'etcd', got %q", defaults.storeBackend)
	}
}

func TestResolveStoreDefaultsPrefixIsSet(testContext *testing.T) {
	defaults := resolveStoreDefaults()
	if defaults.storeKeyPrefix != "/ccattler/" {
		testContext.Errorf("expected prefix '/ccattler/', got %q", defaults.storeKeyPrefix)
	}
}
