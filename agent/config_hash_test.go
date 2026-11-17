package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// configHashTestTimeout bounds each wait in the config hash tests.
const configHashTestTimeout = 3 * time.Second

// lateFactRuntime is a simulator runtime that writes one fact into the store
// during the first Start of an instance, as an apply still writing facts
// while the agent starts the container would (M235).
type lateFactRuntime struct {
	*runtime.SimulatorRuntime                  // SimulatorRuntime runs the workloads.
	factStore                 store.StateStore // factStore receives the late fact.
	lateFactKey               string           // lateFactKey is written during the first Start.
	lateFactValue             string           // lateFactValue is the late fact's value.
	mutex                     sync.Mutex       // mutex guards startCounts.
	startCounts               map[string]int   // startCounts counts Start calls per instance ID.
}

// Start counts the call, writes the late fact on the first start, then starts the workload.
func (lateRuntime *lateFactRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	lateRuntime.mutex.Lock()
	lateRuntime.startCounts[spec.ID]++
	firstStart := lateRuntime.startCounts[spec.ID] == 1
	lateRuntime.mutex.Unlock()
	if firstStart {
		if _, putError := lateRuntime.factStore.Put(ctx, lateRuntime.lateFactKey, []byte(lateRuntime.lateFactValue)); putError != nil {
			return putError
		}
	}
	return lateRuntime.SimulatorRuntime.Start(ctx, spec)
}

// startCount returns how often instanceID was started.
func (lateRuntime *lateFactRuntime) startCount(instanceID string) int {
	lateRuntime.mutex.Lock()
	defer lateRuntime.mutex.Unlock()
	return lateRuntime.startCounts[instanceID]
}

// TestAgentRestartsWhenConfigArrivesDuringStart checks that an env var written
// while the container starts reaches the instance by a restart (spec
// instance-config-reload, M235).
func TestAgentRestartsWhenConfigArrivesDuringStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lateRuntime := &lateFactRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		factStore:        factStore,
		lateFactKey:      types.KeyDesiredServiceConfigEnv("web", "DB_HOST"),
		lateFactValue:    "db.new.local",
		startCounts:      make(map[string]int),
	}
	nodeAgent := New("node-1", factStore, lateRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.old.local"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "late1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "late1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "instance restarted after the env var written during its start", func() bool {
		return lateRuntime.startCount("late1") >= 2
	})
}

// failingConfigScanStore fails every Scan of a service's config or secret
// facts while failScans is set, as a store blip during a reconcile would.
type failingConfigScanStore struct {
	store.StateStore              // StateStore serves every other call.
	failScans        atomic.Bool  // failScans makes config and secret scans fail.
	failedScanCount  atomic.Int64 // failedScanCount counts the scans that failed.
}

// Scan fails config and secret scans while failScans is set.
func (failingStore *failingConfigScanStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	if failingStore.failScans.Load() && (strings.Contains(prefix, "/config/") || strings.Contains(prefix, "/secret/")) {
		failingStore.failedScanCount.Add(1)
		return nil, errors.New("store unavailable")
	}
	return failingStore.StateStore.Scan(ctx, prefix)
}

// TestAgentKeepsInstanceWhenConfigReadFails checks that a store error while
// comparing the config hash does not stop a running instance (M235 review).
func TestAgentKeepsInstanceWhenConfigReadFails(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	failingStore := &failingConfigScanStore{StateStore: memoryStore}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	countingRuntime := &lateFactRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		factStore:        memoryStore,
		lateFactKey:      types.KeyDesiredServiceImage("web"), // same value: changes nothing
		lateFactValue:    "nginx:1.28",
		startCounts:      make(map[string]int),
	}
	nodeAgent := New("node-1", failingStore, countingRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	memoryStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.local"))
	types.WriteInstance(ctx, memoryStore, types.Instance{ID: "blip1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, memoryStore, types.Placement{InstanceID: "blip1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "config hash stored", func() bool {
		_, getError := memoryStore.Get(ctx, types.KeyObservedInstanceConfigHash("blip1"))
		return getError == nil
	})
	failingStore.failScans.Store(true)
	waitFor(t, configHashTestTimeout, "several reconcile cycles with failing config reads", func() bool {
		return failingStore.failedScanCount.Load() >= 10
	})
	observedStatus, statusError := countingRuntime.Status(ctx, "blip1")
	if statusError != nil || !observedStatus.Running {
		t.Fatalf("instance stopped during failing config reads (status %+v, error %v); a read error is not a config change", observedStatus, statusError)
	}
	if starts := countingRuntime.startCount("blip1"); starts != 1 {
		t.Fatalf("instance started %d times; a config read error must not restart it", starts)
	}
}

// TestServiceConfigHashFormatUnchanged pins the bytes the config hash is built
// from: hashes stored by older agents must still match, or an upgrade would
// restart every instance with config (M235 review).
func TestServiceConfigHashFormatUnchanged(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "B_VAR"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "A_VAR"), []byte("1"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigFile("web", "/etc/app.conf"), []byte("port=80"))
	factStore.Put(ctx, types.KeyDesiredServiceSecret("web", "db-password"), []byte("/run/secrets/db"))
	nodeAgent := New("node-1", factStore, runtime.NewSimulatorRuntime())
	legacyInput := "env:A_VAR=1\nenv:B_VAR=2\nfile:/etc/app.conf=port=80\n" +
		"secret:" + types.KeyDesiredServiceSecret("web", "db-password") + "=/run/secrets/db\n"
	legacyDigest := sha256.Sum256([]byte(legacyInput))
	expectedHash := hex.EncodeToString(legacyDigest[:])

	// Without a generation fact (secret older than M236), then at generation 1.
	for _, generationFactValue := range []string{"", "1"} {
		if generationFactValue != "" {
			factStore.Put(ctx, types.KeyDesiredSecretGeneration("db-password"), []byte(generationFactValue))
		}
		actualHash, hashError := nodeAgent.computeServiceConfigHash(ctx, "web")
		if hashError != nil {
			t.Fatalf("hash: %v", hashError)
		}
		if actualHash != expectedHash {
			t.Fatalf("config hash format changed (generation fact %q): got %s, want %s", generationFactValue, actualHash, expectedHash)
		}
	}
}

