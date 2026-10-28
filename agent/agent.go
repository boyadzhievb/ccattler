// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tracing"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultAgentReconcileInterval is the period between periodic reconciliation
// cycles when no explicit interval is set via SetInterval.
const defaultAgentReconcileInterval = 1 * time.Second

// watchReconnectDelay is the pause before re-establishing a watch after the
// watch channel closes (e.g. due to etcd compaction or store shutdown).
const watchReconnectDelay = 2 * time.Second

// heartbeatInterval is the period between independent heartbeat writes. The
// heartbeat goroutine is decoupled from the main reconciliation ticker so
// that long reconciliation cycles (e.g. slow container operations) cannot
// delay heartbeats and cause false node-failure detection.
const heartbeatInterval = 5 * time.Second

// dataPlaneReconcileInterval is the period between independent data plane
// reconciliation cycles. The data plane goroutine is decoupled from the main
// reconciliation ticker so that slow etcd calls in the main loop cannot stall
// VIP rule programming and stale-node eviction — the fast-path traffic shift.
const dataPlaneReconcileInterval = 1 * time.Second

// imagePullBaseDelay is the initial backoff delay after a failed image pull.
const imagePullBaseDelay = 10 * time.Second

// imagePullMaxDelay is the maximum backoff delay between image pull retries.
const imagePullMaxDelay = 5 * time.Minute

// imagePullTimeout is the context deadline for a single image pull attempt.
const imagePullTimeout = 5 * time.Minute

// lifecycleHookTimeout is the context deadline for a single lifecycle hook execution.
const lifecycleHookTimeout = 30 * time.Second

// imagePullBackoffEntry tracks retry state for a single instance's image pull.
type imagePullBackoffEntry struct {
	attempts   int       // attempts is the number of consecutive failed pull attempts.
	retryAfter time.Time // retryAfter is the earliest time the next pull attempt should start.
	lastError  string    // lastError is the error message from the most recent failure.
}

// imagePullStatus is the JSON-serialized pull progress written to the store.
type imagePullStatus struct {
	Error      string `json:"error,omitempty"`       // Error is the reason for the most recent failure.
	Attempts   int    `json:"attempts"`              // Attempts is the number of consecutive failures.
	RetryAfter string `json:"retry_after,omitempty"` // RetryAfter is the RFC3339 time of the next retry.
	Image      string `json:"image"`                 // Image is the reference being pulled.
}

// ccaSecretHelperPath is the container path where the cca-secret helper script
// is bind-mounted. Containers use this to fetch secrets from the agent's API.
const ccaSecretHelperPath = "/usr/local/bin/cca-secret" //nolint:gosec // G101: not a credential, this is a filesystem path

// ccaSecretHelperScript reads the named secret from the materialized directory.
// No curl or network tools needed — the agent bind-mounts decrypted secret
// files at /run/cca-secrets/{name} before starting the container.
const ccaSecretHelperScript = "#!/bin/sh\ncat \"/run/cca-secrets/$1\"\n" //nolint:gosec // G101: script template, not a credential

// ccaSecretMountPrefix is the container directory where all granted secrets are
// materialized as individual files named by their secret key.
const ccaSecretMountPrefix = "/run/cca-secrets/" //nolint:gosec // G101: not a credential, this is a filesystem path

// Agent is the node agent. It runs on each machine and bridges store <-> runtime.
//
// Delegates to four independent goroutines plus the main reconciliation loop:
//   - ProbeScheduler: health checks and probe execution on an independent timer
//   - HeartbeatLoop: writes heartbeat timestamps, decoupled from reconciliation
//   - DataPlaneLoop: VIP DNAT rules and VXLAN tunnels, decoupled from reconciliation
//   - NodeReporter: telemetry collection and node registration (inline in main loop)
type Agent struct {
	nodeID                   string                               // nodeID is the unique identifier for the node this agent manages.
	store                    store.StateStore                     // store is the fact store used to read desired state and write observed state.
	runtime                  runtime.Runtime                      // runtime is the pluggable container/process runtime adapter.
	networkProvider          network.NetworkProvider              // networkProvider allocates IPs for instances; nil means legacy 127.0.0.1 behavior.
	storageProvider          storage.StorageProvider              // storageProvider manages volume attach/detach; nil means no volume support.
	secretProvider           SecretProvider                       // secretProvider retrieves decrypted secrets; nil means no secret support.
	advertiseAddress         string                               // advertiseAddress is this node's LAN-routable IP for cross-host data plane.
	interval                 time.Duration                        // interval is the period between periodic reconciliation cycles.
	heartbeatFrequency       time.Duration                        // heartbeatFrequency is the period between independent heartbeat writes.
	appliedInstanceResources map[string]appliedResourceAllocation // appliedInstanceResources tracks the resources last applied to each running instance for resize detection.
	probeScheduler           *ProbeScheduler                      // probeScheduler runs health checks and probes independently of reconciliation.
	nodeReporter             *NodeReporter                        // nodeReporter collects telemetry and publishes node state to the store.
	dataPlaneReconciler      *DataPlaneReconciler                 // dataPlaneReconciler programs VIP DNAT rules via the data plane provider.
	secretAPIServer          *SecretAPIServer                     // secretAPIServer serves per-instance secret retrieval over HTTP; nil means secrets use file materialization.
	vxlanReconciler          *VXLANReconciler                     // vxlanReconciler manages VXLAN tunnel mesh between nodes; nil means overlay networking is disabled.
	dnsForwarder             *network.DNSForwarder                // dnsForwarder is the per-node caching DNS forwarder; nil means DNS forwarding is disabled.
	nodeSubnet               string                               // nodeSubnet is the pod CIDR allocated to this node (e.g. "10.100.1.0/24"), used for data plane node registration.
	imagePullBackoffs        map[string]*imagePullBackoffEntry    // imagePullBackoffs tracks per-instance image pull retry state.
	initStepBackoffs         map[string]*initStepBackoffEntry     // initStepBackoffs tracks per-instance init step retry state across reconcile cycles so the reconcile loop never blocks on retries.
	bridgeRetryAttempted     bool                                 // bridgeRetryAttempted is true after the first deferred bridge attachment attempt, preventing repeated retries on every container start.
	testHookChannel          chan func()                          // testHookChannel lets tests inject closures that execute inside the agent goroutine, avoiding data races on agent-internal state.
}

// New creates a new Agent for the given node, wired to the provided state store
// and runtime adapter. The default reconciliation interval is 1 second. The
// network provider is nil by default, meaning instances get 127.0.0.1 as their
// IP. Use SetNetworkProvider to enable real IP allocation.
func New(nodeID string, stateStore store.StateStore, runtimeAdapter runtime.Runtime) *Agent {
	return &Agent{
		nodeID:                   nodeID,
		store:                    stateStore,
		runtime:                  runtimeAdapter,
		interval:                 defaultAgentReconcileInterval,
		heartbeatFrequency:       heartbeatInterval,
		appliedInstanceResources: make(map[string]appliedResourceAllocation),
		imagePullBackoffs:        make(map[string]*imagePullBackoffEntry),
		initStepBackoffs:         make(map[string]*initStepBackoffEntry),
		probeScheduler:           NewProbeScheduler(nodeID, stateStore, runtimeAdapter, defaultAgentReconcileInterval),
		nodeReporter:             NewNodeReporter(nodeID, stateStore, runtimeAdapter, ""),
		dataPlaneReconciler:      NewDataPlaneReconciler(nodeID, stateStore, nil, ""),
	}
}

// SetNetworkProvider configures the agent to use the given network provider for
// instance IP allocation. When set, instances receive unique IPs from the
// node's subnet instead of the default 127.0.0.1.
func (nodeAgent *Agent) SetNetworkProvider(networkProvider network.NetworkProvider) {
	nodeAgent.networkProvider = networkProvider
}

// SetStorageProvider configures the agent to use the given storage provider
// for volume attach/detach operations. When set, the agent attaches volumes
// required by a service before starting its instances.
func (nodeAgent *Agent) SetStorageProvider(storageProvider storage.StorageProvider) {
	nodeAgent.storageProvider = storageProvider
}

// SetSecretProvider configures the agent to materialize secrets for instances.
// When set, the agent writes secret files before starting instances and removes
// them when instances stop.
func (nodeAgent *Agent) SetSecretProvider(secretProvider SecretProvider) {
	nodeAgent.secretProvider = secretProvider
}

// SecretProvider returns the agent's configured secret provider, or nil if
// secret support is not enabled.
func (nodeAgent *Agent) SecretProvider() SecretProvider {
	return nodeAgent.secretProvider
}

// SetSecretAPIServer configures the agent to use an HTTP-based secret API
// instead of file materialization. When set, the agent injects CCA_SECRET_URL
// and CCA_SECRET_TOKEN env vars into containers and skips file-based secrets.
func (nodeAgent *Agent) SetSecretAPIServer(secretAPIServer *SecretAPIServer) {
	nodeAgent.secretAPIServer = secretAPIServer
}

// SetDataPlaneProvider configures the agent to program VIP DNAT forwarding
// rules on each reconciliation cycle. When set, the agent reads VIP and
// endpoint facts from the store and calls the provider to ensure iptables
// rules match the desired state.
func (nodeAgent *Agent) SetDataPlaneProvider(dataPlaneProvider network.DataPlaneProvider) {
	nodeAgent.dataPlaneReconciler = NewDataPlaneReconciler(nodeAgent.nodeID, nodeAgent.store, dataPlaneProvider, nodeAgent.advertiseAddress)
}

// SetAdvertiseAddress configures the LAN-routable IP address for this node.
// This address is published to the store so other nodes' data plane
// providers can build cross-host DNAT targets.
func (nodeAgent *Agent) SetAdvertiseAddress(advertiseAddress string) {
	nodeAgent.advertiseAddress = advertiseAddress
	nodeAgent.nodeReporter.advertiseAddress = advertiseAddress
	nodeAgent.dataPlaneReconciler.advertiseAddress = advertiseAddress
}

// DataPlaneProvider returns the configured data plane provider, or nil if
// no data plane is configured. Used during shutdown for cleanup.
func (nodeAgent *Agent) DataPlaneProvider() network.DataPlaneProvider {
	return nodeAgent.dataPlaneReconciler.dataPlaneProvider
}

