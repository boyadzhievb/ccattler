// command_misc.go contains the diff, render, token, join, and completion command
// configurations, argument parsing, and execution logic extracted from main.go.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// diffCommandConfig holds parsed flags for the "diff" command.
type diffCommandConfig struct {
	// configFilePath is the path to the .ccattler DSL file to diff.
	configFilePath string
	// storeBackend selects the state store implementation: "memory" or "etcd".
	storeBackend string
	// etcdEndpoints is the comma-separated list of etcd server addresses.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within a shared etcd cluster.
	storeKeyPrefix string
	// valuesFilePaths holds paths to values files for template rendering (--values).
	valuesFilePaths []string
	// setOverrides holds key=value pairs for template overrides (--set).
	setOverrides []string
	// setFromEnvOverrides holds environment variable names for template overrides (--set-from-env).
	setFromEnvOverrides []string
}

// parseDiffCommandArgs extracts the config file path and optional store flags
// from the arguments following "diff".
func parseDiffCommandArgs(args []string) diffCommandConfig {
	parsedConfig := diffCommandConfig{
		storeBackend:   "memory",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
	}
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--store":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeBackend = args[argIndex]
			}
		case "--endpoints":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdEndpoints = args[argIndex]
			}
		case "--store-prefix":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeKeyPrefix = args[argIndex]
			}
		case "--values", "-f":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.valuesFilePaths = append(parsedConfig.valuesFilePaths, args[argIndex])
			}
		case "--set":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.setOverrides = append(parsedConfig.setOverrides, args[argIndex])
			}
		case "--set-from-env":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.setFromEnvOverrides = append(parsedConfig.setFromEnvOverrides, args[argIndex])
			}
		default:
			if !strings.HasPrefix(currentArg, "-") {
				parsedConfig.configFilePath = currentArg
			}
		}
	}
	return parsedConfig
}

// executeDiffCommand parses a .ccattler file and shows what facts would change
// without writing anything. In etcd mode it compares against the live store.
// In memory mode (default) everything is an "add" since the store is empty.
func executeDiffCommand(parsedConfig diffCommandConfig) {
	fileData, err := os.ReadFile(parsedConfig.configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", parsedConfig.configFilePath, err)
		os.Exit(1)
	}

	dslContent := string(fileData)
	if len(parsedConfig.valuesFilePaths) > 0 || len(parsedConfig.setOverrides) > 0 || len(parsedConfig.setFromEnvOverrides) > 0 {
		renderedContent, renderError := lang.RenderWithValuesFiles(
			dslContent, parsedConfig.valuesFilePaths, parsedConfig.setOverrides, parsedConfig.setFromEnvOverrides)
		if renderError != nil {
			fmt.Fprintf(os.Stderr, "template error: %v\n", renderError)
			os.Exit(1)
		}
		dslContent = renderedContent
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if parsedConfig.storeBackend == "etcd" {
		factStore, storeCreationError := createStateStoreFromServerConfig(
			parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix,
			"", "", "")
		if storeCreationError != nil {
			fmt.Fprintf(os.Stderr, "error connecting to etcd: %v\n", storeCreationError)
			os.Exit(1)
		}
		defer func() { _ = factStore.Close() }()

		changes, diffError := lang.Diff(ctx, factStore, dslContent)
		if diffError != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(diffError, parsedConfig.configFilePath))
			os.Exit(1)
		}
		printDiffChanges(changes)
		return
	}

	apiURL := "http://" + statusAPIListenAddress + "/api/diff"
	httpResponse, diffErr := http.Post(apiURL, "text/plain", strings.NewReader(dslContent))
	if diffErr != nil {
		factStore := store.NewMemoryStore()
		defer func() { _ = factStore.Close() }()

		changes, diffError := lang.Diff(ctx, factStore, dslContent)
		if diffError != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(diffError, parsedConfig.configFilePath))
			os.Exit(1)
		}
		printDiffChanges(changes)
		return
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "diff error: %s\n", string(responseBody))
		os.Exit(1)
	}

	var changes []lang.FactChange
	if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&changes); decodeErr != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
		os.Exit(1)
	}
	printDiffChanges(changes)
}

