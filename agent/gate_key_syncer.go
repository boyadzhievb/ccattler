// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Gate-key sync timing (Flow 28 step 6).
const (
	gateKeyFallbackFetchInterval = 5 * time.Minute  // gateKeyFallbackFetchInterval refetches even without a fact change.
	gateKeyRetryInitialDelay     = 2 * time.Second  // gateKeyRetryInitialDelay is the first retry after a failed fetch.
	gateKeyRetryMaxDelay         = 30 * time.Second // gateKeyRetryMaxDelay caps the retry backoff.
	gateKeyChangeDebounce        = time.Second      // gateKeyChangeDebounce groups bursts of gate TLS fact changes into one fetch.
	gateKeyWatchRestartDelay     = 5 * time.Second  // gateKeyWatchRestartDelay is the wait before re-establishing a closed watch.
	gateKeyStatusRefreshInterval = 30 * time.Minute // gateKeyStatusRefreshInterval rewrites an unchanged gate_keys fact, restoring it if the node's facts were deleted.
)

// gateTLSFactMarker selects route fact changes that can change a gate's pair
// (tls/mode, tls/cert, tls/key_secret, tls/not-after) or remove a TLS gate.
const gateTLSFactMarker = "/tls/"

// nodeGateKeyDeliveryPath mirrors api.NodeGateKeyDeliveryPath (the agent does
// not import the api package).
const nodeGateKeyDeliveryPath = "/api/node/gate-keys"

// GateKeysState is the value of observed/node/{id}/gate_keys.state.
type GateKeysState string

const (
	// GateKeysReady means every TLS gate has a delivered pair in the cache.
	GateKeysReady GateKeysState = "ready"
	// GateKeysWaiting means at least one TLS gate has no current pair.
	GateKeysWaiting GateKeysState = "waiting"
)

// GateKeysWaitReason explains a waiting gate_keys state.
type GateKeysWaitReason string

const (
	// GateKeysUnreachable means the API server could not be reached or failed.
	GateKeysUnreachable GateKeysWaitReason = "unreachable"
	// GateKeysRefused means the API refused delivery to this node (403).
	GateKeysRefused GateKeysWaitReason = "refused"
	// GateKeysPending means some TLS gates have no valid pair on the server yet.
	GateKeysPending GateKeysWaitReason = "pending"
	// GateKeysCacheError means a delivered pair could not be written to tmpfs.
	GateKeysCacheError GateKeysWaitReason = "cache_error"
	// GateKeysNotConfigured means the agent has no node certificate or API address.
	GateKeysNotConfigured GateKeysWaitReason = "not_configured"
)

// gateKeysStatus is the JSON value of observed/node/{id}/gate_keys.
type gateKeysStatus struct {
	State   GateKeysState      `json:"state"`             // State is ready or waiting.
	Reason  GateKeysWaitReason `json:"reason,omitempty"`  // Reason explains a waiting state.
	Pending []string           `json:"pending,omitempty"` // Pending lists gates without a pair on the server.
}

// GateKeyFetchError is a failed gate-key fetch with its wait reason.
type GateKeyFetchError struct {
	Reason GateKeysWaitReason // Reason is unreachable or refused.
	Detail string             // Detail is a human-readable explanation.
}

// Error formats the fetch failure.
func (fetchError *GateKeyFetchError) Error() string {
	return fmt.Sprintf("%s: %s", fetchError.Reason, fetchError.Detail)
}

// GateKeyFetcher fetches every gate's TLS pair for this node.
type GateKeyFetcher interface {
	// FetchGateKeys returns the delivery, or a *GateKeyFetchError.
	FetchGateKeys(ctx context.Context) (*GateKeyDelivery, error)
}

// HTTPGateKeyClient calls POST /api/node/gate-keys over mTLS with the node
// certificate; the server derives the node ID from the certificate.
type HTTPGateKeyClient struct {
	httpClient *http.Client // httpClient carries the node mTLS configuration.
	apiBaseURL string       // apiBaseURL is e.g. "https://10.0.0.5:9770".
}

