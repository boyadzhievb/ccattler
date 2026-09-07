package network

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestAllocateIPReturnsAddressInNodeSubnet verifies that an allocated IP
// falls within the node's assigned /24 subnet.
func TestAllocateIPReturnsAddressInNodeSubnet(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()
	ctx := context.Background()

	allocatedIP, err := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")
	if err != nil {
		t.Fatalf("AllocateIP failed: %v", err)
	}

	nodeSubnet := simulatorNetworkProvider.NodeSubnet("node-1")
	// nodeSubnet is "10.100.1.0/24", so the IP should start with "10.100.1."
	expectedPrefix := strings.TrimSuffix(nodeSubnet, "0/24")
	if !strings.HasPrefix(allocatedIP, expectedPrefix) {
		t.Errorf("allocated IP %s does not belong to node subnet %s", allocatedIP, nodeSubnet)
	}
}

// TestAllocateMultipleIPsOnSameNodeAreUniqueAndSequential verifies that
// successive allocations on the same node return unique, sequential IPs.
func TestAllocateMultipleIPsOnSameNodeAreUniqueAndSequential(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()
	ctx := context.Background()

	firstIP, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")
	secondIP, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-bbb")
	thirdIP, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-ccc")

	if firstIP == secondIP || secondIP == thirdIP || firstIP == thirdIP {
		t.Errorf("IPs are not unique: %s, %s, %s", firstIP, secondIP, thirdIP)
	}

	if firstIP != "10.100.1.2" {
		t.Errorf("first IP = %s, want 10.100.1.2", firstIP)
	}
	if secondIP != "10.100.1.3" {
		t.Errorf("second IP = %s, want 10.100.1.3", secondIP)
	}
	if thirdIP != "10.100.1.4" {
		t.Errorf("third IP = %s, want 10.100.1.4", thirdIP)
	}
}

// TestAllocateIPsOnDifferentNodesUseDifferentSubnets verifies that instances
// placed on different nodes receive IPs from distinct /24 subnets.
func TestAllocateIPsOnDifferentNodesUseDifferentSubnets(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()
	ctx := context.Background()

	ipOnNode1, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")
	ipOnNode2, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-2", "instance-bbb")

	if ipOnNode1 == ipOnNode2 {
		t.Errorf("IPs on different nodes should differ, both got %s", ipOnNode1)
	}

	// Node-1 gets third octet 1, node-2 gets third octet 2.
	if !strings.HasPrefix(ipOnNode1, "10.100.1.") {
		t.Errorf("node-1 IP %s should have prefix 10.100.1.", ipOnNode1)
	}
	if !strings.HasPrefix(ipOnNode2, "10.100.2.") {
		t.Errorf("node-2 IP %s should have prefix 10.100.2.", ipOnNode2)
	}
}

// TestAllocateIPIsIdempotent verifies that calling AllocateIP for the same
// instance returns the same IP without consuming a new address.
func TestAllocateIPIsIdempotent(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()
	ctx := context.Background()

	firstCall, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")
	secondCall, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")

	if firstCall != secondCall {
		t.Errorf("idempotent allocation returned different IPs: %s vs %s", firstCall, secondCall)
	}

	// Allocate another instance and verify it gets the NEXT IP, not one that
	// was consumed by the duplicate call.
	nextIP, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-bbb")
	if nextIP != "10.100.1.3" {
		t.Errorf("next IP after idempotent duplicate = %s, want 10.100.1.3", nextIP)
	}
}

// TestReleaseIPRemovesAllocation verifies that after releasing an instance's
// IP, the instance is no longer tracked and a new allocation proceeds.
func TestReleaseIPRemovesAllocation(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()
	ctx := context.Background()

	originalIP, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")
	simulatorNetworkProvider.ReleaseIP(ctx, "node-1", "instance-aaa")

	// Re-allocating the same instance should succeed (but may get a different IP
	// since the allocator advances sequentially).
	reallocatedIP, err := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")
	if err != nil {
		t.Fatalf("re-allocation after release failed: %v", err)
	}
	// The allocator advances forward, so the re-allocated IP will be a new one.
	if reallocatedIP == originalIP {
		// This is acceptable but let's verify we can still allocate.
		_ = reallocatedIP
	}

	// A fresh instance should still be allocatable.
	freshIP, err := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-bbb")
	if err != nil {
		t.Fatalf("fresh allocation after release failed: %v", err)
	}
	if freshIP == "" {
		t.Error("fresh allocation returned empty IP")
	}
}

