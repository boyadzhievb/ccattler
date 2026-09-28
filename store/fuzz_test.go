package store

import (
	"bytes"
	"context"
	"testing"
)

// FuzzStoreTransaction exercises the store's transactional compare-and-swap mechanism
// with arbitrary keys, values, and revision numbers. It verifies that the store never
// panics and that successful transactions are reflected in subsequent reads while
// failed transactions leave the store unchanged.
func FuzzStoreTransaction(f *testing.F) {
	f.Add("/service/web", []byte("nginx:1.27"), int64(0))
	f.Add("/service/web", []byte("nginx:1.28"), int64(1))
	f.Add("", []byte("empty-key"), int64(0))
	f.Add("/missing", []byte("value"), int64(99))
	f.Add("/service/db", []byte{}, int64(0))

	f.Fuzz(func(t *testing.T, fuzzKey string, fuzzValue []byte, fuzzRevision int64) {
		backgroundContext := context.Background()
		memoryStore := NewMemoryStore()
		defer memoryStore.Close()

		// If the fuzz revision is positive, pre-populate the key so there is a
		// real revision to compare against. This lets the fuzzer explore both
		// the "key exists" and "key does not exist" branches of Transaction.
		var actualRevision int64
		var originalValue []byte
		if fuzzRevision > 0 {
			putRevision, putError := memoryStore.Put(backgroundContext, fuzzKey, []byte("seed-value"))
			if putError != nil {
				// Some keys (e.g. empty string) may legitimately fail; skip them.
				return
			}
			actualRevision = putRevision
			originalValue = []byte("seed-value")
		}

		// Pick the revision to compare: use the real one when the key was
		// pre-populated, otherwise use the raw fuzz revision (which may or may
		// not match reality, exercising the failure path).
		comparisonRevision := fuzzRevision
		if fuzzRevision > 0 {
			comparisonRevision = actualRevision
		}

		transactionSucceeded, transactionError := memoryStore.Transaction(
			backgroundContext,
			[]Compare{{Key: fuzzKey, Revision: comparisonRevision}},
			[]Op{{Type: OpPut, Key: fuzzKey, Value: fuzzValue}},
			nil,
		)
		if transactionError != nil {
			// Transaction errors (e.g. store closed) are acceptable; just
			// verify no panic occurred.
			return
		}

		retrievedFact, getError := memoryStore.Get(backgroundContext, fuzzKey)

		if transactionSucceeded {
			// The compare matched, so the success branch executed: the key
			// must now hold the new fuzz value.
			if getError != nil {
				// A put with an empty value still creates the key, so Get
				// should succeed unless the key itself is problematic.
				if getError == ErrKeyNotFound && len(fuzzValue) == 0 && originalValue != nil && bytes.Equal(originalValue, fuzzValue) {
					// No-op put (same value) is acceptable.
					return
				}
				t.Fatalf("transaction succeeded but Get returned error: %v", getError)
			}
			if !bytes.Equal(retrievedFact.Value, fuzzValue) {
				t.Fatalf("transaction succeeded but value mismatch: got %q, want %q",
					retrievedFact.Value, fuzzValue)
			}
		} else {
			// The compare did not match, so the store should be unchanged.
			if fuzzRevision > 0 {
				// Key was pre-populated; it should still hold the original value.
				if getError != nil {
					t.Fatalf("transaction failed but original key disappeared: %v", getError)
				}
				if !bytes.Equal(retrievedFact.Value, originalValue) {
					t.Fatalf("transaction failed but value changed: got %q, want %q",
						retrievedFact.Value, originalValue)
				}
			} else if getError != ErrKeyNotFound {
				// Key was never created; it should still not exist.
				t.Fatalf("transaction failed but key appeared unexpectedly: err=%v", getError)
			}
		}
	})
}

// FuzzStorePutGet exercises the basic Put/Get round-trip with arbitrary keys and
// values. It verifies that every successfully stored value can be retrieved
// unchanged, and that the store never panics regardless of input.
func FuzzStorePutGet(f *testing.F) {
	f.Add("/service/web", []byte("nginx:1.27"))
	f.Add("/service/db", []byte("postgres:16"))
	f.Add("", []byte("empty-key-value"))
	f.Add("/a", []byte{})
	f.Add("/deep/nested/path/key", []byte("deep-value"))

	f.Fuzz(func(t *testing.T, fuzzKey string, fuzzValue []byte) {
		backgroundContext := context.Background()
		memoryStore := NewMemoryStore()
		defer memoryStore.Close()

		putRevision, putError := memoryStore.Put(backgroundContext, fuzzKey, fuzzValue)
		if putError != nil {
			// Some keys may legitimately be rejected; just ensure no panic.
			return
		}
		if putRevision < 1 {
			t.Fatalf("Put returned invalid revision %d for key %q", putRevision, fuzzKey)
		}

		retrievedFact, getError := memoryStore.Get(backgroundContext, fuzzKey)
		if getError != nil {
			t.Fatalf("Get failed after successful Put for key %q: %v", fuzzKey, getError)
		}
		if !bytes.Equal(retrievedFact.Value, fuzzValue) {
			t.Fatalf("value mismatch for key %q: got %q, want %q",
				fuzzKey, retrievedFact.Value, fuzzValue)
		}
		if retrievedFact.Revision != putRevision {
			t.Fatalf("revision mismatch for key %q: got %d, want %d",
				fuzzKey, retrievedFact.Revision, putRevision)
		}
	})
}
