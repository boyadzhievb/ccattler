// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Shared utility functions used by multiple cca CLI commands.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultStatusAPIPort is the base port for the HTTP status API. When a TLS
// server binds a wildcard address on this port, a plain-HTTP admin listener
// starts on port+1 (9771) so local CLI commands work without mTLS certs.
const defaultStatusAPIPort = "9770"

// statusAPIListenAddress returns the localhost address the CLI should connect
// to. It first tries the admin port (9771), and falls back to the main port
// (9770) for non-TLS or explicit localhost servers.
var statusAPIListenAddress = resolveStatusAPIListenAddress()

func resolveStatusAPIListenAddress() string {
	if envAddress := os.Getenv("CCA_API"); envAddress != "" {
		return envAddress
	}
	return "127.0.0.1:" + defaultStatusAPIPort
}

// writeServerJSONError writes a JSON-formatted error response with the given
// status code. Used by server-side handlers in the cmd package.
func writeServerJSONError(responseWriter http.ResponseWriter, message string, statusCode int) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": message})
}

// checkAPIResponseStatus verifies the HTTP response has a 2xx status code.
// On non-2xx, it reads the body, extracts the error message from JSON if
// possible, prints the error to stderr, and exits the process.
func checkAPIResponseStatus(httpResponse *http.Response) {
	if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
		return
	}
	responseBody, _ := io.ReadAll(httpResponse.Body)
	var errorResponse struct {
		Error string `json:"error"`
	}
	switch {
	case json.Unmarshal(responseBody, &errorResponse) == nil && errorResponse.Error != "":
		fmt.Fprintf(os.Stderr, "API error: %s\n", errorResponse.Error)
	case len(responseBody) > 0:
		fmt.Fprintf(os.Stderr, "API error (HTTP %d): %s\n", httpResponse.StatusCode, strings.TrimSpace(string(responseBody)))
	default:
		fmt.Fprintf(os.Stderr, "API error: HTTP %d\n", httpResponse.StatusCode)
	}
	os.Exit(1)
}

// configureLogger sets up the process-wide structured logger from CLI flags.
func configureLogger(logLevel string, logFormat string) {
	level := logging.LevelInfo
	if logLevel != "" {
		level = logging.ParseLevel(logLevel)
	}
	format := logging.FormatHuman
	if logFormat == "json" {
		format = logging.FormatJSON
	}
	logging.SetDefault(logging.New(os.Stderr, level, format))
}

// createStateStoreFromServerConfig builds the appropriate StateStore for
// the server or agent command configuration. When etcdCertPath, etcdKeyPath,
// and etcdCACertPath are all provided, a TLS config is built for the etcd
// client connection.
func createStateStoreFromServerConfig(storeBackend, etcdEndpoints, storeKeyPrefix, etcdCertPath, etcdKeyPath, etcdCACertPath string) (store.StateStore, error) {
	if storeBackend == "etcd" {
		endpointList := strings.Split(etcdEndpoints, ",")

		// Auto-detect TLS certs from canonical path when endpoints use https://
		// and no explicit cert paths were provided.
		endpointsRequireTLS := strings.Contains(etcdEndpoints, "https://")
		if etcdCertPath == "" && etcdKeyPath == "" && etcdCACertPath == "" && endpointsRequireTLS {
			if fileExists(defaultEtcdCertPath) && fileExists(defaultEtcdKeyPath) && fileExists(defaultEtcdCACertPath) {
				etcdCertPath = defaultEtcdCertPath
				etcdKeyPath = defaultEtcdKeyPath
				etcdCACertPath = defaultEtcdCACertPath
			} else {
				return nil, fmt.Errorf("etcd endpoints use https:// but TLS certificates not found\n"+
					"  Expected:  %s, %s, %s\n"+
					"  Fix:       run 'cca join <server> <token> --node-id <id>' to enroll this node,\n"+
					"             or provide --etcd-cert, --etcd-key, --etcd-ca flags\n"+
					"  See:       design/security-model.md for certificate setup",
					defaultEtcdCertPath, defaultEtcdKeyPath, defaultEtcdCACertPath)
			}
		}

		var etcdTLSConfig *tls.Config
		if etcdCertPath != "" && etcdKeyPath != "" && etcdCACertPath != "" {
			clientCertificate, loadError := tls.LoadX509KeyPair(etcdCertPath, etcdKeyPath)
			if loadError != nil {
				return nil, fmt.Errorf("loading etcd client certificate: %w", loadError)
			}

			caCertPEM, readError := os.ReadFile(etcdCACertPath) //nolint:gosec // reads user-specified config file
			if readError != nil {
				return nil, fmt.Errorf("reading etcd CA certificate: %w", readError)
			}

			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCertPEM) {
				return nil, fmt.Errorf("etcd CA certificate file contains no valid certificates")
			}

			etcdTLSConfig = &tls.Config{
				Certificates: []tls.Certificate{clientCertificate},
				RootCAs:      caCertPool,
				MinVersion:   tls.VersionTLS13,
			}

			for _, endpoint := range endpointList {
				if strings.HasPrefix(endpoint, "http://") {
					return nil, fmt.Errorf("etcd endpoint %q uses http:// but TLS is configured; use https:// or omit the scheme", endpoint)
				}
			}
		}

		return store.NewEtcdStore(store.EtcdStoreConfig{
			Endpoints:   endpointList,
			KeyPrefix:   storeKeyPrefix,
			DialTimeout: types.DefaultEtcdDialTimeout,
			TLSConfig:   etcdTLSConfig,
		})
	}
	return store.NewMemoryStore(), nil
}

