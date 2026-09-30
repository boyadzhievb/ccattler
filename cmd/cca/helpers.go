// Shared utility functions used by multiple cca CLI commands.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// statusAPIListenAddress is the address the HTTP status API binds to when running
// in live mode (run, run-container, demo). The status and metric commands query this.
const statusAPIListenAddress = "127.0.0.1:9770"

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
// values flags are present. Returns a list of rendered contents ready for apply/diff.
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

	hasTemplateFlags := len(valuesFilePaths) > 0 || len(setOverrides) > 0 || len(setFromEnvOverrides) > 0
	var renderedFiles []renderedDSLContent

	for _, filePath := range filePaths {
		fileData, readError := os.ReadFile(filePath) //nolint:gosec // DSL file path from CLI argument
		if readError != nil {
			return nil, fmt.Errorf("error reading %s: %w", filePath, readError)
		}
		dslContent := string(fileData)

		if hasTemplateFlags {
			renderedContent, renderError := lang.RenderWithValuesFiles(
				dslContent, valuesFilePaths, setOverrides, setFromEnvOverrides)
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
