// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// command_token.go implements "cca token create|list|revoke" through
// /api/node/join-token (cluster.admin, audited). The CLI never touches the
// enrollment records in etcd (M219).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/api"
)

// defaultJoinTokenTTL is the lifetime of a token created without --ttl.
const defaultJoinTokenTTL = "15m"

// tokenCommandUsage is printed for an unknown subcommand or flag.
const tokenCommandUsage = "usage: cca token create [--node-id <id>] [--ttl 15m] [--quiet] | list | revoke <token>" //nolint:gosec // G101: usage text, not a credential

// tokenCommandConfig holds parsed flags for the "token" command.
type tokenCommandConfig struct {
	action      string // action is "create", "list" or "revoke".
	nodeID      string // nodeID binds a created token to one node (--node-id).
	tokenTTL    string // tokenTTL is the created token's lifetime (--ttl, default 15m).
	tokenValue  string // tokenValue is the token to revoke, as create printed it.
	quietOutput bool   // quietOutput prints only the token value (--quiet, for scripts).
}

// parseTokenCommandArgs extracts the subcommand and flags following "token".
// Unknown flags, including the removed etcd flags, print usage and exit.
func parseTokenCommandArgs(args []string) tokenCommandConfig {
	parsedConfig := tokenCommandConfig{tokenTTL: defaultJoinTokenTTL}
	if len(args) > 0 {
		parsedConfig.action = args[0]
	}
	for argIndex := 1; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch {
		case currentArg == "--node-id" && argIndex+1 < len(args):
			argIndex++
			parsedConfig.nodeID = args[argIndex]
		case currentArg == "--ttl" && argIndex+1 < len(args):
			argIndex++
			parsedConfig.tokenTTL = args[argIndex]
		case currentArg == "--quiet" || currentArg == "-q":
			parsedConfig.quietOutput = true
		case parsedConfig.action == "revoke" && parsedConfig.tokenValue == "" && !strings.HasPrefix(currentArg, "-"):
			parsedConfig.tokenValue = currentArg
		default:
			fmt.Fprintf(os.Stderr, "error: cca token: unexpected argument %q (tokens are managed through the API server)\n%s\n", currentArg, tokenCommandUsage)
			os.Exit(1)
		}
	}
	return parsedConfig
}

// executeTokenCommand runs the token subcommand against the API server and
// exits non-zero with the server's message on any refusal.
func executeTokenCommand(parsedConfig tokenCommandConfig) {
	ctx := context.Background()
	var commandError error
	switch parsedConfig.action {
	case "create":
		commandError = createJoinTokenThroughAPI(ctx, parsedConfig)
	case "list":
		commandError = listJoinTokensThroughAPI(ctx)
	case "revoke":
		commandError = revokeJoinTokenThroughAPI(ctx, parsedConfig.tokenValue)
	default:
		fmt.Fprintln(os.Stderr, tokenCommandUsage)
		os.Exit(1)
	}
	if commandError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", commandError)
		os.Exit(1)
	}
}

// createJoinTokenThroughAPI creates a token and prints it. The "Token:" line
// is parsed by the Ansible worker role; keep its format.
func createJoinTokenThroughAPI(ctx context.Context, parsedConfig tokenCommandConfig) error {
	responseBody, sendError := sendAPIJSONRequest(ctx, http.MethodPost, api.JoinTokenPath,
		api.JoinTokenCreateRequest{NodeID: parsedConfig.nodeID, TTL: parsedConfig.tokenTTL})
	if sendError != nil {
		return fmt.Errorf("creating join token: %w", sendError)
	}
	var created api.JoinTokenCreateResponse
	if decodeError := json.Unmarshal(responseBody, &created); decodeError != nil || created.Token == "" {
		return fmt.Errorf("creating join token: unexpected answer %q", responseBody)
	}
	if parsedConfig.quietOutput {
		fmt.Print(created.Token)
		return nil
	}
	fmt.Printf("Token:   %s\n", created.Token)
	fmt.Printf("Expires: %s (in %s)\n", created.ExpiresAt.Local().Format("2006-01-02 15:04:05"), parsedConfig.tokenTTL)
	if created.NodeID != "" {
		fmt.Printf("Node:    %s (scoped)\n", created.NodeID)
	} else {
		fmt.Println("Node:    any")
	}
	fmt.Println()
	fmt.Println("Join command:")
	fmt.Printf("  cca join https://<server>:9770 %s --node-id <id>\n", created.Token)
	return nil
}

// listJoinTokensThroughAPI prints active tokens by id; the API never returns
// token values after create.
func listJoinTokensThroughAPI(ctx context.Context) error {
	responseBody, sendError := sendAPIJSONRequest(ctx, http.MethodGet, api.JoinTokenPath, nil)
	if sendError != nil {
		return fmt.Errorf("listing join tokens: %w", sendError)
	}
	var activeTokens []api.JoinTokenListEntry
	if decodeError := json.Unmarshal(responseBody, &activeTokens); decodeError != nil {
		return fmt.Errorf("listing join tokens: unexpected answer %q", responseBody)
	}
	if len(activeTokens) == 0 {
		fmt.Println("no active join tokens")
		return nil
	}
	fmt.Printf("%-10s  %-12s  %s\n", "TOKEN ID", "NODE", "EXPIRES")
	for _, activeToken := range activeTokens {
		nodeScope := "any"
		if activeToken.NodeID != "" {
			nodeScope = activeToken.NodeID
		}
		expiresIn := time.Until(activeToken.ExpiresAt).Round(time.Second)
		fmt.Printf("%-10s  %-12s  %s (in %s)\n", activeToken.ID, nodeScope, activeToken.ExpiresAt.Local().Format("15:04:05"), expiresIn)
	}
	return nil
}

// revokeJoinTokenThroughAPI revokes the token create printed; the value goes
// in the request body, never the URL.
func revokeJoinTokenThroughAPI(ctx context.Context, tokenValue string) error {
	if tokenValue == "" {
		fmt.Fprintln(os.Stderr, tokenCommandUsage)
		os.Exit(1)
	}
	if _, sendError := sendAPIJSONRequest(ctx, http.MethodDelete, api.JoinTokenPath, api.JoinTokenRevokeRequest{Token: tokenValue}); sendError != nil {
		return fmt.Errorf("revoking join token: %w", sendError)
	}
	fmt.Println("Token revoked.")
	return nil
}
