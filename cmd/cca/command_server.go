// Server command implementation for the cca CLI.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// serverCommandConfig holds parsed flags for the "server" command, which runs
// the control plane (controllers + API) against a shared state store.
type serverCommandConfig struct {
	// storeBackend selects the state store implementation: "memory" or "etcd".
	storeBackend string
	// etcdEndpoints is the comma-separated list of etcd server addresses.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within a shared etcd cluster.
	storeKeyPrefix string
	// listenAddress is the host:port the API server binds to.
	listenAddress string
	// tlsEnabled turns on mTLS for the API server with an auto-generated CA.
	tlsEnabled bool
	// tlsCertPath is the path to a PEM-encoded server certificate file.
	tlsCertPath string
	// tlsKeyPath is the path to a PEM-encoded server private key file.
	tlsKeyPath string
	// tlsCACertPath is the path to a PEM-encoded CA certificate for verifying client certs.
	tlsCACertPath string
	// etcdCertPath is the path to a PEM-encoded client certificate for etcd mTLS.
	etcdCertPath string
	// etcdKeyPath is the path to a PEM-encoded client private key for etcd mTLS.
	etcdKeyPath string
	// etcdCACertPath is the path to a PEM-encoded CA certificate for verifying etcd server certs.
	etcdCACertPath string
	// dnsEnabled starts the built-in DNS server alongside the control plane.
	dnsEnabled bool
	// dnsListenAddress is the host:port the DNS server binds to (default ":15353").
	dnsListenAddress string
	// logLevel controls the minimum severity of log messages (debug/info/warn/error).
	logLevel string
	// logFormat selects human-readable or JSON log output (human/json).
	logFormat string
	// apiOnly runs the stateless API server without controllers, for horizontal scaling.
	apiOnly bool
	// controllersOnly runs leader-elected controllers without the API server.
	controllersOnly bool
	// nodeID identifies this control-plane replica for leader election (defaults to hostname).
	nodeID string
	// cloudProviderName selects the cloud provider for node lifecycle, load balancers,
	// and routes. Empty means no cloud integration. Valid: "aws", "gcp", "azure", "simulator".
	cloudProviderName string
	// cloudRegion is the cloud region for the provider (e.g. "us-east-1").
	cloudRegion string
}

// parseServerCommandArgs extracts store-related flags from the arguments
// following "server".
func parseServerCommandArgs(args []string) serverCommandConfig {
	parsedConfig := serverCommandConfig{
		storeBackend:   "etcd",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
		listenAddress:  "0.0.0.0:9770",
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
		case "--listen":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.listenAddress = args[argIndex]
			}
		case "--tls":
			parsedConfig.tlsEnabled = true
		case "--cert":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tlsCertPath = args[argIndex]
			}
		case "--key":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tlsKeyPath = args[argIndex]
			}
		case "--ca":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tlsCACertPath = args[argIndex]
			}
		case "--etcd-cert":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdCertPath = args[argIndex]
			}
		case "--etcd-key":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdKeyPath = args[argIndex]
			}
		case "--etcd-ca":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdCACertPath = args[argIndex]
			}
		case "--dns":
			parsedConfig.dnsEnabled = true
		case "--dns-listen":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.dnsListenAddress = args[argIndex]
				parsedConfig.dnsEnabled = true
			}
		case "--log-level":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.logLevel = args[argIndex]
			}
		case "--log-format":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.logFormat = args[argIndex]
			}
		case "--api-only":
			parsedConfig.apiOnly = true
		case "--controllers-only":
			parsedConfig.controllersOnly = true
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--cloud-provider":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.cloudProviderName = args[argIndex]
			}
		case "--cloud-region":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.cloudRegion = args[argIndex]
			}
		}
	}

	if parsedConfig.dnsEnabled && parsedConfig.dnsListenAddress == "" {
		parsedConfig.dnsListenAddress = ":15353"
	}

	if parsedConfig.storeBackend != "memory" && parsedConfig.storeBackend != "etcd" {
		fmt.Fprintf(os.Stderr, "error: unknown store backend %q (must be \"memory\" or \"etcd\")\n", parsedConfig.storeBackend)
		os.Exit(1)
	}

	if parsedConfig.tlsCertPath != "" || parsedConfig.tlsKeyPath != "" || parsedConfig.tlsCACertPath != "" {
		if parsedConfig.tlsCertPath == "" || parsedConfig.tlsKeyPath == "" || parsedConfig.tlsCACertPath == "" {
			fmt.Fprintln(os.Stderr, "error: --cert, --key, and --ca must all be specified together")
			os.Exit(1)
		}
		parsedConfig.tlsEnabled = true
	}

	if parsedConfig.apiOnly && parsedConfig.controllersOnly {
		fmt.Fprintln(os.Stderr, "error: --api-only and --controllers-only are mutually exclusive")
		os.Exit(1)
	}

	if parsedConfig.nodeID == "" {
		hostname, _ := os.Hostname()
		if hostname != "" {
			parsedConfig.nodeID = hostname
		} else {
			parsedConfig.nodeID = "controlplane-1"
		}
	}

	return parsedConfig
}

