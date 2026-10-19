// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build ebpf

package network

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

// ebpfPinPath is the BPF filesystem directory where maps are pinned for
// persistence across agent restarts and inspection via bpftool.
const ebpfPinPath = "/sys/fs/bpf/cca"

// ebpfCgroupPath is the cgroup v2 unified mount point where socket LB
// programs are attached. On systemd-based systems this is the root cgroup.
const ebpfCgroupPath = "/sys/fs/cgroup"

// ebpfELFPath is the default filesystem path to the compiled BPF ELF object.
// In production, the Rust/aya-ebpf pipeline compiles programs to this path.
// The agent loads BPF programs and map definitions from this ELF file.
const ebpfELFPath = "/usr/lib/cca/cca_ebpf.o"

// ebpfServiceMapName is the BPF map name for the service lookup table.
const ebpfServiceMapName = "cca_services"

// ebpfBackendMapName is the BPF map name for backend endpoint entries.
const ebpfBackendMapName = "cca_backends"

// ebpfAffinityMapName is the BPF map name for session affinity state.
const ebpfAffinityMapName = "cca_affinity"

// ebpfReverseNatMapName is the BPF map name for reverse VIP translation
// used by the getpeername4 program.
const ebpfReverseNatMapName = "cca_reverse_nat"

// ebpfConnect4ProgName is the name of the cgroup/connect4 BPF program
// that intercepts TCP connect() calls destined for a VIP.
const ebpfConnect4ProgName = "cca_connect4"

// ebpfSendmsg4ProgName is the name of the cgroup/sendmsg4 BPF program
// that intercepts UDP sendto()/sendmsg() calls destined for a VIP.
const ebpfSendmsg4ProgName = "cca_sendmsg4"

// ebpfGetpeername4ProgName is the name of the cgroup/getpeername4 BPF
// program that returns the original VIP address for translated sockets.
const ebpfGetpeername4ProgName = "cca_getpeername4"

// ebpfInitialServiceID is the starting value for service ID allocation.
// IDs are monotonically increasing; zero is reserved as an invalid sentinel.
const ebpfInitialServiceID uint32 = 1

// ebpfCgroupAttachment describes a BPF program that is attached to the root
// cgroup for socket-level VIP interception.
type ebpfCgroupAttachment struct {
	// programName is the BPF program name in the ELF object.
	programName string
	// attachType is the BPF cgroup attach type for the hook.
	attachType ebpf.AttachType
}

// ebpfCgroupPrograms lists the BPF programs to attach to the root cgroup.
// Order: connect4 (TCP), sendmsg4 (UDP), getpeername4 (reverse VIP lookup).
var ebpfCgroupPrograms = []ebpfCgroupAttachment{
	{ebpfConnect4ProgName, ebpf.AttachCGroupInet4Connect},
	{ebpfSendmsg4ProgName, ebpf.AttachCGroupUDP4Sendmsg},
	{ebpfGetpeername4ProgName, ebpf.AttachCGroupInet4GetPeername},
}

// EbpfDataPlane implements DataPlaneProvider by loading BPF programs that
// intercept socket syscalls (connect4, sendmsg4, getpeername4) on the host
// cgroup and translating VIP destinations to backend endpoints via BPF hash
// maps. Replaces nftables DNAT for host-originated traffic. Also implements
// DataPlaneInitializer for one-time program loading and map pinning.
type EbpfDataPlane struct {
	// pinPath is the BPF filesystem directory where maps are pinned.
	pinPath string
	// serviceMap is a handle to the cca_services BPF hash map.
	serviceMap *ebpf.Map
	// backendMap is a handle to the cca_backends BPF hash map.
	backendMap *ebpf.Map
	// affinityMap is a handle to the cca_affinity BPF LRU hash map.
	affinityMap *ebpf.Map
	// reverseNatMap is a handle to the cca_reverse_nat BPF LRU hash map.
	reverseNatMap *ebpf.Map
	// bpfCollection holds all loaded BPF programs and maps from the ELF.
	bpfCollection *ebpf.Collection
	// attachedLinks tracks BPF program cgroup attachments for orderly cleanup.
	attachedLinks []link.Link
	// nextServiceID is the next service ID to allocate (monotonically increasing).
	nextServiceID uint32
	// serviceNameToID maps CCattler service names to allocated BPF service IDs.
	serviceNameToID map[string]uint32
	// mutex serializes reconciliation calls and service ID allocation.
	mutex sync.Mutex
	// elfPath is the filesystem path to the compiled BPF ELF object.
	elfPath string
	// initialized tracks whether Initialize completed successfully.
	initialized bool
}