// NewHTTPGateKeyClient creates a gate-key client for the API server at
// apiAddress (host:port or https URL) using the node's mTLS configuration.
func NewHTTPGateKeyClient(apiAddress string, nodeTLSConfig *tls.Config) *HTTPGateKeyClient {
	httpClient, apiBaseURL := newNodeAPIHTTPClient(apiAddress, nodeTLSConfig)
	return &HTTPGateKeyClient{httpClient: httpClient, apiBaseURL: apiBaseURL}
}

// FetchGateKeys requests the delivery. 403 maps to refused; network failures,
// rate limiting and server errors map to unreachable (retried with backoff).
func (gateKeyClient *HTTPGateKeyClient) FetchGateKeys(ctx context.Context) (*GateKeyDelivery, error) {
	request, requestError := http.NewRequestWithContext(ctx, http.MethodPost, gateKeyClient.apiBaseURL+nodeGateKeyDeliveryPath, http.NoBody)
	if requestError != nil {
		return nil, &GateKeyFetchError{Reason: GateKeysUnreachable, Detail: requestError.Error()}
	}
	response, sendError := gateKeyClient.httpClient.Do(request)
	if sendError != nil {
		return nil, &GateKeyFetchError{Reason: GateKeysUnreachable, Detail: sendError.Error()}
	}
	defer func() { _ = response.Body.Close() }()
	responseBytes, readError := io.ReadAll(io.LimitReader(response.Body, secretDeliveryMaxResponseBytes))
	if readError != nil {
		return nil, &GateKeyFetchError{Reason: GateKeysUnreachable, Detail: readError.Error()}
	}
	switch response.StatusCode {
	case http.StatusOK:
		var delivery GateKeyDelivery
		if decodeError := json.Unmarshal(responseBytes, &delivery); decodeError != nil {
			return nil, &GateKeyFetchError{Reason: GateKeysUnreachable, Detail: "invalid gate-key response"}
		}
		return &delivery, nil
	case http.StatusForbidden:
		return nil, &GateKeyFetchError{Reason: GateKeysRefused, Detail: strings.TrimSpace(string(responseBytes))}
	default:
		return nil, &GateKeyFetchError{Reason: GateKeysUnreachable, Detail: fmt.Sprintf("gate-key delivery returned HTTP %d", response.StatusCode)}
	}
}

// PublishGateProxyCapability writes the node's gate-key facts at agent start:
// gate_key_delivery = v1 (this agent never reads key facts) and gate_proxy =
// enabled | disabled. The gate-key migration waits for v1 on every node.
func PublishGateProxyCapability(ctx context.Context, factStore store.StateStore, nodeID string, gateProxyEnabled bool) error {
	gateProxyState := types.GateProxyDisabled
	if gateProxyEnabled {
		gateProxyState = types.GateProxyEnabled
	}
	if _, putError := factStore.Put(ctx, types.KeyObservedNodeGateProxy(nodeID), []byte(gateProxyState)); putError != nil {
		return fmt.Errorf("publish gate_proxy: %w", putError)
	}
	if _, putError := factStore.Put(ctx, types.KeyObservedNodeGateKeyDelivery(nodeID), []byte(types.GateKeyDeliveryVersion)); putError != nil {
		return fmt.Errorf("publish gate_key_delivery: %w", putError)
	}
	return nil
}

// PublishGateProxyCapabilityUntilDone retries PublishGateProxyCapability with
// the gate-key backoff until it succeeds or ctx ends: without these facts the
// server refuses delivery and the migration waits on this node forever.
func PublishGateProxyCapabilityUntilDone(ctx context.Context, factStore store.StateStore, nodeID string, gateProxyEnabled bool) {
	retryDelay := time.Duration(0)
	for {
		publishError := PublishGateProxyCapability(ctx, factStore, nodeID, gateProxyEnabled)
		if publishError == nil {
			return
		}
		retryDelay = nextGateKeyRetryDelay(retryDelay)
		logging.Default().Warn("gate-key capability not published; retrying", "node", nodeID, "error", publishError.Error(), "retry_in", retryDelay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
		}
	}
}

