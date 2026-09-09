//go:build etcd_integration

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// testEtcdEndpoint returns the etcd endpoint for integration tests. Defaults to
// localhost:2379 unless the ETCD_ENDPOINT environment variable is set.
func testEtcdEndpoint() string {
	endpoint := os.Getenv("ETCD_ENDPOINT")
	if endpoint == "" {
		return "localhost:2379"
	}
	return endpoint
}

// createTestEtcdStore creates an EtcdStore with a unique key prefix for test isolation.
// Each test gets its own namespace so concurrent tests don't interfere.
func createTestEtcdStore(testName string) (*EtcdStore, error) {
	uniquePrefix := fmt.Sprintf("/ccattler-test/%s/%d/", testName, time.Now().UnixNano())
	etcdStore, connectionError := NewEtcdStore(EtcdStoreConfig{
		Endpoints:   []string{testEtcdEndpoint()},
		KeyPrefix:   uniquePrefix,
		DialTimeout: 5 * time.Second,
	})
	return etcdStore, connectionError
}

// cleanupTestEtcdStore deletes all keys under the test prefix and closes the store.
func cleanupTestEtcdStore(testInstance *testing.T, etcdStore *EtcdStore) {
	testInstance.Helper()
	ctx := context.Background()
	_, deleteError := etcdStore.etcdClient.Delete(ctx, etcdStore.keyPrefix, clientv3.WithPrefix())
	if deleteError != nil {
		testInstance.Logf("cleanup warning: %v", deleteError)
	}
	etcdStore.Close()
}

func TestEtcdPutAndGet(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("PutAndGet")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	revision, putError := etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))
	if putError != nil {
		testInstance.Fatalf("Put failed: %v", putError)
	}
	if revision == 0 {
		testInstance.Fatal("Put returned revision 0")
	}

	retrievedFact, getError := etcdStore.Get(ctx, "/service/web/image")
	if getError != nil {
		testInstance.Fatalf("Get failed: %v", getError)
	}
	if string(retrievedFact.Value) != "nginx:1.28" {
		testInstance.Fatalf("expected value 'nginx:1.28', got '%s'", string(retrievedFact.Value))
	}
	if retrievedFact.Key != "/service/web/image" {
		testInstance.Fatalf("expected key '/service/web/image', got '%s'", retrievedFact.Key)
	}
	if retrievedFact.Revision == 0 {
		testInstance.Fatal("expected non-zero revision")
	}
	if retrievedFact.CreateRevision == 0 {
		testInstance.Fatal("expected non-zero create revision")
	}
}

func TestEtcdGetKeyNotFound(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("GetKeyNotFound")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	_, getError := etcdStore.Get(ctx, "/nonexistent/key")
	if getError != ErrKeyNotFound {
		testInstance.Fatalf("expected ErrKeyNotFound, got %v", getError)
	}
}

func TestEtcdPutOverwrite(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("PutOverwrite")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	firstRevision, _ := etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.27"))

	secondRevision, _ := etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))
	if secondRevision <= firstRevision {
		testInstance.Fatalf("expected second revision %d > first revision %d", secondRevision, firstRevision)
	}

	retrievedFact, _ := etcdStore.Get(ctx, "/service/web/image")
	if string(retrievedFact.Value) != "nginx:1.28" {
		testInstance.Fatalf("expected 'nginx:1.28', got '%s'", string(retrievedFact.Value))
	}
}

func TestEtcdIdempotentPut(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("IdempotentPut")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	firstRevision, _ := etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))

	secondRevision, _ := etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))
	if secondRevision != firstRevision {
		testInstance.Fatalf("idempotent Put should return same revision: first=%d, second=%d", firstRevision, secondRevision)
	}

	storeRevision, _ := etcdStore.Revision(ctx)
	thirdRevision, _ := etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))
	storeRevisionAfter, _ := etcdStore.Revision(ctx)

	if thirdRevision != firstRevision {
		testInstance.Fatalf("idempotent Put returned different revision: %d vs %d", thirdRevision, firstRevision)
	}
	if storeRevisionAfter != storeRevision {
		testInstance.Fatalf("idempotent Put should not bump global revision: before=%d, after=%d", storeRevision, storeRevisionAfter)
	}
}

func TestEtcdDelete(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("Delete")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))

	deleteError := etcdStore.Delete(ctx, "/service/web/image")
	if deleteError != nil {
		testInstance.Fatalf("Delete failed: %v", deleteError)
	}

	_, getError := etcdStore.Get(ctx, "/service/web/image")
	if getError != ErrKeyNotFound {
		testInstance.Fatalf("expected ErrKeyNotFound after delete, got %v", getError)
	}
}

