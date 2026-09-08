package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

func TestLeaderElectionAcquiresLeadership(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	acquired := make(chan struct{}, 1)

	election := NewLeaderElection(memoryStore, LeaderElectionConfig{
		NodeID:        "node-1",
		LeaseDuration: 1 * time.Second,
		RenewInterval: 100 * time.Millisecond,
		OnAcquired: func() {
			acquired <- struct{}{}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go election.Run(ctx)

	select {
	case <-acquired:
	case <-ctx.Done():
		t.Fatal("timed out waiting for leadership acquisition")
	}

	if !election.IsLeader() {
		t.Error("should be leader after acquisition")
	}

	leader := election.CurrentLeader(ctx)
	if leader != "node-1" {
		t.Errorf("current leader = %q, want node-1", leader)
	}
}

func TestLeaderElectionSecondNodeWaits(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	acquired1 := make(chan struct{}, 1)
	acquired2 := make(chan struct{}, 1)

	election1 := NewLeaderElection(memoryStore, LeaderElectionConfig{
		NodeID:        "node-1",
		LeaseDuration: 5 * time.Second,
		RenewInterval: 100 * time.Millisecond,
		OnAcquired:    func() { acquired1 <- struct{}{} },
	})

	election2 := NewLeaderElection(memoryStore, LeaderElectionConfig{
		NodeID:        "node-2",
		LeaseDuration: 5 * time.Second,
		RenewInterval: 100 * time.Millisecond,
		OnAcquired:    func() { acquired2 <- struct{}{} },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go election1.Run(ctx)

	select {
	case <-acquired1:
	case <-ctx.Done():
		t.Fatal("timed out waiting for node-1 leadership")
	}

	go election2.Run(ctx)

	// node-2 should NOT acquire leadership while node-1's lease is active.
	select {
	case <-acquired2:
		t.Fatal("node-2 should not acquire leadership while node-1 is active")
	case <-time.After(500 * time.Millisecond):
	}

	if election2.IsLeader() {
		t.Error("node-2 should not be leader")
	}
}

func TestLeaderElectionFailover(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	acquired1 := make(chan struct{}, 1)
	acquired2 := make(chan struct{}, 1)

	ctx1, cancel1 := context.WithCancel(context.Background())

	election1 := NewLeaderElection(memoryStore, LeaderElectionConfig{
		NodeID:        "node-1",
		LeaseDuration: 300 * time.Millisecond,
		RenewInterval: 50 * time.Millisecond,
		OnAcquired:    func() { acquired1 <- struct{}{} },
	})

	election2 := NewLeaderElection(memoryStore, LeaderElectionConfig{
		NodeID:        "node-2",
		LeaseDuration: 300 * time.Millisecond,
		RenewInterval: 50 * time.Millisecond,
		OnAcquired:    func() { acquired2 <- struct{}{} },
	})

	go election1.Run(ctx1)

	select {
	case <-acquired1:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for node-1 leadership")
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()

	go election2.Run(ctx2)

	// Kill node-1 — its lease should expire.
	cancel1()

	// node-2 should acquire after lease expiry.
	select {
	case <-acquired2:
	case <-ctx2.Done():
		t.Fatal("timed out waiting for node-2 failover")
	}

	if !election2.IsLeader() {
		t.Error("node-2 should be leader after failover")
	}

	leader := election2.CurrentLeader(ctx2)
	if leader != "node-2" {
		t.Errorf("current leader = %q, want node-2", leader)
	}
}

func TestLeaderElectionReleasesOnShutdown(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	acquired := make(chan struct{}, 1)
	lost := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())

	election := NewLeaderElection(memoryStore, LeaderElectionConfig{
		NodeID:        "node-1",
		LeaseDuration: 5 * time.Second,
		RenewInterval: 50 * time.Millisecond,
		OnAcquired:    func() { acquired <- struct{}{} },
		OnLost:        func() { lost <- struct{}{} },
	})

	go election.Run(ctx)

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for leadership")
	}

	cancel()

	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for leadership release")
	}

	if election.IsLeader() {
		t.Error("should not be leader after shutdown")
	}
}
