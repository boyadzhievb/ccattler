package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// storageCluster holds all components of a 3-node simulated cluster with
// storage support. The caller must defer cancel() and factStore.Close().
type storageCluster struct {
	factStore       store.StateStore
	cancel          context.CancelFunc
	storageProvider *storage.SimulatorStorageProvider
	killNode        map[string]context.CancelFunc
}

// helperSetupStorageCluster creates a 3-node simulated cluster with a
// SimulatorStorageProvider, StorageController, and all standard controllers.
// Each agent has a fast reconciliation interval and the node failure
// controller has a short lease timeout for quick failure detection.
func helperSetupStorageCluster(t *testing.T) *storageCluster {
	t.Helper()

	factStore := store.NewMemoryStore()

	ctx, cancel := context.WithCancel(context.Background())

	simulatorStorageProvider := storage.NewSimulatorStorageProvider()

	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 300 * time.Millisecond
	storageController := controllers.NewStorageController()

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, storageController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	killFunctions := make(map[string]context.CancelFunc)
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		nodeContext, nodeCancel := context.WithCancel(ctx)
		killFunctions[nodeID] = nodeCancel

		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetStorageProvider(simulatorStorageProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(nodeContext)
	}

	return &storageCluster{
		factStore:       factStore,
		cancel:          cancel,
		storageProvider: simulatorStorageProvider,
		killNode:        killFunctions,
	}
}

// TestVolumeCreatedFromDesiredState verifies that writing desired volume facts
// causes the StorageController to create observed volume facts with
// state=available.
func TestVolumeCreatedFromDesiredState(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "100Gi", true)

	waitFor(t, 5*time.Second, "observed volume pgdata with state=available", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAvailable
	})

	volume, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	if volume.Size != "100Gi" {
		t.Errorf("volume size = %s, want 100Gi", volume.Size)
	}
}

// TestVolumeAttachedWhenInstanceStarts verifies that when a service with a
// volume mount is deployed, the agent attaches the volume before starting
// the instance, and the observed volume facts reflect the attachment.
func TestVolumeAttachedWhenInstanceStarts(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "100Gi", true)
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	waitFor(t, 5*time.Second, "volume pgdata attached", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAttached && volume.Node != ""
	})

	volume, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	if volume.MountPath != "/mnt/volumes/pgdata" {
		t.Errorf("volume mount path = %s, want /mnt/volumes/pgdata", volume.MountPath)
	}
	if volume.Instance == "" {
		t.Error("expected volume to be associated with an instance")
	}
}

// TestVolumeDetachedWhenInstanceStops verifies that scaling a service to zero
// causes the agent to detach the volume, setting its state back to available.
func TestVolumeDetachedWhenInstanceStops(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "100Gi", true)
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	waitFor(t, 5*time.Second, "volume attached", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAttached
	})

	// Scale to zero — triggers instance stop and volume detach.
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("0"))

	waitFor(t, 5*time.Second, "volume detached after scale-to-zero", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAvailable
	})
}

// TestVolumeSurvivesNodeFailure is the M5 headline test. It deploys postgres
// with a persistent volume on one node, kills that node, and verifies the
// volume migrates to a replacement instance on a surviving node.
func TestVolumeSurvivesNodeFailure(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 50*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "50Gi", true)
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	// Wait for postgres to be running with volume attached.
	waitFor(t, 5*time.Second, "postgres running with volume attached", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		if err != nil || volume.State != types.VolumeAttached || volume.Node == "" {
			return false
		}
		allInstances, _ := types.ListInstances(ctx, cluster.factStore)
		for _, instance := range allInstances {
			if instance.Service == "postgres" && instance.State == types.InstanceRunning {
				return true
			}
		}
		return false
	})

	// Record which node has postgres.
	volumeBefore, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	originalNodeID := volumeBefore.Node

	// Kill the node running postgres.
	cluster.killNode[originalNodeID]()
	cluster.storageProvider.ForceDetach(ctx, "pgdata")

	// Wait for the volume to reattach on a different node.
	waitFor(t, 10*time.Second, "volume reattached on different node", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAttached && volume.Node != "" && volume.Node != originalNodeID
	})

	// Verify postgres is running again on the new node.
	waitFor(t, 10*time.Second, "postgres running on new node", func() bool {
		allInstances, _ := types.ListInstances(ctx, cluster.factStore)
		for _, instance := range allInstances {
			if instance.Service == "postgres" && instance.State == types.InstanceRunning {
				placementFact, err := cluster.factStore.Get(ctx, types.KeyPlacementInstance(instance.ID))
				if err == nil && string(placementFact.Value) != originalNodeID {
					return true
				}
			}
		}
		return false
	})

	volumeAfter, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	if volumeAfter.Node == originalNodeID {
		t.Errorf("volume still on dead node %s", originalNodeID)
	}
	if volumeAfter.MountPath != "/mnt/volumes/pgdata" {
		t.Errorf("mount path after migration = %s, want /mnt/volumes/pgdata", volumeAfter.MountPath)
	}
}

// TestVolumeForceDetachOnUnreachableNode verifies that the StorageController
// force-detaches a volume from a node that becomes unreachable, setting the
// volume state to available.
func TestVolumeForceDetachOnUnreachableNode(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "100Gi", true)
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	waitFor(t, 5*time.Second, "volume attached", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAttached && volume.Node != ""
	})

	volumeBefore, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	attachedNodeID := volumeBefore.Node

	// Kill the node — it stops heartbeating, lease expires, node marked unreachable.
	cluster.killNode[attachedNodeID]()

	// Wait for StorageController to force-detach.
	waitFor(t, 5*time.Second, "volume force-detached to available", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAvailable
	})
}