// SetVXLANReconciler configures the agent to manage VXLAN overlay tunnels
// for cross-node container networking. When set, the agent creates a VXLAN
// interface, publishes its subnet, and programs routes to peer nodes.
func (nodeAgent *Agent) SetVXLANReconciler(vxlanReconciler *VXLANReconciler) {
	nodeAgent.vxlanReconciler = vxlanReconciler
}

// VXLANReconciler returns the configured VXLAN reconciler, or nil if overlay
// networking is not enabled. Used during shutdown for cleanup.
func (nodeAgent *Agent) VXLANReconciler() *VXLANReconciler {
	return nodeAgent.vxlanReconciler
}

// SetDNSForwarder configures the per-node caching DNS forwarder. When set,
// the forwarder is started in Run() and connection probes use it for resolution.
func (nodeAgent *Agent) SetDNSForwarder(dnsForwarder *network.DNSForwarder) {
	nodeAgent.dnsForwarder = dnsForwarder
}

// SetNodeSubnet configures the pod CIDR allocated to this node (e.g.
// "10.100.1.0/24"). Used by data plane providers that need node subnet
// information for registration (e.g. eBPF node entries).
func (nodeAgent *Agent) SetNodeSubnet(nodeSubnet string) {
	nodeAgent.nodeSubnet = nodeSubnet
}

// SetInterval overrides the default periodic reconciliation interval.
// This is typically used in tests to speed up convergence. Zero or
// negative values are replaced with the default to avoid a panic in
// time.NewTicker.
func (nodeAgent *Agent) SetInterval(reconciliationInterval time.Duration) {
	if reconciliationInterval <= 0 {
		reconciliationInterval = defaultAgentReconcileInterval
	}
	nodeAgent.interval = reconciliationInterval
}

// SetHeartbeatInterval overrides the default heartbeat interval. The heartbeat
// runs in its own goroutine, independent of the reconciliation ticker. Must be
// called before Run. For integration tests with short lease timeouts, set this
// proportionally shorter. Zero or negative values are replaced with the default
// to avoid a panic in time.NewTicker.
func (nodeAgent *Agent) SetHeartbeatInterval(heartbeatFrequency time.Duration) {
	if heartbeatFrequency <= 0 {
		heartbeatFrequency = heartbeatInterval
	}
	nodeAgent.heartbeatFrequency = heartbeatFrequency
}

// EnableTestHooks activates the test hook channel so RunInAgentLoop can inject
// closures into the agent goroutine. Must be called before Run.
func (nodeAgent *Agent) EnableTestHooks() {
	nodeAgent.testHookChannel = make(chan func())
}

// RunInAgentLoop sends a closure to the agent goroutine and blocks until it
// executes. The closure runs inside the reconciliation loop, so it can safely
// access agent-internal state (imagePullBackoffs, etc.) without races.
func (nodeAgent *Agent) RunInAgentLoop(hookFunction func()) {
	done := make(chan struct{})
	nodeAgent.testHookChannel <- func() {
		hookFunction()
		close(done)
	}
	<-done
}

// drainTestHooks executes all pending test closures queued via RunInAgentLoop.
// No-op when testHookChannel is nil (production).
func (nodeAgent *Agent) drainTestHooks() {
	if nodeAgent.testHookChannel == nil {
		return
	}
	for {
		select {
		case hookFunction := <-nodeAgent.testHookChannel:
			hookFunction()
		default:
			return
		}
	}
}

// Run starts the agent loop. It blocks until ctx is cancelled.
//
// On startup it registers the node as alive in the store, performs an initial
// reconciliation, then enters a loop that reconciles on every tick of the
// interval timer and on every placement-key change observed via the store watch.
func (nodeAgent *Agent) Run(ctx context.Context) error {
	// Register this node and write initial heartbeat.
	nodeAgent.nodeReporter.RegisterNode(ctx)
	nodeAgent.nodeReporter.PublishAliveState(ctx)
	nodeAgent.nodeReporter.WriteHeartbeat(ctx)
	nodeAgent.nodeReporter.PublishAdvertiseAddress(ctx)

	if nodeAgent.vxlanReconciler != nil {
		nodeAgent.vxlanReconciler.EnsureSubnetPublished(ctx)
	}

	nodeAgent.initializeDataPlaneIfSupported(ctx)

	if nodeAgent.dnsForwarder != nil {
		go func() {
			if dnsError := nodeAgent.dnsForwarder.Start(ctx); dnsError != nil && ctx.Err() == nil {
				logging.Default().Error("DNS forwarder error", "agent", nodeAgent.nodeID, "error", dnsError.Error())
			}
		}()
	}

	// Initial reconcile.
	if err := nodeAgent.executeReconciliationCycle(ctx); err != nil {
		logging.Default().Error("initial reconcile error", "agent", nodeAgent.nodeID, "error", err.Error())
	}

	// Start the independent probe scheduler goroutine.
	go nodeAgent.probeScheduler.Run(ctx, nodeAgent.findInstancesPlacedOnThisNode)

	// Start the independent heartbeat goroutine so that long reconciliation
	// cycles cannot delay heartbeats and cause false node-failure detection.
	go nodeAgent.heartbeatLoop(ctx)

	// Start the independent data plane goroutine so that slow etcd calls in
	// the main reconciliation loop cannot stall VIP rule updates and
	// stale-node eviction — the fast-path traffic shift.
	go nodeAgent.dataPlaneLoop(ctx)

	// Retry loop: re-establish watch on channel closure.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		watchLoopError := nodeAgent.runWatchLoop(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logging.Default().Warn("watch loop exited, reconnecting",
			"agent", nodeAgent.nodeID, "error", watchLoopError.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(watchReconnectDelay):
		}
	}
}

// runWatchLoop establishes a placement watch and runs the reconciliation
// select loop. Returns an error when the watch channel closes, allowing
// the caller to reconnect.
func (nodeAgent *Agent) runWatchLoop(ctx context.Context) error {
	ticker := time.NewTicker(nodeAgent.interval)
	defer ticker.Stop()

	placementCh, err := nodeAgent.store.Watch(ctx, types.ScanPlacementsForNode(nodeAgent.nodeID), store.WatchOption{Prefix: true})
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if reconcileErr := nodeAgent.executeReconciliationCycle(ctx); reconcileErr != nil {
				logging.Default().Error("reconcile error", "agent", nodeAgent.nodeID, "error", reconcileErr.Error())
			}
			nodeAgent.nodeReporter.CollectAndReportTelemetry(ctx)
		case watchEvent, ok := <-placementCh:
			if !ok {
				return fmt.Errorf("watch channel closed (compaction or store shutdown)")
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if watchEvent.Type == store.EventOverflow {
				logging.Default().Warn("watch events dropped, triggering full resync", "agent", nodeAgent.nodeID)
			}
			if watchEvent.Type == store.EventCompacted {
				logging.Default().Warn("watch revision compacted, triggering full resync", "agent", nodeAgent.nodeID)
			}
			if reconcileErr := nodeAgent.executeReconciliationCycle(ctx); reconcileErr != nil {
				logging.Default().Error("reconcile error", "agent", nodeAgent.nodeID, "error", reconcileErr.Error())
			}
		}
	}
}

// initializeDataPlaneIfSupported checks whether the data plane provider
// implements the optional DataPlaneInitializer and DataPlaneNodeRegistrar
// interfaces. If so, it calls Initialize (e.g. to scan existing identities and
// create leases) and RegisterNode (to publish this node's address and subnet
// for tunnel discovery). Called once at agent startup before the first reconcile.
func (nodeAgent *Agent) initializeDataPlaneIfSupported(ctx context.Context) {
	dataPlaneProvider := nodeAgent.dataPlaneReconciler.dataPlaneProvider
	if dataPlaneProvider == nil {
		return
	}

	if initializer, supportsInit := dataPlaneProvider.(network.DataPlaneInitializer); supportsInit {
		if initError := initializer.Initialize(ctx); initError != nil {
			logging.Default().Error("data plane initialization failed", "agent", nodeAgent.nodeID, "error", initError.Error())
			return
		}
		logging.Default().Info("data plane initialized", "agent", nodeAgent.nodeID)
	}

	if registrar, supportsNodeReg := dataPlaneProvider.(network.DataPlaneNodeRegistrar); supportsNodeReg {
		if nodeAgent.advertiseAddress != "" && nodeAgent.nodeSubnet != "" {
			if regError := registrar.RegisterNode(ctx, nodeAgent.advertiseAddress, nodeAgent.nodeSubnet); regError != nil {
				logging.Default().Error("data plane node registration failed", "agent", nodeAgent.nodeID, "error", regError.Error())
			} else {
				logging.Default().Info("data plane node registered", "agent", nodeAgent.nodeID, "ip", nodeAgent.advertiseAddress, "subnet", nodeAgent.nodeSubnet)
			}
		}
	}
}

// retryBridgeAttachmentIfNeeded attempts deferred eBPF tc bridge attachment
// after the first container start creates the CNI bridge interface. Called once
// per agent lifetime — subsequent container starts skip the retry because the
// bridge is already attached or the provider does not support deferred retry.
func (nodeAgent *Agent) retryBridgeAttachmentIfNeeded(ctx context.Context) {
	if nodeAgent.bridgeRetryAttempted {
		return
	}
	nodeAgent.bridgeRetryAttempted = true

	dataPlaneProvider := nodeAgent.dataPlaneReconciler.dataPlaneProvider
	if dataPlaneProvider == nil {
		return
	}
	bridgeRetrier, supportsBridgeRetry := dataPlaneProvider.(network.DataPlaneBridgeRetrier)
	if !supportsBridgeRetry {
		return
	}
	if nodeAgent.nodeSubnet == "" {
		return
	}
	if retryError := bridgeRetrier.RetryBridgeAttachment(ctx, nodeAgent.nodeSubnet); retryError != nil {
		logging.Default().Error("deferred bridge attachment failed", "agent", nodeAgent.nodeID, "error", retryError.Error())
	} else {
		logging.Default().Info("deferred bridge attachment succeeded", "agent", nodeAgent.nodeID, "subnet", nodeAgent.nodeSubnet)
	}
}

