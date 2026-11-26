// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build etcd_integration

package security

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// tokenSigningEtcdStartTimeout bounds how long the test etcd may take to serve.
const tokenSigningEtcdStartTimeout = 30 * time.Second

// tokenSigningEtcdDialTimeout bounds one connection attempt to the test etcd.
const tokenSigningEtcdDialTimeout = 3 * time.Second

// tokenSigningEtcdPollInterval is the wait between readiness probes.
const tokenSigningEtcdPollInterval = 200 * time.Millisecond

// tokenSigningEtcdKeyPrefix namespaces the test's keys inside the test etcd.
const tokenSigningEtcdKeyPrefix = "/ccattler-token-signing-test/" //nolint:gosec // G101: a store key prefix, not a credential

// freeTokenSigningTestPort returns a loopback TCP port that was free a moment ago.
func freeTokenSigningTestPort(t *testing.T) string {
	t.Helper()
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		t.Fatal(listenError)
	}
	defer func() { _ = listener.Close() }()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

// startTokenSigningTestEtcd starts a real etcd (ETCD_BIN, else etcd on PATH)
// on loopback and returns its client URL; a missing binary fails the test.
func startTokenSigningTestEtcd(t *testing.T) string {
	t.Helper()
	etcdBinary := os.Getenv("ETCD_BIN")
	if etcdBinary == "" {
		foundBinary, lookupError := exec.LookPath("etcd")
		if lookupError != nil {
			t.Fatal("etcd_integration needs an etcd binary: set ETCD_BIN or put etcd on PATH")
		}
		etcdBinary = foundBinary
	}
	clientURL := "http://127.0.0.1:" + freeTokenSigningTestPort(t)
	peerURL := "http://127.0.0.1:" + freeTokenSigningTestPort(t)
	etcdCommand := exec.Command(etcdBinary, //nolint:gosec // test etcd binary chosen by the operator
		"--name", "token-signing-test", "--data-dir", t.TempDir(), "--log-level", "error",
		"--listen-client-urls", clientURL, "--advertise-client-urls", clientURL,
		"--listen-peer-urls", peerURL, "--initial-advertise-peer-urls", peerURL,
		"--initial-cluster", "token-signing-test="+peerURL)
	etcdCommand.Stderr = os.Stderr
	if startError := etcdCommand.Start(); startError != nil {
		t.Fatalf("start etcd: %v", startError)
	}
	t.Cleanup(func() {
		_ = etcdCommand.Process.Kill()
		_ = etcdCommand.Wait()
	})
	return clientURL
}

// openTokenSigningTestEtcdStore connects one replica's store to the test etcd,
// polling until etcd answers or the start timeout passes.
func openTokenSigningTestEtcdStore(t *testing.T, clientURL string) *store.EtcdStore {
	t.Helper()
	deadline := time.Now().Add(tokenSigningEtcdStartTimeout)
	pollTicker := time.NewTicker(tokenSigningEtcdPollInterval)
	defer pollTicker.Stop()
	for {
		etcdStore, connectError := store.NewEtcdStore(store.EtcdStoreConfig{Endpoints: []string{clientURL},
			KeyPrefix: tokenSigningEtcdKeyPrefix, DialTimeout: tokenSigningEtcdDialTimeout})
		if connectError == nil {
			probeContext, cancelProbe := context.WithTimeout(context.Background(), tokenSigningEtcdDialTimeout)
			_, probeError := etcdStore.Get(probeContext, "probe")
			cancelProbe()
			if probeError == nil || errors.Is(probeError, store.ErrKeyNotFound) {
				t.Cleanup(func() { _ = etcdStore.Close() })
				return etcdStore
			}
			_ = etcdStore.Close()
			connectError = probeError
		}
		if time.Now().After(deadline) {
			t.Fatalf("etcd at %s did not answer: %v", clientURL, connectError)
		}
		<-pollTicker.C
	}
}

// TestTokenSigningKeyConcurrentFirstStartConvergesOnEtcd is the real-etcd
// tier of TestTokenSigningKeyConcurrentFirstStartConverges: four replicas,
// each with its own etcd client, start at once on an empty keyring area.
func TestTokenSigningKeyConcurrentFirstStartConvergesOnEtcd(t *testing.T) {
	clientURL := startTokenSigningTestEtcd(t)
	rootKeyProvider := newTestKeyProvider(t)
	replicaCodecs := make([]*KeyringEnvelopeCodec, tokenSigningTestReplicaCount)
	replicaStores := make([]store.StateStore, tokenSigningTestReplicaCount)
	for replicaIndex := range replicaCodecs {
		replicaStores[replicaIndex] = openTokenSigningTestEtcdStore(t, clientURL)
		keyringCodec, codecError := NewKeyringEnvelopeCodec(context.Background(), replicaStores[replicaIndex], rootKeyProvider, nil)
		if codecError != nil {
			t.Fatal(codecError)
		}
		replicaCodecs[replicaIndex] = keyringCodec
	}
	identities := startTokenSigningReplicasOnStores(t, replicaStores, replicaCodecs)
	if t.Failed() {
		return
	}
	assertReplicasConvergedOnOneIdentity(t, replicaStores[0], identities)
}
