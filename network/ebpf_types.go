// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build ebpf

package network

import (
	"encoding/binary"
	"fmt"
	"net"
)

// portToNetworkOrder converts a host-byte-order port to the representation
// used by bpf_sock_addr.user_port. The kernel loads sin_port (__be16) with
// a native 16-bit load, so on little-endian systems the bytes are swapped.
func portToNetworkOrder(hostPort uint16) uint16 {
	networkBytes := [2]byte{}
	binary.BigEndian.PutUint16(networkBytes[:], hostPort)
	return binary.NativeEndian.Uint16(networkBytes[:])
}

// safeUint16 converts an int to uint16, clamping to [0, 65535].
func safeUint16(value int) uint16 {
	if value < 0 {
		return 0
	}
	if value > 65535 {
		return 65535
	}
	return uint16(value) //nolint:gosec // bounds checked above
}

// ebpfServiceFlagStickySourceIP indicates that source-IP session affinity is
// enabled for this service. The BPF program checks the affinity map before
// selecting a new backend via round-robin.
const ebpfServiceFlagStickySourceIP uint16 = 0x01

// ebpfDefaultAffinityTimeoutSeconds is the default session affinity TTL
// for source-IP sticky routes (5 minutes).
const ebpfDefaultAffinityTimeoutSeconds uint32 = 300

// ebpfProtocolTCP is the IANA protocol number for TCP.
const ebpfProtocolTCP uint8 = 6

// ebpfProtocolUDP is the IANA protocol number for UDP.
const ebpfProtocolUDP uint8 = 17

// ebpfTCDNATELFPath is the default filesystem path to the compiled tc DNAT
// BPF ELF object. Contains the cca_tc_dnat program that creates conntrack
// DNAT entries for container-to-VIP traffic on the bridge.
const ebpfTCDNATELFPath = "/usr/lib/cca/cca_tc_dnat.o"

// ebpfTCDNATProgName is the name of the tc ingress BPF program in the tc
// DNAT ELF object.
const ebpfTCDNATProgName = "cca_tc_dnat"

// ebpfTCSNATELFPath is the default filesystem path to the compiled tc SNAT
// BPF ELF object. Contains the cca_tc_snat program that rewrites overlay
// source IPs to the node's advertise address on the physical NIC egress.
const ebpfTCSNATELFPath = "/usr/lib/cca/cca_tc_snat.o"

// ebpfTCSNATProgName is the name of the tc egress BPF program in the tc
// SNAT ELF object.
const ebpfTCSNATProgName = "cca_tc_snat"

// ebpfConfigMapName is the BPF map name for node-level configuration.
const ebpfConfigMapName = "cca_config"

// ebpfConfigIndexNodeIP is the cca_config array index for the node's
// advertise address in network byte order.
const ebpfConfigIndexNodeIP uint32 = 0

// ebpfConfigIndexOverlayPrefix is the cca_config array index for the
// overlay CIDR network prefix in network byte order.
const ebpfConfigIndexOverlayPrefix uint32 = 2

// ebpfConfigIndexOverlayMask is the cca_config array index for the
// overlay CIDR netmask in network byte order.
const ebpfConfigIndexOverlayMask uint32 = 3

// EbpfServiceKey is the BPF map key for the cca_services hash map.
// Layout must match the C struct exactly:
// { vip_addr: u32, port: u16, protocol: u8, _pad: u8 } = 8 bytes.
// IP addresses and ports use the same representation as bpf_sock_addr fields.
type EbpfServiceKey struct {
	// VIPAddr is the service virtual IP (same byte repr as user_ip4).
	VIPAddr uint32
	// Port is the service port in network byte order (portToNetworkOrder).
	Port uint16
	// Protocol is the IP protocol number (6=TCP, 17=UDP).
	Protocol uint8
	// Pad aligns the struct to 8 bytes total.
	Pad uint8
}

// EbpfServiceValue is the BPF map value for the cca_services hash map.
// Layout: { service_id: u32, backend_count: u16, flags: u16,
// affinity_timeout_seconds: u32 } = 12 bytes.
type EbpfServiceValue struct {
	// ServiceID is the unique numeric ID for this service, used as a key
	// component in the cca_backends map for backend lookup.
	ServiceID uint32
	// BackendCount is the number of active backends for round-robin selection.
	// The BPF program selects: slot = counter % backend_count.
	BackendCount uint16
	// Flags holds feature flags: STICKY_SOURCE_IP, STICKY_COOKIE, UDP.
	Flags uint16
	// AffinityTimeoutSeconds is the session affinity TTL in seconds.
	// Zero means no affinity even if a sticky flag is set.
	AffinityTimeoutSeconds uint32
}