// registerEndpointIdentityIfSupported checks if the data plane provider
// implements EndpointIdentityRegistrar and writes an IP-to-identity mapping
// for a newly started container. Called from publishInstanceStateToStore after
// the container IP is known. Skipped if the IP is empty or the provider
// does not support identity registration.
func (nodeAgent *Agent) registerEndpointIdentityIfSupported(ctx context.Context, containerIP string, serviceName string) {
	if containerIP == "" {
		return
	}
	dataPlaneProvider := nodeAgent.dataPlaneReconciler.dataPlaneProvider
	if dataPlaneProvider == nil {
		return
	}
	registrar, supportsIdentityReg := dataPlaneProvider.(network.EndpointIdentityRegistrar)
	if !supportsIdentityReg {
		return
	}

	exposedPorts := nodeAgent.lookupServiceExposedPortsFromStore(ctx, serviceName)
	containerPort := 0
	if len(exposedPorts) > 0 {
		containerPort = exposedPorts[0]
	}

	if regError := registrar.RegisterEndpointIdentity(ctx, containerIP, nodeAgent.advertiseAddress, serviceName, containerPort); regError != nil {
		logging.Default().Error("endpoint identity registration failed",
			"agent", nodeAgent.nodeID, "ip", containerIP, "service", serviceName, "error", regError.Error())
	}
}

// heartbeatLoop runs independently of the main reconciliation ticker, writing
// heartbeat timestamps at a fixed interval. This ensures heartbeats are not
// delayed by long reconciliation cycles (e.g. slow container operations).
// Only the heartbeat timestamp is written here — the NodeFailureController
// owns all node state transitions (unreachable→alive recovery included).
func (nodeAgent *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(nodeAgent.heartbeatFrequency)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nodeAgent.nodeReporter.WriteHeartbeat(ctx)
		}
	}
}

// dataPlaneLoop runs independently of the main reconciliation ticker,
// programming VIP DNAT rules and VXLAN tunnels at a fixed interval. This
// decoupling is critical: the fast-path data plane must detect stale nodes
// and shift traffic within seconds, even if the main loop is blocked on a
// slow etcd call (reconcile, telemetry, etc.).
func (nodeAgent *Agent) dataPlaneLoop(ctx context.Context) {
	ticker := time.NewTicker(dataPlaneReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nodeAgent.dataPlaneReconciler.Reconcile(ctx)
			if nodeAgent.vxlanReconciler != nil {
				nodeAgent.vxlanReconciler.Reconcile(ctx)
			}
		}
	}
}

// executeReconciliationCycle performs a single reconciliation pass: it compares
// the set of instances placed on this node (desired) with what the runtime
// reports as actually running, starts missing instances, stops stale ones,
// and reports health for every desired instance that has a health probe.
func (nodeAgent *Agent) executeReconciliationCycle(ctx context.Context) error {
	nodeAgent.drainTestHooks()
	reconcileTrace := tracing.NewTraceContext()
	ctx = tracing.ContextWithTrace(ctx, reconcileTrace)

	// Find instances placed on this node.
	desired, err := nodeAgent.findInstancesPlacedOnThisNode(ctx)
	if err != nil {
		return err
	}

	// Get current runtime state.
	running, err := nodeAgent.runtime.List(ctx)
	if err != nil {
		return err
	}
	runningByID := make(map[string]runtime.Status)
	for _, runtimeStatus := range running {
		runningByID[runtimeStatus.ID] = runtimeStatus
	}

	logging.Default().Debug("reconcile cycle",
		"agent", nodeAgent.nodeID,
		"desired", strconv.Itoa(len(desired)),
		"running", strconv.Itoa(len(running)))

	desiredByID := make(map[string]bool, len(desired))
	for _, instanceInfo := range desired {
		desiredByID[instanceInfo.id] = true
	}

	// Sort instances: services without init steps first, so infrastructure
	// services (databases, caches) start before dependent services that need
	// init connection checks. Without this ordering, a dependent service can
	// block the reconcile loop retrying init while the service it depends on
	// has not been started yet.
	sort.SliceStable(desired, func(leftIndex, rightIndex int) bool {
		leftHasInit := nodeAgent.hasInitSteps(ctx, desired[leftIndex].service)
		rightHasInit := nodeAgent.hasInitSteps(ctx, desired[rightIndex].service)
		if leftHasInit != rightHasInit {
			return !leftHasInit
		}
		return false
	})

	// Reconcile each desired instance: start missing, observe running, resize if needed.
	for _, instanceInfo := range desired {
		runtimeStatus, exists := runningByID[instanceInfo.id]
		if !exists || !runtimeStatus.Running {
			if exists && runtimeStatus.ExitCode != 0 {
				nodeAgent.publishExitReasonFromStatus(ctx, instanceInfo.id, runtimeStatus)
			}
			logging.Default().Info("starting instance",
				"agent", nodeAgent.nodeID,
				"instance", instanceInfo.id,
				"service", instanceInfo.service)
			nodeAgent.reconcileDesiredInstance(ctx, instanceInfo)
		} else if !nodeAgent.detectConfigChange(ctx, instanceInfo) {
			nodeAgent.reconcileResourceChanges(ctx, instanceInfo)
			nodeAgent.republishHostPortIfNeeded(ctx, instanceInfo.id)
			observedState, exitReason := nodeAgent.observeInstanceState(ctx, instanceInfo.id)
			nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, observedState, runtimeStatus.IP)
			nodeAgent.publishExitReasonIfFailed(ctx, instanceInfo.id, observedState, exitReason)
		}
		delete(runningByID, instanceInfo.id)
	}

	// Clean up stale instances no longer placed on this node. Both running
	// and stopped containers must be removed — a stopped container whose
	// instance ID was replaced by the scheduler will never be restarted and
	// would leak containerd resources (tasks, snapshots, log directories).
	for instanceID, runtimeStatus := range runningByID {
		if runtimeStatus.Running {
			nodeAgent.cleanupUndesiredInstance(ctx, instanceID)
		} else {
			nodeAgent.cleanupStoppedStaleInstance(ctx, instanceID)
		}
	}

	// Clean up pull-backoff state for instances no longer placed on this node.
	for backoffInstanceID := range nodeAgent.imagePullBackoffs {
		if !desiredByID[backoffInstanceID] {
			delete(nodeAgent.imagePullBackoffs, backoffInstanceID)
			nodeAgent.clearPullStatusFromStore(ctx, backoffInstanceID)
		}
	}

	// Clean up init-step backoff state for instances no longer placed on this node.
	for backoffKey := range nodeAgent.initStepBackoffs {
		separatorIndex := strings.Index(backoffKey, "/")
		if separatorIndex < 0 {
			continue
		}
		instanceID := backoffKey[:separatorIndex]
		if !desiredByID[instanceID] {
			delete(nodeAgent.initStepBackoffs, backoffKey)
		}
	}

	return nil
}

// placedInstanceInfo holds the identity of an instance that the scheduler has
// placed on this node. It is used internally during reconciliation to pair
// each instance with its owning service.
type placedInstanceInfo struct {
	id      string // id is the unique instance identifier (e.g. "aaa").
	service string // service is the name of the service this instance belongs to (e.g. "web").
}

// appliedResourceAllocation tracks the CPU and memory resources that were
// last applied to a running instance. The agent uses this to detect when
// effective resources change (e.g. from vertical autoscaling) and trigger
// a live resize or stop-and-restart.
type appliedResourceAllocation struct {
	cpuMillicores int64 // cpuMillicores is the CPU allocation in millicores last applied to this instance.
	memoryBytes   int64 // memoryBytes is the memory allocation in bytes last applied to this instance.
}

// findInstancesPlacedOnThisNode scans the per-node placement index for this
// agent's nodeID, returning only instances assigned to this node. Instances
// that are in the "stopped" state or marked for drain eviction are excluded.
func (nodeAgent *Agent) findInstancesPlacedOnThisNode(ctx context.Context) ([]placedInstanceInfo, error) {
	scanPrefix := types.ScanPlacementsForNode(nodeAgent.nodeID)
	placements, err := nodeAgent.store.Scan(ctx, scanPrefix)
	if err != nil {
		return nil, err
	}

	var result []placedInstanceInfo
	for _, placementFact := range placements {
		instanceID := strings.TrimPrefix(placementFact.Key, scanPrefix)

		// Skip instances marked by controllers via derived/ markers.
		if nodeAgent.isInstanceMarkedForSkip(ctx, instanceID) {
			continue
		}

		// Skip instances that are stopped or failed in observed state.
		stateFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceState(instanceID))
		if err == nil {
			instanceState := types.InstanceState(stateFact.Value)
			if instanceState == types.InstanceStopped || instanceState == types.InstanceFailed {
				continue
			}
		}

		// Check for drain eviction marker written by the DrainController.
		evictKey := types.KeyDerivedNodeDrainEvict(nodeAgent.nodeID, instanceID)
		if _, evictError := nodeAgent.store.Get(ctx, evictKey); evictError == nil {
			continue
		}

		// Get the service name from observed state, or derive it from the
		// instance ID (<service>-<hash>) when no observed fact exists yet.
		// On fresh deployments the agent hasn't published any observed state,
		// so relying solely on KeyObservedInstanceService would silently skip
		// every newly placed instance.
		var serviceName string
		serviceFact, serviceError := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
		if serviceError == nil {
			serviceName = string(serviceFact.Value)
		} else {
			lastDash := strings.LastIndex(instanceID, "-")
			if lastDash > 0 {
				serviceName = instanceID[:lastDash]
			}
		}
		if serviceName == "" {
			continue
		}

		result = append(result, placedInstanceInfo{
			id:      instanceID,
			service: serviceName,
		})
	}
	return result, nil
}