// printDiffChanges renders fact changes as human-readable diff output.
func printDiffChanges(changes []lang.FactChange) {
	addedCount := 0
	modifiedCount := 0
	unchangedCount := 0

	for _, change := range changes {
		switch change.Type {
		case "add":
			addedCount++
			fmt.Printf("+ %s = %s\n", change.Key, change.NewValue)
		case "modify":
			modifiedCount++
			fmt.Printf("~ %s\n", change.Key)
			fmt.Printf("  - %s\n", change.OldValue)
			fmt.Printf("  + %s\n", change.NewValue)
		case "unchanged":
			unchangedCount++
		}
	}

	fmt.Println()
	fmt.Printf("%d added, %d modified, %d unchanged\n", addedCount, modifiedCount, unchangedCount)
}

// renderCommandConfig holds parsed flags for the "render" command.
type renderCommandConfig struct {
	// configFilePath is the path to the .ccattler template file to render.
	configFilePath string
	// valuesFilePaths holds paths to values files for template rendering (--values).
	valuesFilePaths []string
	// setOverrides holds key=value pairs for template overrides (--set).
	setOverrides []string
	// setFromEnvOverrides holds environment variable names for template overrides (--set-from-env).
	setFromEnvOverrides []string
}

// parseRenderCommandArgs extracts the template file path and template flags
// from the arguments following "render".
func parseRenderCommandArgs(args []string) renderCommandConfig {
	parsedConfig := renderCommandConfig{}
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--values", "-f":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.valuesFilePaths = append(parsedConfig.valuesFilePaths, args[argIndex])
			}
		case "--set":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.setOverrides = append(parsedConfig.setOverrides, args[argIndex])
			}
		case "--set-from-env":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.setFromEnvOverrides = append(parsedConfig.setFromEnvOverrides, args[argIndex])
			}
		default:
			if !strings.HasPrefix(currentArg, "-") && parsedConfig.configFilePath == "" {
				parsedConfig.configFilePath = currentArg
			}
		}
	}
	return parsedConfig
}

// executeRenderCommand renders a .ccattler template file with the provided
// values and prints the resulting DSL to stdout. This is useful for debugging
// templates and verifying rendered output before applying.
func executeRenderCommand(parsedConfig renderCommandConfig) {
	fileData, readError := os.ReadFile(parsedConfig.configFilePath)
	if readError != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", parsedConfig.configFilePath, readError)
		os.Exit(1)
	}

	renderedContent, renderError := lang.RenderWithValuesFiles(
		string(fileData), parsedConfig.valuesFilePaths, parsedConfig.setOverrides, parsedConfig.setFromEnvOverrides)
	if renderError != nil {
		fmt.Fprintf(os.Stderr, "template error: %v\n", renderError)
		os.Exit(1)
	}

	fmt.Print(renderedContent)
}

// tokenCommandConfig holds parsed flags for the "token" command, which manages
// enrollment join tokens stored in the shared fact store.
type tokenCommandConfig struct {
	action         string // "create", "list", or "revoke"
	nodeID         string // optional: scope token to a specific node
	tokenTTL       string // default "15m"
	tokenValue     string // for revoke: the token prefix to match
	storeBackend   string
	etcdEndpoints  string
	storeKeyPrefix string
}

// parseTokenCommandArgs extracts the subcommand and flags from the arguments
// following "token".
func parseTokenCommandArgs(args []string) tokenCommandConfig {
	parsedConfig := tokenCommandConfig{
		storeBackend:   "etcd",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
		tokenTTL:       "15m",
	}

	if len(args) > 0 {
		parsedConfig.action = args[0]
	}

	for argIndex := 1; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--ttl":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tokenTTL = args[argIndex]
			}
		case "--store":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeBackend = args[argIndex]
			}
		case "--endpoints":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdEndpoints = args[argIndex]
			}
		case "--store-prefix":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeKeyPrefix = args[argIndex]
			}
		default:
			if parsedConfig.action == "revoke" && parsedConfig.tokenValue == "" {
				parsedConfig.tokenValue = currentArg
			}
		}
	}

	return parsedConfig
}

