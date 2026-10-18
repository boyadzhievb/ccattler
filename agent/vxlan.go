// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"os/exec"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// vxlanInterfaceName is the name of the VXLAN tunnel interface created on each node.
const vxlanInterfaceName = "cca-vxlan0"

// vxlanVNI is the VXLAN Network Identifier shared by all nodes in the cluster.
const vxlanVNI = 42

// vxlanDestinationPort is the UDP port used for VXLAN encapsulated traffic.
const vxlanDestinationPort = 4789

// vxlanBridgeMTU is the MTU for the VXLAN interface, reduced from the standard
// 1500 to accommodate VXLAN encapsulation overhead (50 bytes for outer headers).
const vxlanBridgeMTU = 1450

// maxSubnetAllocationAttempts limits the number of CAS retries when allocating
// a new subnet to avoid infinite loops under high contention.
const maxSubnetAllocationAttempts = 10

// subnetAllocationRangeStart is the first third-octet value available for
// per-node subnet allocation within the cluster CIDR.
const subnetAllocationRangeStart = 1

// subnetAllocationRangeEnd is the last third-octet value (exclusive) for
// per-node subnet allocation. Values 1..254 give 254 possible /24 subnets.
const subnetAllocationRangeEnd = 255

// VXLANReconciler manages a VXLAN tunnel mesh between nodes. It creates a
// VXLAN interface on the host, programs routes and FDB entries for remote
// node subnets, and publishes this node's subnet to the fact store.
type VXLANReconciler struct {
	nodeID           string           // nodeID is the unique identifier for the node this reconciler manages.
	factStore        store.StateStore // factStore is the fact store for reading peer node info and writing subnet facts.
	advertiseAddress string           // advertiseAddress is this node's LAN-routable IP used as the VXLAN tunnel endpoint.
	clusterCIDR      string           // clusterCIDR is the cluster-wide CIDR from which per-node /24 subnets are carved.
	initialized      bool             // initialized tracks whether the VXLAN interface has been created on this host.
}

// NewVXLANReconciler creates a VXLANReconciler for the given node, wired to the
// provided fact store. The advertiseAddress is used as the local VXLAN tunnel
// endpoint. The clusterCIDR defines the IP range for subnet allocation.
func NewVXLANReconciler(nodeID string, factStore store.StateStore, advertiseAddress string, clusterCIDR string) *VXLANReconciler {
	return &VXLANReconciler{
		nodeID:           nodeID,
		factStore:        factStore,
		advertiseAddress: advertiseAddress,
		clusterCIDR:      clusterCIDR,
	}
}

// Reconcile performs a single VXLAN reconciliation pass. It ensures the VXLAN
// interface exists, discovers peer nodes from the fact store, programs routes
// and FDB entries for each remote node's subnet, and removes stale routes.
func (vxlanReconciler *VXLANReconciler) Reconcile(ctx context.Context) {
	if vxlanReconciler.advertiseAddress == "" {
		return
	}

	if !vxlanReconciler.initialized {
		if initError := vxlanReconciler.ensureVXLANInterfaceExists(ctx); initError != nil {
			logging.Default().Error("vxlan: failed to create interface",
				"node", vxlanReconciler.nodeID, "error", initError.Error())
			return
		}
		vxlanReconciler.initialized = true
	}

	peerNodes, discoverError := vxlanReconciler.discoverPeerNodes(ctx)
	if discoverError != nil {
		logging.Default().Error("vxlan: failed to discover peer nodes",
			"node", vxlanReconciler.nodeID, "error", discoverError.Error())
		return
	}

	vxlanReconciler.programPeerRoutes(ctx, peerNodes)
	vxlanReconciler.removeStaleRoutes(ctx, peerNodes)
}

