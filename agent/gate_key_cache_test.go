// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// issueTestGateKeyPair issues a gate pair for host from a fresh test CA.
func issueTestGateKeyPair(testContext *testing.T, gateName string, host string) DeliveredGateKeyPair {
	testContext.Helper()
	certificateAuthority, caError := security.NewCertificateAuthority(time.Hour)
	if caError != nil {
		testContext.Fatal(caError)
	}
	issued, issueError := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{CommonName: host, DNSNames: []string{host}, TTL: time.Hour})
	if issueError != nil {
		testContext.Fatal(issueError)
	}
	return DeliveredGateKeyPair{Gate: gateName, Host: host, CertificatePEM: string(issued.CertificatePEM), PrivateKeyPEM: string(issued.PrivateKeyPEM)}
}

func TestGateKeyCacheAppliesWritesModesAndReloads(testContext *testing.T) {
	cacheDirectory := filepath.Join(testContext.TempDir(), "gate-keys")
	gateKeyCache := NewGateKeyCache(cacheDirectory)
	webPair := issueTestGateKeyPair(testContext, "web", "web.example.com")
	if applyError := gateKeyCache.Apply(&GateKeyDelivery{Gates: []DeliveredGateKeyPair{webPair}}); applyError != nil {
		testContext.Fatalf("Apply: %v", applyError)
	}
	if gateKeyCache.GateCertificate("web") == nil {
		testContext.Fatal("delivered pair not served")
	}
	expectedModes := map[string]os.FileMode{
		cacheDirectory:                       0o700,
		filepath.Join(cacheDirectory, "web"): 0o700,
		filepath.Join(cacheDirectory, "web", gatePrivateKeyFileName):  0o400,
		filepath.Join(cacheDirectory, "web", gateCertificateFileName): 0o444,
	}
	for cachePath, expectedMode := range expectedModes {
		fileInfo, statError := os.Stat(cachePath)
		if statError != nil || fileInfo.Mode().Perm() != expectedMode {
			testContext.Errorf("%s: mode %v, want %v (%v)", cachePath, fileInfo.Mode().Perm(), expectedMode, statError)
		}
	}

	restartedCache := NewGateKeyCache(cacheDirectory)
	if loadedCount := restartedCache.Load(); loadedCount != 1 || restartedCache.GateCertificate("web") == nil {
		testContext.Fatalf("reload after restart: %d gates", loadedCount)
	}
}

func TestGateKeyCacheKeepsPendingGatesAndRemovesGoneGates(testContext *testing.T) {
	cacheDirectory := testContext.TempDir()
	gateKeyCache := NewGateKeyCache(cacheDirectory)
	webPair := issueTestGateKeyPair(testContext, "web", "web.example.com")
	shopPair := issueTestGateKeyPair(testContext, "shop", "shop.example.com")
	if applyError := gateKeyCache.Apply(&GateKeyDelivery{Gates: []DeliveredGateKeyPair{webPair, shopPair}}); applyError != nil {
		testContext.Fatal(applyError)
	}
	// web is renewing (pending on the server); shop was removed.
	if applyError := gateKeyCache.Apply(&GateKeyDelivery{Gates: []DeliveredGateKeyPair{}, Pending: []string{"web"}}); applyError != nil {
		testContext.Fatal(applyError)
	}
	if gateKeyCache.GateCertificate("web") == nil {
		testContext.Error("pending gate lost its cached pair")
	}
	if gateKeyCache.GateCertificate("shop") != nil {
		testContext.Error("removed gate still served")
	}
	if _, statError := os.Stat(filepath.Join(cacheDirectory, "shop")); !errors.Is(statError, os.ErrNotExist) {
		testContext.Errorf("removed gate directory still present: %v", statError)
	}
}

func TestGateKeyCacheRejectsMismatchedPairsAndUnsafeNames(testContext *testing.T) {
	cacheDirectory := testContext.TempDir()
	gateKeyCache := NewGateKeyCache(cacheDirectory)
	webPair := issueTestGateKeyPair(testContext, "web", "web.example.com")
	otherPair := issueTestGateKeyPair(testContext, "web", "web.example.com")
	mismatchedPair := DeliveredGateKeyPair{Gate: "web", CertificatePEM: webPair.CertificatePEM, PrivateKeyPEM: otherPair.PrivateKeyPEM}
	if applyError := gateKeyCache.Apply(&GateKeyDelivery{Gates: []DeliveredGateKeyPair{mismatchedPair}}); applyError == nil {
		testContext.Error("mismatched pair accepted")
	}
	escapingPair := webPair
	escapingPair.Gate = "../etc"
	if applyError := gateKeyCache.Apply(&GateKeyDelivery{Gates: []DeliveredGateKeyPair{escapingPair}}); applyError == nil {
		testContext.Error("path-escaping gate name accepted")
	}
	if len(gateKeyCache.CachedGateNames()) != 0 {
		testContext.Errorf("cached gates = %v", gateKeyCache.CachedGateNames())
	}

	// A cached directory with a broken pair (crash or tampering) is dropped on load.
	brokenDirectory := filepath.Join(cacheDirectory, "broken")
	_ = os.MkdirAll(brokenDirectory, 0o700)
	_ = os.WriteFile(filepath.Join(brokenDirectory, gateCertificateFileName), []byte(webPair.CertificatePEM), gatePrivateKeyFileMode)
	_ = os.WriteFile(filepath.Join(brokenDirectory, gatePrivateKeyFileName), []byte(otherPair.PrivateKeyPEM), gatePrivateKeyFileMode)
	if loadedCount := NewGateKeyCache(cacheDirectory).Load(); loadedCount != 0 {
		testContext.Errorf("loaded %d broken pairs", loadedCount)
	}
	if _, statError := os.Stat(brokenDirectory); !errors.Is(statError, os.ErrNotExist) {
		testContext.Error("broken cached pair not removed")
	}
}

