// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// deployClusterStoreReadLatency is the single-key etcd read latency observed
// on the memory-starved deploy cluster (M175), injected into every read.
const deployClusterStoreReadLatency = 110 * time.Millisecond

// roundTripCountingStore wraps a StateStore, counts every read round-trip and
// optionally sleeps per read to model a slow etcd.
type roundTripCountingStore struct {
	store.StateStore               // StateStore is the wrapped in-memory store
	readLatency      time.Duration // readLatency is slept before every Get/Scan
	readRoundTrips   atomic.Int64  // readRoundTrips counts Get, Scan and ScanWithRevision calls
}

// recordReadRoundTrip counts one store read and applies the injected latency.
func (countingStore *roundTripCountingStore) recordReadRoundTrip() {
	countingStore.readRoundTrips.Add(1)
	if countingStore.readLatency > 0 {
		time.Sleep(countingStore.readLatency)
	}
}

// Get counts the read, then delegates to the wrapped store.
func (countingStore *roundTripCountingStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	countingStore.recordReadRoundTrip()
	return countingStore.StateStore.Get(ctx, key)
}

// Scan counts the read, then delegates to the wrapped store.
func (countingStore *roundTripCountingStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	countingStore.recordReadRoundTrip()
	return countingStore.StateStore.Scan(ctx, prefix)
}

// ScanWithRevision counts the read, then delegates to the wrapped store.
func (countingStore *roundTripCountingStore) ScanWithRevision(ctx context.Context, prefix string) (*store.ScanResult, error) {
	countingStore.recordReadRoundTrip()
	return countingStore.StateStore.ScanWithRevision(ctx, prefix)
}

// statusFixtureSize describes how many objects seedStatusFixture writes.
type statusFixtureSize struct {
	serviceCount        int // serviceCount is the number of desired services
	instancesPerService int // instancesPerService is the number of observed instances per service
	nodeCount           int // nodeCount is the number of observed nodes
	secretCount         int // secretCount is the number of secrets, each granted to every service
}

// seedStatusFixture writes services (with tags, config, owner and secret
// grants), placed instances with metrics and probe facts, nodes with
// utilization, and secrets, so every status section has data to read.
func seedStatusFixture(t testing.TB, ctx context.Context, factStore store.StateStore, fixtureSize statusFixtureSize) {
	t.Helper()
	mustPut := func(key, value string) {
		if _, putError := factStore.Put(ctx, key, []byte(value)); putError != nil {
			t.Fatalf("seed %s: %v", key, putError)
		}
	}
	for nodeIndex := 0; nodeIndex < fixtureSize.nodeCount; nodeIndex++ {
		nodeID := fmt.Sprintf("node%02d", nodeIndex)
		if writeError := types.WriteNode(ctx, factStore, types.Node{ID: nodeID, State: types.NodeAlive, CapacityCPU: 4000, CapacityMemory: 8 << 30}); writeError != nil {
			t.Fatalf("seed node: %v", writeError)
		}
		mustPut(types.KeyObservedNodeUtilizationCPU(nodeID), "25")
		mustPut(types.KeyObservedNodeUtilizationMemory(nodeID), "40")
	}
	for secretIndex := 0; secretIndex < fixtureSize.secretCount; secretIndex++ {
		mustPut(security.SecretStorePrefix+fmt.Sprintf("secret%02d", secretIndex), "sealed")
	}
	for serviceIndex := 0; serviceIndex < fixtureSize.serviceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc%02d", serviceIndex)
		if writeError := types.WriteService(ctx, factStore, types.Service{Name: serviceName, Image: "nginx:1.27", Instances: fixtureSize.instancesPerService, Ports: []int{80}}); writeError != nil {
			t.Fatalf("seed service: %v", writeError)
		}
		mustPut(types.KeyDesiredServiceTagPrefix(serviceName)+"tier", "web")
		mustPut(types.KeyDesiredServiceConfigEnv(serviceName, "LOG_LEVEL"), "info")
		mustPut(types.KeyDesiredServiceOwner(serviceName), "payments")
		for secretIndex := 0; secretIndex < fixtureSize.secretCount; secretIndex++ {
			mustPut(types.KeyDesiredServiceSecret(serviceName, fmt.Sprintf("secret%02d", secretIndex)), "")
		}
		for instanceIndex := 0; instanceIndex < fixtureSize.instancesPerService; instanceIndex++ {
			instanceID := fmt.Sprintf("%s-%02d", serviceName, instanceIndex)
			nodeID := fmt.Sprintf("node%02d", instanceIndex%fixtureSize.nodeCount)
			if writeError := types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstanceRunning, Node: nodeID, IP: "10.100.0.10"}); writeError != nil {
				t.Fatalf("seed instance: %v", writeError)
			}
			mustPut(types.KeyPlacementInstance(instanceID), nodeID)
			mustPut(types.KeyObservedInstanceCPU(instanceID), "120")
			mustPut(types.KeyObservedInstanceMemory(instanceID), "52428800")
			mustPut(types.KeyObservedInstanceRestarts(instanceID), "0")
			mustPut(types.KeyObservedInstanceProbeState(instanceID, types.ProbeReadiness), "passing")
			mustPut(types.KeyObservedInstanceCreatedAt(instanceID), "2026-11-07T00:00:00Z")
		}
	}
}