// AllocatedSubnet returns this node's /24 subnet CIDR from the fact store.
// Returns an empty string if no subnet has been allocated yet.
func (vxlanReconciler *VXLANReconciler) AllocatedSubnet(ctx context.Context) string {
	subnetKey := types.KeyNetworkNodeSubnet(vxlanReconciler.nodeID)
	subnetFact, getError := vxlanReconciler.factStore.Get(ctx, subnetKey)
	if getError != nil || len(subnetFact.Value) == 0 {
		return ""
	}
	return string(subnetFact.Value)
}

// Cleanup removes the VXLAN interface from the host. Called during agent
// shutdown to clean up network resources.
func (vxlanReconciler *VXLANReconciler) Cleanup(ctx context.Context) {
	if !vxlanReconciler.initialized {
		return
	}
	if deleteError := executeIPLinkDelete(ctx, vxlanInterfaceName); deleteError != nil {
		logging.Default().Error("vxlan: failed to delete interface on cleanup",
			"node", vxlanReconciler.nodeID, "error", deleteError.Error())
	}
	vxlanReconciler.initialized = false
}

// EnsureSubnetPublished allocates a /24 subnet for this node within the cluster
// CIDR and writes it to the fact store. Uses a CAS transaction to avoid races
// when multiple agents start simultaneously.
func (vxlanReconciler *VXLANReconciler) EnsureSubnetPublished(ctx context.Context) {
	subnetKey := types.KeyNetworkNodeSubnet(vxlanReconciler.nodeID)
	existingSubnet, getError := vxlanReconciler.factStore.Get(ctx, subnetKey)
	if getError == nil && len(existingSubnet.Value) > 0 {
		logging.Default().Info("vxlan: subnet already published",
			"node", vxlanReconciler.nodeID, "subnet", string(existingSubnet.Value))
		return
	}

	allocatedSubnet, allocateError := vxlanReconciler.allocateNextFreeSubnet(ctx)
	if allocateError != nil {
		logging.Default().Error("vxlan: failed to allocate subnet",
			"node", vxlanReconciler.nodeID, "error", allocateError.Error())
		return
	}

	logging.Default().Info("vxlan: subnet published",
		"node", vxlanReconciler.nodeID, "subnet", allocatedSubnet)
}

// peerNodeInfo holds the address and subnet of a remote node for VXLAN routing.
type peerNodeInfo struct {
	nodeID           string // nodeID is the unique identifier of the peer node.
	advertiseAddress string // advertiseAddress is the peer's LAN-routable IP (VXLAN tunnel endpoint).
	subnetCIDR       string // subnetCIDR is the peer's container subnet in CIDR notation (e.g. "10.100.3.0/24").
}

// ensureVXLANInterfaceExists creates the VXLAN interface if it does not already
// exist, sets it up, configures the MTU, and assigns the node's overlay IP.
func (vxlanReconciler *VXLANReconciler) ensureVXLANInterfaceExists(ctx context.Context) error {
	if interfaceAlreadyExists(ctx, vxlanInterfaceName) {
		return nil
	}

	physicalInterface, detectError := detectPhysicalInterfaceForAddress(vxlanReconciler.advertiseAddress)
	if detectError != nil {
		return fmt.Errorf("detecting physical interface for %s: %w", vxlanReconciler.advertiseAddress, detectError)
	}

	if createError := executeVXLANCreate(ctx, physicalInterface, vxlanReconciler.advertiseAddress); createError != nil {
		return fmt.Errorf("creating VXLAN interface: %w", createError)
	}

	derivedMAC := deriveVXLANMACAddress(vxlanReconciler.nodeID)
	if macError := executeIPLinkSetAddress(ctx, vxlanInterfaceName, derivedMAC); macError != nil {
		logging.Default().Warn("vxlan: failed to set VTEP MAC address",
			"node", vxlanReconciler.nodeID, "mac", derivedMAC, "error", macError.Error())
	} else {
		logging.Default().Info("vxlan: VTEP MAC derived from node ID",
			"node", vxlanReconciler.nodeID, "mac", derivedMAC)
	}

	if setUpError := executeIPLinkSetUp(ctx, vxlanInterfaceName); setUpError != nil {
		return fmt.Errorf("setting VXLAN interface up: %w", setUpError)
	}

	if mtuError := executeIPLinkSetMTU(ctx, vxlanInterfaceName, vxlanBridgeMTU); mtuError != nil {
		return fmt.Errorf("setting VXLAN MTU: %w", mtuError)
	}

	// No IP assigned to the VXLAN device — it acts as a pure tunnel backend.
	// The container bridge (cca-net) owns the node's /24 subnet and serves
	// as the gateway (.1). Cross-node routes use "dev cca-vxlan0" and FDB
	// entries handle underlay encapsulation (Flannel model).

	logging.Default().Info("vxlan: interface created",
		"node", vxlanReconciler.nodeID, "interface", vxlanInterfaceName,
		"mac", derivedMAC, "vni", fmt.Sprintf("%d", vxlanVNI),
		"mtu", fmt.Sprintf("%d", vxlanBridgeMTU))
	return nil
}