// reconcileDesiredInstance brings up a single instance that should be running
// but isn't. Handles init steps, volume attachment, config/env resolution,
// network allocation, container start, host port publishing, and state
// observation. If any prerequisite fails (image lookup, init, volumes), the
// instance is skipped for this cycle. When the secret API is configured, a
// bearer token is registered before init steps so both init containers and the
// main container can fetch secrets via the cca-secret helper.
func (nodeAgent *Agent) reconcileDesiredInstance(ctx context.Context, instanceInfo placedInstanceInfo) {
	image := nodeAgent.lookupServiceImageFromStore(ctx, instanceInfo.service)
	if image == "" {
		return
	}

	credentials := nodeAgent.resolveImagePullCredentials(ctx, instanceInfo.service)

	if !nodeAgent.ensureImageAvailable(ctx, instanceInfo, image, credentials) {
		return
	}

	var secretAPIToken string
	var secretAPIBaseURL string
	if nodeAgent.secretAPIServer != nil {
		var registerError error
		secretAPIToken, registerError = nodeAgent.secretAPIServer.RegisterInstanceToken(instanceInfo.id, instanceInfo.service)
		if registerError != nil {
			logging.Default().Error("failed to register secret token", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", registerError.Error())
		} else {
			secretAPIBaseURL = nodeAgent.resolveSecretAPIBaseURL()
		}
	}

	if nodeAgent.hasInitSteps(ctx, instanceInfo.service) {
		initSucceeded := nodeAgent.executeInitializationSteps(ctx, instanceInfo, image)
		if !initSucceeded {
			logging.Default().Warn("init failed, skipping workload start", "agent", nodeAgent.nodeID, "instance", instanceInfo.id)
			if secretAPIToken != "" {
				nodeAgent.secretAPIServer.RevokeInstanceToken(instanceInfo.id)
			}
			return
		}
	}

	if nodeAgent.storageProvider != nil {
		nodeAgent.publishStatusReason(ctx, instanceInfo.id, "waiting for volumes")
		volumesReady, attachError := nodeAgent.ensureVolumesAttachedForInstance(ctx, instanceInfo)
		if attachError != nil {
			logging.Default().Error("volume error", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", attachError.Error())
		}
		if !volumesReady {
			if secretAPIToken != "" {
				nodeAgent.secretAPIServer.RevokeInstanceToken(instanceInfo.id)
			}
			return
		}
	}

	nodeAgent.publishStatusReason(ctx, instanceInfo.id, "starting")
	runtimeSpec := nodeAgent.buildRuntimeSpecForInstance(ctx, instanceInfo, image)
	nodeAgent.materializeSecretFiles(ctx, instanceInfo.service, &runtimeSpec)
	nodeAgent.materializeConnectionFiles(ctx, instanceInfo.service, &runtimeSpec)
	if secretAPIToken != "" {
		nodeAgent.injectSecretAPIIntoSpec(secretAPIToken, secretAPIBaseURL, &runtimeSpec)
	}
	nodeAgent.startInstanceAndPublishState(ctx, instanceInfo, image, runtimeSpec)
}

// ensureImageAvailable checks that the container image is locally cached,
// pulling it if necessary. Returns true when the image is ready. Returns
// false when the runtime does not support pulling, the image is being
// pulled, or the pull is in backoff — the caller should skip this instance.
// Respects the service's pull_policy fact: "never" skips pulling, "always"
// re-pulls even when cached, "if_not_present" or empty uses cache-first.
// When credentials is non-empty, it is passed as user:pass to the runtime.
func (nodeAgent *Agent) ensureImageAvailable(ctx context.Context, instanceInfo placedInstanceInfo, image string, credentials string) bool {
	imagePuller, supportsPull := nodeAgent.runtime.(runtime.ImagePuller)
	if !supportsPull {
		return true
	}

	pullPolicy := nodeAgent.lookupServicePullPolicyFromStore(ctx, instanceInfo.service)

	if pullPolicy == "never" {
		return true
	}

	if pullPolicy != "always" && imagePuller.IsImageCached(ctx, image) {
		delete(nodeAgent.imagePullBackoffs, instanceInfo.id)
		nodeAgent.clearPullStatusFromStore(ctx, instanceInfo.id)
		return true
	}

	if backoffEntry, hasBackoff := nodeAgent.imagePullBackoffs[instanceInfo.id]; hasBackoff {
		if time.Now().Before(backoffEntry.retryAfter) {
			return false
		}
	}

	nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstancePulling, "")
	logging.Default().Info("pulling image", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "image", image)

	pullContext, cancelPull := context.WithTimeout(ctx, imagePullTimeout)
	defer cancelPull()

	if pullError := imagePuller.PullImage(pullContext, image, credentials); pullError != nil {
		nodeAgent.recordImagePullFailure(ctx, instanceInfo, image, pullError.Error())
		return false
	}

	delete(nodeAgent.imagePullBackoffs, instanceInfo.id)
	nodeAgent.clearPullStatusFromStore(ctx, instanceInfo.id)
	logging.Default().Info("image pulled", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "image", image)
	return true
}

// recordImagePullFailure increments the backoff counter for an instance and
// writes the pull status to the store so it's visible via cca get instances.
func (nodeAgent *Agent) recordImagePullFailure(ctx context.Context, instanceInfo placedInstanceInfo, image string, errorMessage string) {
	backoffEntry, exists := nodeAgent.imagePullBackoffs[instanceInfo.id]
	if !exists {
		backoffEntry = &imagePullBackoffEntry{}
		nodeAgent.imagePullBackoffs[instanceInfo.id] = backoffEntry
	}
	backoffEntry.attempts++
	backoffEntry.lastError = errorMessage

	delaySeconds := float64(imagePullBaseDelay/time.Second) * math.Pow(2, float64(backoffEntry.attempts-1))
	if delaySeconds > float64(imagePullMaxDelay/time.Second) {
		delaySeconds = float64(imagePullMaxDelay / time.Second)
	}
	backoffEntry.retryAfter = time.Now().Add(time.Duration(delaySeconds) * time.Second)

	logging.Default().Warn("image pull failed",
		"agent", nodeAgent.nodeID, "instance", instanceInfo.id, "image", image,
		"attempt", strconv.Itoa(backoffEntry.attempts), "retry_after", backoffEntry.retryAfter.Format(time.RFC3339),
		"error", errorMessage)

	nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceImagePullBackoff, "")

	pullStatus := imagePullStatus{
		Error:      errorMessage,
		Attempts:   backoffEntry.attempts,
		RetryAfter: backoffEntry.retryAfter.Format(time.RFC3339),
		Image:      image,
	}
	statusJSON, marshalError := json.Marshal(pullStatus)
	if marshalError != nil {
		return
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstancePullStatus(instanceInfo.id), statusJSON); putError != nil {
		logging.Default().Error("failed to write pull status", "instance", instanceInfo.id, "error", putError.Error())
	}
	statusReason := fmt.Sprintf("pulling image %s (attempt %d): %s", image, backoffEntry.attempts, errorMessage)
	nodeAgent.publishStatusReason(ctx, instanceInfo.id, statusReason)
}

// publishStatusReason writes a human-readable status reason to the store so
// the CLI and API can show why an instance is in its current state.
func (nodeAgent *Agent) publishStatusReason(ctx context.Context, instanceID string, reason string) {
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceStatusReason(instanceID), []byte(reason)); putError != nil {
		logging.Default().Error("failed to write status reason", "instance", instanceID, "error", putError.Error())
	}
}

// clearPullStatusFromStore removes the pull-status fact after a successful pull.
func (nodeAgent *Agent) clearPullStatusFromStore(ctx context.Context, instanceID string) {
	if deleteError := nodeAgent.store.Delete(ctx, types.KeyObservedInstancePullStatus(instanceID)); deleteError != nil && !errors.Is(deleteError, store.ErrKeyNotFound) {
		logging.Default().Error("failed to delete pull status", "instance", instanceID, "error", deleteError.Error())
	}
}

// publishExitReasonFromStatus publishes an exit-reason fact from the runtime
// status when a stopped workload has a non-zero exit code.
func (nodeAgent *Agent) publishExitReasonFromStatus(ctx context.Context, instanceID string, runtimeStatus runtime.Status) {
	exitReason := runtimeStatus.Error
	if exitReason == "" && runtimeStatus.ExitCode != 0 {
		exitReason = fmt.Sprintf("exit(%d)", runtimeStatus.ExitCode)
	}
	if exitReason != "" {
		if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceExitReason(instanceID), []byte(exitReason)); putError != nil {
			logging.Default().Error("failed to publish exit reason", "instance", instanceID, "error", putError.Error())
		}
	}
}

// publishExitReasonIfFailed writes the exit-reason fact when an instance is
// failed, or deletes it when the instance has recovered.
func (nodeAgent *Agent) publishExitReasonIfFailed(ctx context.Context, instanceID string, state types.InstanceState, exitReason string) {
	if state == types.InstanceFailed && exitReason != "" {
		if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceExitReason(instanceID), []byte(exitReason)); putError != nil {
			logging.Default().Error("failed to publish exit reason", "instance", instanceID, "error", putError.Error())
		}
	} else {
		if deleteError := nodeAgent.store.Delete(ctx, types.KeyObservedInstanceExitReason(instanceID)); deleteError != nil && !errors.Is(deleteError, store.ErrKeyNotFound) {
			logging.Default().Error("failed to delete exit reason", "instance", instanceID, "error", deleteError.Error())
		}
	}
}

