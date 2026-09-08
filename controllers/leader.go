package controllers

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// LeaderElection implements leader election for multi-node control planes.
// Only the current leader runs controllers; standby nodes continuously
// attempt to acquire leadership when it expires. Leadership is maintained
// by periodically renewing a lease key in the fact store.
type LeaderElection struct {
	// nodeID identifies this control-plane node.
	nodeID string
	// factStore is the shared state store used for the lease key.
	factStore store.StateStore
	// leaseDuration is how long a lease is valid before it must be renewed.
	leaseDuration time.Duration
	// renewInterval is how often the leader refreshes its lease.
	renewInterval time.Duration
	// isLeader tracks whether this node currently holds the lease.
	isLeader bool
	// onAcquired is called when this node becomes the leader.
	onAcquired func()
	// onLost is called when this node loses leadership.
	onLost func()
	mutex  sync.Mutex
}

// LeaderElectionConfig holds settings for leader election.
type LeaderElectionConfig struct {
	// NodeID identifies this control-plane node.
	NodeID string
	// LeaseDuration is how long a lease is valid (default 15s).
	LeaseDuration time.Duration
	// RenewInterval is how often the leader renews (default 5s).
	RenewInterval time.Duration
	// OnAcquired is called when this node becomes leader.
	OnAcquired func()
	// OnLost is called when this node loses leadership.
	OnLost func()
}

// leaderLeaseKey is the fact store key used for the leader lease.
const leaderLeaseKey = types.Root + "/leader/controlplane"

// leaderLeaseHolderKey stores the identity of the current leader.
const leaderLeaseHolderKey = types.Root + "/leader/controlplane/holder"

// NewLeaderElection creates a leader election instance for the given node.
func NewLeaderElection(factStore store.StateStore, config LeaderElectionConfig) *LeaderElection {
	leaseDuration := config.LeaseDuration
	if leaseDuration == 0 {
		leaseDuration = 15 * time.Second
	}
	renewInterval := config.RenewInterval
	if renewInterval == 0 {
		renewInterval = 5 * time.Second
	}

	return &LeaderElection{
		nodeID:        config.NodeID,
		factStore:     factStore,
		leaseDuration: leaseDuration,
		renewInterval: renewInterval,
		onAcquired:    config.OnAcquired,
		onLost:        config.OnLost,
	}
}

// Run starts the leader election loop. It blocks until ctx is cancelled.
// The node continuously attempts to acquire or renew the lease.
func (election *LeaderElection) Run(ctx context.Context) error {
	ticker := time.NewTicker(election.renewInterval)
	defer ticker.Stop()

	election.tryAcquireOrRenew(ctx)

	for {
		select {
		case <-ctx.Done():
			election.release(ctx)
			return ctx.Err()
		case <-ticker.C:
			election.tryAcquireOrRenew(ctx)
		}
	}
}

// IsLeader returns whether this node currently holds the leader lease.
func (election *LeaderElection) IsLeader() bool {
	election.mutex.Lock()
	defer election.mutex.Unlock()
	return election.isLeader
}

// CurrentLeader returns the node ID of the current leader, or empty string
// if no leader is elected.
func (election *LeaderElection) CurrentLeader(ctx context.Context) string {
	holderFact, err := election.factStore.Get(ctx, leaderLeaseHolderKey)
	if err != nil {
		return ""
	}
	return string(holderFact.Value)
}

// tryAcquireOrRenew attempts to acquire or renew the leader lease.
func (election *LeaderElection) tryAcquireOrRenew(ctx context.Context) {
	election.mutex.Lock()
	defer election.mutex.Unlock()

	now := time.Now()
	nowStr := fmt.Sprintf("%d", now.UnixMilli())

	if election.isLeader {
		// Renew: update the lease timestamp.
		election.factStore.Put(ctx, leaderLeaseKey, []byte(nowStr))
		return
	}

	// Check if current lease has expired.
	leaseFact, err := election.factStore.Get(ctx, leaderLeaseKey)
	if err != nil {
		// No lease exists — try to acquire.
		election.acquireLease(ctx, nowStr)
		return
	}

	leaseTimestamp := parseLeaseTimestamp(string(leaseFact.Value))
	leaseAge := now.Sub(leaseTimestamp)

	if leaseAge > election.leaseDuration {
		// Lease expired — try to acquire using compare-and-swap.
		ok, txnErr := election.factStore.Transaction(ctx,
			[]store.Compare{{Key: leaderLeaseKey, Revision: leaseFact.Revision}},
			[]store.Op{
				{Type: store.OpPut, Key: leaderLeaseKey, Value: []byte(nowStr)},
				{Type: store.OpPut, Key: leaderLeaseHolderKey, Value: []byte(election.nodeID)},
			},
			nil,
		)
		if txnErr != nil {
			return
		}
		if ok {
			election.isLeader = true
			log.Printf("leader election: %s acquired leadership (expired lease)", election.nodeID)
			if election.onAcquired != nil {
				election.onAcquired()
			}
		}
	}
}

// acquireLease writes the initial lease when no lease exists.
func (election *LeaderElection) acquireLease(ctx context.Context, nowStr string) {
	ok, err := election.factStore.Transaction(ctx,
		[]store.Compare{{Key: leaderLeaseKey, Revision: 0}},
		[]store.Op{
			{Type: store.OpPut, Key: leaderLeaseKey, Value: []byte(nowStr)},
			{Type: store.OpPut, Key: leaderLeaseHolderKey, Value: []byte(election.nodeID)},
		},
		nil,
	)
	if err != nil {
		return
	}
	if ok {
		election.isLeader = true
		log.Printf("leader election: %s acquired leadership (new lease)", election.nodeID)
		if election.onAcquired != nil {
			election.onAcquired()
		}
	}
}

// release gives up leadership on shutdown.
func (election *LeaderElection) release(ctx context.Context) {
	election.mutex.Lock()
	defer election.mutex.Unlock()

	if !election.isLeader {
		return
	}

	election.factStore.Delete(ctx, leaderLeaseKey)
	election.factStore.Delete(ctx, leaderLeaseHolderKey)
	election.isLeader = false
	log.Printf("leader election: %s released leadership", election.nodeID)
	if election.onLost != nil {
		election.onLost()
	}
}

// parseLeaseTimestamp parses a millisecond Unix timestamp string.
func parseLeaseTimestamp(value string) time.Time {
	var millis int64
	fmt.Sscanf(value, "%d", &millis)
	return time.UnixMilli(millis)
}