// NewEbpfDataPlane creates an EbpfDataPlane ready to load BPF programs and
// manage BPF maps for socket-level VIP load balancing.
func NewEbpfDataPlane() *EbpfDataPlane {
	return &EbpfDataPlane{
		pinPath:         ebpfPinPath,
		nextServiceID:   ebpfInitialServiceID,
		serviceNameToID: make(map[string]uint32),
		elfPath:         ebpfELFPath,
	}
}

// Initialize loads BPF programs from the compiled ELF, pins maps to the BPF
// filesystem for persistence, and attaches cgroup programs for socket-level
// VIP interception. Must be called before the first reconciliation cycle.
func (ebpfDataPlane *EbpfDataPlane) Initialize(ctx context.Context) error {
	ebpfDataPlane.mutex.Lock()
	defer ebpfDataPlane.mutex.Unlock()

	logger := logging.Default()
	logger.Info("ebpf data plane: initializing", "elf_path", ebpfDataPlane.elfPath)

	if rlimitError := rlimit.RemoveMemlock(); rlimitError != nil {
		return fmt.Errorf("removing memlock rlimit for BPF: %w", rlimitError)
	}

	if directoryError := ebpfDataPlane.ensurePinDirectory(); directoryError != nil {
		return fmt.Errorf("creating BPF pin directory %s: %w", ebpfDataPlane.pinPath, directoryError)
	}

	collectionSpec, loadError := ebpf.LoadCollectionSpec(ebpfDataPlane.elfPath)
	if loadError != nil {
		return fmt.Errorf("loading BPF ELF from %s: %w", ebpfDataPlane.elfPath, loadError)
	}

	collection, createError := ebpf.NewCollection(collectionSpec)
	if createError != nil {
		return fmt.Errorf("creating BPF collection: %w", createError)
	}
	ebpfDataPlane.bpfCollection = collection

	if mapError := ebpfDataPlane.extractAndPinMaps(); mapError != nil {
		collection.Close()
		return fmt.Errorf("extracting BPF maps: %w", mapError)
	}

	if attachError := ebpfDataPlane.attachCgroupPrograms(); attachError != nil {
		ebpfDataPlane.closeAllResources()
		return fmt.Errorf("attaching BPF cgroup programs: %w", attachError)
	}

	if scanError := ebpfDataPlane.scanExistingServiceIDs(); scanError != nil {
		logger.Warn("ebpf data plane: could not scan existing service IDs",
			"error", scanError.Error())
	}

	ebpfDataPlane.initialized = true
	logger.Info("ebpf data plane: initialization complete")
	return nil
}

// ensurePinDirectory creates the BPF filesystem directory for pinned maps.
func (ebpfDataPlane *EbpfDataPlane) ensurePinDirectory() error {
	return os.MkdirAll(ebpfDataPlane.pinPath, 0700)
}