// executeTokenCommand dispatches to the appropriate token subcommand: create
// generates a new join token, list shows active tokens, and revoke removes one.
func executeTokenCommand(parsedConfig tokenCommandConfig) {
	factStore, storeCreationError := createStateStoreFromServerConfig(
		parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix,
		"", "", "")
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer func() { _ = factStore.Close() }()

	enrollmentService := security.NewEnrollmentService(factStore, nil, nil, 0)

	ctx := context.Background()

	switch parsedConfig.action {
	case "create":
		tokenDuration, parseError := time.ParseDuration(parsedConfig.tokenTTL)
		if parseError != nil {
			fmt.Fprintf(os.Stderr, "error: invalid TTL %q: %v\n", parsedConfig.tokenTTL, parseError)
			os.Exit(1)
		}

		joinToken, createError := enrollmentService.GenerateJoinToken(ctx, parsedConfig.nodeID, tokenDuration)
		if createError != nil {
			fmt.Fprintf(os.Stderr, "error creating token: %v\n", createError)
			os.Exit(1)
		}

		fmt.Printf("Token:   %s\n", joinToken.Token)
		fmt.Printf("Expires: %s (in %s)\n", joinToken.ExpiresAt.Format("2006-01-02 15:04:05"), parsedConfig.tokenTTL)
		if parsedConfig.nodeID != "" {
			fmt.Printf("Node:    %s (scoped)\n", parsedConfig.nodeID)
		} else {
			fmt.Println("Node:    any")
		}
		fmt.Println()
		fmt.Println("Join command:")
		fmt.Printf("  cca join https://<server>:9770 %s --node-id <id>\n", joinToken.Token)

	case "list":
		tokens, listError := enrollmentService.ListJoinTokens(ctx)
		if listError != nil {
			fmt.Fprintf(os.Stderr, "error listing tokens: %v\n", listError)
			os.Exit(1)
		}

		if len(tokens) == 0 {
			fmt.Println("no active join tokens")
			return
		}

		fmt.Printf("%-72s  %-12s  %s\n", "TOKEN", "NODE", "EXPIRES")
		for _, token := range tokens {
			nodeScope := "any"
			if token.NodeID != "" {
				nodeScope = token.NodeID
			}
			expiresIn := time.Until(token.ExpiresAt).Round(time.Second)
			fmt.Printf("%-72s  %-12s  %s (in %s)\n",
				token.Token, nodeScope, token.ExpiresAt.Format("15:04:05"), expiresIn)
		}

	case "revoke":
		if parsedConfig.tokenValue == "" {
			fmt.Fprintln(os.Stderr, "usage: cca token revoke <token-prefix>")
			os.Exit(1)
		}
		revokeError := enrollmentService.RevokeJoinToken(ctx, parsedConfig.tokenValue)
		if revokeError != nil {
			fmt.Fprintf(os.Stderr, "error revoking token: %v\n", revokeError)
			os.Exit(1)
		}
		fmt.Println("Token revoked.")

	default:
		fmt.Fprintln(os.Stderr, "usage: cca token <create|list|revoke> [flags]")
		os.Exit(1)
	}
}

// joinCommandConfig holds parsed flags for the "join" command, which enrolls
// a node with the cluster by presenting a join token to the server.
type joinCommandConfig struct {
	serverAddress string // https://host:port
	joinToken     string
	nodeID        string
	caCertPath    string // optional: verify server cert against this CA
	dataDirectory string // where to write cert/key/ca (default ".ccattler")
}

// parseJoinCommandArgs extracts the server URL, token, and flags from the
// arguments following "join".
func parseJoinCommandArgs(args []string) joinCommandConfig {
	parsedConfig := joinCommandConfig{
		dataDirectory: ".ccattler",
	}

	positionalIndex := 0
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--ca-cert":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.caCertPath = args[argIndex]
			}
		case "--data-dir":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.dataDirectory = args[argIndex]
			}
		default:
			switch positionalIndex {
			case 0:
				parsedConfig.serverAddress = currentArg
			case 1:
				parsedConfig.joinToken = currentArg
			}
			positionalIndex++
		}
	}

	return parsedConfig
}