// EbpfBackendKey is the BPF map key for the cca_backends hash map.
// Layout: { service_id: u32, slot: u16, _pad: u16 } = 8 bytes.
type EbpfBackendKey struct {
	// ServiceID references the service this backend belongs to.
	ServiceID uint32
	// Slot is the zero-based index of this backend within the service.
	Slot uint16
	// Pad aligns the struct to 8 bytes total.
	Pad uint16
}

// EbpfBackendValue is the BPF map value for the cca_backends hash map.
// Layout: { backend_addr: u32, port: u16, _pad: u16 } = 8 bytes.
type EbpfBackendValue struct {
	// BackendAddr is the backend IP (same byte repr as user_ip4).
	BackendAddr uint32
	// Port is the backend port in network byte order (portToNetworkOrder).
	Port uint16
	// Pad aligns the struct to 8 bytes total.
	Pad uint16
}

// EbpfAffinityKey is the BPF map key for the cca_affinity LRU hash map.
// Layout: { client_addr: u32, service_id: u32 } = 8 bytes.
type EbpfAffinityKey struct {
	// ClientAddr is the client source IP in network byte order.
	ClientAddr uint32
	// ServiceID references the target service.
	ServiceID uint32
}

// EbpfAffinityValue is the BPF map value for the cca_affinity LRU hash map.
// Layout: { backend_addr: u32, port: u16, _pad: u16, timestamp: u64 } = 16 bytes.
type EbpfAffinityValue struct {
	// BackendAddr is the selected backend IP in network byte order.
	BackendAddr uint32
	// Port is the selected backend port.
	Port uint16
	// Pad aligns the Timestamp field to an 8-byte boundary.
	Pad uint16
	// Timestamp is the BPF ktime_ns when the affinity entry was created.
	Timestamp uint64
}

// EbpfReverseNatKey is the BPF map key for the cca_reverse_nat LRU hash map.
// Keyed by socket cookie + backend to avoid collisions when services share
// backends. Layout: { cookie: u64, addr: u32, port: u16, pad: u16 } = 16 bytes.
type EbpfReverseNatKey struct {
	// SocketCookie is the per-socket unique ID from bpf_get_socket_cookie().
	SocketCookie uint64
	// BackendAddr is the backend IP (same byte repr as user_ip4).
	BackendAddr uint32
	// BackendPort is the backend port in network byte order.
	BackendPort uint16
	// Pad aligns the struct to 16 bytes total.
	Pad uint16
}

// EbpfReverseNatValue is the BPF map value for the cca_reverse_nat LRU hash
// map. Layout: { vip_addr: u32, vip_port: u16, _pad: u16 } = 8 bytes.
type EbpfReverseNatValue struct {
	// VIPAddr is the original service VIP (same byte repr as user_ip4).
	VIPAddr uint32
	// VIPPort is the original service port in network byte order.
	VIPPort uint16
	// Pad aligns the struct to 8 bytes total.
	Pad uint16
}

// ipToUint32 converts a dotted-decimal IPv4 string to the uint32
// representation used by bpf_sock_addr.user_ip4. The kernel stores
// sin_addr.s_addr (__be32 bytes) loaded as a native uint32, so we use
// NativeEndian to produce the same integer value. Returns 0 on invalid input.
func ipToUint32(ipAddress string) uint32 {
	parsedIP := net.ParseIP(ipAddress)
	if parsedIP == nil {
		return 0
	}
	ipv4Bytes := parsedIP.To4()
	if ipv4Bytes == nil {
		return 0
	}
	return binary.NativeEndian.Uint32(ipv4Bytes)
}

// uint32ToIP converts a BPF user_ip4-style uint32 back to a dotted-decimal
// IPv4 string for logging and diagnostics.
func uint32ToIP(bpfAddr uint32) string {
	ipBytes := make([]byte, 4)
	binary.NativeEndian.PutUint32(ipBytes, bpfAddr)
	return fmt.Sprintf("%d.%d.%d.%d", ipBytes[0], ipBytes[1], ipBytes[2], ipBytes[3])
}

// ipBytesToUint32 converts a 4-byte IP mask or address to the uint32
// representation used by BPF maps. Uses NativeEndian to match the kernel's
// in-memory layout of __be32 loaded as a native integer.
func ipBytesToUint32(ipBytes []byte) uint32 {
	if len(ipBytes) < 4 {
		return 0
	}
	return binary.NativeEndian.Uint32(ipBytes[:4])
}

// protocolToNumber converts a protocol name ("tcp" or "udp") to its IANA
// protocol number. Defaults to TCP (6) for unrecognized protocol names.
func protocolToNumber(protocolName string) uint8 {
	switch protocolName {
	case "udp":
		return ebpfProtocolUDP
	case "tcp":
		return ebpfProtocolTCP
	default:
		return ebpfProtocolTCP
	}
}