// extractAndPinMaps retrieves map handles from the loaded BPF collection and
// pins each map to the BPF filesystem for persistence and bpftool inspection.
func (ebpfDataPlane *EbpfDataPlane) extractAndPinMaps() error {
	requiredMapNames := []string{
		ebpfServiceMapName, ebpfBackendMapName,
		ebpfAffinityMapName, ebpfReverseNatMapName,
	}

	extractedMaps := make(map[string]*ebpf.Map, len(requiredMapNames))
	for _, mapName := range requiredMapNames {
		bpfMap := ebpfDataPlane.bpfCollection.Maps[mapName]
		if bpfMap == nil {
			return fmt.Errorf("BPF map %s not found in ELF object", mapName)
		}
		pinFilePath := ebpfDataPlane.pinPath + "/" + mapName
		if pinError := bpfMap.Pin(pinFilePath); pinError != nil {
			return fmt.Errorf("pinning map %s to %s: %w", mapName, pinFilePath, pinError)
		}
		extractedMaps[mapName] = bpfMap
	}

	ebpfDataPlane.serviceMap = extractedMaps[ebpfServiceMapName]
	ebpfDataPlane.backendMap = extractedMaps[ebpfBackendMapName]
	ebpfDataPlane.affinityMap = extractedMaps[ebpfAffinityMapName]
	ebpfDataPlane.reverseNatMap = extractedMaps[ebpfReverseNatMapName]
	return nil
}

// attachCgroupPrograms attaches each socket-level BPF program to the root
// cgroup. Programs intercept connect4, sendmsg4, and getpeername4 syscalls.
func (ebpfDataPlane *EbpfDataPlane) attachCgroupPrograms() error {
	for _, attachment := range ebpfCgroupPrograms {
		program := ebpfDataPlane.bpfCollection.Programs[attachment.programName]
		if program == nil {
			return fmt.Errorf("BPF program %s not found in ELF object", attachment.programName)
		}
		cgroupLink, attachError := link.AttachCgroup(link.CgroupOptions{
			Path:    ebpfCgroupPath,
			Attach:  attachment.attachType,
			Program: program,
		})
		if attachError != nil {
			return fmt.Errorf("attaching %s to cgroup %s: %w",
				attachment.programName, ebpfCgroupPath, attachError)
		}
		ebpfDataPlane.attachedLinks = append(ebpfDataPlane.attachedLinks, cgroupLink)
	}
	return nil
}

// scanExistingServiceIDs iterates the service map to discover the highest
// allocated service ID from a previous agent run. This prevents ID collisions
// after an agent restart when pinned maps retain prior state.
func (ebpfDataPlane *EbpfDataPlane) scanExistingServiceIDs() error {
	var serviceKey EbpfServiceKey
	var serviceValue EbpfServiceValue
	maxObservedID := uint32(0)

	mapIterator := ebpfDataPlane.serviceMap.Iterate()
	for mapIterator.Next(&serviceKey, &serviceValue) {
		if serviceValue.ServiceID > maxObservedID {
			maxObservedID = serviceValue.ServiceID
		}
	}

	if maxObservedID >= ebpfDataPlane.nextServiceID {
		ebpfDataPlane.nextServiceID = maxObservedID + 1
		logging.Default().Info("ebpf data plane: resumed service ID allocation",
			"next_id", strconv.FormatUint(uint64(ebpfDataPlane.nextServiceID), 10))
	}
	return nil
}

// ReconcileVIPDataPlane computes the desired BPF map state from the given
// service VIP configurations and applies changes in the correct order:
// add backends first, update services second, remove stale entries last.
// This ordering ensures services always reference valid backends.
func (ebpfDataPlane *EbpfDataPlane) ReconcileVIPDataPlane(ctx context.Context, serviceConfigs []ServiceVIPConfig) error {
	ebpfDataPlane.mutex.Lock()
	defer ebpfDataPlane.mutex.Unlock()

	if ebpfDataPlane.serviceMap == nil {
		return fmt.Errorf("ebpf data plane not initialized: call Initialize first")
	}

	desiredServices, desiredBackends := ebpfDataPlane.buildDesiredVIPState(serviceConfigs)

	if backendError := ebpfDataPlane.applyDesiredBackendEntries(desiredBackends); backendError != nil {
		return fmt.Errorf("applying backend entries: %w", backendError)
	}
	if serviceError := ebpfDataPlane.applyDesiredServiceEntries(desiredServices); serviceError != nil {
		return fmt.Errorf("applying service entries: %w", serviceError)
	}
	if staleServiceError := ebpfDataPlane.removeStaleServiceEntries(desiredServices); staleServiceError != nil {
		return fmt.Errorf("removing stale service entries: %w", staleServiceError)
	}
	if staleBackendError := ebpfDataPlane.removeStaleBackendEntries(desiredBackends); staleBackendError != nil {
		return fmt.Errorf("removing stale backend entries: %w", staleBackendError)
	}

	return nil
}