// executeServerCommand starts the control plane: all reconciliation controllers
// and the HTTP API server. It connects to the shared state store and blocks
// until Ctrl+C. No node agent or runtime — that runs separately via "cca agent".
func executeServerCommand(parsedConfig serverCommandConfig) {
	configureLogger(parsedConfig.logLevel, parsedConfig.logFormat)
	factStore, storeCreationError := createStateStoreFromServerConfig(
		parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix,
		parsedConfig.etcdCertPath, parsedConfig.etcdKeyPath, parsedConfig.etcdCACertPath)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer func() { _ = factStore.Close() }()

	if parsedConfig.storeBackend == "etcd" {
		fmt.Printf("CCattler server connected to etcd at %s (prefix: %s)\n", parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	runControllers := !parsedConfig.apiOnly
	runAPIServer := !parsedConfig.controllersOnly

	eventLog := types.NewEventLog(factStore, 1000)

	if runControllers {
		instanceController := controllers.NewInstanceController()
		schedulerController := scheduler.NewScheduler()
		endpointController := controllers.NewEndpointController()
		failureController := controllers.NewFailureController()
		nodeFailureController := controllers.NewNodeFailureController()
		networkController := controllers.NewNetworkController()
		autoscaleController := controllers.NewAutoscaleController()
		intentResolverController := controllers.NewIntentResolverController()
		rolloutController := controllers.NewRolloutController()
		initController := controllers.NewInitController()
		warmZeroController := controllers.NewWarmZeroController()

		metricsCollector := controllers.NewMetricsCollector()

		controllerList := []controllers.Controller{
			instanceController, schedulerController, endpointController,
			failureController, nodeFailureController, networkController,
			autoscaleController, intentResolverController, rolloutController,
			initController, warmZeroController,
		}

		if parsedConfig.cloudProviderName != "" {
			cloudProviderInstance := createCloudProvider(parsedConfig.cloudProviderName, parsedConfig.cloudRegion)
			if cloudProviderInstance != nil {
				controllerList = append(controllerList,
					controllers.NewNodeLifecycleController(cloudProviderInstance),
					controllers.NewCloudLoadBalancerController(cloudProviderInstance),
					controllers.NewCloudRouteController(cloudProviderInstance),
				)
				fmt.Printf("Cloud controllers enabled (provider: %s)\n", parsedConfig.cloudProviderName)
			}
		}

		haControllerRunner := controllers.NewHARunner(factStore, parsedConfig.nodeID, metricsCollector,
			controllerList...)

		go func() {
			if runError := haControllerRunner.Run(ctx); runError != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "controller runner error: %v\n", runError)
			}
		}()
		fmt.Printf("Controllers started with leader election (node: %s)\n", parsedConfig.nodeID)
	}

	if runAPIServer {
		var serverTLSConfig *tls.Config
		var clusterCertificateAuthority *security.CertificateAuthority
		enrollmentEnabled := false
		if parsedConfig.tlsCertPath != "" {
			serverTLSConfig = loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath)
		} else if parsedConfig.tlsEnabled {
			serverTLSConfig, clusterCertificateAuthority = buildServerTLSConfig(ctx, parsedConfig.listenAddress)
			enrollmentEnabled = true
		}

		statusAPIServer := launchStatusAPIServer(factStore, parsedConfig.listenAddress, serverTLSConfig, enrollmentEnabled)
		statusAPIServer.SetEventLog(eventLog)
		statusAPIServer.SetWatchMultiplexer(api.NewWatchMultiplexer(factStore))

		if parsedConfig.apiOnly {
			statusAPIServer.SetServerMode(api.ServerModeAPIOnly)
		}

		if clusterCertificateAuthority != nil {
			enrollmentService := security.NewEnrollmentService(factStore, clusterCertificateAuthority, nil, 24*time.Hour)
			statusAPIServer.SetEnrollmentService(enrollmentService)
			fmt.Println("Node enrollment enabled — use 'cca token create' to generate join tokens")
		}

		if parsedConfig.dnsEnabled {
			serviceResolver := network.NewStoreBackedResolver(factStore)
			dnsServer := network.NewDNSServer(serviceResolver, parsedConfig.dnsListenAddress)
			go func() {
				if dnsStartError := dnsServer.Start(ctx); dnsStartError != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "DNS server error: %v\n", dnsStartError)
				}
			}()
			fmt.Printf("DNS server listening on %s (resolving *.%s)\n",
				parsedConfig.dnsListenAddress, network.DefaultDNSDomain)
		}
	}

	modeLabel := "full"
	if parsedConfig.apiOnly {
		modeLabel = "api-only"
	} else if parsedConfig.controllersOnly {
		modeLabel = "controllers-only"
	}

	protocol := "http"
	if parsedConfig.tlsEnabled {
		protocol = "https (mTLS)"
	}

	if runAPIServer {
		fmt.Printf("Server running (%s). API on %s (%s). Press Ctrl+C to stop.\n",
			modeLabel, parsedConfig.listenAddress, protocol)
	} else {
		fmt.Printf("Server running (%s). Leader election active (node: %s). Press Ctrl+C to stop.\n",
			modeLabel, parsedConfig.nodeID)
	}

	<-ctx.Done()
	fmt.Println("\nServer shutting down...")
}

