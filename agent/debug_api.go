// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultDebugReadTimeout is the HTTP read timeout for the debug server.
const defaultDebugReadTimeout = 10 * time.Second

// defaultDebugWriteTimeout is the HTTP write timeout for the debug server.
// Must accommodate slow operations like image pulls on constrained nodes.
const defaultDebugWriteTimeout = 120 * time.Second

// defaultHTTPShutdownTimeout is the grace period for HTTP server shutdown.
const defaultHTTPShutdownTimeout = 5 * time.Second

// DebugServer serves agent-local debug endpoints that expose runtime state:
// /debug/containers, /debug/images, /debug/stats, /debug/logs, /debug/exec and
// /debug/images/pull. Users reach it only through the API server proxy.
//
// The debug API can run commands in any container. Without TLS it only binds
// to loopback; with SetMutualTLS it requires a cluster-CA client certificate
// whose principal passes the access checker (Flow 24).
type DebugServer struct {
	nodeID         string             // nodeID identifies this node in response payloads.
	runtimeAdapter runtime.Runtime    // runtimeAdapter is the runtime to query for containers and stats.
	logBaseDir     string             // logBaseDir is the directory where container log subdirectories are stored.
	httpServer     *http.Server       // httpServer is the underlying HTTP server.
	tlsConfig      *tls.Config        // tlsConfig enables mTLS when non-nil; nil means loopback-only plain HTTP.
	accessChecker  DebugAccessChecker // accessChecker authorizes the client certificate CN when TLS is enabled.
}

// DebugAccessChecker reports whether the client whose certificate carries the
// given common name may use the debug API.
type DebugAccessChecker func(ctx context.Context, clientCommonName string) bool

// NewClusterAdminGrantChecker returns a DebugAccessChecker that allows only
// principals "node:<cn>" holding the cluster-admin role grant in the store.
// The API server principal has this grant; enrolled nodes never do.
func NewClusterAdminGrantChecker(factStore store.StateStore) DebugAccessChecker {
	return func(ctx context.Context, clientCommonName string) bool {
		_, getError := factStore.Get(ctx, types.KeyAuthGrant("node", clientCommonName, clusterAdminRoleName))
		return getError == nil
	}
}

// clusterAdminRoleName is the role whose grant admits a client to the debug API.
const clusterAdminRoleName = "cluster-admin"

// NewDebugServer creates a DebugServer bound to the given listen address.
// The server is not started until Start is called.
func NewDebugServer(nodeID string, listenAddress string, runtimeAdapter runtime.Runtime, logBaseDir string) *DebugServer {
	debugServer := &DebugServer{
		nodeID:         nodeID,
		runtimeAdapter: runtimeAdapter,
		logBaseDir:     logBaseDir,
	}

	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/debug/containers", debugServer.handleDebugContainers)
	serveMux.HandleFunc("/debug/images", debugServer.handleDebugImages)
	serveMux.HandleFunc("/debug/stats", debugServer.handleDebugStats)
	serveMux.HandleFunc("/debug/exec", debugServer.handleDebugExec)
	serveMux.HandleFunc("/debug/logs", debugServer.handleDebugLogs)
	serveMux.HandleFunc("/debug/images/pull", debugServer.handleDebugImagePull)

	debugServer.httpServer = &http.Server{
		Addr:         listenAddress,
		Handler:      serveMux,
		ReadTimeout:  defaultDebugReadTimeout,
		WriteTimeout: defaultDebugWriteTimeout,
	}

	return debugServer
}

// Start begins serving debug endpoints. It blocks until the server is shut
// down or the context is cancelled.
func (debugServer *DebugServer) Start(ctx context.Context) error {
	if securityError := debugServer.CheckListenSecurity(); securityError != nil {
		return securityError
	}

	listener, listenError := net.Listen("tcp", debugServer.httpServer.Addr)
	if listenError != nil {
		return listenError
	}
	if debugServer.tlsConfig != nil {
		listener = tls.NewListener(listener, debugServer.tlsConfig)
	}

	go func() { //nolint:gosec // G118: shutdown goroutine needs context.Background — parent ctx is already done
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), defaultHTTPShutdownTimeout)
		defer shutdownCancel()
		_ = debugServer.httpServer.Shutdown(shutdownCtx)
	}()

	logging.Default().Info("debug API listening", "node", debugServer.nodeID, "address", debugServer.httpServer.Addr)
	if serveError := debugServer.httpServer.Serve(listener); serveError != nil && serveError != http.ErrServerClosed {
		return serveError
	}
	return nil
}