// executeJoinCommand enrolls this node with the cluster by contacting the
// server's enrollment endpoint, presenting the join token, and saving the
// issued certificate material to the data directory.
func executeJoinCommand(parsedConfig joinCommandConfig) {
	fmt.Printf("Enrolling node %s with %s...\n", parsedConfig.nodeID, parsedConfig.serverAddress)

	var transportTLSConfig *tls.Config
	if parsedConfig.caCertPath != "" {
		caCertPEM, readError := os.ReadFile(parsedConfig.caCertPath)
		if readError != nil {
			fmt.Fprintf(os.Stderr, "error reading CA certificate: %v\n", readError)
			os.Exit(1)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCertPEM) {
			fmt.Fprintln(os.Stderr, "error: CA certificate file contains no valid certificates")
			os.Exit(1)
		}
		transportTLSConfig = &tls.Config{
			RootCAs:    caCertPool,
			MinVersion: tls.VersionTLS13,
		}
	} else {
		transportTLSConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // TLS verification disabled during node bootstrap when no CA cert provided
			MinVersion:         tls.VersionTLS13,
		}
		fmt.Println("WARNING: no --ca-cert provided, server certificate will not be verified")
	}

	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: transportTLSConfig},
		Timeout:   30 * time.Second,
	}

	localIPAddresses := detectLocalIPAddresses()
	ipStrings := make([]string, len(localIPAddresses))
	for ipIndex, ipAddr := range localIPAddresses {
		ipStrings[ipIndex] = ipAddr.String()
	}

	enrollmentRequestBody, _ := json.Marshal(map[string]interface{}{
		"token":        parsedConfig.joinToken,
		"node_id":      parsedConfig.nodeID,
		"ip_addresses": ipStrings,
	})

	enrollmentURL := parsedConfig.serverAddress + "/api/enroll"
	httpResponse, requestError := httpClient.Post(enrollmentURL, "application/json",
		strings.NewReader(string(enrollmentRequestBody)))
	if requestError != nil {
		fmt.Fprintf(os.Stderr, "error contacting server: %v\n", requestError)
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)

	var enrollmentResponse struct {
		CertificatePEM string `json:"certificate_pem"`
		PrivateKeyPEM  string `json:"private_key_pem"`
		CACertPEM      string `json:"ca_cert_pem"`
		Principal      string `json:"principal"`
		Error          string `json:"error"`
	}
	if jsonError := json.Unmarshal(responseBody, &enrollmentResponse); jsonError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", jsonError)
		os.Exit(1)
	}

	if enrollmentResponse.Error != "" {
		fmt.Fprintf(os.Stderr, "enrollment failed: %s\n", enrollmentResponse.Error)
		os.Exit(1)
	}

	if mkdirError := os.MkdirAll(parsedConfig.dataDirectory, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating data directory: %v\n", mkdirError)
		os.Exit(1)
	}

	certPath := parsedConfig.dataDirectory + "/node.pem"
	keyPath := parsedConfig.dataDirectory + "/node-key.pem"
	caCertPath := parsedConfig.dataDirectory + "/ca.pem"

	if writeError := os.WriteFile(certPath, []byte(enrollmentResponse.CertificatePEM), 0600); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing certificate: %v\n", writeError)
		os.Exit(1)
	}
	if writeError := os.WriteFile(keyPath, []byte(enrollmentResponse.PrivateKeyPEM), 0600); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing private key: %v\n", writeError)
		os.Exit(1)
	}
	if writeError := os.WriteFile(caCertPath, []byte(enrollmentResponse.CACertPEM), 0600); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing CA certificate: %v\n", writeError)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("Enrolled as %s\n", enrollmentResponse.Principal)
	fmt.Printf("  Certificate: %s\n", certPath)
	fmt.Printf("  Private key: %s\n", keyPath)
	fmt.Printf("  CA cert:     %s\n", caCertPath)
	fmt.Println()
	fmt.Println("Start the agent with:")
	fmt.Printf("  cca agent --node-id %s --cert %s --key %s --ca %s\n",
		parsedConfig.nodeID, certPath, keyPath, caCertPath)
}