func TestEtcdDeleteKeyNotFound(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("DeleteKeyNotFound")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	deleteError := etcdStore.Delete(ctx, "/nonexistent/key")
	if deleteError != ErrKeyNotFound {
		testInstance.Fatalf("expected ErrKeyNotFound, got %v", deleteError)
	}
}

func TestEtcdScan(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("Scan")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	etcdStore.Put(ctx, "/service/api/image", []byte("myapi:v3"))
	etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))
	etcdStore.Put(ctx, "/service/web/instances", []byte("3"))
	etcdStore.Put(ctx, "/node/node-1/state", []byte("alive"))

	scanResults, scanError := etcdStore.Scan(ctx, "/service/web/")
	if scanError != nil {
		testInstance.Fatalf("Scan failed: %v", scanError)
	}
	if len(scanResults) != 2 {
		testInstance.Fatalf("expected 2 results, got %d", len(scanResults))
	}
	if scanResults[0].Key != "/service/web/image" {
		testInstance.Fatalf("expected first key '/service/web/image', got '%s'", scanResults[0].Key)
	}
	if scanResults[1].Key != "/service/web/instances" {
		testInstance.Fatalf("expected second key '/service/web/instances', got '%s'", scanResults[1].Key)
	}

	allServiceResults, _ := etcdStore.Scan(ctx, "/service/")
	if len(allServiceResults) != 3 {
		testInstance.Fatalf("expected 3 results for /service/ prefix, got %d", len(allServiceResults))
	}
}

func TestEtcdScanEmpty(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("ScanEmpty")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	scanResults, scanError := etcdStore.Scan(ctx, "/nonexistent/")
	if scanError != nil {
		testInstance.Fatalf("Scan failed: %v", scanError)
	}
	if len(scanResults) != 0 {
		testInstance.Fatalf("expected 0 results, got %d", len(scanResults))
	}
}

func TestEtcdRevision(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("Revision")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	revisionBefore, revisionError := etcdStore.Revision(ctx)
	if revisionError != nil {
		testInstance.Fatalf("Revision failed: %v", revisionError)
	}

	etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))

	revisionAfter, _ := etcdStore.Revision(ctx)
	if revisionAfter <= revisionBefore {
		testInstance.Fatalf("revision should increase after Put: before=%d, after=%d", revisionBefore, revisionAfter)
	}
}

func TestEtcdTransactionSuccess(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("TransactionSuccess")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	etcdStore.Put(ctx, "/service/web/instances", []byte("3"))

	existingFact, _ := etcdStore.Get(ctx, "/service/web/instances")

	succeeded, transactionError := etcdStore.Transaction(ctx,
		[]Compare{{Key: "/service/web/instances", Revision: existingFact.Revision}},
		[]Op{{Type: OpPut, Key: "/service/web/instances", Value: []byte("5")}},
		nil,
	)
	if transactionError != nil {
		testInstance.Fatalf("Transaction failed: %v", transactionError)
	}
	if !succeeded {
		testInstance.Fatal("Transaction should have succeeded")
	}

	updatedFact, _ := etcdStore.Get(ctx, "/service/web/instances")
	if string(updatedFact.Value) != "5" {
		testInstance.Fatalf("expected '5', got '%s'", string(updatedFact.Value))
	}
}

func TestEtcdTransactionConflict(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("TransactionConflict")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	etcdStore.Put(ctx, "/service/web/instances", []byte("3"))

	// Use a stale revision to trigger a conflict.
	succeeded, transactionError := etcdStore.Transaction(ctx,
		[]Compare{{Key: "/service/web/instances", Revision: 1}},
		[]Op{{Type: OpPut, Key: "/service/web/instances", Value: []byte("5")}},
		nil,
	)
	if transactionError != nil {
		testInstance.Fatalf("Transaction error: %v", transactionError)
	}
	if succeeded {
		testInstance.Fatal("Transaction should have failed due to revision conflict")
	}

	unchangedFact, _ := etcdStore.Get(ctx, "/service/web/instances")
	if string(unchangedFact.Value) != "3" {
		testInstance.Fatalf("value should be unchanged, got '%s'", string(unchangedFact.Value))
	}
}

func TestEtcdTransactionCreateIfNotExists(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("TransactionCreateIfNotExists")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	succeeded, transactionError := etcdStore.Transaction(ctx,
		[]Compare{{Key: "/placement/instance/abc", Revision: 0}},
		[]Op{{Type: OpPut, Key: "/placement/instance/abc", Value: []byte("node-1")}},
		nil,
	)
	if transactionError != nil {
		testInstance.Fatalf("Transaction error: %v", transactionError)
	}
	if !succeeded {
		testInstance.Fatal("Transaction should have succeeded — key does not exist")
	}

	createdFact, _ := etcdStore.Get(ctx, "/placement/instance/abc")
	if string(createdFact.Value) != "node-1" {
		testInstance.Fatalf("expected 'node-1', got '%s'", string(createdFact.Value))
	}

	// Second attempt should fail — key already exists.
	succeededAgain, _ := etcdStore.Transaction(ctx,
		[]Compare{{Key: "/placement/instance/abc", Revision: 0}},
		[]Op{{Type: OpPut, Key: "/placement/instance/abc", Value: []byte("node-2")}},
		nil,
	)
	if succeededAgain {
		testInstance.Fatal("Transaction should have failed — key already exists")
	}
}