// buildDesiredVIPState computes the complete desired BPF map state from the
// given service VIP configurations. Returns service entries and backend
// entries. Reverse NAT entries are written per-connection by the BPF programs
// using socket cookies, so the Go control plane does not manage that map.
func (ebpfDataPlane *EbpfDataPlane) buildDesiredVIPState(serviceConfigs []ServiceVIPConfig) (
	map[EbpfServiceKey]EbpfServiceValue,
	map[EbpfBackendKey]EbpfBackendValue,
) {
	desiredServices := make(map[EbpfServiceKey]EbpfServiceValue)
	desiredBackends := make(map[EbpfBackendKey]EbpfBackendValue)

	sortedConfigs := make([]ServiceVIPConfig, len(serviceConfigs))
	copy(sortedConfigs, serviceConfigs)
	sort.Slice(sortedConfigs, func(i, j int) bool {
		return sortedConfigs[i].ServiceName < sortedConfigs[j].ServiceName
	})

	for _, serviceConfig := range sortedConfigs {
		if len(serviceConfig.Backends) == 0 {
			continue
		}
		ebpfDataPlane.buildServiceEntries(
			serviceConfig, desiredServices, desiredBackends,
		)
	}

	return desiredServices, desiredBackends
}

// buildServiceEntries populates the desired map entries for a single service
// VIP configuration: one service entry and N backend entries (one per backend).
func (ebpfDataPlane *EbpfDataPlane) buildServiceEntries(
	serviceConfig ServiceVIPConfig,
	desiredServices map[EbpfServiceKey]EbpfServiceValue,
	desiredBackends map[EbpfBackendKey]EbpfBackendValue,
) {
	serviceID := ebpfDataPlane.assignOrLookupServiceID(serviceConfig.ServiceName)
	protocolNumber := protocolToNumber(serviceConfig.EffectiveProtocol())
	vipAddr := ipToUint32(serviceConfig.VirtualIP)

	vipPort := portToNetworkOrder(uint16(serviceConfig.Port))
	serviceKey := EbpfServiceKey{
		VIPAddr: vipAddr, Port: vipPort, Protocol: protocolNumber,
	}
	serviceValue := EbpfServiceValue{
		ServiceID: serviceID, BackendCount: uint16(len(serviceConfig.Backends)),
	}
	desiredServices[serviceKey] = serviceValue

	for slotIndex, backend := range serviceConfig.Backends {
		backendAddr := ipToUint32(backend.Address)
		backendPort := portToNetworkOrder(uint16(backend.Port))

		desiredBackends[EbpfBackendKey{
			ServiceID: serviceID, Slot: uint16(slotIndex),
		}] = EbpfBackendValue{BackendAddr: backendAddr, Port: backendPort}
	}
}

// assignOrLookupServiceID returns the existing BPF service ID for the given
// service name, or allocates a new monotonically increasing ID if none exists.
func (ebpfDataPlane *EbpfDataPlane) assignOrLookupServiceID(serviceName string) uint32 {
	if existingID, found := ebpfDataPlane.serviceNameToID[serviceName]; found {
		return existingID
	}
	allocatedID := ebpfDataPlane.nextServiceID
	ebpfDataPlane.nextServiceID++
	ebpfDataPlane.serviceNameToID[serviceName] = allocatedID
	return allocatedID
}

