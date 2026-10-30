// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// auditFact builds a store.Fact with a zero-padded audit key and a JSON-encoded
// AuditEntry value at the given timestamp.
func auditFact(sequence int, timestamp time.Time, principal, action string) store.Fact {
	entry := security.AuditEntry{
		Timestamp:  timestamp,
		Principal:  principal,
		Action:     action,
		Target:     "desired/service/web",
		Decision:   "allow",
		AuthMethod: "local-token",
	}
	entryJSON, _ := json.Marshal(entry)
	return store.Fact{
		Key:   fmt.Sprintf("audit/%020d", sequence),
		Value: entryJSON,
	}
}

// TestAuditLogGCKeepsRecentEntries verifies that entries within the retention
// period are not deleted.
func TestAuditLogGCKeepsRecentEntries(t *testing.T) {
	gcController := NewAuditLogGCController()
	gcController.RetentionPeriod = 7 * 24 * time.Hour
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	facts := []store.Fact{
		auditFact(1, now.Add(-1*time.Hour), "user:alice", "get"),
		auditFact(2, now.Add(-24*time.Hour), "user:bob", "put"),
		auditFact(3, now.Add(-3*24*time.Hour), "node:node-1", "scan"),
	}

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no deletions for recent entries, got %d", len(changes))
	}
}

// TestAuditLogGCDeletesExpiredEntries verifies that entries older than the
// retention period are deleted.
func TestAuditLogGCDeletesExpiredEntries(t *testing.T) {
	gcController := NewAuditLogGCController()
	gcController.RetentionPeriod = 7 * 24 * time.Hour
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	facts := []store.Fact{
		auditFact(1, now.Add(-10*24*time.Hour), "user:alice", "get"),
		auditFact(2, now.Add(-8*24*time.Hour), "user:bob", "put"),
		auditFact(3, now.Add(-1*time.Hour), "node:node-1", "scan"),
	}

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 deletions for expired entries, got %d", len(changes))
	}

	for _, change := range changes {
		if change.Type != store.OpDelete {
			t.Errorf("expected Delete op, got %v for key %s", change.Type, change.Key)
		}
	}

	if changes[0].Key != fmt.Sprintf("audit/%020d", 1) {
		t.Errorf("expected oldest key first, got %s", changes[0].Key)
	}
	if changes[1].Key != fmt.Sprintf("audit/%020d", 2) {
		t.Errorf("expected second oldest key second, got %s", changes[1].Key)
	}
}

// TestAuditLogGCRespectsMaxDeletionsPerCycle verifies that only
// MaxDeletionsPerCycle entries are deleted per reconciliation cycle.
func TestAuditLogGCRespectsMaxDeletionsPerCycle(t *testing.T) {
	gcController := NewAuditLogGCController()
	gcController.RetentionPeriod = 1 * time.Hour
	gcController.MaxDeletionsPerCycle = 2
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	facts := []store.Fact{
		auditFact(1, now.Add(-5*time.Hour), "user:alice", "get"),
		auditFact(2, now.Add(-4*time.Hour), "user:bob", "put"),
		auditFact(3, now.Add(-3*time.Hour), "user:carol", "delete"),
		auditFact(4, now.Add(-2*time.Hour), "user:dave", "scan"),
	}

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 2 {
		t.Errorf("expected 2 deletions (max per cycle), got %d", len(changes))
	}

	// Oldest entries should be deleted first.
	if changes[0].Key != fmt.Sprintf("audit/%020d", 1) {
		t.Errorf("expected oldest key deleted first, got %s", changes[0].Key)
	}
	if changes[1].Key != fmt.Sprintf("audit/%020d", 2) {
		t.Errorf("expected second oldest deleted second, got %s", changes[1].Key)
	}
}

// TestAuditLogGCSkipsUnparseableEntries verifies that entries with invalid JSON
// are silently skipped without crashing or being deleted.
func TestAuditLogGCSkipsUnparseableEntries(t *testing.T) {
	gcController := NewAuditLogGCController()
	gcController.RetentionPeriod = 1 * time.Hour
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	facts := []store.Fact{
		{Key: "audit/00000000000000000001", Value: []byte("not valid json")},
		{Key: "audit/00000000000000000002", Value: []byte("{broken")},
		auditFact(3, now.Add(-5*time.Hour), "user:alice", "get"),
	}

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 deletion (only the parseable expired entry), got %d", len(changes))
	}

	if changes[0].Key != fmt.Sprintf("audit/%020d", 3) {
		t.Errorf("expected only parseable expired entry deleted, got %s", changes[0].Key)
	}
}

// TestAuditLogGCSkipsZeroTimestamp verifies that entries with a zero timestamp
// are kept rather than treated as expired.
func TestAuditLogGCSkipsZeroTimestamp(t *testing.T) {
	gcController := NewAuditLogGCController()
	gcController.RetentionPeriod = 1 * time.Hour
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	entryWithZeroTimestamp := security.AuditEntry{
		Principal: "user:alice",
		Action:    "get",
		Target:    "desired/service/web",
		Decision:  "allow",
	}
	zeroTimestampJSON, _ := json.Marshal(entryWithZeroTimestamp)

	facts := []store.Fact{
		{Key: "audit/00000000000000000001", Value: zeroTimestampJSON},
		auditFact(2, now.Add(-5*time.Hour), "user:bob", "put"),
	}

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 deletion (only the non-zero expired entry), got %d", len(changes))
	}

	if changes[0].Key != fmt.Sprintf("audit/%020d", 2) {
		t.Errorf("expected only non-zero timestamp entry deleted, got %s", changes[0].Key)
	}
}
