// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
)

// dnsCacheMaxTTL is the maximum TTL for cached DNS responses. Matches the
// 30-second TTL used by the control plane DNSServer's A records.
const dnsCacheMaxTTL = 30 * time.Second

// dnsForwarderReadTimeout is the deadline for reading a response from an
// upstream DNS server.
const dnsForwarderReadTimeout = 3 * time.Second

// dnsForwarderMaxCacheEntries prevents unbounded memory growth from diverse
// query patterns.
const dnsForwarderMaxCacheEntries = 1000

// DNSForwarder is a caching DNS forwarder that runs on each agent node. It
// listens on the agent's advertise address, port 53 (UDP), and serves as
// the single DNS endpoint for containers and connection probes.
//
// Queries for *.cca.local are forwarded to the control plane DNS server.
// All other queries are forwarded to the system DNS resolver. Responses
// are cached with TTL-based expiry.
type DNSForwarder struct {
	// listenAddress is the UDP address to bind (e.g. "192.168.124.20:53").
	listenAddress string
	// ccaUpstream is the control plane DNS address (e.g. "192.168.124.10:15353").
	ccaUpstream string
	// systemUpstream is the fallback DNS address for non-cca.local queries.
	systemUpstream string
	// domain is the DNS suffix to match (default "cca.local").
	domain string
	// cache stores recent DNS responses keyed by query name.
	cache *dnsCache
	// mutex protects the UDP connection during concurrent access.
	mutex sync.Mutex
	// udpConnection is the active UDP listener.
	udpConnection *net.UDPConn
}

// dnsCacheEntry holds a single cached DNS response with its expiry time.
type dnsCacheEntry struct {
	// responsePacket is the raw DNS response (txid will be patched per query).
	responsePacket []byte
	// expiresAt is when this cache entry becomes stale.
	expiresAt time.Time
}

// dnsCache is a simple TTL-based cache for DNS responses. Entries expire
// based on the TTL extracted from the DNS response.
type dnsCache struct {
	// mutex protects concurrent cache access.
	mutex sync.RWMutex
	// entries maps lowercase query names to cached responses.
	entries map[string]*dnsCacheEntry
}

// NewDNSForwarder creates a caching DNS forwarder. The listenAddress is the
// local UDP address to bind (e.g. "10.0.0.5:53"). The ccaUpstream is the
// control plane DNS for *.cca.local queries. The systemUpstream is used for
// all other queries.
func NewDNSForwarder(listenAddress string, ccaUpstream string, systemUpstream string) *DNSForwarder {
	return &DNSForwarder{
		listenAddress:  listenAddress,
		ccaUpstream:    ccaUpstream,
		systemUpstream: systemUpstream,
		domain:         DefaultDNSDomain,
		cache: &dnsCache{
			entries: make(map[string]*dnsCacheEntry),
		},
	}
}

// Start begins listening for DNS queries on the configured UDP address. It
// blocks until the provided context is cancelled. Each incoming query is
// handled in its own goroutine.
func (dnsForwarder *DNSForwarder) Start(ctx context.Context) error {
	udpAddress, resolveError := net.ResolveUDPAddr("udp", dnsForwarder.listenAddress)
	if resolveError != nil {
		return fmt.Errorf("resolving DNS forwarder listen address: %w", resolveError)
	}

	udpConnection, listenError := net.ListenUDP("udp", udpAddress)
	if listenError != nil {
		return fmt.Errorf("starting DNS forwarder listener: %w", listenError)
	}

	dnsForwarder.mutex.Lock()
	dnsForwarder.udpConnection = udpConnection
	dnsForwarder.mutex.Unlock()

	logging.Default().Info("DNS forwarder listening",
		"address", dnsForwarder.listenAddress,
		"cca_upstream", dnsForwarder.ccaUpstream,
		"system_upstream", dnsForwarder.systemUpstream)

	go func() {
		<-ctx.Done()
		_ = udpConnection.Close()
	}()

	packetBuffer := make([]byte, dnsReadBufferSize)
	for {
		bytesRead, remoteAddress, readError := udpConnection.ReadFromUDP(packetBuffer)
		if readError != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		queryPacket := make([]byte, bytesRead)
		copy(queryPacket, packetBuffer[:bytesRead])
		go dnsForwarder.handleForwardedQuery(ctx, udpConnection, remoteAddress, queryPacket)
	}
}