// GateKeySyncer keeps the gate-key cache in line with the control plane: it
// fetches at start, after gate TLS fact changes, every 5 minutes, and retries
// failures with backoff while the cache keeps serving (Flow 28 step 6).
type GateKeySyncer struct {
	nodeID              string           // nodeID is this agent's node.
	factStore           store.StateStore // factStore is watched for gate TLS changes and receives gate_keys.
	gateKeyFetcher      GateKeyFetcher   // gateKeyFetcher calls the API; nil means delivery is not configured.
	gateKeyCache        *GateKeyCache    // gateKeyCache holds the delivered pairs.
	lastPublishedStatus string           // lastPublishedStatus is the last gate_keys value written, to skip unchanged writes.
	lastPublishedAt     time.Time        // lastPublishedAt is when gate_keys was last written.
}

// NewGateKeySyncer creates a syncer for a cache the caller has already loaded
// (GateKeyCache.Load, before the gate proxy starts). A nil fetcher only
// reports gate_keys = waiting (not_configured).
func NewGateKeySyncer(nodeID string, factStore store.StateStore, gateKeyFetcher GateKeyFetcher, gateKeyCache *GateKeyCache) *GateKeySyncer {
	return &GateKeySyncer{nodeID: nodeID, factStore: factStore, gateKeyFetcher: gateKeyFetcher, gateKeyCache: gateKeyCache}
}

// Run fetches until ctx is cancelled: retries (2s…30s) while a fetch fails or
// gates are pending, otherwise refetches on change or every 5 minutes.
func (gateKeySyncer *GateKeySyncer) Run(ctx context.Context) {
	if gateKeySyncer.gateKeyFetcher == nil {
		gateKeySyncer.publishStatus(ctx, gateKeysStatus{State: GateKeysWaiting, Reason: GateKeysNotConfigured})
		return
	}
	changeSignal := make(chan struct{}, 1)
	go gateKeySyncer.watchGateTLSFacts(ctx, changeSignal)

	fetchTimer := time.NewTimer(0)
	defer fetchTimer.Stop()
	retryDelay := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-changeSignal:
			fetchTimer.Reset(gateKeyChangeDebounce)
		case <-fetchTimer.C:
			if gateKeySyncer.syncOnce(ctx) {
				retryDelay = 0
				fetchTimer.Reset(gateKeyFallbackFetchInterval)
				continue
			}
			retryDelay = nextGateKeyRetryDelay(retryDelay)
			fetchTimer.Reset(retryDelay)
		}
	}
}

// nextGateKeyRetryDelay doubles the retry delay from 2s up to 30s.
func nextGateKeyRetryDelay(previousDelay time.Duration) time.Duration {
	if previousDelay == 0 {
		return gateKeyRetryInitialDelay
	}
	return min(previousDelay*2, gateKeyRetryMaxDelay)
}

