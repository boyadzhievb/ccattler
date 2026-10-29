// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Context management for multi-cluster CLI configuration.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// configFileName is the name of the multi-context config file stored in the
// user's home directory under .ccattler/.
const configFileName = "config.json"

// defaultContextName is the name assigned to auto-migrated credentials and
// new contexts when no explicit name is given.
const defaultContextName = "default"

// contextConfig holds the full multi-context configuration stored in
// ~/.ccattler/config.json.
type contextConfig struct {
	CurrentContext string                  `json:"current_context"` // name of the active context
	Contexts       map[string]contextEntry `json:"contexts"`        // named contexts keyed by name
}

// contextEntry holds per-cluster connection and auth configuration.
type contextEntry struct {
	Server       string `json:"server"`                   // API server host:port
	AuthMethod   string `json:"auth_method"`              // "oidc", "token", "cert", "local-token"
	Token        string `json:"token,omitempty"`          // JWT bearer token
	RefreshToken string `json:"refresh_token,omitempty"`  // OAuth2 refresh token
	CertPath     string `json:"cert_path,omitempty"`      // client certificate path for mTLS
	KeyPath      string `json:"key_path,omitempty"`       // client private key path for mTLS
	CACertPath   string `json:"ca_cert_path,omitempty"`   // CA certificate for server verification
	User         string `json:"user,omitempty"`           // username from auth
	Team         string `json:"team,omitempty"`           // default team scope
	Expires      string `json:"expires,omitempty"`        // token expiry (RFC3339)
	OIDCIssuer   string `json:"oidc_issuer,omitempty"`    // external OIDC issuer URL
	OIDCClientID string `json:"oidc_client_id,omitempty"` // external OIDC client ID
}

// executeContextCommand dispatches context subcommands: list, use, current, delete.
func executeContextCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cca context <list|use|current|delete> [name]")
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		executeContextListCommand()
	case "use":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: cca context use <name>")
			os.Exit(1)
		}
		executeContextUseCommand(args[1])
	case "current":
		executeContextCurrentCommand()
	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: cca context delete <name>")
			os.Exit(1)
		}
		executeContextDeleteCommand(args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown context subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: cca context <list|use|current|delete> [name]")
		os.Exit(1)
	}
}

// executeContextListCommand prints all configured contexts, marking the current
// one with an asterisk.
func executeContextListCommand() {
	config := loadOrCreateContextConfig()
	if len(config.Contexts) == 0 {
		fmt.Println("no contexts configured")
		fmt.Println("use: cca login <server> to create one")
		return
	}

	contextNames := make([]string, 0, len(config.Contexts))
	for contextName := range config.Contexts {
		contextNames = append(contextNames, contextName)
	}
	sort.Strings(contextNames)

	for _, contextName := range contextNames {
		entry := config.Contexts[contextName]
		marker := "  "
		if contextName == config.CurrentContext {
			marker = "* "
		}
		authDisplay := entry.AuthMethod
		if authDisplay == "" {
			authDisplay = "token"
		}
		fmt.Printf("%s%-16s  server=%s  user=%s  auth=%s\n",
			marker, contextName, entry.Server, entry.User, authDisplay)
	}
}

// executeContextUseCommand switches the current context to the named one.
func executeContextUseCommand(contextName string) {
	config := loadOrCreateContextConfig()
	if _, exists := config.Contexts[contextName]; !exists {
		fmt.Fprintf(os.Stderr, "context %q not found\n", contextName)
		fmt.Fprintln(os.Stderr, "available contexts:")
		for name := range config.Contexts {
			fmt.Fprintf(os.Stderr, "  %s\n", name)
		}
		os.Exit(1)
	}
	config.CurrentContext = contextName
	writeContextConfig(config)
	fmt.Printf("Switched to context %q\n", contextName)
}

// executeContextCurrentCommand prints the name and details of the active context.
func executeContextCurrentCommand() {
	config := loadOrCreateContextConfig()
	if config.CurrentContext == "" {
		fmt.Println("no current context set")
		return
	}
	entry, exists := config.Contexts[config.CurrentContext]
	if !exists {
		fmt.Printf("current context %q does not exist in config\n", config.CurrentContext)
		return
	}
	fmt.Printf("Context: %s\n", config.CurrentContext)
	fmt.Printf("Server:  %s\n", entry.Server)
	if entry.User != "" {
		fmt.Printf("User:    %s\n", entry.User)
	}
	if entry.Team != "" {
		fmt.Printf("Team:    %s\n", entry.Team)
	}
	authDisplay := entry.AuthMethod
	if authDisplay == "" {
		authDisplay = "token"
	}
	fmt.Printf("Auth:    %s\n", authDisplay)
}

// executeContextDeleteCommand removes a named context from the config.
func executeContextDeleteCommand(contextName string) {
	config := loadOrCreateContextConfig()
	if _, exists := config.Contexts[contextName]; !exists {
		fmt.Fprintf(os.Stderr, "context %q not found\n", contextName)
		os.Exit(1)
	}
	delete(config.Contexts, contextName)
	if config.CurrentContext == contextName {
		config.CurrentContext = ""
		for name := range config.Contexts {
			config.CurrentContext = name
			break
		}
	}
	writeContextConfig(config)
	fmt.Printf("Deleted context %q\n", contextName)
}

// resolveConfigFilePath returns the path to ~/.ccattler/config.json.
func resolveConfigFilePath() string {
	homeDirectory, homeError := os.UserHomeDir()
	if homeError != nil {
		return filepath.Join(".ccattler", configFileName)
	}
	return filepath.Join(homeDirectory, ".ccattler", configFileName)
}