// TestAgentRestartsWhenFirstConfigArrivesAfterStart checks that the first env
// var of a service started with no config inputs restarts its instance (spec
// instance-config-reload, M235).
func TestAgentRestartsWhenFirstConfigArrivesAfterStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	countingRuntime := &lateFactRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		factStore:        factStore,
		lateFactKey:      types.KeyDesiredServiceImage("web"), // rewrites the image with the same value: no config input
		lateFactValue:    "nginx:1.28",
		startCounts:      make(map[string]int),
	}
	nodeAgent := New("node-1", factStore, countingRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "first1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "first1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "instance running", func() bool {
		observedStatus, statusError := countingRuntime.Status(ctx, "first1")
		return statusError == nil && observedStatus.Running
	})
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.local"))

	waitFor(t, configHashTestTimeout, "instance restarted after the first env var", func() bool {
		return countingRuntime.startCount("first1") >= 2
	})
}

// configChangeRestartCase seeds a service, waits for its instance to run,
// applies changedFacts and expects a restart (M236).
type configChangeRestartCase struct {
	seedFacts    map[string]string // seedFacts are written before the agent starts.
	changedFacts map[string]string // changedFacts are written once the instance runs.
	secretValues map[string][]byte // secretValues are delivered by the fake delivery client.
}