// buildServerTLSConfig creates an ephemeral CA, issues a server certificate
// createCloudProvider returns a CloudProvider for the given provider name, or
// nil if the name is unrecognized. This is the factory used by `cca server`
// to instantiate the right cloud adapter based on the --cloud-provider flag.
func createCloudProvider(providerName string, region string) cloud.CloudProvider {
	switch providerName {
	case "aws":
		return cloud.NewAWSCloudProvider(region)
	case "gcp":
		return cloud.NewGCPCloudProvider("", region)
	case "azure":
		return cloud.NewAzureCloudProvider("", "", region)
	case "simulator":
		return cloud.NewSimulatorCloudProvider()
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown cloud provider %q, cloud controllers disabled\n", providerName)
		return nil
	}
}

// with auto-rotation, and returns a tls.Config plus the CA. The TLS config uses
// VerifyClientCertIfGiven so the enrollment endpoint can accept unauthenticated
// connections while all other endpoints enforce client certs via middleware. The
// CA certificate is written to ca.pem in the .ccattler/ data directory.
func buildServerTLSConfig(ctx context.Context, listenAddress string) (*tls.Config, *security.CertificateAuthority) {
	certificateAuthority, err := security.NewCertificateAuthority(10 * 365 * 24 * time.Hour)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating CA: %v\n", err)
		os.Exit(1)
	}

	listenHost, _, splitError := net.SplitHostPort(listenAddress)
	if splitError != nil {
		listenHost = listenAddress
	}

	var serverIPAddresses []net.IP
	if parsedIP := net.ParseIP(listenHost); parsedIP != nil {
		serverIPAddresses = append(serverIPAddresses, parsedIP)
	}
	serverIPAddresses = append(serverIPAddresses, net.ParseIP("127.0.0.1"))

	serverDNSNames := []string{"localhost"}
	if net.ParseIP(listenHost) == nil && listenHost != "" {
		serverDNSNames = append(serverDNSNames, listenHost)
	}

	certificateRequest := security.IssueCertificateRequest{
		CommonName:  "ccattler-server",
		DNSNames:    serverDNSNames,
		IPAddresses: serverIPAddresses,
		TTL:         24 * time.Hour,
	}

	serverCertRotator, err := security.NewCertificateRotator(certificateAuthority, certificateRequest, 0.7)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating server certificate: %v\n", err)
		os.Exit(1)
	}
	serverCertRotator.Start(ctx)

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(certificateAuthority.CACertificatePEM())

	dataDirectory := ".ccattler"
	if mkdirError := os.MkdirAll(dataDirectory, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating data directory: %v\n", mkdirError)
		os.Exit(1)
	}

	caCertPath := dataDirectory + "/ca.pem"
	if writeError := os.WriteFile(caCertPath, certificateAuthority.CACertificatePEM(), 0600); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing CA certificate: %v\n", writeError)
		os.Exit(1)
	}
	fmt.Printf("CA certificate written to %s\n", caCertPath)

	return &tls.Config{
		GetCertificate: serverCertRotator.GetCertificate,
		ClientCAs:      caCertPool,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		MinVersion:     tls.VersionTLS13,
	}, certificateAuthority
}