// fileExists returns true if the given path exists and is a regular file.
func fileExists(filePath string) bool {
	fileInfo, statError := os.Stat(filePath)
	return statError == nil && !fileInfo.IsDir()
}

// detectLocalIPAddresses returns the non-loopback IPv4 addresses of this machine.
func detectLocalIPAddresses() []net.IP {
	var localAddresses []net.IP
	networkInterfaces, interfaceError := net.Interfaces()
	if interfaceError != nil {
		return localAddresses
	}
	for _, networkInterface := range networkInterfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		interfaceAddresses, addressError := networkInterface.Addrs()
		if addressError != nil {
			continue
		}
		for _, interfaceAddress := range interfaceAddresses {
			var ipAddress net.IP
			switch typedAddress := interfaceAddress.(type) {
			case *net.IPNet:
				ipAddress = typedAddress.IP
			case *net.IPAddr:
				ipAddress = typedAddress.IP
			}
			if ipAddress != nil && ipAddress.To4() != nil && !ipAddress.IsLoopback() {
				localAddresses = append(localAddresses, ipAddress)
			}
		}
	}
	return localAddresses
}

// annotateErrorWithFileName sets the File field on a ParseError if the error
// is of that type. This adds the source filename to diagnostic output.
func annotateErrorWithFileName(originalError error, fileName string) error {
	if parseError, isParseError := originalError.(*lang.ParseError); isParseError {
		parseError.File = fileName
	}
	return originalError
}

// registerLocalNode registers a single node with default simulated capacity.
// Used by single-machine modes (run, run-container, demo) and the agent command.
func registerLocalNode(ctx context.Context, factStore store.StateStore, nodeID string) {
	if writeError := types.WriteNode(ctx, factStore, types.Node{
		ID: nodeID, State: types.NodeAlive,
		CapacityCPU: types.DefaultSimulatedNodeCPU, CapacityMemory: types.DefaultSimulatedNodeMemory,
		AvailableCPU: types.DefaultSimulatedNodeCPU, AvailableMemory: types.DefaultSimulatedNodeMemory,
	}); writeError != nil {
		logging.Default().Error("failed to write node", "node", nodeID, "error", writeError.Error())
	}
}

// registerSimulatedNodes registers multiple simulated nodes with default capacity.
// Used by multi-node demos and local simulation mode.
func registerSimulatedNodes(ctx context.Context, factStore store.StateStore, nodeIDs []string) {
	for _, nodeID := range nodeIDs {
		registerLocalNode(ctx, factStore, nodeID)
	}
}