// buildRuntimeSpecForInstance resolves the complete runtime specification for an
// instance by reading config env vars, config files, exposed ports, resource
// limits, and allocating a network IP address from the configured provider.
func (nodeAgent *Agent) buildRuntimeSpecForInstance(ctx context.Context, instanceInfo placedInstanceInfo, image string) runtime.Spec {
	envVars := nodeAgent.resolveServiceConfigEnvVars(ctx, instanceInfo.service)
	configFiles := nodeAgent.resolveServiceConfigFiles(ctx, instanceInfo.service)
	exposedPorts := nodeAgent.lookupServiceExposedPortsFromStore(ctx, instanceInfo.service)
	cpuMillicores, memoryBytes := nodeAgent.nodeReporter.lookupServiceResourcesFromStore(ctx, instanceInfo.service)
	commandArgs := nodeAgent.lookupServiceCommandFromStore(ctx, instanceInfo.service)

	allocatedIP := ""
	if nodeAgent.networkProvider != nil {
		networkIP, allocateError := nodeAgent.networkProvider.AllocateIP(ctx, nodeAgent.nodeID, instanceInfo.id)
		if allocateError != nil {
			logging.Default().Error("failed to allocate IP", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", allocateError.Error())
		} else {
			allocatedIP = networkIP
		}
	}

	return runtime.Spec{
		ID:          instanceInfo.id,
		ServiceName: instanceInfo.service,
		Image:       image,
		Command:     commandArgs,
		Env:         envVars,
		ConfigFiles: configFiles,
		Ports:       exposedPorts,
		IP:          allocatedIP,
		CPUm:        cpuMillicores,
		MemoryB:     memoryBytes,
	}
}

// startInstanceAndPublishState starts an instance using the given runtime spec,
// records the applied resource allocation, publishes host port information for
// container runtimes, observes the instance state from the runtime, and writes
// the observed state and image to the store. On failure, cleans up secrets,
// publishes a failed state, and records the image reference.
func (nodeAgent *Agent) startInstanceAndPublishState(ctx context.Context, instanceInfo placedInstanceInfo, image string, runtimeSpec runtime.Spec) {
	if startError := nodeAgent.runtime.Start(ctx, runtimeSpec); startError != nil {
		logging.Default().Error("failed to start instance", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", startError.Error())
		if nodeAgent.secretAPIServer != nil {
			nodeAgent.secretAPIServer.RevokeInstanceToken(instanceInfo.id)
		}
		nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceFailed, "")
		nodeAgent.publishStatusReason(ctx, instanceInfo.id, "start failed: "+startError.Error())
		if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceImage(instanceInfo.id), []byte(image)); putError != nil {
			logging.Default().Error("failed to write instance image", "instance", instanceInfo.id, "error", putError.Error())
		}
		return
	}

	nodeAgent.appliedInstanceResources[instanceInfo.id] = appliedResourceAllocation{
		cpuMillicores: runtimeSpec.CPUm,
		memoryBytes:   runtimeSpec.MemoryB,
	}

	nodeAgent.retryBridgeAttachmentIfNeeded(ctx)

	nodeAgent.executeLifecycleHook(ctx, instanceInfo.id, instanceInfo.service, "post_start")

	runtimeIP := ""
	if hostPortRuntime, hasHostPorts := nodeAgent.runtime.(runtime.HostPortProvider); hasHostPorts {
		portMappings := hostPortRuntime.HostPortsForInstance(instanceInfo.id)
		publishInstanceHostPorts(ctx, nodeAgent.store, instanceInfo.id, portMappings)
	}
	if runtimeStatus, statusErr := nodeAgent.runtime.Status(ctx, instanceInfo.id); statusErr == nil && runtimeStatus.IP != "" {
		runtimeIP = runtimeStatus.IP
	}

	observedState, exitReason := nodeAgent.observeInstanceState(ctx, instanceInfo.id)
	nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, observedState, runtimeIP)
	nodeAgent.publishExitReasonIfFailed(ctx, instanceInfo.id, observedState, exitReason)
	if observedState == types.InstanceRunning {
		nodeAgent.publishStatusReason(ctx, instanceInfo.id, "running")
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceImage(instanceInfo.id), []byte(image)); putError != nil {
		logging.Default().Error("failed to write instance image", "instance", instanceInfo.id, "error", putError.Error())
	}
	configHash := nodeAgent.computeServiceConfigHash(ctx, instanceInfo.service)
	if configHash != "" {
		if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceConfigHash(instanceInfo.id), []byte(configHash)); putError != nil {
			logging.Default().Error("failed to write config hash", "instance", instanceInfo.id, "error", putError.Error())
		}
	}
}

// republishHostPortIfNeeded re-publishes the host port fact for a running
// instance on every steady-state cycle. The endpoint controller requires
// this fact to create endpoints with host-reachable addresses, and the
// data plane reconciler on remote nodes needs it for cross-node DNAT rules.
func (nodeAgent *Agent) republishHostPortIfNeeded(ctx context.Context, instanceID string) {
	if hostPortRuntime, hasHostPorts := nodeAgent.runtime.(runtime.HostPortProvider); hasHostPorts {
		portMappings := hostPortRuntime.HostPortsForInstance(instanceID)
		publishInstanceHostPorts(ctx, nodeAgent.store, instanceID, portMappings)
	}
}

// cleanupStoppedStaleInstance removes a container that has already exited and
// whose instance ID is no longer in the desired set. Unlike cleanupUndesiredInstance,
// it skips lifecycle hooks and graceful stop since the process is already dead.
func (nodeAgent *Agent) cleanupStoppedStaleInstance(ctx context.Context, instanceID string) {
	logging.Default().Info("cleaning up stopped stale container", "agent", nodeAgent.nodeID, "instance", instanceID)
	delete(nodeAgent.appliedInstanceResources, instanceID)
	delete(nodeAgent.imagePullBackoffs, instanceID)
	nodeAgent.clearPullStatusFromStore(ctx, instanceID)
	if nodeAgent.secretAPIServer != nil {
		nodeAgent.secretAPIServer.RevokeInstanceToken(instanceID)
	}
	if nodeAgent.storageProvider != nil {
		nodeAgent.detachVolumesForInstance(ctx, instanceID)
	}
	if stopError := nodeAgent.runtime.Stop(ctx, instanceID); stopError != nil {
		logging.Default().Error("failed to remove stopped container", "instance", instanceID, "error", stopError.Error())
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceStopped))); putError != nil {
		logging.Default().Error("failed to publish stopped state for stale instance", "instance", instanceID, "error", putError.Error())
	}
	nodeAgent.probeScheduler.CleanupInstance(instanceID)
	if nodeAgent.networkProvider != nil {
		if releaseError := nodeAgent.networkProvider.ReleaseIP(ctx, nodeAgent.nodeID, instanceID); releaseError != nil {
			logging.Default().Error("failed to release IP for stale instance", "instance", instanceID, "error", releaseError.Error())
		}
		if deleteError := types.DeleteNetworkAllocation(ctx, nodeAgent.store, instanceID); deleteError != nil {
			logging.Default().Error("failed to delete network allocation for stale instance", "instance", instanceID, "error", deleteError.Error())
		}
	}
}

// cleanupUndesiredInstance tears down an instance that is no longer placed on
// this node. Cleans up secrets, detaches volumes, stops the runtime process,
// removes probe state, releases the network IP, deletes the network
// allocation fact, and removes the applied resource tracking entry.
func (nodeAgent *Agent) cleanupUndesiredInstance(ctx context.Context, instanceID string) {
	delete(nodeAgent.appliedInstanceResources, instanceID)
	delete(nodeAgent.imagePullBackoffs, instanceID)
	nodeAgent.clearPullStatusFromStore(ctx, instanceID)
	if nodeAgent.secretAPIServer != nil {
		nodeAgent.secretAPIServer.RevokeInstanceToken(instanceID)
	}
	if nodeAgent.storageProvider != nil {
		nodeAgent.detachVolumesForInstance(ctx, instanceID)
	}
	serviceName := nodeAgent.lookupServiceNameForInstance(ctx, instanceID)
	if serviceName != "" {
		nodeAgent.executeLifecycleHook(ctx, instanceID, serviceName, "pre_stop")
	}
	if stopError := nodeAgent.stopInstanceWithGracePeriod(ctx, instanceID, serviceName); stopError != nil {
		logging.Default().Error("failed to stop instance", "instance", instanceID, "error", stopError.Error())
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceStopped))); putError != nil {
		logging.Default().Error("failed to publish stopped state", "instance", instanceID, "error", putError.Error())
	}
	nodeAgent.probeScheduler.CleanupInstance(instanceID)
	if nodeAgent.networkProvider != nil {
		if releaseError := nodeAgent.networkProvider.ReleaseIP(ctx, nodeAgent.nodeID, instanceID); releaseError != nil {
			logging.Default().Error("failed to release IP for instance", "instance", instanceID, "error", releaseError.Error())
		}
		if deleteError := types.DeleteNetworkAllocation(ctx, nodeAgent.store, instanceID); deleteError != nil {
			logging.Default().Error("failed to delete network allocation", "instance", instanceID, "error", deleteError.Error())
		}
	}
}

// lookupServiceImageFromStore retrieves the container image reference for the
// given service from the desired-state section of the store. Returns an empty
// string if the image fact is missing.
func (nodeAgent *Agent) lookupServiceImageFromStore(ctx context.Context, service string) string {
	factEntry, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceImage(service))
	if err != nil {
		return ""
	}
	return string(factEntry.Value)
}

// lookupServicePullPolicyFromStore retrieves the image pull policy for the given
// service. Returns empty string (meaning "if_not_present") when no policy is set.
func (nodeAgent *Agent) lookupServicePullPolicyFromStore(ctx context.Context, service string) string {
	factEntry, err := nodeAgent.store.Get(ctx, types.KeyDesiredServicePullPolicy(service))
	if err != nil {
		return ""
	}
	return string(factEntry.Value)
}

// lookupImagePullSecretFromStore retrieves the image pull secret name for the
// given service. Returns empty string when no secret is configured.
func (nodeAgent *Agent) lookupImagePullSecretFromStore(ctx context.Context, service string) string {
	factEntry, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceImagePullSecret(service))
	if err != nil {
		return ""
	}
	return string(factEntry.Value)
}

// resolveImagePullCredentials looks up the image pull secret name for a service,
// fetches the secret value via the secret provider, and returns "user:pass" for
// nerdctl --creds. Returns empty string if no secret is configured or resolution fails.
func (nodeAgent *Agent) resolveImagePullCredentials(ctx context.Context, service string) string {
	secretName := nodeAgent.lookupImagePullSecretFromStore(ctx, service)
	if secretName == "" {
		return ""
	}
	if nodeAgent.secretProvider == nil {
		logging.Default().Warn("image_pull_secret requires secret provider", "agent", nodeAgent.nodeID, "service", service, "secret", secretName)
		return ""
	}
	plaintext, _, resolveError := nodeAgent.secretProvider.GetSecretForService(ctx, service, secretName)
	if resolveError != nil {
		logging.Default().Error("failed to resolve image pull secret", "agent", nodeAgent.nodeID, "service", service, "secret", secretName, "error", resolveError.Error())
		return ""
	}
	var registryCredentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if parseError := json.Unmarshal(plaintext, &registryCredentials); parseError != nil {
		logging.Default().Error("invalid image pull secret JSON", "agent", nodeAgent.nodeID, "service", service, "secret", secretName, "error", parseError.Error())
		return ""
	}
	if registryCredentials.Username == "" || registryCredentials.Password == "" {
		logging.Default().Error("image pull secret missing username or password", "agent", nodeAgent.nodeID, "service", service, "secret", secretName)
		return ""
	}
	return registryCredentials.Username + ":" + registryCredentials.Password
}