// TestMultipleServicesIndependentVolumes verifies that two services with
// different volumes have independent volume lifecycles.
func TestMultipleServicesIndependentVolumes(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)
	cluster.storageProvider.CreateVolume(ctx, "redis-data", 10*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "100Gi", true)
	types.WriteDesiredVolume(ctx, cluster.factStore, "redis-data", "10Gi", true)

	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("redis"), []byte("redis:7"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("redis", "redis-data"), []byte("/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("redis"), []byte("1"))

	waitFor(t, 5*time.Second, "both volumes attached", func() bool {
		pgVolume, pgErr := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		redisVolume, redisErr := types.ReadObservedVolume(ctx, cluster.factStore, "redis-data")
		return pgErr == nil && redisErr == nil &&
			pgVolume.State == types.VolumeAttached &&
			redisVolume.State == types.VolumeAttached
	})

	pgVolume, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	redisVolume, _ := types.ReadObservedVolume(ctx, cluster.factStore, "redis-data")

	if pgVolume.Instance == redisVolume.Instance {
		t.Error("volumes should be attached to different instances")
	}
}

// TestServiceWithoutVolumeUnaffected verifies that services without volume
// mounts work normally alongside services that use volumes.
func TestServiceWithoutVolumeUnaffected(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "100Gi", true)
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	// Web service has no volume.
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	waitFor(t, 5*time.Second, "4 total running instances", func() bool {
		allInstances, _ := types.ListInstances(ctx, cluster.factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount >= 4
	})

	allInstances, _ := types.ListInstances(ctx, cluster.factStore)
	runningByService := make(map[string]int)
	for _, instance := range allInstances {
		if instance.State == types.InstanceRunning {
			runningByService[instance.Service]++
		}
	}
	if runningByService["web"] < 3 {
		t.Errorf("web: expected 3 running, got %d", runningByService["web"])
	}
	if runningByService["postgres"] < 1 {
		t.Errorf("postgres: expected 1 running, got %d", runningByService["postgres"])
	}
}

// TestVolumeStateReflectedInStore verifies that all expected observed volume
// facts are present and correct after a volume is attached.
func TestVolumeStateReflectedInStore(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "100Gi", true)
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	waitFor(t, 5*time.Second, "volume attached with all facts", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAttached &&
			volume.Node != "" && volume.Instance != "" && volume.MountPath != ""
	})

	volume, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")

	// Verify each individual fact key exists and has the correct value.
	stateFact, err := cluster.factStore.Get(ctx, types.KeyObservedVolumeState("pgdata"))
	if err != nil || string(stateFact.Value) != string(types.VolumeAttached) {
		t.Errorf("volume state fact: got %v (err=%v), want attached", string(stateFact.Value), err)
	}

	nodeFact, err := cluster.factStore.Get(ctx, types.KeyObservedVolumeNode("pgdata"))
	if err != nil || string(nodeFact.Value) != volume.Node {
		t.Errorf("volume node fact: got %v, want %s", string(nodeFact.Value), volume.Node)
	}

	instanceFact, err := cluster.factStore.Get(ctx, types.KeyObservedVolumeInstance("pgdata"))
	if err != nil || string(instanceFact.Value) == "" {
		t.Error("volume instance fact missing or empty")
	}

	mountPathFact, err := cluster.factStore.Get(ctx, types.KeyObservedVolumeMountPath("pgdata"))
	if err != nil || string(mountPathFact.Value) != "/mnt/volumes/pgdata" {
		t.Errorf("volume mount_path fact: got %v, want /mnt/volumes/pgdata", string(mountPathFact.Value))
	}

	sizeFact, err := cluster.factStore.Get(ctx, types.KeyObservedVolumeSize("pgdata"))
	if err != nil || string(sizeFact.Value) != "100Gi" {
		t.Errorf("volume size fact: got %v, want 100Gi", string(sizeFact.Value))
	}
}

// TestVolumeReattachAfterForceDetach verifies that after a volume is
// force-detached from an unreachable node, a new instance on a surviving
// node successfully attaches it.
func TestVolumeReattachAfterForceDetach(t *testing.T) {
	cluster := helperSetupStorageCluster(t)
	defer cluster.cancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.storageProvider.CreateVolume(ctx, "pgdata", 50*1024*1024*1024)

	types.WriteDesiredVolume(ctx, cluster.factStore, "pgdata", "50Gi", true)
	cluster.factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	cluster.factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	waitFor(t, 5*time.Second, "postgres running with volume", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAttached && volume.Node != ""
	})

	volumeBefore, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	originalNodeID := volumeBefore.Node

	// Kill the node and force-detach in the provider.
	cluster.killNode[originalNodeID]()
	cluster.storageProvider.ForceDetach(ctx, "pgdata")

	// Wait for the volume to be force-detached by the StorageController.
	waitFor(t, 5*time.Second, "volume available after force-detach", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAvailable
	})

	// Wait for the volume to be reattached on a different node.
	waitFor(t, 10*time.Second, "volume reattached on surviving node", func() bool {
		volume, err := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
		return err == nil && volume.State == types.VolumeAttached && volume.Node != originalNodeID
	})

	volumeAfter, _ := types.ReadObservedVolume(ctx, cluster.factStore, "pgdata")
	attached, attachedNode, _ := cluster.storageProvider.IsAttached(ctx, "pgdata")
	if !attached {
		t.Error("volume not attached in storage provider after reattach")
	}
	if attachedNode != volumeAfter.Node {
		t.Errorf("provider says attached to %s, store says %s", attachedNode, volumeAfter.Node)
	}
}
