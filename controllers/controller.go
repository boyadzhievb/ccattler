package controllers

import (
	"context"

	"github.com/boyadzhievb/ccatler/store"
)

type Change struct {
	Type  store.OpType
	Key   string
	Value []byte
}

type Controller interface {
	Name() string
	Watch() []string
	Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error)
}