const bashCompletionScript = `# cca bash completion — source this or add to .bashrc:
#   eval "$(cca completion bash)"

_cca_completions() {
    local current_word="${COMP_WORDS[COMP_CWORD]}"
    local previous_word="${COMP_WORDS[COMP_CWORD-1]}"

    local commands="apply run run-container server agent token join demo demo-distributed demo-network demo-storage chaos top status describe get diff events logs scale watch metric completion version"

    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=($(compgen -W "$commands" -- "$current_word"))
        return
    fi

    local command="${COMP_WORDS[1]}"

    case "$command" in
        get)
            COMPREPLY=($(compgen -W "services instances nodes volumes networking secrets config" -- "$current_word"))
            ;;
        top)
            COMPREPLY=($(compgen -W "nodes workloads" -- "$current_word"))
            ;;
        describe)
            if [ "$COMP_CWORD" -eq 2 ]; then
                COMPREPLY=($(compgen -W "service node instance" -- "$current_word"))
            fi
            ;;
        token)
            if [ "$COMP_CWORD" -eq 2 ]; then
                COMPREPLY=($(compgen -W "create list revoke" -- "$current_word"))
            fi
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --ttl|--node-id|--endpoints|--store-prefix) ;;
                create|list|revoke) COMPREPLY=($(compgen -W "--store --endpoints --store-prefix --node-id --ttl" -- "$current_word")) ;;
            esac
            ;;
        metric)
            if [ "$COMP_CWORD" -eq 2 ]; then
                COMPREPLY=($(compgen -W "set" -- "$current_word"))
            fi
            ;;
        completion)
            COMPREPLY=($(compgen -W "bash zsh" -- "$current_word"))
            ;;
        apply|diff)
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --endpoints|--store-prefix) ;;
                *) COMPREPLY=($(compgen -f -W "--store --endpoints --store-prefix" -- "$current_word")) ;;
            esac
            ;;
        run|run-container)
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --endpoints|--store-prefix) ;;
                *) COMPREPLY=($(compgen -f -W "--watch -w --store --endpoints --store-prefix" -- "$current_word")) ;;
            esac
            ;;
        server)
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --listen|--endpoints|--store-prefix) ;;
                --cert|--key|--ca) COMPREPLY=($(compgen -f -- "$current_word")) ;;
                *) COMPREPLY=($(compgen -W "--listen --tls --cert --key --ca --store --endpoints --store-prefix --dns --dns-listen" -- "$current_word")) ;;
            esac
            ;;
        agent)
            case "$previous_word" in
                --runtime) COMPREPLY=($(compgen -W "process container" -- "$current_word")) ;;
                --node-id|--endpoints|--store-prefix|--advertise-address) ;;
                --cert|--key|--ca) COMPREPLY=($(compgen -f -- "$current_word")) ;;
                *) COMPREPLY=($(compgen -W "--node-id --runtime --cert --key --ca --store --endpoints --store-prefix --advertise-address --proxy --proxy-listen" -- "$current_word")) ;;
            esac
            ;;
        join)
            case "$previous_word" in
                --node-id|--data-dir) ;;
                --ca-cert) COMPREPLY=($(compgen -f -- "$current_word")) ;;
                *) COMPREPLY=($(compgen -W "--node-id --ca-cert --data-dir" -- "$current_word")) ;;
            esac
            ;;
        events)
            case "$previous_word" in
                --service|-s) ;;
                *) COMPREPLY=($(compgen -W "--follow -f --service -s" -- "$current_word")) ;;
            esac
            ;;
    esac
}

complete -F _cca_completions cca
`

// executeCompletionCommand outputs shell completion scripts for bash or zsh.
func executeCompletionCommand(shellName string) {
	switch shellName {
	case "bash":
		fmt.Print(bashCompletionScript)
	case "zsh":
		fmt.Print(buildZshCompletionScript())
	default:
		fmt.Fprintf(os.Stderr, "unsupported shell %q (use bash or zsh)\n", shellName)
		os.Exit(1)
	}
}