// ListenPort returns the actual port the DNS forwarder is listening on. This
// is useful in tests where port 0 is used for automatic assignment.
func (dnsForwarder *DNSForwarder) ListenPort() int {
	dnsForwarder.mutex.Lock()
	defer dnsForwarder.mutex.Unlock()
	if dnsForwarder.udpConnection == nil {
		return 0
	}
	localAddress, _ := dnsForwarder.udpConnection.LocalAddr().(*net.UDPAddr)
	return localAddress.Port
}

// handleForwardedQuery processes a single DNS query by checking the cache,
// determining the upstream server, forwarding the query, caching the
// response, and relaying it back to the client.
func (dnsForwarder *DNSForwarder) handleForwardedQuery(ctx context.Context, connection *net.UDPConn, remoteAddress *net.UDPAddr, queryPacket []byte) {
	if len(queryPacket) < 12 {
		return
	}

	queryName, _ := parseDNSQuestionName(queryPacket, 12)
	if queryName == "" {
		return
	}
	queryNameLower := strings.ToLower(queryName)

	if cachedResponse := dnsForwarder.cache.get(queryNameLower); cachedResponse != nil {
		patchedResponse := make([]byte, len(cachedResponse))
		copy(patchedResponse, cachedResponse)
		copy(patchedResponse[0:2], queryPacket[0:2])
		_, _ = connection.WriteToUDP(patchedResponse, remoteAddress)
		return
	}

	upstreamAddress := dnsForwarder.systemUpstream
	if strings.HasSuffix(queryNameLower, "."+dnsForwarder.domain) {
		upstreamAddress = dnsForwarder.ccaUpstream
	}

	responsePacket, forwardError := dnsForwarder.forwardToUpstream(ctx, upstreamAddress, queryPacket)
	if forwardError != nil {
		logging.Default().Warn("DNS forwarder: upstream failed",
			"query", queryNameLower,
			"upstream", upstreamAddress,
			"error", forwardError.Error())
		return
	}

	responseTTL := extractTTLFromDNSResponse(responsePacket)
	if responseTTL > 0 {
		dnsForwarder.cache.put(queryNameLower, responsePacket, responseTTL)
	}

	patchedResponse := make([]byte, len(responsePacket))
	copy(patchedResponse, responsePacket)
	copy(patchedResponse[0:2], queryPacket[0:2])
	_, _ = connection.WriteToUDP(patchedResponse, remoteAddress)
}

// forwardToUpstream sends a DNS query packet to the given upstream address
// and reads the response. Returns the raw response packet.
func (dnsForwarder *DNSForwarder) forwardToUpstream(ctx context.Context, upstreamAddress string, queryPacket []byte) ([]byte, error) {
	upstreamUDP, resolveError := net.ResolveUDPAddr("udp", upstreamAddress)
	if resolveError != nil {
		return nil, fmt.Errorf("resolving upstream %s: %w", upstreamAddress, resolveError)
	}

	upstreamConnection, dialError := net.DialUDP("udp", nil, upstreamUDP)
	if dialError != nil {
		return nil, fmt.Errorf("dialing upstream %s: %w", upstreamAddress, dialError)
	}
	defer func() { _ = upstreamConnection.Close() }()

	_ = upstreamConnection.SetDeadline(time.Now().Add(dnsForwarderReadTimeout))

	if _, writeError := upstreamConnection.Write(queryPacket); writeError != nil {
		return nil, fmt.Errorf("writing to upstream: %w", writeError)
	}

	responseBuffer := make([]byte, dnsReadBufferSize)
	bytesRead, readError := upstreamConnection.Read(responseBuffer)
	if readError != nil {
		return nil, fmt.Errorf("reading from upstream: %w", readError)
	}

	response := make([]byte, bytesRead)
	copy(response, responseBuffer[:bytesRead])
	return response, nil
}

