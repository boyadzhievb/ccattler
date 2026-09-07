// Package controllers implements the reconciliation controllers for CCattler.
// Each controller watches a set of fact prefixes in the store, compares desired
// state against observed state, and produces a list of proposed changes that
// the runner applies transactionally.
package controllers

import (
	"context"

	"github.com/boyadzhievb/ccattler/store"
)

// Change represents a single proposed mutation to the fact store. Controllers
// return slices of Change from their Reconcile methods; the Runner applies
// them sequentially.
type Change struct {
	// Type indicates the operation: put (create/update) or delete.
	Type store.OpType
	// Key is the fact store key to modify (e.g. "/observed/instance/abc/state").
	Key string
	// Value is the new value for put operations; ignored for deletes.
	Value []byte
}

// Controller is the interface that every reconciliation controller must
// implement. The Runner uses it to discover which fact prefixes to watch and
// to invoke reconciliation when those facts change.
type Controller interface {
	// Name returns a human-readable identifier for the controller, used in
	// log messages and error reporting (e.g. "instance", "endpoint").
	Name() string
	// Watch returns the fact-store key prefixes that this controller cares
	// about. The runner sets up watches on each prefix and triggers
	// reconciliation whenever any watched fact changes.
	Watch() []string
	// Reconcile examines the current facts and returns a list of proposed
	// changes that move the system toward the desired state. It must be
	// idempotent: calling it with the same facts must produce the same
	// (or convergent) changes.
	Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error)
}