// discoverPeerNodes reads observed node addresses and network subnet facts
// from the store to build a list of all peer nodes with their VXLAN tunnel
// endpoints and container subnets. The local node is excluded from the result.
func (vxlanReconciler *VXLANReconciler) discoverPeerNodes(ctx context.Context) ([]peerNodeInfo, error) {
	addressFacts, addressScanError := vxlanReconciler.factStore.Scan(ctx, types.ScanObservedNodes)
	if addressScanError != nil {
		return nil, fmt.Errorf("scanning observed nodes: %w", addressScanError)
	}

	nodeAddressMap := buildNodeAddressMap(addressFacts)

	subnetFacts, subnetScanError := vxlanReconciler.factStore.Scan(ctx, types.ScanNetworkNodeSubnets)
	if subnetScanError != nil {
		return nil, fmt.Errorf("scanning network subnets: %w", subnetScanError)
	}

	nodeSubnetMap := buildNodeSubnetMap(subnetFacts)

	var peerNodes []peerNodeInfo
	for peerNodeID, peerAddress := range nodeAddressMap {
		if peerNodeID == vxlanReconciler.nodeID {
			continue
		}
		peerSubnet, hasSubnet := nodeSubnetMap[peerNodeID]
		if !hasSubnet || peerSubnet == "" {
			continue
		}
		peerNodes = append(peerNodes, peerNodeInfo{
			nodeID:           peerNodeID,
			advertiseAddress: peerAddress,
			subnetCIDR:       peerSubnet,
		})
	}

	sort.Slice(peerNodes, func(indexI, indexJ int) bool {
		return peerNodes[indexI].nodeID < peerNodes[indexJ].nodeID
	})

	return peerNodes, nil
}

// buildNodeAddressMap extracts node IDs and their advertise addresses from
// observed node address facts. The key format is "observed/node/{nodeID}/address".
func buildNodeAddressMap(addressFacts []store.Fact) map[string]string {
	nodeAddressMap := make(map[string]string)
	for _, addressFact := range addressFacts {
		relativePath := strings.TrimPrefix(addressFact.Key, types.ScanObservedNodes)
		if strings.HasSuffix(relativePath, "/address") {
			peerNodeID := strings.TrimSuffix(relativePath, "/address")
			nodeAddressMap[peerNodeID] = string(addressFact.Value)
		}
	}
	return nodeAddressMap
}

// buildNodeSubnetMap extracts node IDs and their subnet CIDRs from network
// node subnet facts. The key format is "network/node/{nodeID}/subnet".
func buildNodeSubnetMap(subnetFacts []store.Fact) map[string]string {
	nodeSubnetMap := make(map[string]string)
	for _, subnetFact := range subnetFacts {
		relativePath := strings.TrimPrefix(subnetFact.Key, types.ScanNetworkNodeSubnets)
		if strings.HasSuffix(relativePath, "/subnet") {
			peerNodeID := strings.TrimSuffix(relativePath, "/subnet")
			nodeSubnetMap[peerNodeID] = string(subnetFact.Value)
		}
	}
	return nodeSubnetMap
}