// lookupServiceCommandFromStore retrieves the container command override for the
// given service from the desired-state section of the store. Returns nil if no
// command is set (the image default CMD will be used). When the command string
// contains shell metacharacters (pipes, redirects, variables, compound
// statements), it is wrapped in "sh -c" for evaluation. Simple commands are
// split into tokens and passed directly so they work with container images that
// have custom entrypoints (e.g. Airflow checks $1 before delegating).
func (nodeAgent *Agent) lookupServiceCommandFromStore(ctx context.Context, service string) []string {
	factEntry, lookupError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceCommand(service))
	if lookupError != nil || string(factEntry.Value) == "" {
		return nil
	}
	commandString := string(factEntry.Value)
	if commandRequiresShell(commandString) {
		return []string{"sh", "-c", commandString}
	}
	return splitSimpleCommand(commandString)
}

// commandRequiresShell returns true when the command string contains shell
// metacharacters that need interpretation by a shell (pipes, redirects,
// variable expansion, compound statements, subshells, globbing).
func commandRequiresShell(commandString string) bool {
	return strings.ContainsAny(commandString, "|&;<>$`(){}*?!~")
}

// splitSimpleCommand splits a shell-style command string into tokens, respecting
// single and double quotes. This handles commands like: airflow webserver,
// bash -c 'long command', and redis-server --save "60 1000".
func splitSimpleCommand(commandString string) []string {
	var tokens []string
	var currentToken strings.Builder
	inSingleQuote := false
	inDoubleQuote := false

	for index := 0; index < len(commandString); index++ {
		currentChar := commandString[index]
		switch {
		case currentChar == '\'' && !inDoubleQuote:
			inSingleQuote = !inSingleQuote
		case currentChar == '"' && !inSingleQuote:
			inDoubleQuote = !inDoubleQuote
		case currentChar == ' ' && !inSingleQuote && !inDoubleQuote:
			if currentToken.Len() > 0 {
				tokens = append(tokens, currentToken.String())
				currentToken.Reset()
			}
		default:
			currentToken.WriteByte(currentChar)
		}
	}
	if currentToken.Len() > 0 {
		tokens = append(tokens, currentToken.String())
	}
	return tokens
}

// lookupServiceExposedPortsFromStore reads all exposed port declarations for
// the given service and returns them as a slice of port numbers.
func (nodeAgent *Agent) lookupServiceExposedPortsFromStore(ctx context.Context, service string) []int {
	exposeFacts, err := nodeAgent.store.Scan(ctx, fmt.Sprintf("%s/service/%s/expose/", types.PrefixDesired, service))
	if err != nil || len(exposeFacts) == 0 {
		return nil
	}

	ports := make([]int, 0, len(exposeFacts))
	for _, fact := range exposeFacts {
		portStr := fact.Key[strings.LastIndex(fact.Key, "/")+1:]
		if parsedPort, err := strconv.Atoi(portStr); err == nil {
			ports = append(ports, parsedPort)
		}
	}
	return ports
}

// resolveServiceConfigEnvVars reads the desired config env vars for a service
// from the store and returns them as a map for the runtime spec.
func (nodeAgent *Agent) resolveServiceConfigEnvVars(ctx context.Context, serviceName string) map[string]string {
	prefix := types.ScanDesiredServiceConfig(serviceName)
	facts, err := nodeAgent.store.Scan(ctx, prefix)
	if err != nil || len(facts) == 0 {
		return nil
	}

	envVars := make(map[string]string)
	envPrefix := fmt.Sprintf("%s/service/%s/config/env/", types.PrefixDesired, serviceName)
	for _, fact := range facts {
		if strings.HasPrefix(fact.Key, envPrefix) {
			envVarName := strings.TrimPrefix(fact.Key, envPrefix)
			envVars[envVarName] = string(fact.Value)
		}
	}
	return envVars
}

// resolveServiceConfigFiles reads the desired config file mount definitions for
// a service from the store and returns them as a map from container-absolute
// file path to file content string.
func (nodeAgent *Agent) resolveServiceConfigFiles(ctx context.Context, serviceName string) map[string]string {
	prefix := types.ScanDesiredServiceConfig(serviceName)
	facts, err := nodeAgent.store.Scan(ctx, prefix)
	if err != nil || len(facts) == 0 {
		return nil
	}

	configFileMounts := make(map[string]string)
	filePrefix := fmt.Sprintf("%s/service/%s/config/file/", types.PrefixDesired, serviceName)
	for _, fact := range facts {
		if strings.HasPrefix(fact.Key, filePrefix) {
			containerFilePath := strings.TrimPrefix(fact.Key, filePrefix)
			configFileMounts[containerFilePath] = string(fact.Value)
		}
	}
	return configFileMounts
}

// detectConfigChange checks whether the config hash for a running instance has
// changed since it was started. If so, stops the container so the next cycle
// restarts it with the new config. Returns true if the instance was stopped.
func (nodeAgent *Agent) detectConfigChange(ctx context.Context, instanceInfo placedInstanceInfo) bool {
	storedHashFact, getError := nodeAgent.store.Get(ctx, types.KeyObservedInstanceConfigHash(instanceInfo.id))
	if getError != nil {
		return false
	}
	storedHash := string(storedHashFact.Value)
	currentHash := nodeAgent.computeServiceConfigHash(ctx, instanceInfo.service)
	if currentHash == "" || currentHash == storedHash {
		return false
	}
	logging.Default().Info("config changed, restarting instance",
		"agent", nodeAgent.nodeID, "instance", instanceInfo.id,
		"service", instanceInfo.service)
	nodeAgent.executeLifecycleHook(ctx, instanceInfo.id, instanceInfo.service, "pre_stop")
	if stopError := nodeAgent.stopInstanceWithGracePeriod(ctx, instanceInfo.id, instanceInfo.service); stopError != nil {
		logging.Default().Error("failed to stop instance for config reload",
			"instance", instanceInfo.id, "error", stopError.Error())
	}
	return true
}

// executeLifecycleHook runs a pre_stop or post_start hook command inside a
// running instance. Hooks are best-effort: failures are logged but do not
// block the stop/start operation.
func (nodeAgent *Agent) executeLifecycleHook(ctx context.Context, instanceID string, serviceName string, hookType string) {
	var hookKey string
	switch hookType {
	case "pre_stop":
		hookKey = types.KeyDesiredServiceLifecyclePreStop(serviceName)
	case "post_start":
		hookKey = types.KeyDesiredServiceLifecyclePostStart(serviceName)
	default:
		return
	}

	hookFact, getError := nodeAgent.store.Get(ctx, hookKey)
	if getError != nil || len(hookFact.Value) == 0 {
		return
	}
	hookCommand := string(hookFact.Value)

	hookContext, cancelHook := context.WithTimeout(ctx, lifecycleHookTimeout)
	defer cancelHook()

	logging.Default().Info("executing lifecycle hook",
		"agent", nodeAgent.nodeID, "instance", instanceID,
		"hook", hookType, "command", hookCommand)

	if execError := nodeAgent.runtime.Exec(hookContext, instanceID, runtime.ExecSpec{Command: hookCommand}); execError != nil {
		logging.Default().Warn("lifecycle hook failed",
			"agent", nodeAgent.nodeID, "instance", instanceID,
			"hook", hookType, "error", execError.Error())
	}
}

// stopInstanceWithGracePeriod stops an instance, using the per-service
// termination_grace_period if configured, otherwise the default runtime Stop.
func (nodeAgent *Agent) stopInstanceWithGracePeriod(ctx context.Context, instanceID string, serviceName string) error {
	if serviceName != "" {
		if gracefulStopper, supportsGrace := nodeAgent.runtime.(runtime.GracefulStopper); supportsGrace {
			graceFact, graceError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceTerminationGracePeriod(serviceName))
			if graceError == nil && len(graceFact.Value) > 0 {
				graceDuration, parseError := time.ParseDuration(string(graceFact.Value))
				if parseError == nil {
					return gracefulStopper.StopWithGracePeriod(ctx, instanceID, graceDuration)
				}
			}
		}
	}
	return nodeAgent.runtime.Stop(ctx, instanceID)
}

// lookupServiceNameForInstance reads the observed service name for an instance
// from the store. Returns empty string if not found.
func (nodeAgent *Agent) lookupServiceNameForInstance(ctx context.Context, instanceID string) string {
	serviceFact, getError := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
	if getError != nil {
		return ""
	}
	return string(serviceFact.Value)
}

