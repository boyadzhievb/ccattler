// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateContextConfigMigratesLegacy(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := overrideHomeDir(t, tempDir)
	defer restoreHomeDir(originalHome)

	legacyCredential := credentialFile{ //nolint:gosec // test data
		Server:       "192.168.1.100:9770",
		Token:        "test-jwt-token",
		User:         "user:alice",
		Team:         "platform",
		Expires:      "2026-11-01T00:00:00Z",
		OIDCIssuer:   "https://accounts.google.com",
		OIDCClientID: "test-client-id",
	}
	writeLegacyCredential(t, tempDir, legacyCredential)

	config := loadOrCreateContextConfig()

	if config.CurrentContext != defaultContextName {
		t.Errorf("current_context = %q, want %q", config.CurrentContext, defaultContextName)
	}
	entry, exists := config.Contexts[defaultContextName]
	if !exists {
		t.Fatal("default context not created during migration")
	}
	if entry.Server != "192.168.1.100:9770" {
		t.Errorf("server = %q", entry.Server)
	}
	if entry.Token != "test-jwt-token" {
		t.Errorf("token not migrated")
	}
	if entry.AuthMethod != "oidc" {
		t.Errorf("auth_method = %q, want %q", entry.AuthMethod, "oidc")
	}
	if entry.OIDCIssuer != "https://accounts.google.com" {
		t.Errorf("oidc_issuer not migrated")
	}

	configPath := filepath.Join(tempDir, ".ccattler", configFileName)
	if _, statError := os.Stat(configPath); os.IsNotExist(statError) {
		t.Error("config.json should have been written during migration")
	}
}

func TestLoadOrCreateContextConfigReadsExisting(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := overrideHomeDir(t, tempDir)
	defer restoreHomeDir(originalHome)

	config := contextConfig{
		CurrentContext: "production",
		Contexts: map[string]contextEntry{
			"production": {
				Server:     "prod.example.com:9770",
				AuthMethod: "oidc",
				User:       "user:bob",
			},
			"staging": {
				Server:     "staging.example.com:9770",
				AuthMethod: "token",
				User:       "user:bob",
			},
		},
	}
	writeTestContextConfig(t, tempDir, config)

	loaded := loadOrCreateContextConfig()

	if loaded.CurrentContext != "production" {
		t.Errorf("current_context = %q, want %q", loaded.CurrentContext, "production")
	}
	if len(loaded.Contexts) != 2 {
		t.Errorf("context count = %d, want 2", len(loaded.Contexts))
	}
}

func TestContextSwitching(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := overrideHomeDir(t, tempDir)
	defer restoreHomeDir(originalHome)

	config := contextConfig{
		CurrentContext: "dev",
		Contexts: map[string]contextEntry{
			"dev": {
				Server:     "dev.example.com:9770",
				AuthMethod: "token",
				Token:      "dev-token",
				User:       "user:alice",
			},
			"prod": {
				Server:     "prod.example.com:9770",
				AuthMethod: "oidc",
				Token:      "prod-token",
				User:       "user:alice",
			},
		},
	}
	writeTestContextConfig(t, tempDir, config)

	loaded := loadOrCreateContextConfig()
	if loaded.CurrentContext != "dev" {
		t.Fatalf("current_context = %q, want %q", loaded.CurrentContext, "dev")
	}

	loaded.CurrentContext = "prod"
	writeContextConfig(loaded)

	reloaded := loadOrCreateContextConfig()
	if reloaded.CurrentContext != "prod" {
		t.Errorf("after switch, current_context = %q, want %q", reloaded.CurrentContext, "prod")
	}
}

func TestContextDeleteRemovesEntry(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := overrideHomeDir(t, tempDir)
	defer restoreHomeDir(originalHome)

	config := contextConfig{
		CurrentContext: "alpha",
		Contexts: map[string]contextEntry{
			"alpha": {Server: "alpha:9770", User: "user:a"},
			"beta":  {Server: "beta:9770", User: "user:b"},
		},
	}
	writeTestContextConfig(t, tempDir, config)

	loaded := loadOrCreateContextConfig()
	delete(loaded.Contexts, "alpha")
	if loaded.CurrentContext == "alpha" {
		loaded.CurrentContext = ""
		for name := range loaded.Contexts {
			loaded.CurrentContext = name
			break
		}
	}
	writeContextConfig(loaded)

	reloaded := loadOrCreateContextConfig()
	if _, exists := reloaded.Contexts["alpha"]; exists {
		t.Error("alpha context should have been deleted")
	}
	if reloaded.CurrentContext != "beta" {
		t.Errorf("current_context = %q, want %q after deletion", reloaded.CurrentContext, "beta")
	}
}

