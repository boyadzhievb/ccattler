// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
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
	// lastSuccessfulRenewal tracks when the lease was last successfully renewed.
	lastSuccessfulRenewal time.Time
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

const (
	// leaderLeaseKey is the fact store key used for the leader lease.
	leaderLeaseKey = "leader/controlplane"

	// leaderLeaseHolderKey stores the identity of the current leader.
	leaderLeaseHolderKey = "leader/controlplane/holder"

	// defaultLeaderLeaseDuration is how long a leader lease is valid before
	// it must be renewed. If the leader fails to renew within this window,
	// another node may acquire leadership.
	defaultLeaderLeaseDuration = 15 * time.Second

	// defaultLeaderLeaseRenewInterval is how often the current leader
	// refreshes its lease timestamp in the fact store.
	defaultLeaderLeaseRenewInterval = 5 * time.Second
)

// NewLeaderElection creates a leader election instance for the given node.
func NewLeaderElection(factStore store.StateStore, config LeaderElectionConfig) *LeaderElection {
	leaseDuration := config.LeaseDuration
	if leaseDuration == 0 {
		leaseDuration = defaultLeaderLeaseDuration
	}
	renewInterval := config.RenewInterval
	if renewInterval == 0 {
		renewInterval = defaultLeaderLeaseRenewInterval
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
		election.renewLeaseWithCAS(ctx, nowStr)
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
			election.lastSuccessfulRenewal = time.Now()
			logging.Default().Info("acquired leadership (expired lease)", "node", election.nodeID)
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
		logging.Default().Warn("leader election transaction failed", "node", election.nodeID, "error", err.Error())
		return
	}
	if ok {
		election.isLeader = true
		election.lastSuccessfulRenewal = time.Now()
		logging.Default().Info("acquired leadership (new lease)", "node", election.nodeID)
		if election.onAcquired != nil {
			election.onAcquired()
		}
	}
}

// renewLeaseWithCAS renews the leader lease using compare-and-swap to detect
// if another node has taken over. Transient etcd errors (timeouts, unavailable)
// do not immediately relinquish leadership — the node retains leadership as
// long as the time since the last successful renewal is within the lease
// duration. Only a confirmed holder change or exceeding the lease window
// triggers leadership loss.
func (election *LeaderElection) renewLeaseWithCAS(ctx context.Context, nowStr string) {
	holderFact, getErr := election.factStore.Get(ctx, leaderLeaseHolderKey)
	if getErr != nil {
		election.handleTransientRenewalFailure("holder read failed")
		return
	}
	if string(holderFact.Value) != election.nodeID {
		election.relinquishLeadership("holder changed")
		return
	}
	renewed, txnErr := election.factStore.Transaction(ctx,
		[]store.Compare{{Key: leaderLeaseHolderKey, Revision: holderFact.Revision}},
		[]store.Op{{Type: store.OpPut, Key: leaderLeaseKey, Value: []byte(nowStr)}},
		nil,
	)
	if txnErr != nil {
		election.handleTransientRenewalFailure("CAS transaction error")
		return
	}
	if !renewed {
		election.relinquishLeadership("CAS failed")
		return
	}
	election.lastSuccessfulRenewal = time.Now()
}

// handleTransientRenewalFailure checks whether the cumulative time since the
// last successful renewal exceeds the lease duration. If so, leadership is
// relinquished. Otherwise the failure is logged and leadership is retained.
func (election *LeaderElection) handleTransientRenewalFailure(reason string) {
	sinceLast := time.Since(election.lastSuccessfulRenewal)
	if sinceLast > election.leaseDuration {
		election.relinquishLeadership(fmt.Sprintf("%s (exceeded lease duration: %s since last renewal)", reason, sinceLast.Round(time.Second)))
		return
	}
	logging.Default().Warn("transient renewal failure, retaining leadership",
		"node", election.nodeID, "reason", reason,
		"since_last_renewal", sinceLast.Round(time.Second).String(),
		"lease_duration", election.leaseDuration.String())
}

// relinquishLeadership gives up the leader role and notifies the callback.
func (election *LeaderElection) relinquishLeadership(reason string) {
	election.isLeader = false
	logging.Default().Warn("lost leadership during renewal", "node", election.nodeID, "reason", reason)
	if election.onLost != nil {
		election.onLost()
	}
}

// release gives up leadership on shutdown. Uses compare-and-swap to verify
// this node is still the holder before deleting — avoids accidentally
// deleting another node's valid lease.
func (election *LeaderElection) release(ctx context.Context) {
	election.mutex.Lock()
	defer election.mutex.Unlock()

	if !election.isLeader {
		return
	}

	holderFact, getErr := election.factStore.Get(ctx, leaderLeaseHolderKey)
	if getErr != nil || string(holderFact.Value) != election.nodeID {
		election.isLeader = false
		logging.Default().Warn("skipped lease cleanup (no longer holder)", "node", election.nodeID)
		return
	}

	if _, txnErr := election.factStore.Transaction(ctx,
		[]store.Compare{{Key: leaderLeaseHolderKey, Revision: holderFact.Revision}},
		[]store.Op{
			{Type: store.OpDelete, Key: leaderLeaseKey},
			{Type: store.OpDelete, Key: leaderLeaseHolderKey},
		},
		nil,
	); txnErr != nil {
		logging.Default().Error("failed to release leader lease", "node", election.nodeID, "error", txnErr.Error())
	}
	election.isLeader = false
	logging.Default().Info("released leadership", "node", election.nodeID)
	if election.onLost != nil {
		election.onLost()
	}
}

// parseLeaseTimestamp parses a millisecond Unix timestamp string.
func parseLeaseTimestamp(value string) time.Time {
	var millis int64
	if _, scanErr := fmt.Sscanf(value, "%d", &millis); scanErr != nil {
		logging.Default().Warn("corrupt lease timestamp", "value", value)
	}
	return time.UnixMilli(millis)
}
