package store

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkPut(b *testing.B) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()
	benchContext := context.Background()

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		memoryStore.Put(benchContext, fmt.Sprintf("/bench/key/%d", iteration), []byte("value"))
	}
}

func BenchmarkGet(b *testing.B) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()
	benchContext := context.Background()

	for index := 0; index < 1000; index++ {
		memoryStore.Put(benchContext, fmt.Sprintf("/bench/key/%d", index), []byte("value"))
	}

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		memoryStore.Get(benchContext, fmt.Sprintf("/bench/key/%d", iteration%1000))
	}
}

func BenchmarkScan(b *testing.B) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()
	benchContext := context.Background()

	for index := 0; index < 1000; index++ {
		memoryStore.Put(benchContext, fmt.Sprintf("/bench/service/%d", index), []byte("value"))
	}

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		memoryStore.Scan(benchContext, "/bench/service/")
	}
}

func BenchmarkTransaction(b *testing.B) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()
	benchContext := context.Background()

	memoryStore.Put(benchContext, "/bench/txn/key", []byte("initial"))
	initialFact, _ := memoryStore.Get(benchContext, "/bench/txn/key")

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		memoryStore.Transaction(benchContext,
			[]Compare{{Key: "/bench/txn/key", Revision: initialFact.Revision}},
			[]Op{{Type: OpPut, Key: "/bench/txn/key", Value: []byte(fmt.Sprintf("v%d", iteration))}},
			nil,
		)
		initialFact, _ = memoryStore.Get(benchContext, "/bench/txn/key")
	}
}

func BenchmarkPutParallel(b *testing.B) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()
	benchContext := context.Background()

	b.RunParallel(func(parallelBench *testing.PB) {
		counter := 0
		for parallelBench.Next() {
			memoryStore.Put(benchContext, fmt.Sprintf("/bench/parallel/%d", counter), []byte("value"))
			counter++
		}
	})
}