// programPeerRoutes programs a route and FDB entry for each remote node so
// traffic destined for the remote node's container subnet is sent through
// the VXLAN tunnel.
func (vxlanReconciler *VXLANReconciler) programPeerRoutes(ctx context.Context, peerNodes []peerNodeInfo) {
	for _, peer := range peerNodes {
		if _, _, cidrError := net.ParseCIDR(peer.subnetCIDR); cidrError != nil {
			logging.Default().Error("vxlan: invalid peer subnet CIDR, skipping",
				"peer", peer.nodeID, "subnet", peer.subnetCIDR)
			continue
		}
		if net.ParseIP(peer.advertiseAddress) == nil {
			logging.Default().Error("vxlan: invalid peer address, skipping",
				"peer", peer.nodeID, "address", peer.advertiseAddress)
			continue
		}
		if routeError := executeIPRouteReplace(ctx, peer.subnetCIDR); routeError != nil {
			logging.Default().Error("vxlan: failed to program route",
				"node", vxlanReconciler.nodeID, "peer", peer.nodeID,
				"subnet", peer.subnetCIDR, "error", routeError.Error())
		}
		if fdbError := executeBridgeFDBAppend(ctx, peer.advertiseAddress); fdbError != nil {
			logging.Default().Error("vxlan: failed to program FDB entry",
				"node", vxlanReconciler.nodeID, "peer", peer.nodeID,
				"dst", peer.advertiseAddress, "error", fdbError.Error())
		}
	}
}

// removeStaleRoutes removes routes for remote subnets that no longer correspond
// to any active peer node. It reads the current route table for the VXLAN
// interface and deletes entries whose destination subnet is not in the active
// peer set.
func (vxlanReconciler *VXLANReconciler) removeStaleRoutes(ctx context.Context, activePeerNodes []peerNodeInfo) {
	activeSubnets := make(map[string]bool, len(activePeerNodes))
	for _, peer := range activePeerNodes {
		activeSubnets[peer.subnetCIDR] = true
	}

	installedSubnets, listError := listVXLANRouteSubnets(ctx)
	if listError != nil {
		logging.Default().Error("vxlan: failed to list installed routes",
			"node", vxlanReconciler.nodeID, "error", listError.Error())
		return
	}

	for _, installedSubnet := range installedSubnets {
		if !activeSubnets[installedSubnet] {
			if deleteError := executeIPRouteDelete(ctx, installedSubnet); deleteError != nil {
				logging.Default().Error("vxlan: failed to remove stale route",
					"node", vxlanReconciler.nodeID, "subnet", installedSubnet,
					"error", deleteError.Error())
			} else {
				logging.Default().Info("vxlan: removed stale route",
					"node", vxlanReconciler.nodeID, "subnet", installedSubnet)
			}
		}
	}
}