// buildZshCompletionScript generates the zsh completion script as a string.
// Built with a string builder because the script contains backticks that
// cannot appear inside a Go raw string literal.
func buildZshCompletionScript() string {
	var builder strings.Builder
	builder.WriteString("#compdef cca\n")
	builder.WriteString("# cca zsh completion — source this or add to fpath:\n")
	builder.WriteString("#   eval \"$(cca completion zsh)\"\n\n")
	builder.WriteString("_cca() {\n")
	builder.WriteString("    local -a commands\n")
	builder.WriteString("    commands=(\n")
	for _, entry := range []struct{ name, description string }{
		{"apply", "parse .ccattler file and show reconciliation"},
		{"run", "start real processes"},
		{"run-container", "start real containers"},
		{"server", "run control plane (controllers + API)"},
		{"agent", "run node agent"},
		{"token", "manage join tokens (create/list/revoke)"},
		{"join", "enroll this node with the cluster"},
		{"demo", "built-in demo with simulated runtime"},
		{"demo-distributed", "3 simulated nodes with recovery"},
		{"demo-network", "3 nodes with IP, VIPs, DNS, LB"},
		{"demo-storage", "3 nodes with persistent volumes"},
		{"chaos", "random failure injection"},
		{"top", "resource utilization overview"},
		{"status", "show cluster status"},
		{"describe", "detailed resource view"},
		{"get", "list resources"},
		{"diff", "dry-run apply showing fact changes"},
		{"events", "event stream"},
		{"logs", "cluster event log"},
		{"scale", "scale a service"},
		{"watch", "stream fact store changes"},
		{"metric", "inject simulated metric"},
		{"completion", "output shell completion script"},
		{"version", "print version"},
	} {
		fmt.Fprintf(&builder, "        '%s:%s'\n", entry.name, entry.description)
	}
	builder.WriteString("    )\n\n")
	builder.WriteString("    if (( CURRENT == 2 )); then\n")
	builder.WriteString("        _describe 'command' commands\n")
	builder.WriteString("        return\n")
	builder.WriteString("    fi\n\n")
	builder.WriteString("    case \"${words[2]}\" in\n")
	builder.WriteString("        get)\n")
	builder.WriteString("            local -a resources\n")
	builder.WriteString("            resources=('services' 'instances' 'nodes' 'volumes' 'networking' 'secrets' 'config')\n")
	builder.WriteString("            _describe 'resource' resources\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        top)\n")
	builder.WriteString("            local -a views\n")
	builder.WriteString("            views=('nodes' 'workloads')\n")
	builder.WriteString("            _describe 'view' views\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        describe)\n")
	builder.WriteString("            if (( CURRENT == 3 )); then\n")
	builder.WriteString("                local -a types\n")
	builder.WriteString("                types=('service' 'node' 'instance')\n")
	builder.WriteString("                _describe 'type' types\n")
	builder.WriteString("            fi\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        token)\n")
	builder.WriteString("            if (( CURRENT == 3 )); then\n")
	builder.WriteString("                local -a subcommands\n")
	builder.WriteString("                subcommands=('create:generate a join token' 'list:list active tokens' 'revoke:revoke a token')\n")
	builder.WriteString("                _describe 'subcommand' subcommands\n")
	builder.WriteString("            else\n")
	builder.WriteString("                _arguments \\\n")
	builder.WriteString("                    '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                    '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                    '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                    '--node-id[scope to node]:node:' \\\n")
	builder.WriteString("                    '--ttl[token lifetime]:duration:'\n")
	builder.WriteString("            fi\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        metric)\n")
	builder.WriteString("            if (( CURRENT == 3 )); then\n")
	builder.WriteString("                local -a subcommands\n")
	builder.WriteString("                subcommands=('set:inject simulated metric')\n")
	builder.WriteString("                _describe 'subcommand' subcommands\n")
	builder.WriteString("            fi\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        completion)\n")
	builder.WriteString("            local -a shells\n")
	builder.WriteString("            shells=('bash' 'zsh')\n")
	builder.WriteString("            _describe 'shell' shells\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        apply|diff)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '*:file:_files -g \"*.cca *.ccattler *.ccl\"'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        run|run-container)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '(-w --watch)'{-w,--watch}'[print status periodically]' \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '*:file:_files -g \"*.cca *.ccattler *.ccl\"'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        server)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--listen[API listen address]:address:' \\\n")
	builder.WriteString("                '--tls[enable mTLS]' \\\n")
	builder.WriteString("                '--cert[PEM server certificate]:file:_files' \\\n")
	builder.WriteString("                '--key[PEM server private key]:file:_files' \\\n")
	builder.WriteString("                '--ca[PEM CA certificate]:file:_files' \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '--dns[enable DNS server]' \\\n")
	builder.WriteString("                '--dns-listen[DNS listen address]:address:'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        agent)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--node-id[unique node identifier]:id:' \\\n")
	builder.WriteString("                '--runtime[workload runtime]:runtime:(process container)' \\\n")
	builder.WriteString("                '--cert[PEM agent certificate]:file:_files' \\\n")
	builder.WriteString("                '--key[PEM agent private key]:file:_files' \\\n")
	builder.WriteString("                '--ca[PEM CA certificate]:file:_files' \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '--advertise-address[node LAN address]:address:' \\\n")
	builder.WriteString("                '--proxy[enable HTTP proxy]' \\\n")
	builder.WriteString("                '--proxy-listen[proxy listen address]:address:'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        join)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--node-id[unique node identifier]:id:' \\\n")
	builder.WriteString("                '--ca-cert[PEM CA certificate]:file:_files' \\\n")
	builder.WriteString("                '--data-dir[cert/key directory]:directory:_directories'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        events)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '(-f --follow)'{-f,--follow}'[stream events in real-time]' \\\n")
	builder.WriteString("                '(-s --service)'{-s,--service}'[filter by service]:service:'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("    esac\n")
	builder.WriteString("}\n\n")
	builder.WriteString("_cca \"$@\"\n")
	return builder.String()
}