// computeServiceConfigHash computes a deterministic SHA256 hash of all config
// inputs (env vars, config files, secrets) for a service. Returns empty string
// if the service has no config.
func (nodeAgent *Agent) computeServiceConfigHash(ctx context.Context, serviceName string) string {
	hasher := sha256.New()
	hasAnyInput := false

	envVars := nodeAgent.resolveServiceConfigEnvVars(ctx, serviceName)
	if len(envVars) > 0 {
		hasAnyInput = true
		sortedEnvKeys := make([]string, 0, len(envVars))
		for envKey := range envVars {
			sortedEnvKeys = append(sortedEnvKeys, envKey)
		}
		sort.Strings(sortedEnvKeys)
		for _, envKey := range sortedEnvKeys {
			hasher.Write([]byte("env:" + envKey + "=" + envVars[envKey] + "\n"))
		}
	}

	configFiles := nodeAgent.resolveServiceConfigFiles(ctx, serviceName)
	if len(configFiles) > 0 {
		hasAnyInput = true
		sortedFilePaths := make([]string, 0, len(configFiles))
		for filePath := range configFiles {
			sortedFilePaths = append(sortedFilePaths, filePath)
		}
		sort.Strings(sortedFilePaths)
		for _, filePath := range sortedFilePaths {
			hasher.Write([]byte("file:" + filePath + "=" + configFiles[filePath] + "\n"))
		}
	}

	secretFacts, scanError := nodeAgent.store.Scan(ctx, types.ScanDesiredServiceSecrets(serviceName))
	if scanError == nil && len(secretFacts) > 0 {
		hasAnyInput = true
		for _, secretFact := range secretFacts {
			hasher.Write([]byte("secret:" + secretFact.Key + "=" + string(secretFact.Value) + "\n"))
		}
	}

	if !hasAnyInput {
		return ""
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// resolveSecretAPIBaseURL computes the HTTP base URL for the secret API server,
// using the advertise address when configured so containers on the network can
// reach the agent's API.
func (nodeAgent *Agent) resolveSecretAPIBaseURL() string {
	secretAPIAddress := nodeAgent.secretAPIServer.httpServer.Addr
	if nodeAgent.advertiseAddress != "" {
		_, port, splitError := net.SplitHostPort(secretAPIAddress)
		if splitError == nil {
			secretAPIAddress = net.JoinHostPort(nodeAgent.advertiseAddress, port)
		}
	}
	return "http://" + secretAPIAddress
}

// injectSecretAPIIntoSpec adds CCA_SECRET_URL, CCA_SECRET_TOKEN, and the
// cca-secret helper script to a runtime spec. The token must already be
// registered via SecretAPIServer.RegisterInstanceToken before calling this.
func (nodeAgent *Agent) injectSecretAPIIntoSpec(secretAPIToken string, secretAPIBaseURL string, runtimeSpec *runtime.Spec) {
	if runtimeSpec.Env == nil {
		runtimeSpec.Env = make(map[string]string)
	}
	runtimeSpec.Env["CCA_SECRET_URL"] = secretAPIBaseURL
	runtimeSpec.Env["CCA_SECRET_TOKEN"] = secretAPIToken

	if runtimeSpec.ConfigFiles == nil {
		runtimeSpec.ConfigFiles = make(map[string]string)
	}
	runtimeSpec.ConfigFiles[ccaSecretHelperPath] = ccaSecretHelperScript
}

// materializeSecretFiles decrypts all granted secrets for a service and adds
// them to the runtime spec as bind-mounted files. Each secret is materialized
// at /run/cca-secrets/{name} (for the cca-secret helper) and at the explicit
// mount path from the secret directive (for apps like POSTGRES_PASSWORD_FILE).
func (nodeAgent *Agent) materializeSecretFiles(ctx context.Context, serviceName string, runtimeSpec *runtime.Spec) {
	if nodeAgent.secretProvider == nil {
		return
	}
	grantPrefix := types.ScanDesiredServiceSecrets(serviceName)
	grantFacts, scanError := nodeAgent.store.Scan(ctx, grantPrefix)
	if scanError != nil || len(grantFacts) == 0 {
		return
	}
	if runtimeSpec.ConfigFiles == nil {
		runtimeSpec.ConfigFiles = make(map[string]string)
	}
	for _, grantFact := range grantFacts {
		secretName := strings.TrimPrefix(grantFact.Key, grantPrefix)
		if secretName == "" {
			continue
		}
		explicitMountPath := string(grantFact.Value)
		plaintext, _, decryptError := nodeAgent.secretProvider.GetSecretForService(ctx, serviceName, secretName)
		if decryptError != nil {
			logging.Default().Error("failed to decrypt secret for materialization",
				"agent", nodeAgent.nodeID, "service", serviceName,
				"secret", secretName, "error", decryptError.Error())
			continue
		}
		secretValue := string(plaintext)
		runtimeSpec.ConfigFiles[ccaSecretMountPrefix+secretName] = secretValue
		if explicitMountPath != "" && strings.HasPrefix(explicitMountPath, "/") {
			runtimeSpec.ConfigFiles[explicitMountPath] = secretValue
		}
	}
}

// materializeConnectionFiles reads all connection references for a service,
// looks up each connection definition, retrieves the password from the secret
// store, assembles the connection URL, and writes it to the mount path.
func (nodeAgent *Agent) materializeConnectionFiles(ctx context.Context, serviceName string, runtimeSpec *runtime.Spec) {
	connectionRefs, scanError := nodeAgent.store.Scan(ctx, types.ScanDesiredServiceConnections(serviceName))
	if scanError != nil || len(connectionRefs) == 0 {
		return
	}
	if runtimeSpec.ConfigFiles == nil {
		runtimeSpec.ConfigFiles = make(map[string]string)
	}
	for _, connectionRef := range connectionRefs {
		connectionName := strings.TrimPrefix(connectionRef.Key, types.ScanDesiredServiceConnections(serviceName))
		if connectionName == "" {
			continue
		}
		mountPath := string(connectionRef.Value)
		connectionURL := nodeAgent.assembleConnectionURL(ctx, connectionName, serviceName)
		if connectionURL == "" {
			continue
		}
		runtimeSpec.ConfigFiles[mountPath] = connectionURL
	}
}

// assembleConnectionURL reads a connection definition from the store, retrieves
// the password from the secret provider, and builds a standard connection URL:
// driver://user:password@host:port/database
func (nodeAgent *Agent) assembleConnectionURL(ctx context.Context, connectionName string, serviceName string) string {
	connectionFact, getError := nodeAgent.store.Get(ctx, types.KeyDesiredConnection(connectionName))
	if getError != nil {
		logging.Default().Error("connection definition not found",
			"agent", nodeAgent.nodeID, "connection", connectionName)
		return ""
	}
	connectionDef := parseConnectionJSON(string(connectionFact.Value))
	if connectionDef.driver == "" || connectionDef.host == "" {
		logging.Default().Error("connection definition missing driver or host",
			"agent", nodeAgent.nodeID, "connection", connectionName)
		return ""
	}

	password := ""
	if connectionDef.secret != "" && nodeAgent.secretProvider != nil {
		plaintext, _, decryptError := nodeAgent.secretProvider.GetSecretForService(ctx, serviceName, connectionDef.secret)
		if decryptError != nil {
			logging.Default().Error("failed to decrypt connection secret",
				"agent", nodeAgent.nodeID, "connection", connectionName,
				"secret", connectionDef.secret, "error", decryptError.Error())
			return ""
		}
		password = string(plaintext)
	}

	return buildConnectionURL(connectionDef, password)
}

// connectionDefinition holds the parsed fields from a connection fact's JSON value.
type connectionDefinition struct {
	driver   string // driver/protocol prefix (e.g. "postgresql+psycopg2")
	user     string // username for authentication
	secret   string // secret name for the password
	host     string // hostname to connect to
	port     int    // port number (0 = omit from URL)
	database string // database name
}

// parseConnectionJSON extracts connection fields from the JSON value stored in
// the fact store. Uses simple string parsing to avoid a JSON library dependency.
func parseConnectionJSON(jsonValue string) connectionDefinition {
	var connectionDef connectionDefinition
	connectionDef.driver = extractJSONStringField(jsonValue, "driver")
	connectionDef.user = extractJSONStringField(jsonValue, "user")
	connectionDef.secret = extractJSONStringField(jsonValue, "secret")
	connectionDef.host = extractJSONStringField(jsonValue, "host")
	connectionDef.database = extractJSONStringField(jsonValue, "database")
	connectionDef.port = extractJSONIntField(jsonValue, "port")
	return connectionDef
}

// extractJSONStringField extracts a quoted string value for a given key from a
// flat JSON object. Returns empty string if not found.
func extractJSONStringField(jsonValue string, fieldName string) string {
	searchKey := fmt.Sprintf(`"%s":"`, fieldName)
	keyIndex := strings.Index(jsonValue, searchKey)
	if keyIndex < 0 {
		return ""
	}
	valueStart := keyIndex + len(searchKey)
	valueEnd := strings.Index(jsonValue[valueStart:], `"`)
	if valueEnd < 0 {
		return ""
	}
	return jsonValue[valueStart : valueStart+valueEnd]
}

// extractJSONIntField extracts an integer value for a given key from a flat
// JSON object. Returns 0 if not found or not parseable.
func extractJSONIntField(jsonValue string, fieldName string) int {
	searchKey := fmt.Sprintf(`"%s":`, fieldName)
	keyIndex := strings.Index(jsonValue, searchKey)
	if keyIndex < 0 {
		return 0
	}
	valueStart := keyIndex + len(searchKey)
	remaining := jsonValue[valueStart:]
	endIndex := strings.IndexAny(remaining, ",}")
	if endIndex < 0 {
		return 0
	}
	parsedValue, parseError := strconv.Atoi(strings.TrimSpace(remaining[:endIndex]))
	if parseError != nil {
		return 0
	}
	return parsedValue
}

// buildConnectionURL assembles a standard connection URL from the definition
// fields and decrypted password: driver://user:password@host:port/database.
// Credentials are percent-encoded via net/url so special characters (@:/?#)
// produce valid URLs.
func buildConnectionURL(connectionDef connectionDefinition, password string) string {
	connectionURL := &url.URL{
		Scheme: connectionDef.driver,
		Host:   connectionDef.host,
	}
	if connectionDef.port > 0 {
		connectionURL.Host = net.JoinHostPort(connectionDef.host, strconv.Itoa(connectionDef.port))
	}
	if connectionDef.user != "" {
		if password != "" {
			connectionURL.User = url.UserPassword(connectionDef.user, password)
		} else {
			connectionURL.User = url.User(connectionDef.user)
		}
	}
	if connectionDef.database != "" {
		connectionURL.Path = "/" + connectionDef.database
	}
	return connectionURL.String()
}

// lookupServiceVolumeMountsFromStore reads the desired volume mounts for a
// service. Returns a map from volume name to mount path. Returns an empty map
// if the service has no volume mounts.
func (nodeAgent *Agent) lookupServiceVolumeMountsFromStore(ctx context.Context, serviceName string) map[string]string {
	prefix := fmt.Sprintf("%s/service/%s/volume/", types.PrefixDesired, serviceName)
	facts, err := nodeAgent.store.Scan(ctx, prefix)
	if err != nil || len(facts) == 0 {
		return nil
	}

	volumeMounts := make(map[string]string)
	for _, fact := range facts {
		volumeName := strings.TrimPrefix(fact.Key, prefix)
		mountPath := string(fact.Value)
		volumeMounts[volumeName] = mountPath
	}
	return volumeMounts
}

// ensureVolumesAttachedForInstance checks whether all volumes required by the
// instance's service are available, and attaches them to this node if so.
// Returns (true, nil) when all volumes are ready. Returns (false, nil) when a
// volume exists but is attached to a different node (the instance should wait).
// Returns (false, error) on unexpected failures.
func (nodeAgent *Agent) ensureVolumesAttachedForInstance(ctx context.Context, instanceInfo placedInstanceInfo) (bool, error) {
	volumeMounts := nodeAgent.lookupServiceVolumeMountsFromStore(ctx, instanceInfo.service)
	if len(volumeMounts) == 0 {
		return true, nil
	}

	for volumeName := range volumeMounts {
		volumeStateFact, err := nodeAgent.store.Get(ctx, types.KeyObservedVolumeState(volumeName))
		if err != nil {
			return false, nil
		}

		volumeState := types.VolumeState(volumeStateFact.Value)

		if volumeState == types.VolumeAttached {
			nodeFact, nodeErr := nodeAgent.store.Get(ctx, types.KeyObservedVolumeNode(volumeName))
			if nodeErr == nil && string(nodeFact.Value) == nodeAgent.nodeID {
				continue
			}
			return false, nil
		}

		if volumeState != types.VolumeAvailable && volumeState != types.VolumeMigrating {
			return false, nil
		}

		mountPath, err := nodeAgent.storageProvider.AttachVolume(ctx, volumeName, nodeAgent.nodeID)
		if err != nil {
			return false, fmt.Errorf("attaching volume %s: %w", volumeName, err)
		}

		sizeFact, _ := nodeAgent.store.Get(ctx, types.KeyObservedVolumeSize(volumeName))
		sizeValue := ""
		if sizeFact.Value != nil {
			sizeValue = string(sizeFact.Value)
		}

		var usedBytes, capacityBytes int64
		usedBytes, capacityBytes, _ = nodeAgent.storageProvider.VolumeUsage(ctx, volumeName)

		if writeError := types.WriteObservedVolume(ctx, nodeAgent.store, types.Volume{
			Name:          volumeName,
			Size:          sizeValue,
			State:         types.VolumeAttached,
			Node:          nodeAgent.nodeID,
			Instance:      instanceInfo.id,
			MountPath:     mountPath,
			UsedBytes:     usedBytes,
			CapacityBytes: capacityBytes,
		}); writeError != nil {
			logging.Default().Error("failed to write observed volume", "volume", volumeName, "error", writeError.Error())
		}

		if deleteError := nodeAgent.store.Delete(ctx, types.KeyObservedVolumeMigrationSource(volumeName)); deleteError != nil {
			logging.Default().Error("failed to delete volume migration source", "volume", volumeName, "error", deleteError.Error())
		}
	}

	return true, nil
}

// detachVolumesForInstance finds volumes attached to the given instance and
// detaches them, setting their state back to available.
func (nodeAgent *Agent) detachVolumesForInstance(ctx context.Context, instanceID string) {
	serviceFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
	if err != nil {
		logging.Default().Warn("failed to look up service for volume detach", "instance", instanceID, "error", err.Error())
		return
	}
	serviceName := string(serviceFact.Value)

	volumeMounts := nodeAgent.lookupServiceVolumeMountsFromStore(ctx, serviceName)
	for volumeName := range volumeMounts {
		if detachError := nodeAgent.storageProvider.DetachVolume(ctx, volumeName, nodeAgent.nodeID); detachError != nil {
			logging.Default().Error("failed to detach volume", "volume", volumeName, "node", nodeAgent.nodeID, "error", detachError.Error())
		}

		sizeFact, _ := nodeAgent.store.Get(ctx, types.KeyObservedVolumeSize(volumeName))
		sizeValue := ""
		if sizeFact.Value != nil {
			sizeValue = string(sizeFact.Value)
		}

		if writeError := types.WriteObservedVolume(ctx, nodeAgent.store, types.Volume{
			Name:  volumeName,
			Size:  sizeValue,
			State: types.VolumeAvailable,
		}); writeError != nil {
			logging.Default().Error("failed to write observed volume after detach", "volume", volumeName, "error", writeError.Error())
		}
	}
}

// isInstanceMarkedForSkip checks derived/ markers (node_failure,
// controller_stopped) that signal the agent to skip this instance.
func (nodeAgent *Agent) isInstanceMarkedForSkip(ctx context.Context, instanceID string) bool {
	if _, err := nodeAgent.store.Get(ctx, types.KeyDerivedInstanceNodeFailure(instanceID)); err == nil {
		return true
	}
	if _, err := nodeAgent.store.Get(ctx, types.KeyDerivedInstanceControllerStopped(instanceID)); err == nil {
		return true
	}
	return false
}

// observeInstanceState queries the runtime for the current state of an instance
// and returns the corresponding InstanceState and exit reason (if failed).
func (nodeAgent *Agent) observeInstanceState(ctx context.Context, instanceID string) (types.InstanceState, string) {
	observedStatus, statusError := nodeAgent.runtime.Status(ctx, instanceID)
	if statusError != nil {
		return types.InstanceStarting, ""
	}
	if observedStatus.Running {
		return types.InstanceRunning, ""
	}
	if observedStatus.ExitCode != 0 || observedStatus.Error != "" {
		exitReason := observedStatus.Error
		if exitReason == "" {
			exitReason = fmt.Sprintf("exit(%d)", observedStatus.ExitCode)
		}
		return types.InstanceFailed, exitReason
	}
	return types.InstanceStarting, ""
}

// publishInstanceStateToStore writes a complete set of observed-state facts for
// the given instance: its existence marker, owning service, current state, the
// node it is running on, and its IP address. When a NetworkProvider is configured,
// an IP is allocated from the node's subnet. Without a provider, the container's
// bridge IP from the runtime is published if available.
func (nodeAgent *Agent) publishInstanceStateToStore(ctx context.Context, instanceID, service string, state types.InstanceState, runtimeIP string) {
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstance(instanceID), []byte("")); putError != nil {
		logging.Default().Error("failed to publish instance state", "instance", instanceID, "error", putError.Error())
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte(service)); putError != nil {
		logging.Default().Error("failed to publish instance state", "instance", instanceID, "error", putError.Error())
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(state))); putError != nil {
		logging.Default().Error("failed to publish instance state", "instance", instanceID, "error", putError.Error())
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceNode(instanceID), []byte(nodeAgent.nodeID)); putError != nil {
		logging.Default().Error("failed to publish instance state", "instance", instanceID, "error", putError.Error())
	}

	publishedContainerIP := ""
	if nodeAgent.networkProvider != nil {
		allocatedIP, allocateError := nodeAgent.networkProvider.AllocateIP(ctx, nodeAgent.nodeID, instanceID)
		if allocateError != nil {
			logging.Default().Error("failed to allocate IP", "agent", nodeAgent.nodeID, "instance", instanceID, "error", allocateError.Error())
		} else {
			publishedContainerIP = allocatedIP
			if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceIP(instanceID), []byte(allocatedIP)); putError != nil {
				logging.Default().Error("failed to publish instance state", "instance", instanceID, "error", putError.Error())
			}
			if _, writeError := types.WriteNetworkAllocation(ctx, nodeAgent.store, instanceID, allocatedIP); writeError != nil {
				logging.Default().Error("failed to write network allocation", "instance", instanceID, "error", writeError.Error())
			}
		}
	} else if runtimeIP != "" {
		if net.ParseIP(runtimeIP) == nil {
			logging.Default().Warn("invalid container IP from runtime", "instance", instanceID, "ip", runtimeIP)
		} else {
			publishedContainerIP = runtimeIP
			if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceIP(instanceID), []byte(runtimeIP)); putError != nil {
				logging.Default().Error("failed to publish container IP", "instance", instanceID, "error", putError.Error())
			}
		}
	}

	nodeAgent.registerEndpointIdentityIfSupported(ctx, publishedContainerIP, service)
}