// loadServerTLSConfig reads PEM-encoded certificate, key, and CA files from
// disk and returns a tls.Config that serves mTLS using those credentials.
// Used when --cert/--key/--ca flags are provided instead of --tls auto-generation.
func loadServerTLSConfig(certPath, keyPath, caCertPath string) *tls.Config {
	serverCertificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading server certificate: %v\n", err)
		os.Exit(1)
	}

	caCertPEM, err := os.ReadFile(caCertPath) //nolint:gosec // reads user-specified config file
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading CA certificate: %v\n", err)
		os.Exit(1)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCertPEM) {
		fmt.Fprintln(os.Stderr, "error: CA certificate file contains no valid certificates")
		os.Exit(1)
	}

	fmt.Printf("TLS: cert=%s key=%s ca=%s\n", certPath, keyPath, caCertPath)

	return &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		ClientCAs:    caCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// clusterStatusResponse is the structured representation of the full cluster status,
// used for JSON serialization via the status API.
type clusterStatusResponse struct {
	Services   []serviceStatusEntry  `json:"services"`             // all registered services with desired/running counts
	Instances  []instanceStatusEntry `json:"instances"`            // all active (non-stopped) instances
	Nodes      []nodeStatusEntry     `json:"nodes"`                // all registered nodes with capacity info
	Networking []networkStatusEntry  `json:"networking,omitempty"` // service VIP and DNS assignments
	Volumes    []volumeStatusEntry   `json:"volumes,omitempty"`    // persistent volumes with attachment state
}

// serviceStatusEntry represents one service in the cluster status output.
type serviceStatusEntry struct {
	Name         string `json:"name"`            // service name from the DSL config
	Image        string `json:"image"`           // container image or process command
	DesiredCount int    `json:"desired"`         // how many instances should be running
	RunningCount int    `json:"running"`         // how many instances are currently running
	ExposedPorts []int  `json:"ports,omitempty"` // ports exposed by this service
}

// instanceStatusEntry represents one instance in the cluster status output.
type instanceStatusEntry struct {
	ID          string `json:"id"`      // unique instance identifier
	ServiceName string `json:"service"` // which service this instance belongs to
	State       string `json:"state"`   // current state: pending, running, failed
	NodeID      string `json:"node"`    // which node this instance is placed on
	IPAddress   string `json:"ip"`      // allocated IP address
	HealthState string `json:"health"`  // health check result: healthy, unhealthy, or "-"
}

// networkStatusEntry represents a service's networking configuration.
type networkStatusEntry struct {
	ServiceName string `json:"service"` // service name
	VIP         string `json:"vip"`     // virtual IP address
	Port        int    `json:"port"`    // VIP port
	DNS         string `json:"dns"`     // DNS name mapping
}

// nodeStatusEntry represents one node in the cluster status output.
type nodeStatusEntry struct {
	ID              string `json:"id"`               // unique node identifier
	State           string `json:"state"`            // node state: alive, unreachable, draining
	PlacedInstances int    `json:"instances"`        // number of active instances on this node
	AvailableCPU    int64  `json:"available_cpu"`    // remaining CPU capacity in millicores
	CapacityCPU     int64  `json:"capacity_cpu"`     // total CPU capacity in millicores
	AvailableMemory int64  `json:"available_memory"` // remaining memory capacity in MiB
	CapacityMemory  int64  `json:"capacity_memory"`  // total memory capacity in MiB
}

// volumeStatusEntry represents one persistent volume in the cluster status output.
type volumeStatusEntry struct {
	Name      string `json:"name"`                 // volume name from the DSL config
	Size      string `json:"size"`                 // declared size (e.g. "50Gi")
	State     string `json:"state"`                // current state: available, attached
	Node      string `json:"node,omitempty"`       // node the volume is attached to
	Instance  string `json:"instance,omitempty"`   // instance the volume is mounted into
	MountPath string `json:"mount_path,omitempty"` // filesystem mount path
}