// applyDesiredBackendEntries writes all desired backend entries to the BPF
// map. Existing entries with the same key are overwritten (idempotent put).
func (ebpfDataPlane *EbpfDataPlane) applyDesiredBackendEntries(
	desiredEntries map[EbpfBackendKey]EbpfBackendValue,
) error {
	for backendKey, backendValue := range desiredEntries {
		if putError := ebpfDataPlane.backendMap.Put(backendKey, backendValue); putError != nil {
			return fmt.Errorf("writing backend {service_id=%d, slot=%d}: %w",
				backendKey.ServiceID, backendKey.Slot, putError)
		}
	}
	return nil
}

// applyDesiredServiceEntries writes all desired service entries to the BPF
// map. Existing entries with the same key are overwritten (idempotent put).
func (ebpfDataPlane *EbpfDataPlane) applyDesiredServiceEntries(
	desiredEntries map[EbpfServiceKey]EbpfServiceValue,
) error {
	for serviceKey, serviceValue := range desiredEntries {
		if putError := ebpfDataPlane.serviceMap.Put(serviceKey, serviceValue); putError != nil {
			return fmt.Errorf("writing service {vip=%s, port=%d}: %w",
				uint32ToIP(serviceKey.VIPAddr), serviceKey.Port, putError)
		}
	}
	return nil
}

// removeStaleServiceEntries iterates the service BPF map and deletes entries
// that are not in the desired set. Called after applying desired entries.
func (ebpfDataPlane *EbpfDataPlane) removeStaleServiceEntries(
	desiredEntries map[EbpfServiceKey]EbpfServiceValue,
) error {
	var currentKey EbpfServiceKey
	var currentValue EbpfServiceValue
	var staleKeys []EbpfServiceKey

	mapIterator := ebpfDataPlane.serviceMap.Iterate()
	for mapIterator.Next(&currentKey, &currentValue) {
		if _, isDesired := desiredEntries[currentKey]; !isDesired {
			staleKeys = append(staleKeys, currentKey)
		}
	}

	for _, staleKey := range staleKeys {
		if deleteError := ebpfDataPlane.serviceMap.Delete(staleKey); deleteError != nil {
			logging.Default().Warn("ebpf: failed to delete stale service entry",
				"vip", uint32ToIP(staleKey.VIPAddr),
				"port", strconv.Itoa(int(staleKey.Port)))
		}
	}
	return nil
}

// removeStaleBackendEntries iterates the backend BPF map and deletes entries
// that are not in the desired set. Called after stale service removal to
// clean up orphaned backends from deleted or rescaled services.
func (ebpfDataPlane *EbpfDataPlane) removeStaleBackendEntries(
	desiredEntries map[EbpfBackendKey]EbpfBackendValue,
) error {
	var currentKey EbpfBackendKey
	var currentValue EbpfBackendValue
	var staleKeys []EbpfBackendKey

	mapIterator := ebpfDataPlane.backendMap.Iterate()
	for mapIterator.Next(&currentKey, &currentValue) {
		if _, isDesired := desiredEntries[currentKey]; !isDesired {
			staleKeys = append(staleKeys, currentKey)
		}
	}

	for _, staleKey := range staleKeys {
		if deleteError := ebpfDataPlane.backendMap.Delete(staleKey); deleteError != nil {
			logging.Default().Warn("ebpf: failed to delete stale backend entry",
				"service_id", strconv.FormatUint(uint64(staleKey.ServiceID), 10),
				"slot", strconv.Itoa(int(staleKey.Slot)))
		}
	}
	return nil
}

// ReconcileRouteDataPlane enables source-IP session affinity on service
// entries that have matching route configurations. Cookie-based sticky routes
// are not handled by BPF (they require the userspace cookie proxy).
func (ebpfDataPlane *EbpfDataPlane) ReconcileRouteDataPlane(ctx context.Context, routeConfigs []RouteDataPlaneConfig) error {
	ebpfDataPlane.mutex.Lock()
	defer ebpfDataPlane.mutex.Unlock()

	if ebpfDataPlane.serviceMap == nil {
		return fmt.Errorf("ebpf data plane not initialized: call Initialize first")
	}

	for _, routeConfig := range routeConfigs {
		if routeConfig.StickyMethod != "source-ip" || routeConfig.VirtualIP == "" {
			continue
		}
		ebpfDataPlane.enableStickyAffinityForService(routeConfig)
	}
	return nil
}