func TestEtcdTransactionWithDelete(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("TransactionWithDelete")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	etcdStore.Put(ctx, "/placement/instance/abc", []byte("node-1"))
	existingFact, _ := etcdStore.Get(ctx, "/placement/instance/abc")

	succeeded, _ := etcdStore.Transaction(ctx,
		[]Compare{{Key: "/placement/instance/abc", Revision: existingFact.Revision}},
		[]Op{{Type: OpDelete, Key: "/placement/instance/abc"}},
		nil,
	)
	if !succeeded {
		testInstance.Fatal("Transaction should have succeeded")
	}

	_, getError := etcdStore.Get(ctx, "/placement/instance/abc")
	if getError != ErrKeyNotFound {
		testInstance.Fatalf("expected ErrKeyNotFound after transactional delete, got %v", getError)
	}
}

func TestEtcdTransactionOnFailureOps(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("TransactionOnFailureOps")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx := context.Background()

	etcdStore.Put(ctx, "/service/web/instances", []byte("3"))

	succeeded, _ := etcdStore.Transaction(ctx,
		[]Compare{{Key: "/service/web/instances", Revision: 1}},
		[]Op{{Type: OpPut, Key: "/service/web/instances", Value: []byte("5")}},
		[]Op{{Type: OpPut, Key: "/service/web/instances", Value: []byte("10")}},
	)
	if succeeded {
		testInstance.Fatal("Transaction should have failed (stale revision)")
	}

	result, _ := etcdStore.Get(ctx, "/service/web/instances")
	if string(result.Value) != "10" {
		testInstance.Fatalf("expected onFailure value '10', got '%s'", string(result.Value))
	}
}

func TestEtcdWatchExactKey(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("WatchExactKey")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx, cancelFunction := context.WithCancel(context.Background())
	defer cancelFunction()

	eventChannel, watchError := etcdStore.Watch(ctx, "/service/web/image", WatchOption{Prefix: false})
	if watchError != nil {
		testInstance.Fatalf("Watch failed: %v", watchError)
	}

	// Small delay to let watch establish.
	time.Sleep(100 * time.Millisecond)

	etcdStore.Put(context.Background(), "/service/web/image", []byte("nginx:1.28"))

	select {
	case receivedEvent := <-eventChannel:
		if receivedEvent.Type != EventPut {
			testInstance.Fatalf("expected EventPut, got %v", receivedEvent.Type)
		}
		if string(receivedEvent.Fact.Value) != "nginx:1.28" {
			testInstance.Fatalf("expected 'nginx:1.28', got '%s'", string(receivedEvent.Fact.Value))
		}
		if receivedEvent.Fact.Key != "/service/web/image" {
			testInstance.Fatalf("expected key '/service/web/image', got '%s'", receivedEvent.Fact.Key)
		}
	case <-time.After(5 * time.Second):
		testInstance.Fatal("timed out waiting for watch event")
	}
}

func TestEtcdWatchPrefix(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("WatchPrefix")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx, cancelFunction := context.WithCancel(context.Background())
	defer cancelFunction()

	eventChannel, watchError := etcdStore.Watch(ctx, "/service/web/", WatchOption{Prefix: true})
	if watchError != nil {
		testInstance.Fatalf("Watch failed: %v", watchError)
	}

	time.Sleep(100 * time.Millisecond)

	etcdStore.Put(context.Background(), "/service/web/image", []byte("nginx:1.28"))
	etcdStore.Put(context.Background(), "/service/web/instances", []byte("3"))

	receivedEvents := 0
	timeout := time.After(5 * time.Second)
	for receivedEvents < 2 {
		select {
		case event := <-eventChannel:
			if event.Type != EventPut {
				testInstance.Fatalf("expected EventPut, got %v", event.Type)
			}
			receivedEvents++
		case <-timeout:
			testInstance.Fatalf("timed out after receiving %d events, expected 2", receivedEvents)
		}
	}
}

func TestEtcdWatchDelete(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("WatchDelete")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx, cancelFunction := context.WithCancel(context.Background())
	defer cancelFunction()

	etcdStore.Put(context.Background(), "/service/web/image", []byte("nginx:1.28"))

	eventChannel, watchError := etcdStore.Watch(ctx, "/service/web/image", WatchOption{Prefix: false})
	if watchError != nil {
		testInstance.Fatalf("Watch failed: %v", watchError)
	}

	time.Sleep(100 * time.Millisecond)

	etcdStore.Delete(context.Background(), "/service/web/image")

	select {
	case receivedEvent := <-eventChannel:
		if receivedEvent.Type != EventDelete {
			testInstance.Fatalf("expected EventDelete, got %v", receivedEvent.Type)
		}
	case <-time.After(5 * time.Second):
		testInstance.Fatal("timed out waiting for delete event")
	}
}