// requireClientCertMiddleware wraps an HTTP handler to reject requests that
// lack a verified client certificate. The /api/enroll path is exempted because
// joining nodes do not yet have credentials.
func requireClientCertMiddleware(wrappedHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/enroll", "/healthz", "/metrics":
			wrappedHandler.ServeHTTP(responseWriter, request)
			return
		}
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
			http.Error(responseWriter, "client certificate required", http.StatusUnauthorized)
			return
		}
		wrappedHandler.ServeHTTP(responseWriter, request)
	})
}

// launchStatusAPIServer starts the HTTP API server in the background. It hosts
// both the legacy /status and /metric endpoints and the new /api/* endpoints.
// When serverTLSConfig is non-nil, the listener is wrapped with TLS for mTLS.
// When enrollmentEnabled is true, client certs are enforced via middleware
// (except on /api/enroll) instead of at the TLS layer.
// Returns the api.Server so callers can attach optional components like EventLog.
func launchStatusAPIServer(factStore store.StateStore, listenAddress string, serverTLSConfig *tls.Config, enrollmentEnabled bool) *api.Server {
	apiServer := api.NewServer(factStore)

	httpMux := http.NewServeMux()
	httpMux.Handle("/api/", apiServer.Handler())

	// Expose /healthz and /metrics at the root level so load balancers and
	// Prometheus scrapers can reach them without the /api/ prefix.
	httpMux.Handle("/healthz", apiServer.Handler())
	httpMux.Handle("/metrics", apiServer.Handler())

	// Expose OIDC endpoints at root level when configured.
	httpMux.Handle("/.well-known/", apiServer.Handler())
	httpMux.Handle("/oidc/", apiServer.Handler())

	// Legacy status endpoint for backward compatibility with 'cca status'.
	httpMux.HandleFunc("/status", func(responseWriter http.ResponseWriter, request *http.Request) {
		requestContext := request.Context()
		acceptHeader := request.Header.Get("Accept")
		if strings.Contains(acceptHeader, "application/json") {
			responseWriter.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(responseWriter).Encode(buildClusterStatusJSON(requestContext, factStore))
			return
		}
		responseWriter.Header().Set("Content-Type", "text/plain")
		_, _ = responseWriter.Write([]byte(buildStatusTextOutput(requestContext, factStore)))
	})

	// Legacy metric endpoint for backward compatibility with 'cca metric set'.
	httpMux.HandleFunc("/metric", func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
			return
		}
		serviceName := request.URL.Query().Get("service")
		metricName := request.URL.Query().Get("metric")
		metricValue := request.URL.Query().Get("value")
		if serviceName == "" || metricName == "" || metricValue == "" {
			http.Error(responseWriter, "service, metric, and value required", http.StatusBadRequest)
			return
		}
		requestContext := request.Context()
		metricKey := types.KeyObservedMetric(serviceName, metricName)
		if _, putError := factStore.Put(requestContext, metricKey, []byte(metricValue)); putError != nil {
			logging.Default().Error("failed to write metric", "key", metricKey, "error", putError.Error())
		}
		_, _ = fmt.Fprintf(responseWriter, "set %s.%s = %s\n", serviceName, metricName, metricValue) //nolint:gosec // internal CLI metric endpoint, not user-facing
	})

	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return apiServer
	}
	if serverTLSConfig != nil {
		listener = tls.NewListener(listener, serverTLSConfig)
	}

	var serverHandler http.Handler = httpMux
	if enrollmentEnabled {
		serverHandler = requireClientCertMiddleware(httpMux)
	}

	statusHTTPServer := &http.Server{
		Handler:           serverHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if serveError := statusHTTPServer.Serve(listener); serveError != nil {
			fmt.Fprintf(os.Stderr, "status api server: %v\n", serveError)
		}
	}()
	return apiServer
}