// SetMutualTLS makes the server require a client certificate signed by the CA
// in serverTLSConfig.ClientCAs and admit only clients that pass accessChecker.
// Must be called before Start.
func (debugServer *DebugServer) SetMutualTLS(serverTLSConfig *tls.Config, accessChecker DebugAccessChecker) {
	mutualTLSConfig := serverTLSConfig.Clone()
	mutualTLSConfig.ClientAuth = tls.RequireAndVerifyClientCert
	mutualTLSConfig.MinVersion = tls.VersionTLS13
	debugServer.tlsConfig = mutualTLSConfig
	debugServer.accessChecker = accessChecker
	debugServer.httpServer.Handler = debugServer.requireAuthorizedClient(debugServer.httpServer.Handler)
}

// requireAuthorizedClient rejects requests whose verified client certificate
// CN does not pass the access checker. The TLS layer has already verified the
// certificate chain against the cluster CA.
func (debugServer *DebugServer) requireAuthorizedClient(wrappedHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
			writeDebugJSONError(responseWriter, "client certificate required", http.StatusUnauthorized)
			return
		}
		clientCommonName := request.TLS.PeerCertificates[0].Subject.CommonName
		if clientCommonName == "" || !debugServer.accessChecker(request.Context(), clientCommonName) {
			logging.Default().Warn("debug API request denied", "node", debugServer.nodeID, "client", clientCommonName, "path", request.URL.Path)
			writeDebugJSONError(responseWriter, "forbidden", http.StatusForbidden)
			return
		}
		wrappedHandler.ServeHTTP(responseWriter, request)
	})
}

// CheckListenSecurity returns an error when the server would serve plain HTTP
// on a non-loopback address. Start enforces it; callers can check it first to
// fail fast on misconfiguration.
func (debugServer *DebugServer) CheckListenSecurity() error {
	if debugServer.tlsConfig == nil && !isLoopbackListenAddress(debugServer.httpServer.Addr) {
		return fmt.Errorf("debug API on %s needs TLS credentials (--cert/--key/--ca); without them it may only listen on loopback", debugServer.httpServer.Addr)
	}
	return nil
}

// isLoopbackListenAddress reports whether a host:port listen address binds
// only to a loopback interface.
func isLoopbackListenAddress(listenAddress string) bool {
	listenHost, _, splitError := net.SplitHostPort(listenAddress)
	if splitError != nil {
		return false
	}
	if listenHost == "localhost" {
		return true
	}
	listenIP := net.ParseIP(listenHost)
	return listenIP != nil && listenIP.IsLoopback()
}

// DebugContainerEntry is the JSON representation of a single container in the
// /debug/containers response.
type DebugContainerEntry struct {
	InstanceID    string `json:"instance_id"`
	Running       bool   `json:"running"`
	PID           int    `json:"pid,omitempty"`
	ExitCode      int    `json:"exit_code,omitempty"`
	Error         string `json:"error,omitempty"`
	CPUMillicores int64  `json:"cpu_millicores,omitempty"`
	MemoryBytes   int64  `json:"memory_bytes,omitempty"`
}

// DebugContainersResponse is the JSON envelope for /debug/containers.
type DebugContainersResponse struct {
	NodeID     string                `json:"node_id"`
	Count      int                   `json:"count"`
	Containers []DebugContainerEntry `json:"containers"`
}

// handleDebugContainers lists all containers tracked by the runtime along with
// their current resource usage.
func (debugServer *DebugServer) handleDebugContainers(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := request.Context()
	workloads, listError := debugServer.runtimeAdapter.List(ctx)
	if listError != nil {
		writeDebugJSONError(responseWriter, listError.Error(), http.StatusInternalServerError)
		return
	}

	sort.Slice(workloads, func(indexA, indexB int) bool {
		return workloads[indexA].ID < workloads[indexB].ID
	})

	entries := make([]DebugContainerEntry, 0, len(workloads))
	for _, workloadStatus := range workloads {
		entry := DebugContainerEntry{
			InstanceID: workloadStatus.ID,
			Running:    workloadStatus.Running,
			PID:        workloadStatus.PID,
			ExitCode:   workloadStatus.ExitCode,
			Error:      workloadStatus.Error,
		}
		if workloadStatus.Running {
			resourceStats, statsError := debugServer.runtimeAdapter.Stats(ctx, workloadStatus.ID)
			if statsError == nil {
				entry.CPUMillicores = resourceStats.CPUMillicores
				entry.MemoryBytes = resourceStats.MemoryBytes
			}
		}
		entries = append(entries, entry)
	}

	writeJSONResponse(responseWriter, DebugContainersResponse{
		NodeID:     debugServer.nodeID,
		Count:      len(entries),
		Containers: entries,
	})
}