// expectRestartAfterConfigChange runs the agent for one instance of "web",
// writes the changed facts once it runs and waits for a second start.
func expectRestartAfterConfigChange(t *testing.T, restartCase configChangeRestartCase) {
	t.Helper()
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	countingRuntime := &lateFactRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		factStore:        factStore,
		lateFactKey:      types.KeyDesiredServiceImage("web"), // same value: changes nothing
		lateFactValue:    "nginx:1.28",
		startCounts:      make(map[string]int),
	}
	nodeAgent := New("node-1", factStore, countingRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.SetSecretDelivery(&fakeSecretDeliveryClient{secretValues: restartCase.secretValues}, NewSecretMaterialStore(t.TempDir()))

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	for factKey, factValue := range restartCase.seedFacts {
		factStore.Put(ctx, factKey, []byte(factValue))
	}
	types.WriteInstance(ctx, factStore, types.Instance{ID: "change1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "change1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "config hash stored", func() bool {
		_, getError := factStore.Get(ctx, types.KeyObservedInstanceConfigHash("change1"))
		return getError == nil
	})
	for factKey, factValue := range restartCase.changedFacts {
		factStore.Put(ctx, factKey, []byte(factValue))
	}
	waitFor(t, configHashTestTimeout, "instance restarted after the change", func() bool {
		return countingRuntime.startCount("change1") >= 2
	})
}

// TestAgentRestartsWhenConnectionChanges checks that a changed connection
// definition restarts its consumer (spec instance-config-reload, M236).
func TestAgentRestartsWhenConnectionChanges(t *testing.T) {
	expectRestartAfterConfigChange(t, configChangeRestartCase{
		seedFacts: map[string]string{
			types.KeyDesiredServiceConnection("web", "db"): "/run/connections/db",
			types.KeyDesiredConnection("db"):               `{"driver":"postgresql","host":"db-old","port":5432}`,
		},
		changedFacts: map[string]string{
			types.KeyDesiredConnection("db"): `{"driver":"postgresql","host":"db-new","port":5432}`,
		},
	})
}

// TestAgentRestartsWhenPullSecretChanges checks that a changed image pull
// secret reference restarts the instance (spec instance-config-reload, M236).
func TestAgentRestartsWhenPullSecretChanges(t *testing.T) {
	registryCredentials := []byte(`{"username":"bot","password":"token"}`)
	expectRestartAfterConfigChange(t, configChangeRestartCase{
		seedFacts:    map[string]string{types.KeyDesiredServiceImagePullSecret("web"): "registry-a"},
		changedFacts: map[string]string{types.KeyDesiredServiceImagePullSecret("web"): "registry-b"},
		secretValues: map[string][]byte{"registry-a": registryCredentials, "registry-b": registryCredentials},
	})
}

// TestAgentRestartsWhenSecretValueGenerationChanges checks that a raised value
// generation of a granted secret restarts its consumer (spec
// instance-config-reload, M236).
func TestAgentRestartsWhenSecretValueGenerationChanges(t *testing.T) {
	expectRestartAfterConfigChange(t, configChangeRestartCase{
		seedFacts: map[string]string{
			types.KeyDesiredServiceSecret("web", "api-key"): "/run/secrets/api-key",
			types.KeyDesiredSecretGeneration("api-key"):     "1",
		},
		changedFacts: map[string]string{types.KeyDesiredSecretGeneration("api-key"): "2"},
		secretValues: map[string][]byte{"api-key": []byte("value")},
	})
}

// TestAgentRefetchesSecretsWhenRestartingAfterValueChange checks that a placed
// instance that is not running (exited, crash loop, agent restart) and whose
// config hash changed is started with freshly delivered secrets, not the stale
// material on tmpfs (M236 review).
func TestAgentRefetchesSecretsWhenRestartingAfterValueChange(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	materialStore := NewSecretMaterialStore(t.TempDir())
	if writeError := materialStore.WriteInstanceSecrets("stale1", map[string][]byte{"api-key": []byte("old")}); writeError != nil {
		t.Fatalf("seed material: %v", writeError)
	}
	deliveryClient := &fakeSecretDeliveryClient{secretValues: map[string][]byte{"api-key": []byte("new")}}
	nodeAgent := New("node-1", factStore, runtime.NewSimulatorRuntime())
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.SetSecretDelivery(deliveryClient, materialStore)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceSecret("web", "api-key"), []byte("/run/secrets/api-key"))
	factStore.Put(ctx, types.KeyDesiredSecretGeneration("api-key"), []byte("2"))
	// Hash stored at the previous start, before the value changed.
	factStore.Put(ctx, types.KeyObservedInstanceConfigHash("stale1"), []byte("hash-before-value-change"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "stale1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "stale1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "instance started with the new secret value", func() bool {
		deliveryClient.mutex.Lock()
		fetchCount := deliveryClient.fetchCount
		deliveryClient.mutex.Unlock()
		materialValues, hasMaterial := materialStore.ReadInstanceSecrets("stale1")
		storedHash, hashError := factStore.Get(ctx, types.KeyObservedInstanceConfigHash("stale1"))
		return fetchCount >= 1 && hasMaterial && string(materialValues["api-key"]) == "new" &&
			hashError == nil && string(storedHash.Value) != "hash-before-value-change"
	})
}

// TestSecretDeleteKeepsConsumerConfigHash checks that deleting a secret whose
// value was changed does not change its consumers' config hash (the
// generation stays as a tombstone, M236 review).
func TestSecretDeleteKeepsConsumerConfigHash(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	secretStore, storeError := security.NewSecretStoreWithMasterKey(factStore, make([]byte, 32))
	if storeError != nil {
		t.Fatalf("secret store: %v", storeError)
	}
	for _, secretValue := range []string{"first", "second"} {
		if putError := secretStore.PutSecret(ctx, "api-key", []byte(secretValue)); putError != nil {
			t.Fatalf("put: %v", putError)
		}
	}
	factStore.Put(ctx, types.KeyDesiredServiceSecret("web", "api-key"), []byte("/run/secrets/api-key"))
	nodeAgent := New("node-1", factStore, runtime.NewSimulatorRuntime())

	hashBeforeDelete, hashError := nodeAgent.computeServiceConfigHash(ctx, "web")
	if hashError != nil {
		t.Fatalf("hash: %v", hashError)
	}
	if deleteError := secretStore.DeleteSecret(ctx, "api-key"); deleteError != nil {
		t.Fatalf("delete: %v", deleteError)
	}
	hashAfterDelete, hashError := nodeAgent.computeServiceConfigHash(ctx, "web")
	if hashError != nil {
		t.Fatalf("hash: %v", hashError)
	}
	if hashAfterDelete != hashBeforeDelete {
		t.Fatalf("deleting the secret changed the consumer's hash: %s → %s", hashBeforeDelete, hashAfterDelete)
	}
}

// TestAgentRestartsWhenConnectionMountPathChanges checks that moving a
// connection file restarts the instance (M236 review).
func TestAgentRestartsWhenConnectionMountPathChanges(t *testing.T) {
	expectRestartAfterConfigChange(t, configChangeRestartCase{
		seedFacts: map[string]string{
			types.KeyDesiredServiceConnection("web", "db"): "/run/connections/db",
			types.KeyDesiredConnection("db"):               `{"driver":"postgresql","host":"db","port":5432}`,
		},
		changedFacts: map[string]string{types.KeyDesiredServiceConnection("web", "db"): "/etc/app/db"},
	})
}