// syncOnce fetches the delivery, applies it to the cache and publishes
// gate_keys. Returns false (retry with backoff) when the fetch or a cache write
// failed or a gate is pending, e.g. its key arrives after its certificate.
func (gateKeySyncer *GateKeySyncer) syncOnce(ctx context.Context) bool {
	delivery, fetchError := gateKeySyncer.gateKeyFetcher.FetchGateKeys(ctx)
	if fetchError != nil {
		waitReason := GateKeysUnreachable
		if typedError, isTyped := fetchError.(*GateKeyFetchError); isTyped {
			waitReason = typedError.Reason
		}
		logging.Default().Warn("gate-key fetch failed; serving cached pairs", "node", gateKeySyncer.nodeID, "error", fetchError.Error())
		gateKeySyncer.publishStatus(ctx, gateKeysStatus{State: GateKeysWaiting, Reason: waitReason})
		return false
	}
	if applyError := gateKeySyncer.gateKeyCache.Apply(delivery); applyError != nil {
		logging.Default().Error("gate-key cache write failed", "node", gateKeySyncer.nodeID, "error", applyError.Error())
		gateKeySyncer.publishStatus(ctx, gateKeysStatus{State: GateKeysWaiting, Reason: GateKeysCacheError})
		return false
	}
	if len(delivery.Pending) > 0 {
		gateKeySyncer.publishStatus(ctx, gateKeysStatus{State: GateKeysWaiting, Reason: GateKeysPending, Pending: delivery.Pending})
		return false
	}
	gateKeySyncer.publishStatus(ctx, gateKeysStatus{State: GateKeysReady})
	return true
}

// publishStatus writes observed/node/{id}/gate_keys when the value changed or
// gateKeyStatusRefreshInterval passed since the last write.
func (gateKeySyncer *GateKeySyncer) publishStatus(ctx context.Context, status gateKeysStatus) {
	encodedStatus, _ := json.Marshal(status)
	if string(encodedStatus) == gateKeySyncer.lastPublishedStatus && time.Since(gateKeySyncer.lastPublishedAt) < gateKeyStatusRefreshInterval {
		return
	}
	if _, putError := gateKeySyncer.factStore.Put(ctx, types.KeyObservedNodeGateKeys(gateKeySyncer.nodeID), encodedStatus); putError != nil {
		logging.Default().Warn("publish gate_keys failed", "node", gateKeySyncer.nodeID, "error", putError.Error())
		return
	}
	gateKeySyncer.lastPublishedStatus = string(encodedStatus)
	gateKeySyncer.lastPublishedAt = time.Now()
}

// watchGateTLSFacts signals changeSignal on every desired or derived route
// change under a gate's tls/ attributes. A closed watch is re-established
// after a delay and followed by a signal, since events may have been missed.
func (gateKeySyncer *GateKeySyncer) watchGateTLSFacts(ctx context.Context, changeSignal chan<- struct{}) {
	for ctx.Err() == nil {
		watchContext, cancelWatches := context.WithCancel(ctx)
		desiredEvents, desiredError := gateKeySyncer.factStore.Watch(watchContext, types.ScanDesiredRoutes, store.WatchOption{Prefix: true})
		derivedEvents, derivedError := gateKeySyncer.factStore.Watch(watchContext, types.ScanDerivedRoutes, store.WatchOption{Prefix: true})
		if desiredError == nil && derivedError == nil {
			forwardGateTLSEvents(watchContext, desiredEvents, derivedEvents, changeSignal)
		}
		cancelWatches() // ends the other watch when only one channel closed
		select {
		case <-ctx.Done():
			return
		case <-time.After(gateKeyWatchRestartDelay):
		}
		signalGateKeyChange(changeSignal)
	}
}

// forwardGateTLSEvents signals changeSignal for gate TLS events until either
// watch channel closes or ctx is cancelled.
func forwardGateTLSEvents(ctx context.Context, desiredEvents, derivedEvents <-chan store.Event, changeSignal chan<- struct{}) {
	for {
		var routeEvent store.Event
		var open bool
		select {
		case <-ctx.Done():
			return
		case routeEvent, open = <-desiredEvents:
		case routeEvent, open = <-derivedEvents:
		}
		if !open {
			return
		}
		if strings.Contains(routeEvent.Fact.Key, gateTLSFactMarker) {
			signalGateKeyChange(changeSignal)
		}
	}
}

// signalGateKeyChange sends a non-blocking change signal.
func signalGateKeyChange(changeSignal chan<- struct{}) {
	select {
	case changeSignal <- struct{}{}:
	default:
	}
}