// buildClusterStatusJSON collects the full cluster state from the fact store
// and assembles it into a structured clusterStatusResponse for JSON serialization.
func buildClusterStatusJSON(ctx context.Context, factStore store.StateStore) clusterStatusResponse {
	var statusResponse clusterStatusResponse

	// Load all instances and sort by ID for deterministic output.
	allInstances, _ := types.ListInstances(ctx, factStore)
	sort.Slice(allInstances, func(i, j int) bool { return allInstances[i].ID < allInstances[j].ID })

	// Discover all unique service names from the desired state.
	desiredFacts, _ := factStore.Scan(ctx, types.ScanDesiredServices)
	uniqueServiceNames := make(map[string]bool)
	for _, fact := range desiredFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		serviceName := strings.SplitN(relativePath, "/", 2)[0]
		uniqueServiceNames[serviceName] = true
	}
	sortedServiceNames := make([]string, 0, len(uniqueServiceNames))
	for serviceName := range uniqueServiceNames {
		sortedServiceNames = append(sortedServiceNames, serviceName)
	}
	sort.Strings(sortedServiceNames)

	// Build service status entries with running instance counts.
	for _, serviceName := range sortedServiceNames {
		service, err := types.ReadService(ctx, factStore, serviceName)
		if err != nil {
			continue
		}
		runningInstanceCount := 0
		for _, instance := range allInstances {
			if instance.Service == serviceName && instance.State == types.InstanceRunning {
				runningInstanceCount++
			}
		}
		statusResponse.Services = append(statusResponse.Services, serviceStatusEntry{
			Name: service.Name, Image: service.Image, DesiredCount: service.Instances,
			RunningCount: runningInstanceCount, ExposedPorts: service.Ports,
		})
	}

	// Build instance status entries, excluding stopped instances.
	for _, instance := range allInstances {
		if instance.State == types.InstanceStopped {
			continue
		}
		placedNodeID := ""
		if placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); err == nil {
			placedNodeID = string(placementFact.Value)
		}
		healthDisplay := string(instance.Health)
		if healthDisplay == "" {
			healthDisplay = "-"
		}
		instanceIPAddress := instance.IP
		if instanceIPAddress == "" {
			instanceIPAddress = "-"
		}
		statusResponse.Instances = append(statusResponse.Instances, instanceStatusEntry{
			ID: instance.ID, ServiceName: instance.Service, State: string(instance.State),
			NodeID: placedNodeID, IPAddress: instanceIPAddress, HealthState: healthDisplay,
		})
	}

	// Build networking status entries from VIP and DNS facts.
	vipFacts, _ := factStore.Scan(ctx, types.ScanNetworkVIPs)
	dnsFacts, _ := factStore.Scan(ctx, types.ScanNetworkDNS)
	dnsMapping := make(map[string]string)
	for _, dnsFact := range dnsFacts {
		serviceName := strings.TrimPrefix(dnsFact.Key, types.ScanNetworkDNS)
		dnsMapping[serviceName] = string(dnsFact.Value)
	}
	vipByService := make(map[string]string)
	vipPortByService := make(map[string]int)
	for _, vipFact := range vipFacts {
		relativePath := strings.TrimPrefix(vipFact.Key, types.ScanNetworkVIPs)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 1 {
			vipByService[pathParts[0]] = string(vipFact.Value)
		} else if len(pathParts) == 2 && pathParts[1] == "port" {
			portValue := 0
			_, _ = fmt.Sscanf(string(vipFact.Value), "%d", &portValue)
			vipPortByService[pathParts[0]] = portValue
		}
	}
	for serviceName, vipAddress := range vipByService {
		dnsName := serviceName + "." + network.DefaultDNSDomain
		statusResponse.Networking = append(statusResponse.Networking, networkStatusEntry{
			ServiceName: serviceName,
			VIP:         vipAddress,
			Port:        vipPortByService[serviceName],
			DNS:         dnsName,
		})
	}
	sort.Slice(statusResponse.Networking, func(i, j int) bool {
		return statusResponse.Networking[i].ServiceName < statusResponse.Networking[j].ServiceName
	})

	// Build volume status entries from observed volume facts.
	allVolumes, _ := types.ListObservedVolumes(ctx, factStore)
	sort.Slice(allVolumes, func(i, j int) bool { return allVolumes[i].Name < allVolumes[j].Name })
	for _, volume := range allVolumes {
		volumeEntry := volumeStatusEntry{
			Name:      volume.Name,
			Size:      volume.Size,
			State:     string(volume.State),
			Node:      volume.Node,
			Instance:  volume.Instance,
			MountPath: volume.MountPath,
		}
		statusResponse.Volumes = append(statusResponse.Volumes, volumeEntry)
	}

	// Build node status entries with placement counts.
	allNodes, _ := types.ListNodes(ctx, factStore)
	sort.Slice(allNodes, func(i, j int) bool { return allNodes[i].ID < allNodes[j].ID })
	for _, node := range allNodes {
		placedInstanceCount := 0
		for _, instance := range allInstances {
			if instance.State == types.InstanceStopped {
				continue
			}
			if placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); err == nil && string(placementFact.Value) == node.ID {
				placedInstanceCount++
			}
		}
		statusResponse.Nodes = append(statusResponse.Nodes, nodeStatusEntry{
			ID: node.ID, State: string(node.State), PlacedInstances: placedInstanceCount,
			AvailableCPU: node.AvailableCPU, CapacityCPU: node.CapacityCPU,
			AvailableMemory: node.AvailableMemory, CapacityMemory: node.CapacityMemory,
		})
	}

	return statusResponse
}

