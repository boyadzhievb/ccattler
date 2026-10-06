// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
)

// defaultDebugReadTimeout is the HTTP read timeout for the debug server.
const defaultDebugReadTimeout = 10 * time.Second

// defaultDebugWriteTimeout is the HTTP write timeout for the debug server.
const defaultDebugWriteTimeout = 30 * time.Second

// DebugServer serves agent-local HTTP debug endpoints that expose runtime
// state without going through the control plane. It provides three endpoints:
//
//   - GET /debug/containers — list all containers with their running state
//   - GET /debug/images     — list locally cached container images
//   - GET /debug/stats      — aggregate resource usage across all containers
type DebugServer struct {
	nodeID         string          // nodeID identifies this node in response payloads.
	runtimeAdapter runtime.Runtime // runtimeAdapter is the runtime to query for containers and stats.
	httpServer     *http.Server    // httpServer is the underlying HTTP server.
}

// NewDebugServer creates a DebugServer bound to the given listen address.
// The server is not started until Start is called.
func NewDebugServer(nodeID string, listenAddress string, runtimeAdapter runtime.Runtime) *DebugServer {
	debugServer := &DebugServer{
		nodeID:         nodeID,
		runtimeAdapter: runtimeAdapter,
	}

	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/debug/containers", debugServer.handleDebugContainers)
	serveMux.HandleFunc("/debug/images", debugServer.handleDebugImages)
	serveMux.HandleFunc("/debug/stats", debugServer.handleDebugStats)
	serveMux.HandleFunc("/debug/exec", debugServer.handleDebugExec)

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
	listener, listenError := net.Listen("tcp", debugServer.httpServer.Addr)
	if listenError != nil {
		return listenError
	}

	go func() { //nolint:gosec // G118: shutdown goroutine needs context.Background — parent ctx is already done
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = debugServer.httpServer.Shutdown(shutdownCtx)
	}()

	logging.Default().Info("debug API listening", "node", debugServer.nodeID, "address", debugServer.httpServer.Addr)
	if serveError := debugServer.httpServer.Serve(listener); serveError != nil && serveError != http.ErrServerClosed {
		return serveError
	}
	return nil
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
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := request.Context()
	workloads, listError := debugServer.runtimeAdapter.List(ctx)
	if listError != nil {
		http.Error(responseWriter, listError.Error(), http.StatusInternalServerError)
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
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var images []DebugImageEntry

	if imageLister, supportsImages := debugServer.runtimeAdapter.(runtime.ImageLister); supportsImages {
		imageList, listError := imageLister.ListImages(request.Context())
		if listError != nil {
			http.Error(responseWriter, listError.Error(), http.StatusInternalServerError)
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
	TotalCPUMillicores int64  `json:"total_cpu_millicores"`
	TotalMemoryBytes   int64  `json:"total_memory_bytes"`
}

// handleDebugStats returns aggregate resource usage across all running
// workloads on this node.
func (debugServer *DebugServer) handleDebugStats(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := request.Context()
	workloads, listError := debugServer.runtimeAdapter.List(ctx)
	if listError != nil {
		http.Error(responseWriter, listError.Error(), http.StatusInternalServerError)
		return
	}

	var runningCount int
	var totalCPUMillicores int64
	var totalMemoryBytes int64

	for _, workloadStatus := range workloads {
		if !workloadStatus.Running {
			continue
		}
		runningCount++
		resourceStats, statsError := debugServer.runtimeAdapter.Stats(ctx, workloadStatus.ID)
		if statsError == nil {
			totalCPUMillicores += resourceStats.CPUMillicores
			totalMemoryBytes += resourceStats.MemoryBytes
		}
	}

	writeJSONResponse(responseWriter, DebugStatsResponse{
		NodeID:             debugServer.nodeID,
		WorkloadCount:      len(workloads),
		RunningCount:       runningCount,
		TotalCPUMillicores: totalCPUMillicores,
		TotalMemoryBytes:   totalMemoryBytes,
	})
}

// DebugExecRequest is the JSON body for POST /debug/exec.
type DebugExecRequest struct {
	InstanceID string `json:"instance_id"`
	Command    string `json:"command"`
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
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var execRequest DebugExecRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&execRequest); decodeError != nil {
		http.Error(responseWriter, "invalid request body: "+decodeError.Error(), http.StatusBadRequest)
		return
	}
	if execRequest.InstanceID == "" || execRequest.Command == "" {
		http.Error(responseWriter, "instance_id and command are required", http.StatusBadRequest)
		return
	}

	execCapturer, supportsCapture := debugServer.runtimeAdapter.(runtime.ExecCapturer)
	if !supportsCapture {
		http.Error(responseWriter, "runtime does not support exec capture", http.StatusNotImplemented)
		return
	}

	output, execError := execCapturer.ExecCapture(
		request.Context(), execRequest.InstanceID, runtime.ExecSpec{Command: execRequest.Command})

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

// writeJSONResponse marshals the payload as JSON and writes it with the
// appropriate Content-Type header. Sends a 500 on marshal failure.
func writeJSONResponse(responseWriter http.ResponseWriter, payload interface{}) {
	responseWriter.Header().Set("Content-Type", "application/json")
	if encodeError := json.NewEncoder(responseWriter).Encode(payload); encodeError != nil {
		http.Error(responseWriter, encodeError.Error(), http.StatusInternalServerError)
	}
}