// reconcileResourceChanges compares the effective resource allocation for an
// instance's service against the resources that were last applied to the
// instance. If they differ (e.g. vertical autoscaler changed effective CPU or
// memory), the agent attempts a live resize via the runtime. If the runtime
// does not support live resize, the instance is stopped so the next
// reconciliation cycle restarts it with the updated resources.
func (nodeAgent *Agent) reconcileResourceChanges(ctx context.Context, instanceInfo placedInstanceInfo) {
	effectiveCPU, effectiveMemory := nodeAgent.nodeReporter.lookupServiceResourcesFromStore(ctx, instanceInfo.service)
	if effectiveCPU == 0 && effectiveMemory == 0 {
		return
	}

	applied, tracked := nodeAgent.appliedInstanceResources[instanceInfo.id]
	if !tracked {
		nodeAgent.appliedInstanceResources[instanceInfo.id] = appliedResourceAllocation{
			cpuMillicores: effectiveCPU,
			memoryBytes:   effectiveMemory,
		}
		return
	}

	if applied.cpuMillicores == effectiveCPU && applied.memoryBytes == effectiveMemory {
		return
	}

	logging.Default().Info("resource change detected",
		"agent", nodeAgent.nodeID,
		"instance", instanceInfo.id,
		"service", instanceInfo.service,
		"old_cpu_m", strconv.FormatInt(applied.cpuMillicores, 10),
		"new_cpu_m", strconv.FormatInt(effectiveCPU, 10),
		"old_memory_b", strconv.FormatInt(applied.memoryBytes, 10),
		"new_memory_b", strconv.FormatInt(effectiveMemory, 10),
	)

	resizeError := nodeAgent.runtime.Resize(ctx, instanceInfo.id, effectiveCPU, effectiveMemory)
	if resizeError == nil {
		nodeAgent.appliedInstanceResources[instanceInfo.id] = appliedResourceAllocation{
			cpuMillicores: effectiveCPU,
			memoryBytes:   effectiveMemory,
		}
		return
	}

	if errors.Is(resizeError, runtime.ErrResizeUnsupported) {
		logging.Default().Info("live resize unsupported, stopping instance for restart",
			"agent", nodeAgent.nodeID,
			"instance", instanceInfo.id,
		)
		nodeAgent.executeLifecycleHook(ctx, instanceInfo.id, instanceInfo.service, "pre_stop")
		if stopError := nodeAgent.stopInstanceWithGracePeriod(ctx, instanceInfo.id, instanceInfo.service); stopError != nil {
			logging.Default().Error("failed to stop instance for resize",
				"agent", nodeAgent.nodeID,
				"instance", instanceInfo.id,
				"error", stopError.Error(),
			)
		}
		delete(nodeAgent.appliedInstanceResources, instanceInfo.id)
		return
	}

	logging.Default().Error("resize failed",
		"agent", nodeAgent.nodeID,
		"instance", instanceInfo.id,
		"error", resizeError.Error(),
	)
}
