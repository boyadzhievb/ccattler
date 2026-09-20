package network

import (
	"sync"
	"testing"
)

func TestLeastConnectionsSelectsLowestLoad(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()
	candidates := []string{"backend-a:8080", "backend-b:8080", "backend-c:8080"}

	balancer.SelectBackend([]string{"backend-a:8080"})
	balancer.SelectBackend([]string{"backend-a:8080"})
	balancer.SelectBackend([]string{"backend-b:8080"})

	selected := balancer.SelectBackend(candidates)

	if selected != "backend-c:8080" {
		t.Errorf("expected backend-c (0 connections), got %s", selected)
	}
}

func TestLeastConnectionsRelease(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()
	candidates := []string{"backend-a:8080", "backend-b:8080"}

	balancer.SelectBackend([]string{"backend-a:8080"})
	balancer.SelectBackend([]string{"backend-a:8080"})
	balancer.SelectBackend([]string{"backend-b:8080"})

	balancer.ReleaseBackend("backend-a:8080")
	balancer.ReleaseBackend("backend-a:8080")

	selected := balancer.SelectBackend(candidates)
	if selected != "backend-a:8080" {
		t.Errorf("expected backend-a after release (0 connections), got %s", selected)
	}
}

func TestLeastConnectionsEmptyCandidates(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()

	selected := balancer.SelectBackend(nil)
	if selected != "" {
		t.Errorf("expected empty string for nil candidates, got %s", selected)
	}

	selected = balancer.SelectBackend([]string{})
	if selected != "" {
		t.Errorf("expected empty string for empty candidates, got %s", selected)
	}
}

func TestLeastConnectionsActiveCount(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()

	balancer.SelectBackend([]string{"backend-a:8080"})
	balancer.SelectBackend([]string{"backend-a:8080"})
	balancer.SelectBackend([]string{"backend-a:8080"})

	if count := balancer.ActiveConnections("backend-a:8080"); count != 3 {
		t.Errorf("expected 3 active connections, got %d", count)
	}

	balancer.ReleaseBackend("backend-a:8080")

	if count := balancer.ActiveConnections("backend-a:8080"); count != 2 {
		t.Errorf("expected 2 active connections after release, got %d", count)
	}
}

func TestLeastConnectionsUnknownBackendRelease(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()
	balancer.ReleaseBackend("unknown:8080")

	if count := balancer.ActiveConnections("unknown:8080"); count != 0 {
		t.Errorf("expected 0 for unknown backend, got %d", count)
	}
}

func TestLeastConnectionsDistributesUnderLoad(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()
	candidates := []string{"backend-a:8080", "backend-b:8080", "backend-c:8080"}

	selected := make([]string, 0, 6)
	for requestIndex := 0; requestIndex < 6; requestIndex++ {
		result := balancer.SelectBackend(candidates)
		selected = append(selected, result)
	}

	counts := make(map[string]int)
	for _, address := range selected {
		counts[address]++
	}

	for _, address := range candidates {
		if counts[address] != 2 {
			t.Errorf("expected 2 selections for %s under load, got %d", address, counts[address])
		}
	}

	for _, address := range selected {
		balancer.ReleaseBackend(address)
	}
}

func TestLeastConnectionsConcurrentAccess(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()
	candidates := []string{"backend-a:8080", "backend-b:8080"}
	waitGroup := sync.WaitGroup{}

	for goroutineIndex := 0; goroutineIndex < 100; goroutineIndex++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			selected := balancer.SelectBackend(candidates)
			if selected != "" {
				balancer.ReleaseBackend(selected)
			}
		}()
	}

	waitGroup.Wait()

	countA := balancer.ActiveConnections("backend-a:8080")
	countB := balancer.ActiveConnections("backend-b:8080")
	if countA != 0 || countB != 0 {
		t.Errorf("expected 0 active connections after all releases, got A=%d B=%d", countA, countB)
	}
}