// allocateNextFreeSubnet scans existing subnet allocations and claims the next
// available third octet using a CAS transaction to prevent races between
// concurrent agents.
func (vxlanReconciler *VXLANReconciler) allocateNextFreeSubnet(ctx context.Context) (string, error) {
	clusterBaseFirst, clusterBaseSecond, parseError := parseClusterCIDRBaseOctets(vxlanReconciler.clusterCIDR)
	if parseError != nil {
		return "", parseError
	}

	for attempt := 0; attempt < maxSubnetAllocationAttempts; attempt++ {
		scanResult, scanError := vxlanReconciler.factStore.ScanWithRevision(ctx, types.ScanNetworkNodeSubnets)
		if scanError != nil {
			return "", fmt.Errorf("scanning existing subnets: %w", scanError)
		}

		occupiedOctets := collectOccupiedThirdOctets(scanResult.Facts)
		nextFreeOctet := findNextFreeThirdOctetForNode(occupiedOctets, vxlanReconciler.nodeID)
		if nextFreeOctet < 0 {
			return "", fmt.Errorf("all /24 subnets in %s are allocated", vxlanReconciler.clusterCIDR)
		}

		subnetCIDR := fmt.Sprintf("%d.%d.%d.0/24", clusterBaseFirst, clusterBaseSecond, nextFreeOctet)
		subnetKey := types.KeyNetworkNodeSubnet(vxlanReconciler.nodeID)

		committed, txnError := vxlanReconciler.factStore.Transaction(ctx,
			[]store.Compare{{Key: subnetKey, Revision: 0}},
			[]store.Op{{Type: store.OpPut, Key: subnetKey, Value: []byte(subnetCIDR)}},
			nil,
		)
		if txnError != nil {
			return "", fmt.Errorf("subnet allocation transaction: %w", txnError)
		}
		if committed {
			return subnetCIDR, nil
		}

		// Another agent claimed this node's key first — re-read to see if
		// our own key was written (concurrent agent for the same nodeID).
		existingFact, getError := vxlanReconciler.factStore.Get(ctx, subnetKey)
		if getError == nil && len(existingFact.Value) > 0 {
			return string(existingFact.Value), nil
		}
	}

	return "", fmt.Errorf("subnet allocation failed after %d attempts", maxSubnetAllocationAttempts)
}

// parseClusterCIDRBaseOctets extracts the first two octets from a /16 CIDR
// string (e.g. "10.100.0.0/16" yields 10 and 100). Returns an error if the
// CIDR is not exactly /16, since subnet carving assumes 254 /24 subnets.
func parseClusterCIDRBaseOctets(clusterCIDR string) (int, int, error) {
	parsedIP, parsedNet, parseError := net.ParseCIDR(clusterCIDR)
	if parseError != nil {
		return 0, 0, fmt.Errorf("parsing cluster CIDR %q: %w", clusterCIDR, parseError)
	}
	ipv4 := parsedIP.To4()
	if ipv4 == nil {
		return 0, 0, fmt.Errorf("cluster CIDR %q is not IPv4", clusterCIDR)
	}
	prefixLength, _ := parsedNet.Mask.Size()
	if prefixLength != 16 {
		return 0, 0, fmt.Errorf("cluster CIDR %q must be a /16 (got /%d)", clusterCIDR, prefixLength)
	}
	return int(ipv4[0]), int(ipv4[1]), nil
}

// collectOccupiedThirdOctets parses existing subnet facts and returns a set
// of third-octet values that are already allocated.
func collectOccupiedThirdOctets(subnetFacts []store.Fact) map[int]bool {
	occupied := make(map[int]bool)
	for _, subnetFact := range subnetFacts {
		subnetCIDR := string(subnetFact.Value)
		parsedIP, _, parseError := net.ParseCIDR(subnetCIDR)
		if parseError != nil {
			continue
		}
		ipv4 := parsedIP.To4()
		if ipv4 == nil {
			continue
		}
		occupied[int(ipv4[2])] = true
	}
	return occupied
}

// findNextFreeThirdOctetForNode returns the first unused third-octet value
// starting from a deterministic offset derived from the node ID. This prevents
// two nodes starting simultaneously from racing for the same octet.
func findNextFreeThirdOctetForNode(occupiedOctets map[int]bool, nodeID string) int {
	allocationRange := subnetAllocationRangeEnd - subnetAllocationRangeStart
	startOctet := hashNodeIDToOctetOffset(nodeID, allocationRange)
	for offset := 0; offset < allocationRange; offset++ {
		candidate := subnetAllocationRangeStart + ((startOctet + offset) % allocationRange)
		if !occupiedOctets[candidate] {
			return candidate
		}
	}
	return -1
}

// hashNodeIDToOctetOffset returns a deterministic offset in [0, rangeSize)
// based on the node ID, so different nodes start searching from different
// octets and are unlikely to collide on the same candidate.
func hashNodeIDToOctetOffset(nodeID string, rangeSize int) int {
	hasher := fnv.New32a()
	hasher.Write([]byte(nodeID))
	hashValue := hasher.Sum32()
	return int(hashValue % uint32(rangeSize)) //nolint:gosec // rangeSize is always 1..254, safe for uint32
}