// DebugImageEntry is the JSON representation of a single image in the
// /debug/images response.
type DebugImageEntry struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	ImageID    string `json:"image_id"`
	SizeBytes  int64  `json:"size_bytes"`
}

// DebugImagesResponse is the JSON envelope for /debug/images.
type DebugImagesResponse struct {
	NodeID string            `json:"node_id"`
	Count  int               `json:"count"`
	Images []DebugImageEntry `json:"images"`
}

// handleDebugImages lists locally cached container images. Returns an empty
// list when the runtime does not implement ImageLister (e.g. ProcessRuntime).
func (debugServer *DebugServer) handleDebugImages(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var images []DebugImageEntry

	if imageLister, supportsImages := debugServer.runtimeAdapter.(runtime.ImageLister); supportsImages {
		imageList, listError := imageLister.ListImages(request.Context())
		if listError != nil {
			writeDebugJSONError(responseWriter, listError.Error(), http.StatusInternalServerError)
			return
		}
		images = make([]DebugImageEntry, 0, len(imageList))
		for _, imageInfo := range imageList {
			images = append(images, DebugImageEntry{
				Repository: imageInfo.Repository,
				Tag:        imageInfo.Tag,
				ImageID:    imageInfo.ImageID,
				SizeBytes:  imageInfo.SizeBytes,
			})
		}
	}

	if images == nil {
		images = []DebugImageEntry{}
	}

	writeJSONResponse(responseWriter, DebugImagesResponse{
		NodeID: debugServer.nodeID,
		Count:  len(images),
		Images: images,
	})
}

// DebugStatsResponse is the JSON envelope for /debug/stats.
type DebugStatsResponse struct {
	NodeID             string `json:"node_id"`
	WorkloadCount      int    `json:"workload_count"`
	RunningCount       int    `json:"running_count"`
	StoppedCount       int    `json:"stopped_count"`
	LogDirCount        int    `json:"log_dir_count"`
	TotalCPUMillicores int64  `json:"total_cpu_millicores"`
	TotalMemoryBytes   int64  `json:"total_memory_bytes"`
}

// handleDebugStats returns aggregate resource usage across all running
// workloads on this node.
func (debugServer *DebugServer) handleDebugStats(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := request.Context()
	workloads, listError := debugServer.runtimeAdapter.List(ctx)
	if listError != nil {
		writeDebugJSONError(responseWriter, listError.Error(), http.StatusInternalServerError)
		return
	}

	var runningCount int
	var stoppedCount int
	var totalCPUMillicores int64
	var totalMemoryBytes int64

	for _, workloadStatus := range workloads {
		if !workloadStatus.Running {
			stoppedCount++
			continue
		}
		runningCount++
		resourceStats, statsError := debugServer.runtimeAdapter.Stats(ctx, workloadStatus.ID)
		if statsError == nil {
			totalCPUMillicores += resourceStats.CPUMillicores
			totalMemoryBytes += resourceStats.MemoryBytes
		}
	}

	logDirCount := countLogDirectories(debugServer.logBaseDir)

	writeJSONResponse(responseWriter, DebugStatsResponse{
		NodeID:             debugServer.nodeID,
		WorkloadCount:      len(workloads),
		RunningCount:       runningCount,
		StoppedCount:       stoppedCount,
		LogDirCount:        logDirCount,
		TotalCPUMillicores: totalCPUMillicores,
		TotalMemoryBytes:   totalMemoryBytes,
	})
}

// DebugExecRequest is the JSON body for POST /debug/exec.
type DebugExecRequest struct {
	InstanceID string   `json:"instance_id"` // instance to run the command in
	Args       []string `json:"args"`        // argv executed as given, without a shell (Flow 24)
}