// extractTTLFromDNSResponse reads the TTL from the first answer record in a
// DNS response packet. Returns 0 if the response has no answer section or
// cannot be parsed. Caps at dnsCacheMaxTTL.
func extractTTLFromDNSResponse(responsePacket []byte) time.Duration {
	if len(responsePacket) < 12 {
		return 0
	}

	answerCount := binary.BigEndian.Uint16(responsePacket[6:8])
	if answerCount == 0 {
		return 0
	}

	// Skip the question section to find the first answer record.
	currentOffset := 12
	_, questionEndOffset := parseDNSQuestionName(responsePacket, currentOffset)
	if questionEndOffset == 0 {
		return 0
	}
	currentOffset = questionEndOffset + 4 // skip QTYPE (2) + QCLASS (2)

	// Parse the answer record name (may be a pointer).
	if currentOffset >= len(responsePacket) {
		return 0
	}
	if responsePacket[currentOffset]&0xC0 == 0xC0 {
		currentOffset += 2 // name pointer (2 bytes)
	} else {
		_, nameEnd := parseDNSQuestionName(responsePacket, currentOffset)
		if nameEnd == 0 {
			return 0
		}
		currentOffset = nameEnd
	}

	// TYPE (2) + CLASS (2) + TTL (4) = need at least 8 bytes after name.
	if currentOffset+8 > len(responsePacket) {
		return 0
	}

	ttlSeconds := binary.BigEndian.Uint32(responsePacket[currentOffset+4 : currentOffset+8])
	ttlDuration := time.Duration(ttlSeconds) * time.Second
	if ttlDuration > dnsCacheMaxTTL {
		ttlDuration = dnsCacheMaxTTL
	}
	return ttlDuration
}

// get returns a cached DNS response packet for the given query name, or nil
// if the entry is missing or expired.
func (dnsCache *dnsCache) get(queryName string) []byte {
	dnsCache.mutex.RLock()
	defer dnsCache.mutex.RUnlock()

	entry, exists := dnsCache.entries[queryName]
	if !exists || time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry.responsePacket
}

// put stores a DNS response packet in the cache with the given TTL. Evicts
// expired entries when the cache exceeds dnsForwarderMaxCacheEntries.
func (dnsCache *dnsCache) put(queryName string, responsePacket []byte, ttlDuration time.Duration) {
	dnsCache.mutex.Lock()
	defer dnsCache.mutex.Unlock()

	if len(dnsCache.entries) >= dnsForwarderMaxCacheEntries {
		currentTime := time.Now()
		for entryName, entry := range dnsCache.entries {
			if currentTime.After(entry.expiresAt) {
				delete(dnsCache.entries, entryName)
			}
		}
		if len(dnsCache.entries) >= dnsForwarderMaxCacheEntries {
			for entryName := range dnsCache.entries {
				delete(dnsCache.entries, entryName)
				break
			}
		}
	}

	storedPacket := make([]byte, len(responsePacket))
	copy(storedPacket, responsePacket)
	dnsCache.entries[queryName] = &dnsCacheEntry{
		responsePacket: storedPacket,
		expiresAt:      time.Now().Add(ttlDuration),
	}
}

// ParseSystemResolvers reads /etc/resolv.conf and returns the first
// nameserver that is not the given excludeAddress. This prevents a
// forwarding loop when the host's resolv.conf points to the forwarder's
// own address.
func ParseSystemResolvers(resolvConfPath string, excludeAddress string) string {
	resolvFile, openError := os.Open(resolvConfPath) // #nosec G304 — path is a well-known system config file
	if openError != nil {
		return "127.0.0.1:53"
	}
	defer func() { _ = resolvFile.Close() }()

	scanner := bufio.NewScanner(resolvFile)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		nameserverAddress := fields[1]
		if nameserverAddress == excludeAddress {
			continue
		}
		if !strings.Contains(nameserverAddress, ":") {
			nameserverAddress = net.JoinHostPort(nameserverAddress, "53")
		}
		return nameserverAddress
	}

	return "127.0.0.1:53"
}