// interfaceAlreadyExists checks whether a network interface with the given
// name exists on the host by running "ip link show".
func interfaceAlreadyExists(ctx context.Context, interfaceName string) bool {
	checkCommand := exec.CommandContext(ctx, "ip", "link", "show", interfaceName) //nolint:gosec // ip commands with validated interface name
	return checkCommand.Run() == nil
}

// detectPhysicalInterfaceForAddress returns the name of the network interface
// that has the given IP address assigned. This is used to determine the
// underlay device for the VXLAN tunnel.
func detectPhysicalInterfaceForAddress(advertiseAddress string) (string, error) {
	networkInterfaces, listError := net.Interfaces()
	if listError != nil {
		return "", fmt.Errorf("listing network interfaces: %w", listError)
	}
	for _, networkInterface := range networkInterfaces {
		interfaceAddresses, addrError := networkInterface.Addrs()
		if addrError != nil {
			continue
		}
		for _, interfaceAddress := range interfaceAddresses {
			addressIP, _, parseError := net.ParseCIDR(interfaceAddress.String())
			if parseError != nil {
				continue
			}
			if addressIP.String() == advertiseAddress {
				return networkInterface.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no interface found with address %s", advertiseAddress)
}

// deriveVXLANMACAddress generates a deterministic locally-administered MAC
// address from the node ID. The 0x02 bit in the first octet marks it as
// locally assigned (IEEE 802). This prevents MAC collisions on cloned VMs.
func deriveVXLANMACAddress(nodeID string) string {
	hasher := fnv.New32a()
	hasher.Write([]byte(nodeID))
	hashValue := hasher.Sum32()
	octet1 := byte(hashValue >> 24)     //nolint:gosec // truncation is intentional for MAC derivation
	octet2 := byte(hashValue >> 16)     //nolint:gosec // truncation is intentional for MAC derivation
	octet3 := byte(hashValue >> 8)      //nolint:gosec // truncation is intentional for MAC derivation
	octet4 := byte(hashValue)           //nolint:gosec // truncation is intentional for MAC derivation
	octet5 := byte(hashValue>>4) ^ 0xab //nolint:gosec // truncation is intentional for MAC derivation
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", octet1, octet2, octet3, octet4, octet5)
}

// executeIPLinkSetAddress sets the hardware (MAC) address on a network interface.
func executeIPLinkSetAddress(ctx context.Context, interfaceName string, macAddress string) error {
	addrCommand := exec.CommandContext(ctx, "ip", "link", "set", interfaceName, "address", macAddress) //nolint:gosec // ip commands with validated arguments
	if output, runError := addrCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// executeVXLANCreate runs the ip command to create the VXLAN interface with
// the configured VNI, destination port, underlay device, and local address.
func executeVXLANCreate(ctx context.Context, physicalInterface string, localAddress string) error {
	createCommand := exec.CommandContext(ctx, "ip", "link", "add", //nolint:gosec // ip commands with validated arguments
		vxlanInterfaceName, "type", "vxlan",
		"id", fmt.Sprintf("%d", vxlanVNI),
		"dstport", fmt.Sprintf("%d", vxlanDestinationPort),
		"dev", physicalInterface,
		"local", localAddress,
	)
	if output, runError := createCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// executeIPLinkSetUp runs the ip command to bring a network interface up.
func executeIPLinkSetUp(ctx context.Context, interfaceName string) error {
	setUpCommand := exec.CommandContext(ctx, "ip", "link", "set", interfaceName, "up") //nolint:gosec // ip commands with validated interface name
	if output, runError := setUpCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// executeIPLinkSetMTU runs the ip command to set the MTU on a network interface.
func executeIPLinkSetMTU(ctx context.Context, interfaceName string, mtuValue int) error {
	setMTUCommand := exec.CommandContext(ctx, "ip", "link", "set", interfaceName, "mtu", fmt.Sprintf("%d", mtuValue)) //nolint:gosec // ip commands with validated arguments
	if output, runError := setMTUCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// executeIPLinkDelete runs the ip command to remove a network interface.
func executeIPLinkDelete(ctx context.Context, interfaceName string) error {
	deleteCommand := exec.CommandContext(ctx, "ip", "link", "del", interfaceName) //nolint:gosec // ip commands with validated interface name
	if output, runError := deleteCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// executeIPRouteReplace runs the ip command to add or replace a route for a
// remote subnet through the VXLAN interface. Traffic destined for the remote
// subnet is sent to the VXLAN device; FDB entries handle underlay encapsulation.
func executeIPRouteReplace(ctx context.Context, destinationSubnet string) error {
	routeCommand := exec.CommandContext(ctx, "ip", "route", "replace", //nolint:gosec // ip commands with validated arguments
		destinationSubnet, "dev", vxlanInterfaceName)
	if output, runError := routeCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// executeIPRouteDelete runs the ip command to remove a route for a subnet
// from the VXLAN interface.
func executeIPRouteDelete(ctx context.Context, destinationSubnet string) error {
	deleteCommand := exec.CommandContext(ctx, "ip", "route", "del", //nolint:gosec // ip commands with validated arguments
		destinationSubnet, "dev", vxlanInterfaceName)
	if output, runError := deleteCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// executeBridgeFDBAppend adds an FDB entry for the all-zero MAC on the VXLAN
// interface. Skips if the entry already exists to avoid duplicates (append is
// not idempotent). Uses "append" because some kernels reject "replace" on
// all-zero MAC catch-all entries used for BUM traffic forwarding.
func executeBridgeFDBAppend(ctx context.Context, destinationAddress string) error {
	if bridgeFDBEntryExists(ctx, destinationAddress) {
		return nil
	}
	fdbCommand := exec.CommandContext(ctx, "bridge", "fdb", "append", //nolint:gosec // bridge commands with validated arguments
		"00:00:00:00:00:00", "dev", vxlanInterfaceName, "dst", destinationAddress)
	if output, runError := fdbCommand.CombinedOutput(); runError != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), runError)
	}
	return nil
}

// bridgeFDBEntryExists checks whether an FDB entry for the given destination
// already exists on the VXLAN interface.
func bridgeFDBEntryExists(ctx context.Context, destinationAddress string) bool {
	listCommand := exec.CommandContext(ctx, "bridge", "fdb", "show", "dev", vxlanInterfaceName) //nolint:gosec // bridge commands with constant device name
	output, runError := listCommand.CombinedOutput()
	if runError != nil {
		return false
	}
	searchPattern := "dst " + destinationAddress
	return strings.Contains(string(output), searchPattern)
}

// listVXLANRouteSubnets reads the kernel routing table for routes that use
// the VXLAN interface and returns the destination subnet CIDRs.
func listVXLANRouteSubnets(ctx context.Context) ([]string, error) {
	listCommand := exec.CommandContext(ctx, "ip", "route", "show", "dev", vxlanInterfaceName) //nolint:gosec // ip commands with constant device name
	routeOutput, runError := listCommand.CombinedOutput()
	if runError != nil {
		return nil, fmt.Errorf("listing routes: %s: %w", strings.TrimSpace(string(routeOutput)), runError)
	}

	var subnets []string
	for _, routeLine := range strings.Split(strings.TrimSpace(string(routeOutput)), "\n") {
		routeLine = strings.TrimSpace(routeLine)
		if routeLine == "" {
			continue
		}
		// Route lines look like: "10.100.2.0/24 via 192.168.1.2 onlink"
		routeFields := strings.Fields(routeLine)
		if len(routeFields) > 0 {
			subnets = append(subnets, routeFields[0])
		}
	}
	return subnets, nil
}