// DebugExecResponse is the JSON envelope for /debug/exec.
type DebugExecResponse struct {
	NodeID     string `json:"node_id"`
	InstanceID string `json:"instance_id"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
}

// handleDebugExec runs a command inside a container and returns the output.
// Requires the runtime to implement ExecCapturer; returns 501 otherwise.
func (debugServer *DebugServer) handleDebugExec(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var execRequest DebugExecRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&execRequest); decodeError != nil {
		writeDebugJSONError(responseWriter, "invalid request body: "+decodeError.Error(), http.StatusBadRequest)
		return
	}
	if execRequest.InstanceID == "" || len(execRequest.Args) == 0 || execRequest.Args[0] == "" {
		writeDebugJSONError(responseWriter, "instance_id and args are required", http.StatusBadRequest)
		return
	}

	execCapturer, supportsCapture := debugServer.runtimeAdapter.(runtime.ExecCapturer)
	if !supportsCapture {
		writeDebugJSONError(responseWriter, "runtime does not support exec capture", http.StatusNotImplemented)
		return
	}

	output, execError := execCapturer.ExecCapture(
		request.Context(), execRequest.InstanceID, runtime.ExecSpec{Args: execRequest.Args})

	response := DebugExecResponse{
		NodeID:     debugServer.nodeID,
		InstanceID: execRequest.InstanceID,
		Output:     string(output),
	}
	if execError != nil {
		response.Error = execError.Error()
		response.ExitCode = 1
	}
	writeJSONResponse(responseWriter, response)
}

// handleDebugLogs streams container logs for a single instance. Query
// parameters: instance (required), tail, since, timestamps, follow.
func (debugServer *DebugServer) handleDebugLogs(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	queryParams := request.URL.Query()
	instanceID := queryParams.Get("instance")
	if instanceID == "" {
		writeDebugJSONError(responseWriter, "instance query parameter required", http.StatusBadRequest)
		return
	}

	logsOptions := runtime.LogsOptions{
		Follow:     queryParams.Get("follow") == "true",
		TailLines:  queryParams.Get("tail"),
		Since:      queryParams.Get("since"),
		Timestamps: queryParams.Get("timestamps") == "true",
	}

	logsReader, logsError := debugServer.runtimeAdapter.Logs(request.Context(), instanceID, logsOptions)
	if logsError != nil {
		writeDebugJSONError(responseWriter, "logs: "+logsError.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = logsReader.Close() }()

	responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	responseWriter.WriteHeader(http.StatusOK)
	if flusher, canFlush := responseWriter.(http.Flusher); canFlush {
		buffer := make([]byte, 4096)
		for {
			bytesRead, readError := logsReader.Read(buffer)
			if bytesRead > 0 {
				_, _ = responseWriter.Write(buffer[:bytesRead])
				flusher.Flush()
			}
			if readError != nil {
				return
			}
		}
	} else {
		_, _ = io.Copy(responseWriter, logsReader)
	}
}

// DebugImagePullRequest is the JSON body for POST /debug/images/pull.
type DebugImagePullRequest struct {
	ImageReference string `json:"image"`
}

// DebugImagePullResponse is the JSON envelope for /debug/images/pull.
type DebugImagePullResponse struct {
	NodeID         string `json:"node_id"`
	ImageReference string `json:"image"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
}

// handleDebugImagePull pulls a container image on this node. Requires the
// runtime to implement ImagePuller; returns 501 otherwise.
func (debugServer *DebugServer) handleDebugImagePull(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var pullRequest DebugImagePullRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&pullRequest); decodeError != nil {
		writeDebugJSONError(responseWriter, "invalid request body: "+decodeError.Error(), http.StatusBadRequest)
		return
	}
	if pullRequest.ImageReference == "" {
		writeDebugJSONError(responseWriter, "image is required", http.StatusBadRequest)
		return
	}

	imagePuller, supportsPull := debugServer.runtimeAdapter.(runtime.ImagePuller)
	if !supportsPull {
		writeDebugJSONError(responseWriter, "runtime does not support image pull", http.StatusNotImplemented)
		return
	}

	pullError := imagePuller.PullImage(request.Context(), pullRequest.ImageReference, "")
	response := DebugImagePullResponse{
		NodeID:         debugServer.nodeID,
		ImageReference: pullRequest.ImageReference,
		Success:        pullError == nil,
	}
	if pullError != nil {
		response.Error = pullError.Error()
	}
	writeJSONResponse(responseWriter, response)
}

// writeDebugJSONError writes a JSON-formatted error response with the given
// status code, ensuring the Content-Type is always application/json.
func writeDebugJSONError(responseWriter http.ResponseWriter, message string, statusCode int) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": message})
}

// writeJSONResponse marshals the payload as JSON and writes it with the
// appropriate Content-Type header. Sends a 500 on marshal failure.
func writeJSONResponse(responseWriter http.ResponseWriter, payload interface{}) {
	responseWriter.Header().Set("Content-Type", "application/json")
	if encodeError := json.NewEncoder(responseWriter).Encode(payload); encodeError != nil {
		writeDebugJSONError(responseWriter, encodeError.Error(), http.StatusInternalServerError)
	}
}

// countLogDirectories counts the number of immediate subdirectories under the
// log base dir. Each container gets its own subdirectory, so the count reflects
// how many containers have (or had) log storage on this node.
func countLogDirectories(logBaseDir string) int {
	if logBaseDir == "" {
		return 0
	}
	entries, readError := os.ReadDir(logBaseDir)
	if readError != nil {
		return 0
	}
	dirCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			dirCount++
		}
	}
	return dirCount
}