func TestEtcdWatchContextCancellation(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("WatchContextCancellation")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}
	defer cleanupTestEtcdStore(testInstance, etcdStore)

	ctx, cancelFunction := context.WithCancel(context.Background())

	eventChannel, watchError := etcdStore.Watch(ctx, "/service/", WatchOption{Prefix: true})
	if watchError != nil {
		testInstance.Fatalf("Watch failed: %v", watchError)
	}

	cancelFunction()

	// The channel should close after context cancellation.
	select {
	case _, channelOpen := <-eventChannel:
		if channelOpen {
			// We might get a residual event, drain it.
			select {
			case _, stillOpen := <-eventChannel:
				if stillOpen {
					testInstance.Fatal("expected channel to close after context cancellation")
				}
			case <-time.After(3 * time.Second):
				testInstance.Fatal("timed out waiting for channel close")
			}
		}
	case <-time.After(3 * time.Second):
		testInstance.Fatal("timed out waiting for channel close after context cancellation")
	}
}

func TestEtcdOperationsAfterClose(testInstance *testing.T) {
	etcdStore, connectionError := createTestEtcdStore("OperationsAfterClose")
	if connectionError != nil {
		testInstance.Fatalf("failed to create etcd store: %v", connectionError)
	}

	ctx := context.Background()
	etcdStore.Put(ctx, "/service/web/image", []byte("nginx:1.28"))

	_, deleteError := etcdStore.etcdClient.Delete(ctx, etcdStore.keyPrefix, clientv3.WithPrefix())
	if deleteError != nil {
		testInstance.Logf("cleanup warning: %v", deleteError)
	}

	etcdStore.Close()

	_, getError := etcdStore.Get(ctx, "/service/web/image")
	if getError != ErrStoreClosed {
		testInstance.Fatalf("expected ErrStoreClosed from Get, got %v", getError)
	}

	_, putError := etcdStore.Put(ctx, "/key", []byte("value"))
	if putError != ErrStoreClosed {
		testInstance.Fatalf("expected ErrStoreClosed from Put, got %v", putError)
	}

	deleteErr := etcdStore.Delete(ctx, "/key")
	if deleteErr != ErrStoreClosed {
		testInstance.Fatalf("expected ErrStoreClosed from Delete, got %v", deleteErr)
	}

	_, scanError := etcdStore.Scan(ctx, "/")
	if scanError != ErrStoreClosed {
		testInstance.Fatalf("expected ErrStoreClosed from Scan, got %v", scanError)
	}

	_, watchError := etcdStore.Watch(ctx, "/", WatchOption{})
	if watchError != ErrStoreClosed {
		testInstance.Fatalf("expected ErrStoreClosed from Watch, got %v", watchError)
	}

	_, txnError := etcdStore.Transaction(ctx, nil, nil, nil)
	if txnError != ErrStoreClosed {
		testInstance.Fatalf("expected ErrStoreClosed from Transaction, got %v", txnError)
	}

	_, revError := etcdStore.Revision(ctx)
	if revError != ErrStoreClosed {
		testInstance.Fatalf("expected ErrStoreClosed from Revision, got %v", revError)
	}
}

func TestEtcdKeyPrefixIsolation(testInstance *testing.T) {
	storeA, errorA := createTestEtcdStore("PrefixIsolation-A")
	if errorA != nil {
		testInstance.Fatalf("failed to create store A: %v", errorA)
	}
	defer cleanupTestEtcdStore(testInstance, storeA)

	storeB, errorB := createTestEtcdStore("PrefixIsolation-B")
	if errorB != nil {
		testInstance.Fatalf("failed to create store B: %v", errorB)
	}
	defer cleanupTestEtcdStore(testInstance, storeB)

	ctx := context.Background()

	storeA.Put(ctx, "/service/web/image", []byte("nginx:1.27"))
	storeB.Put(ctx, "/service/web/image", []byte("nginx:1.28"))

	factA, _ := storeA.Get(ctx, "/service/web/image")
	factB, _ := storeB.Get(ctx, "/service/web/image")

	if string(factA.Value) != "nginx:1.27" {
		testInstance.Fatalf("store A should have 'nginx:1.27', got '%s'", string(factA.Value))
	}
	if string(factB.Value) != "nginx:1.28" {
		testInstance.Fatalf("store B should have 'nginx:1.28', got '%s'", string(factB.Value))
	}
}