// coreControllers creates the 8 reconciliation controllers present in every mode:
// instance, scheduler, endpoint, failure, autoscale, intent resolver, rollout, init.
// Commands append mode-specific controllers (nodeFailure, network, storage, warmZero,
// clusterAutoscale) before passing the slice to startControllerRunner or NewRunner.
func coreControllers() []controllers.Controller {
	return []controllers.Controller{
		controllers.NewInstanceController(),
		scheduler.NewScheduler(),
		controllers.NewEndpointController(),
		controllers.NewFailureController(),
		controllers.NewAutoscaleController(),
		controllers.NewIntentResolverController(),
		controllers.NewRolloutController(),
		controllers.NewInitController(),
		controllers.NewStatefulDNSController(),
		controllers.NewStatefulVolumeController(),
	}
}

// startControllerRunner creates an event log, wraps the given controllers in a
// Runner, sets the event log, and starts reconciliation in a background goroutine.
// Returns the event log for API server integration.
func startControllerRunner(ctx context.Context, factStore store.StateStore, controllerList []controllers.Controller) *types.EventLog {
	eventLog := types.NewEventLog(factStore, types.DefaultEventLogMaxEvents)
	controllerRunner := controllers.NewRunner(factStore, controllerList...)
	controllerRunner.SetEventLog(eventLog)
	go func() {
		if runError := controllerRunner.Run(ctx); runError != nil {
			logging.Default().Error("controller runner exited with error", "error", runError.Error())
		}
	}()
	return eventLog
}

// renderedDSLContent holds a resolved DSL file's path and its rendered content,
// used to process single files or directories of DSL files uniformly.
type renderedDSLContent struct {
	// filePath is the original path to the DSL file on disk.
	filePath string
	// content is the rendered DSL content (after template processing if applicable).
	content string
}

// resolveAndRenderDSLFiles resolves the config path (a single file or a directory)
// into one or more DSL files, reads each, and applies template rendering when any
// values flags are present or a default values file exists. Returns a list of
// rendered contents ready for apply/diff.
func resolveAndRenderDSLFiles(
	configPath string,
	valuesFilePaths []string,
	setOverrides []string,
	setFromEnvOverrides []string,
) ([]renderedDSLContent, error) {
	filePaths, resolveError := resolveDSLFilePaths(configPath)
	if resolveError != nil {
		return nil, resolveError
	}

	var renderedFiles []renderedDSLContent

	for _, filePath := range filePaths {
		fileData, readError := os.ReadFile(filePath) //nolint:gosec // DSL file path from CLI argument
		if readError != nil {
			return nil, fmt.Errorf("error reading %s: %w", filePath, readError)
		}
		dslContent := string(fileData)

		effectiveValuesPaths := prependDefaultValuesFile(filePath, valuesFilePaths)
		hasTemplateFlags := len(effectiveValuesPaths) > 0 || len(setOverrides) > 0 || len(setFromEnvOverrides) > 0

		if hasTemplateFlags {
			renderedContent, renderError := lang.RenderWithValuesFiles(
				dslContent, effectiveValuesPaths, setOverrides, setFromEnvOverrides)
			if renderError != nil {
				return nil, fmt.Errorf("template error in %s: %w", filePath, renderError)
			}
			dslContent = renderedContent
		}

		renderedFiles = append(renderedFiles, renderedDSLContent{
			filePath: filePath,
			content:  dslContent,
		})
	}

	return renderedFiles, nil
}

// prependDefaultValuesFile checks for a convention-based default values file
// alongside the DSL template. For a template named "airflow.cca", it looks for
// "airflow/values.ccv" in the same directory. If found, it is prepended to the
// values file list so that explicit --values flags override the defaults.
func prependDefaultValuesFile(dslFilePath string, valuesFilePaths []string) []string {
	directoryPath := filepath.Dir(dslFilePath)
	baseName := filepath.Base(dslFilePath)
	templateName := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	defaultValuesPath := filepath.Join(directoryPath, templateName, "values.ccv")
	if _, statError := os.Stat(defaultValuesPath); statError != nil {
		return valuesFilePaths
	}

	effectivePaths := make([]string, 0, 1+len(valuesFilePaths))
	effectivePaths = append(effectivePaths, defaultValuesPath)
	effectivePaths = append(effectivePaths, valuesFilePaths...)
	return effectivePaths
}