// buildStatusTextOutput renders the cluster status as human-readable formatted text.
// It delegates to buildClusterStatusJSON for data collection, then formats the result.
func buildStatusTextOutput(ctx context.Context, factStore store.StateStore) string {
	var textBuilder strings.Builder
	textBuilder.WriteString("=== CLUSTER STATUS ===\n\n")

	statusData := buildClusterStatusJSON(ctx, factStore)

	// Render services section.
	for _, service := range statusData.Services {
		fmt.Fprintf(&textBuilder, "SERVICE  %-12s  image=%-16s  desired=%d  running=%d",
			service.Name, service.Image, service.DesiredCount, service.RunningCount)
		if len(service.ExposedPorts) > 0 {
			fmt.Fprintf(&textBuilder, "  ports=%v", service.ExposedPorts)
		}
		textBuilder.WriteByte('\n')
	}

	// Render instances section with IP addresses.
	textBuilder.WriteByte('\n')
	fmt.Fprintf(&textBuilder, "INSTANCES (%d active)\n", len(statusData.Instances))
	for _, instance := range statusData.Instances {
		fmt.Fprintf(&textBuilder, "  %-12s  service=%-8s  state=%-8s  node=%-8s  ip=%-16s  health=%-8s\n",
			instance.ID, instance.ServiceName, instance.State, instance.NodeID, instance.IPAddress, instance.HealthState)
	}

	// Render nodes section.
	textBuilder.WriteByte('\n')
	fmt.Fprintf(&textBuilder, "NODES (%d)\n", len(statusData.Nodes))
	for _, node := range statusData.Nodes {
		fmt.Fprintf(&textBuilder, "  %-12s  state=%-12s  instances=%d  cpu=%d/%d  memory=%d/%d\n",
			node.ID, node.State, node.PlacedInstances, node.AvailableCPU, node.CapacityCPU,
			node.AvailableMemory, node.CapacityMemory)
	}

	// Render networking section if VIPs exist.
	if len(statusData.Networking) > 0 {
		textBuilder.WriteByte('\n')
		fmt.Fprintf(&textBuilder, "NETWORKING (%d services)\n", len(statusData.Networking))
		for _, networkEntry := range statusData.Networking {
			fmt.Fprintf(&textBuilder, "  %-12s  vip=%s:%d  dns=%s\n",
				networkEntry.ServiceName, networkEntry.VIP, networkEntry.Port, networkEntry.DNS)
		}
	}

	// Render volumes section if volumes exist.
	if len(statusData.Volumes) > 0 {
		textBuilder.WriteByte('\n')
		fmt.Fprintf(&textBuilder, "VOLUMES (%d)\n", len(statusData.Volumes))
		for _, volumeEntry := range statusData.Volumes {
			nodeDisplay := volumeEntry.Node
			if nodeDisplay == "" {
				nodeDisplay = "-"
			}
			instanceDisplay := volumeEntry.Instance
			if instanceDisplay == "" {
				instanceDisplay = "-"
			}
			mountPathDisplay := volumeEntry.MountPath
			if mountPathDisplay == "" {
				mountPathDisplay = "-"
			}
			fmt.Fprintf(&textBuilder, "  %-12s  size=%-8s  state=%-10s  node=%-8s  instance=%-8s  mount=%s\n",
				volumeEntry.Name, volumeEntry.Size, volumeEntry.State, nodeDisplay, instanceDisplay, mountPathDisplay)
		}
	}

	return textBuilder.String()
}