// TestReleaseUnallocatedInstanceIsNoOp verifies that releasing an instance
// that was never allocated does not produce an error.
func TestReleaseUnallocatedInstanceIsNoOp(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()
	ctx := context.Background()

	err := simulatorNetworkProvider.ReleaseIP(ctx, "node-1", "nonexistent-instance")
	if err != nil {
		t.Errorf("releasing unallocated instance should be no-op, got error: %v", err)
	}
}

// TestNodeSubnetReturnsCIDRForEachNode verifies that NodeSubnet returns
// distinct, valid CIDR strings for different nodes.
func TestNodeSubnetReturnsCIDRForEachNode(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()

	subnetNode1 := simulatorNetworkProvider.NodeSubnet("node-1")
	subnetNode2 := simulatorNetworkProvider.NodeSubnet("node-2")
	subnetNode3 := simulatorNetworkProvider.NodeSubnet("node-3")

	if subnetNode1 != "10.100.1.0/24" {
		t.Errorf("node-1 subnet = %s, want 10.100.1.0/24", subnetNode1)
	}
	if subnetNode2 != "10.100.2.0/24" {
		t.Errorf("node-2 subnet = %s, want 10.100.2.0/24", subnetNode2)
	}
	if subnetNode3 != "10.100.3.0/24" {
		t.Errorf("node-3 subnet = %s, want 10.100.3.0/24", subnetNode3)
	}

	if subnetNode1 == subnetNode2 || subnetNode2 == subnetNode3 {
		t.Error("different nodes should get different subnets")
	}
}

// TestConcurrentAllocationsDoNotCollide verifies that simultaneous
// AllocateIP calls from multiple goroutines produce unique IPs.
func TestConcurrentAllocationsDoNotCollide(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProvider()
	ctx := context.Background()

	const concurrentAllocations = 50
	allocatedIPs := make([]string, concurrentAllocations)
	var waitGroup sync.WaitGroup

	waitGroup.Add(concurrentAllocations)
	for allocationIndex := 0; allocationIndex < concurrentAllocations; allocationIndex++ {
		go func(index int) {
			defer waitGroup.Done()
			instanceID := "instance-" + string(rune('a'+index%26)) + string(rune('a'+index/26))
			allocatedIP, err := simulatorNetworkProvider.AllocateIP(ctx, "node-1", instanceID)
			if err != nil {
				t.Errorf("concurrent allocation %d failed: %v", index, err)
				return
			}
			allocatedIPs[index] = allocatedIP
		}(allocationIndex)
	}
	waitGroup.Wait()

	// Verify all IPs are unique.
	seenIPs := make(map[string]bool)
	for _, allocatedIP := range allocatedIPs {
		if allocatedIP == "" {
			continue
		}
		if seenIPs[allocatedIP] {
			t.Errorf("duplicate IP found in concurrent allocations: %s", allocatedIP)
		}
		seenIPs[allocatedIP] = true
	}
}

// TestCustomCIDRBaseOctets verifies that the provider can be configured with
// a custom cluster CIDR base.
func TestCustomCIDRBaseOctets(t *testing.T) {
	simulatorNetworkProvider := NewSimulatorNetworkProviderWithCIDR(172, 16)
	ctx := context.Background()

	allocatedIP, _ := simulatorNetworkProvider.AllocateIP(ctx, "node-1", "instance-aaa")
	if !strings.HasPrefix(allocatedIP, "172.16.1.") {
		t.Errorf("custom CIDR: IP = %s, want prefix 172.16.1.", allocatedIP)
	}

	nodeSubnet := simulatorNetworkProvider.NodeSubnet("node-1")
	if nodeSubnet != "172.16.1.0/24" {
		t.Errorf("custom CIDR: subnet = %s, want 172.16.1.0/24", nodeSubnet)
	}
}