// scriptedGateKeyFetcher returns queued results, then repeats the last one.
type scriptedGateKeyFetcher struct {
	mutex      sync.Mutex         // mutex guards results and fetchCount.
	results    []scriptedGateKeys // results are returned in order.
	fetchCount int                // fetchCount counts calls.
}

// scriptedGateKeys is one scripted fetch result.
type scriptedGateKeys struct {
	delivery   *GateKeyDelivery // delivery is returned on success.
	fetchError error            // fetchError is returned instead when set.
}

// FetchGateKeys returns the next scripted result.
func (fetcher *scriptedGateKeyFetcher) FetchGateKeys(context.Context) (*GateKeyDelivery, error) {
	fetcher.mutex.Lock()
	defer fetcher.mutex.Unlock()
	result := fetcher.results[min(fetcher.fetchCount, len(fetcher.results)-1)]
	fetcher.fetchCount++
	return result.delivery, result.fetchError
}

// fetches returns the number of calls so far.
func (fetcher *scriptedGateKeyFetcher) fetches() int {
	fetcher.mutex.Lock()
	defer fetcher.mutex.Unlock()
	return fetcher.fetchCount
}

// waitForGateKeysState polls observed/node/{id}/gate_keys until it has the
// wanted state and reason.
func waitForGateKeysState(testContext *testing.T, factStore store.StateStore, nodeID string, wantedState GateKeysState, wantedReason GateKeysWaitReason) {
	testContext.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastValue string
	for time.Now().Before(deadline) {
		if fact, getError := factStore.Get(context.Background(), types.KeyObservedNodeGateKeys(nodeID)); getError == nil {
			lastValue = string(fact.Value)
			var status gateKeysStatus
			if json.Unmarshal(fact.Value, &status) == nil && status.State == wantedState && status.Reason == wantedReason {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	testContext.Fatalf("gate_keys = %q, want %s/%s", lastValue, wantedState, wantedReason)
}

func TestGateKeySyncerServesCacheWhileAPIIsDownAndRefetchesOnChange(testContext *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	cacheDirectory := testContext.TempDir()
	webPair := issueTestGateKeyPair(testContext, "web", "web.example.com")

	// Previous agent run on this boot left the pair in tmpfs.
	if applyError := NewGateKeyCache(cacheDirectory).Apply(&GateKeyDelivery{Gates: []DeliveredGateKeyPair{webPair}}); applyError != nil {
		testContext.Fatal(applyError)
	}
	fetcher := &scriptedGateKeyFetcher{results: []scriptedGateKeys{
		{fetchError: &GateKeyFetchError{Reason: GateKeysUnreachable, Detail: "connection refused"}},
		{delivery: &GateKeyDelivery{Gates: []DeliveredGateKeyPair{webPair}, Pending: []string{"shop"}}},
	}}
	gateKeyCache := NewGateKeyCache(cacheDirectory)
	if loadedCount := gateKeyCache.Load(); loadedCount != 1 {
		testContext.Fatalf("loaded %d cached pairs, want 1", loadedCount)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go NewGateKeySyncer("node-1", factStore, fetcher, gateKeyCache).Run(ctx)

	waitForGateKeysState(testContext, factStore, "node-1", GateKeysWaiting, GateKeysUnreachable)
	if gateKeyCache.GateCertificate("web") == nil {
		testContext.Fatal("cached pair not served while the API is down")
	}
	// A gate TLS fact change triggers a fetch well before the 2s retry.
	_, _ = factStore.Put(ctx, types.KeyDerivedRouteTLSCert("web"), []byte("changed"))
	waitForGateKeysState(testContext, factStore, "node-1", GateKeysWaiting, GateKeysPending)
	if fetcher.fetches() < 2 {
		testContext.Errorf("fetches = %d, want >= 2", fetcher.fetches())
	}
}

func TestGateKeySyncerWithoutFetcherReportsNotConfigured(testContext *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	NewGateKeySyncer("node-1", factStore, nil, NewGateKeyCache(testContext.TempDir())).Run(context.Background())
	waitForGateKeysState(testContext, factStore, "node-1", GateKeysWaiting, GateKeysNotConfigured)
}

func TestPublishGateProxyCapabilityWritesBothFacts(testContext *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	if publishError := PublishGateProxyCapability(ctx, factStore, "node-1", false); publishError != nil {
		testContext.Fatal(publishError)
	}
	gateProxyFact, _ := factStore.Get(ctx, types.KeyObservedNodeGateProxy("node-1"))
	deliveryFact, _ := factStore.Get(ctx, types.KeyObservedNodeGateKeyDelivery("node-1"))
	if gateProxyFact == nil || string(gateProxyFact.Value) != string(types.GateProxyDisabled) ||
		deliveryFact == nil || string(deliveryFact.Value) != types.GateKeyDeliveryVersion {
		testContext.Fatalf("facts = %v %v", gateProxyFact, deliveryFact)
	}
}

func TestHTTPGateKeyClientMapsResponses(testContext *testing.T) {
	webPair := issueTestGateKeyPair(testContext, "web", "web.example.com")
	statusCode := http.StatusOK
	apiServer := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != nodeGateKeyDeliveryPath {
			responseWriter.WriteHeader(http.StatusNotFound)
			return
		}
		responseWriter.WriteHeader(statusCode)
		if statusCode == http.StatusOK {
			_ = json.NewEncoder(responseWriter).Encode(GateKeyDelivery{Gates: []DeliveredGateKeyPair{webPair}, Pending: []string{}})
		}
	}))
	defer apiServer.Close()
	gateKeyClient := NewHTTPGateKeyClient(apiServer.URL, apiServer.Client().Transport.(*http.Transport).TLSClientConfig)

	delivery, fetchError := gateKeyClient.FetchGateKeys(context.Background())
	if fetchError != nil || len(delivery.Gates) != 1 || !strings.Contains(delivery.Gates[0].PrivateKeyPEM, "PRIVATE KEY") {
		testContext.Fatalf("delivery = %+v, error %v", delivery, fetchError)
	}
	for responseStatus, expectedReason := range map[int]GateKeysWaitReason{
		http.StatusForbidden:           GateKeysRefused,
		http.StatusInternalServerError: GateKeysUnreachable,
		http.StatusTooManyRequests:     GateKeysUnreachable,
	} {
		statusCode = responseStatus
		_, fetchError = gateKeyClient.FetchGateKeys(context.Background())
		var typedError *GateKeyFetchError
		if !errors.As(fetchError, &typedError) || typedError.Reason != expectedReason {
			testContext.Errorf("HTTP %d: error %v, want reason %s", responseStatus, fetchError, expectedReason)
		}
	}
}

func TestGateKeySyncerRetriesPendingGatesWithBackoff(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	webPair := issueTestGateKeyPair(t, "web", "web.example.com")
	fetcher := &scriptedGateKeyFetcher{results: []scriptedGateKeys{
		{delivery: &GateKeyDelivery{Gates: []DeliveredGateKeyPair{}, Pending: []string{"web"}}},
		{delivery: &GateKeyDelivery{Gates: []DeliveredGateKeyPair{webPair}, Pending: []string{}}},
	}}
	gateKeyCache := NewGateKeyCache(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go NewGateKeySyncer("node-1", factStore, fetcher, gateKeyCache).Run(ctx)
	// The second fetch comes after the 2s retry, not the 5 min fallback.
	waitForGateKeysState(t, factStore, "node-1", GateKeysReady, "")
	if gateKeyCache.GateCertificate("web") == nil {
		t.Error("pair delivered on retry is not served")
	}
}

func TestPublishGateProxyCapabilityUntilDoneRetries(t *testing.T) {
	factStore := &failingPutStore{StateStore: store.NewMemoryStore(), failuresLeft: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	PublishGateProxyCapabilityUntilDone(ctx, factStore, "node-1", true)
	if fact, getError := factStore.Get(ctx, types.KeyObservedNodeGateKeyDelivery("node-1")); getError != nil || string(fact.Value) != types.GateKeyDeliveryVersion {
		t.Fatalf("capability not published after a failed first attempt: %v", getError)
	}
}

// failingPutStore fails the first failuresLeft Put calls.
type failingPutStore struct {
	store.StateStore     // StateStore handles every other call.
	failuresLeft     int // failuresLeft is the number of Puts still to fail.
}

// Put fails while failuresLeft > 0, then delegates.
func (failingStore *failingPutStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	if failingStore.failuresLeft > 0 {
		failingStore.failuresLeft--
		return 0, errors.New("etcd unavailable")
	}
	return failingStore.StateStore.Put(ctx, key, value)
}