// loadOrCreateContextConfig reads ~/.ccattler/config.json, auto-migrating from
// the legacy credentials.json if config.json does not exist.
func loadOrCreateContextConfig() contextConfig {
	configPath := resolveConfigFilePath()
	configJSON, readError := os.ReadFile(configPath) //nolint:gosec // path derived from user's home
	if readError == nil {
		var config contextConfig
		if unmarshalError := json.Unmarshal(configJSON, &config); unmarshalError == nil {
			if config.Contexts == nil {
				config.Contexts = make(map[string]contextEntry)
			}
			return config
		}
	}

	config := contextConfig{
		Contexts: make(map[string]contextEntry),
	}

	legacyCredential, legacyError := loadCredentialFile()
	if legacyError == nil && legacyCredential.Token != "" {
		config.CurrentContext = defaultContextName
		authMethod := "token"
		if legacyCredential.OIDCIssuer != "" {
			authMethod = "oidc"
		}
		config.Contexts[defaultContextName] = contextEntry{
			Server:       legacyCredential.Server,
			AuthMethod:   authMethod,
			Token:        legacyCredential.Token,
			RefreshToken: legacyCredential.RefreshToken,
			User:         legacyCredential.User,
			Team:         legacyCredential.Team,
			Expires:      legacyCredential.Expires,
			OIDCIssuer:   legacyCredential.OIDCIssuer,
			OIDCClientID: legacyCredential.OIDCClientID,
		}
		writeContextConfig(config)
	}

	return config
}

// writeContextConfig writes the context config to ~/.ccattler/config.json.
func writeContextConfig(config contextConfig) {
	configPath := resolveConfigFilePath()
	configDirectory := filepath.Dir(configPath)

	if mkdirError := os.MkdirAll(configDirectory, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s: %v\n", configDirectory, mkdirError)
		os.Exit(1)
	}

	configJSON, marshalError := json.MarshalIndent(config, "", "  ") //nolint:gosec // config contains tokens that must be persisted
	if marshalError != nil {
		fmt.Fprintf(os.Stderr, "error marshaling config: %v\n", marshalError)
		os.Exit(1)
	}

	if writeError := os.WriteFile(configPath, configJSON, 0600); writeError != nil { //nolint:gosec // config file with restricted permissions
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", configPath, writeError)
		os.Exit(1)
	}
}

// resolveCurrentContext returns the active context entry, or nil if none is set.
// Respects --context override and CCA_CONTEXT environment variable.
func resolveCurrentContext() *contextEntry {
	config := loadOrCreateContextConfig()
	contextName := config.CurrentContext

	if overrideName := resolveContextOverride(); overrideName != "" {
		contextName = overrideName
	}

	if contextName == "" {
		return nil
	}
	entry, exists := config.Contexts[contextName]
	if !exists {
		return nil
	}
	return &entry
}

// resolveContextOverride returns the context name from --context flag or
// CCA_CONTEXT env var, or empty string if neither is set.
func resolveContextOverride() string {
	if envContext := os.Getenv("CCA_CONTEXT"); envContext != "" {
		return envContext
	}
	for argIndex := 1; argIndex < len(os.Args)-1; argIndex++ {
		if os.Args[argIndex] == "--context" {
			return os.Args[argIndex+1]
		}
	}
	return ""
}

// updateContextFromLogin creates or updates a context entry after a successful
// login. The context name is derived from the server address unless --context
// is specified.
func updateContextFromLogin(serverAddress string, credential credentialFile) {
	config := loadOrCreateContextConfig()

	contextName := resolveContextOverride()
	if contextName == "" {
		contextName = deriveContextNameFromServer(serverAddress)
	}

	authMethod := "token"
	if credential.OIDCIssuer != "" {
		authMethod = "oidc"
	}

	config.Contexts[contextName] = contextEntry{
		Server:       credential.Server,
		AuthMethod:   authMethod,
		Token:        credential.Token,
		RefreshToken: credential.RefreshToken,
		User:         credential.User,
		Team:         credential.Team,
		Expires:      credential.Expires,
		OIDCIssuer:   credential.OIDCIssuer,
		OIDCClientID: credential.OIDCClientID,
	}
	config.CurrentContext = contextName
	writeContextConfig(config)
}

// deriveContextNameFromServer creates a context name from a server address by
// stripping the port and replacing dots with dashes.
func deriveContextNameFromServer(serverAddress string) string {
	hostPart := serverAddress
	if colonIndex := strings.LastIndex(serverAddress, ":"); colonIndex >= 0 {
		hostPart = serverAddress[:colonIndex]
	}
	contextName := strings.ReplaceAll(hostPart, ".", "-")
	if contextName == "" || contextName == "127-0-0-1" || contextName == "localhost" {
		contextName = defaultContextName
	}
	return contextName
}

// updateContextCredentials updates the token and expiry of the current context
// after a token refresh.
func updateContextCredentials(token string, refreshToken string, expires string) {
	config := loadOrCreateContextConfig()
	contextName := config.CurrentContext
	if overrideName := resolveContextOverride(); overrideName != "" {
		contextName = overrideName
	}
	if contextName == "" {
		return
	}
	entry, exists := config.Contexts[contextName]
	if !exists {
		return
	}
	entry.Token = token
	if refreshToken != "" {
		entry.RefreshToken = refreshToken
	}
	entry.Expires = expires
	config.Contexts[contextName] = entry
	writeContextConfig(config)
}