// resolveDSLFilePaths returns the list of DSL file paths to process. When the
// given path is a directory, it collects all .cca and .ccattler files in it.
// When the path is a single file, it returns a slice containing just that file.
func resolveDSLFilePaths(configPath string) ([]string, error) {
	fileInfo, statError := os.Stat(configPath)
	if statError != nil {
		return nil, fmt.Errorf("cannot access %s: %w", configPath, statError)
	}
	if fileInfo.IsDir() {
		return lang.CollectDSLFiles(configPath)
	}
	return []string{configPath}, nil
}

// combineDSLFileContents joins multiple rendered DSL file contents into a single
// DSL string by concatenating with newlines. Used by diff and other commands that
// need to process a directory of DSL files as a single unit.
func combineDSLFileContents(renderedFiles []renderedDSLContent) string {
	if len(renderedFiles) == 1 {
		return renderedFiles[0].content
	}
	var contentParts []string
	for _, rendered := range renderedFiles {
		contentParts = append(contentParts, rendered.content)
	}
	return strings.Join(contentParts, "\n")
}

// localTokenRoundTripper is an http.RoundTripper that injects the local bearer
// token into every outgoing request's Authorization header. Used by CLI commands
// to authenticate against the non-TLS server.
type localTokenRoundTripper struct {
	// baseTransport is the underlying transport that performs the actual HTTP call.
	baseTransport http.RoundTripper
	// bearerToken is the hex-encoded local token read from .ccattler/local-token.
	bearerToken string
}

// RoundTrip adds the Authorization header and delegates to the base transport.
func (roundTripper *localTokenRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	request.Header.Set("Authorization", "Bearer "+roundTripper.bearerToken)
	return roundTripper.baseTransport.RoundTrip(request)
}

// cachedAuthenticatedClient holds the lazily-initialized HTTP client with token auth.
var cachedAuthenticatedClient *http.Client

// authenticatedClientOnce ensures the client is built exactly once.
var authenticatedClientOnce sync.Once

// buildAuthenticatedHTTPClient returns an *http.Client that automatically injects
// a bearer token into every request. The token is resolved in priority order:
// 1. $CCA_TOKEN environment variable (explicit override, for CI/scripts)
// 2. ~/.ccattler/credentials.json token field (user login)
// 3. /etc/ccattler/local-token or .ccattler/local-token (operator on node)
// Falls back to http.DefaultClient when no token source is available.
func buildAuthenticatedHTTPClient() *http.Client {
	authenticatedClientOnce.Do(func() {
		resolvedToken := resolveAuthenticationToken()
		if resolvedToken == "" {
			cachedAuthenticatedClient = http.DefaultClient
			return
		}
		cachedAuthenticatedClient = &http.Client{
			Transport: &localTokenRoundTripper{
				baseTransport: http.DefaultTransport,
				bearerToken:   resolvedToken,
			},
		}
	})
	return cachedAuthenticatedClient
}

// resolveAuthenticationToken checks each credential source in priority order
// and returns the first available token, or empty string if none found.
func resolveAuthenticationToken() string {
	if envToken := os.Getenv("CCA_TOKEN"); envToken != "" {
		return envToken
	}
	credential, loadError := loadCredentialFile()
	if loadError == nil && credential.Token != "" {
		return credential.Token
	}
	localToken, readError := security.ReadLocalToken()
	if readError == nil {
		return localToken
	}
	return ""
}

// buildBearerTokenHTTPClient returns an *http.Client that injects the given
// bearer token into every request's Authorization header. Used by `cca login`
// to validate a token against the server before storing it.
func buildBearerTokenHTTPClient(bearerToken string) *http.Client {
	return &http.Client{
		Transport: &localTokenRoundTripper{
			baseTransport: http.DefaultTransport,
			bearerToken:   bearerToken,
		},
	}
}

// runDemoStatusLoop prints cluster status at the given interval until the context
// is cancelled. Used by demo commands that loop after their initial setup phase.
func runDemoStatusLoop(ctx context.Context, factStore store.StateStore, interval time.Duration) {
	statusPrintTicker := time.NewTicker(interval)
	defer statusPrintTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nShutting down...")
			return
		case <-statusPrintTicker.C:
			fmt.Println()
			fmt.Print(buildStatusTextOutput(ctx, factStore))
		}
	}
}