func TestDeriveContextNameFromServer(t *testing.T) {
	testCases := []struct {
		serverAddress       string
		expectedContextName string
	}{
		{"192.168.100.215:9770", "192-168-100-215"},
		{"prod.example.com:9770", "prod-example-com"},
		{"127.0.0.1:9770", "default"},
		{"localhost:9770", "default"},
		{":9770", "default"},
	}

	for _, testCase := range testCases {
		result := deriveContextNameFromServer(testCase.serverAddress)
		if result != testCase.expectedContextName {
			t.Errorf("deriveContextNameFromServer(%q) = %q, want %q",
				testCase.serverAddress, result, testCase.expectedContextName)
		}
	}
}

func TestUpdateContextFromLogin(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := overrideHomeDir(t, tempDir)
	defer restoreHomeDir(originalHome)

	config := contextConfig{
		CurrentContext: "default",
		Contexts:       map[string]contextEntry{},
	}
	writeTestContextConfig(t, tempDir, config)

	credential := credentialFile{
		Server:       "prod.example.com:9770",
		Token:        "new-token",
		RefreshToken: "new-refresh",
		User:         "user:charlie",
		Team:         "backend",
		Expires:      "2026-12-01T00:00:00Z",
		OIDCIssuer:   "https://auth.example.com",
		OIDCClientID: "prod-client",
	}

	updateContextFromLogin("prod.example.com:9770", credential)

	loaded := loadOrCreateContextConfig()
	if loaded.CurrentContext != "prod-example-com" {
		t.Errorf("current_context = %q, want %q", loaded.CurrentContext, "prod-example-com")
	}
	entry, exists := loaded.Contexts["prod-example-com"]
	if !exists {
		t.Fatal("context prod-example-com not created")
	}
	if entry.Token != "new-token" {
		t.Errorf("token = %q", entry.Token)
	}
	if entry.AuthMethod != "oidc" {
		t.Errorf("auth_method = %q, want %q", entry.AuthMethod, "oidc")
	}
}

func TestUpdateContextCredentials(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := overrideHomeDir(t, tempDir)
	defer restoreHomeDir(originalHome)

	config := contextConfig{
		CurrentContext: "myctx",
		Contexts: map[string]contextEntry{
			"myctx": {
				Server: "server:9770",
				Token:  "old-token",
			},
		},
	}
	writeTestContextConfig(t, tempDir, config)

	updateContextCredentials("refreshed-token", "new-refresh", "2026-12-31T00:00:00Z")

	loaded := loadOrCreateContextConfig()
	entry := loaded.Contexts["myctx"]
	if entry.Token != "refreshed-token" {
		t.Errorf("token = %q, want %q", entry.Token, "refreshed-token")
	}
	if entry.RefreshToken != "new-refresh" {
		t.Errorf("refresh_token = %q", entry.RefreshToken)
	}
}

func TestEmptyConfigReturnsNilContext(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := overrideHomeDir(t, tempDir)
	defer restoreHomeDir(originalHome)

	entry := resolveCurrentContext()
	if entry != nil {
		t.Error("expected nil context when no config exists and no legacy creds")
	}
}

// overrideHomeDir sets HOME (and UserHomeDir) to the temp dir for testing.
func overrideHomeDir(t *testing.T, tempDir string) string {
	t.Helper()
	originalHome := os.Getenv("HOME")
	t.Setenv("HOME", tempDir)
	return originalHome
}

// restoreHomeDir restores the original HOME environment variable.
func restoreHomeDir(originalHome string) {
	os.Setenv("HOME", originalHome) //nolint:errcheck // best effort restore
}

// writeLegacyCredential writes a credentials.json file in the temp dir.
func writeLegacyCredential(t *testing.T, tempDir string, credential credentialFile) {
	t.Helper()
	credentialDir := filepath.Join(tempDir, ".ccattler")
	if mkdirError := os.MkdirAll(credentialDir, 0700); mkdirError != nil {
		t.Fatalf("create dir: %v", mkdirError)
	}
	credentialJSON, _ := json.MarshalIndent(credential, "", "  ") //nolint:gosec // test helper
	if writeError := os.WriteFile(filepath.Join(credentialDir, credentialFileName), credentialJSON, 0600); writeError != nil {
		t.Fatalf("write credential: %v", writeError)
	}
}

// writeTestContextConfig writes a config.json file in the temp dir.
func writeTestContextConfig(t *testing.T, tempDir string, config contextConfig) {
	t.Helper()
	configDir := filepath.Join(tempDir, ".ccattler")
	if mkdirError := os.MkdirAll(configDir, 0700); mkdirError != nil {
		t.Fatalf("create dir: %v", mkdirError)
	}
	configJSON, _ := json.MarshalIndent(config, "", "  ")
	if writeError := os.WriteFile(filepath.Join(configDir, configFileName), configJSON, 0600); writeError != nil {
		t.Fatalf("write config: %v", writeError)
	}
}