// enableStickyAffinityForService reads the service entry matching the route's
// VIP/port, sets the STICKY_SOURCE_IP flag and affinity timeout, and writes
// it back. Logs a warning if the matching service entry is not found.
func (ebpfDataPlane *EbpfDataPlane) enableStickyAffinityForService(routeConfig RouteDataPlaneConfig) {
	serviceKey := EbpfServiceKey{
		VIPAddr:  ipToUint32(routeConfig.VirtualIP),
		Port:     portToNetworkOrder(uint16(routeConfig.Port)),
		Protocol: ebpfProtocolTCP,
	}

	var serviceValue EbpfServiceValue
	lookupError := ebpfDataPlane.serviceMap.Lookup(serviceKey, &serviceValue)
	if lookupError != nil {
		logging.Default().Warn("ebpf route: service not found for sticky affinity update",
			"route", routeConfig.RouteName, "vip", routeConfig.VirtualIP,
			"port", strconv.Itoa(routeConfig.Port))
		return
	}

	serviceValue.Flags |= ebpfServiceFlagStickySourceIP
	serviceValue.AffinityTimeoutSeconds = ebpfDefaultAffinityTimeoutSeconds

	if putError := ebpfDataPlane.serviceMap.Put(serviceKey, serviceValue); putError != nil {
		logging.Default().Warn("ebpf route: failed to enable sticky affinity",
			"route", routeConfig.RouteName, "error", putError.Error())
	}
}

// Cleanup detaches all BPF programs from the cgroup, unpins maps from the
// BPF filesystem, and closes all BPF resource handles.
func (ebpfDataPlane *EbpfDataPlane) Cleanup(ctx context.Context) error {
	ebpfDataPlane.mutex.Lock()
	defer ebpfDataPlane.mutex.Unlock()

	logging.Default().Info("ebpf data plane: cleaning up BPF programs and maps")

	ebpfDataPlane.closeAllResources()
	ebpfDataPlane.removePinnedMaps()

	ebpfDataPlane.serviceNameToID = make(map[string]uint32)
	ebpfDataPlane.nextServiceID = ebpfInitialServiceID
	ebpfDataPlane.initialized = false
	return nil
}

// closeAllResources detaches all BPF cgroup links and closes the BPF
// collection (which closes all program and map file descriptors).
func (ebpfDataPlane *EbpfDataPlane) closeAllResources() {
	logger := logging.Default()
	for _, attachedLink := range ebpfDataPlane.attachedLinks {
		if closeError := attachedLink.Close(); closeError != nil {
			logger.Warn("ebpf: failed to detach BPF program", "error", closeError.Error())
		}
	}
	ebpfDataPlane.attachedLinks = nil

	if ebpfDataPlane.bpfCollection != nil {
		ebpfDataPlane.bpfCollection.Close()
		ebpfDataPlane.bpfCollection = nil
	}

	ebpfDataPlane.serviceMap = nil
	ebpfDataPlane.backendMap = nil
	ebpfDataPlane.affinityMap = nil
	ebpfDataPlane.reverseNatMap = nil
}

// removePinnedMaps removes all pinned map files from the BPF filesystem
// directory. Non-existent files are silently skipped.
func (ebpfDataPlane *EbpfDataPlane) removePinnedMaps() {
	pinnedMapNames := []string{
		ebpfServiceMapName, ebpfBackendMapName,
		ebpfAffinityMapName, ebpfReverseNatMapName,
	}
	for _, mapName := range pinnedMapNames {
		pinFilePath := ebpfDataPlane.pinPath + "/" + mapName
		if removeError := os.Remove(pinFilePath); removeError != nil && !os.IsNotExist(removeError) {
			logging.Default().Warn("ebpf: failed to remove pinned map",
				"path", pinFilePath, "error", removeError.Error())
		}
	}
}