// countStatusBuildRoundTrips seeds a fixture of the given size and returns the
// number of store reads one status build issues for the given visibility.
func countStatusBuildRoundTrips(t *testing.T, fixtureSize statusFixtureSize, visibility *workloadVisibility) int64 {
	t.Helper()
	ctx := context.Background()
	countingStore := &roundTripCountingStore{StateStore: store.NewMemoryStore()}
	defer countingStore.Close()
	seedStatusFixture(t, ctx, countingStore, fixtureSize)
	countingStore.readRoundTrips.Store(0)
	if _, buildError := buildStatusFromStore(ctx, countingStore, visibility, true); buildError != nil {
		t.Fatalf("build status: %v", buildError)
	}
	return countingStore.readRoundTrips.Load()
}

// TestStatusBuildRoundTripsConstantInClusterSize asserts the number of store
// reads for one status build does not grow with instance, node, service or
// secret count, for both cluster-scope and team-scoped visibility (M175).
func TestStatusBuildRoundTripsConstantInClusterSize(t *testing.T) {
	smallCluster := statusFixtureSize{serviceCount: 2, instancesPerService: 2, nodeCount: 1, secretCount: 1}
	largeCluster := statusFixtureSize{serviceCount: 12, instancesPerService: 4, nodeCount: 5, secretCount: 6}
	visibilityCases := map[string]*workloadVisibility{
		"cluster": nil,
		"team":    {teams: []string{"payments"}},
	}
	for caseName, visibility := range visibilityCases {
		t.Run(caseName, func(t *testing.T) {
			smallRoundTrips := countStatusBuildRoundTrips(t, smallCluster, visibility)
			largeRoundTrips := countStatusBuildRoundTrips(t, largeCluster, visibility)
			t.Logf("store round-trips: small=%d large=%d", smallRoundTrips, largeRoundTrips)
			if smallRoundTrips != largeRoundTrips {
				t.Errorf("status build store reads grow with cluster size: small=%d large=%d", smallRoundTrips, largeRoundTrips)
			}
		})
	}
}

// BenchmarkStatusBuildWithDeployClusterLatency measures one status build for a
// deploy-cluster-sized fixture (16 instances, 3 nodes) with 110ms injected per
// store read. Run with -benchtime=1x; reports round-trips per build.
func BenchmarkStatusBuildWithDeployClusterLatency(b *testing.B) {
	ctx := context.Background()
	countingStore := &roundTripCountingStore{StateStore: store.NewMemoryStore()}
	defer countingStore.Close()
	seedStatusFixture(b, ctx, countingStore, statusFixtureSize{serviceCount: 8, instancesPerService: 2, nodeCount: 3, secretCount: 3})
	countingStore.readLatency = deployClusterStoreReadLatency
	countingStore.readRoundTrips.Store(0)

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, buildError := buildStatusFromStore(ctx, countingStore, nil, false); buildError != nil {
			b.Fatalf("build status: %v", buildError)
		}
	}
	b.ReportMetric(float64(countingStore.readRoundTrips.Load())/float64(b.N), "roundtrips/op")
}
