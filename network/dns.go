package network

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
)

// DefaultDNSDomain is the DNS suffix used for service discovery. Queries for
// "<service>.ccattler.local" are resolved to the service's VIP.
const DefaultDNSDomain = "ccattler.local"

// DNSServer is a lightweight UDP DNS server that resolves service names to
// their virtual IP addresses by reading VIP facts from the state store via
// a ServiceResolver. It handles only A-record queries for the
// *.ccattler.local domain; all other queries receive NXDOMAIN.
type DNSServer struct {
	// resolver is used to look up VIP addresses for service names.
	resolver ServiceResolver
	// listenAddress is the UDP address the server binds to (e.g. ":15353").
	listenAddress string
	// domain is the DNS suffix to match (default "ccattler.local").
	domain string
	// mutex protects the connection during concurrent access and shutdown.
	mutex sync.Mutex
	// udpConnection is the active UDP listener, set after Start is called.
	udpConnection *net.UDPConn
}

// NewDNSServer creates a DNS server that resolves service names using the
// provided resolver. It listens on the given address (e.g. ":15353").
func NewDNSServer(resolver ServiceResolver, listenAddress string) *DNSServer {
	return &DNSServer{
		resolver:      resolver,
		listenAddress: listenAddress,
		domain:        DefaultDNSDomain,
	}
}

// Start begins listening for DNS queries on the configured UDP address. It
// blocks until the provided context is cancelled. Each incoming query is
// handled in its own goroutine.
func (dnsServer *DNSServer) Start(ctx context.Context) error {
	udpAddress, err := net.ResolveUDPAddr("udp", dnsServer.listenAddress)
	if err != nil {
		return fmt.Errorf("resolving DNS listen address: %w", err)
	}

	udpConnection, err := net.ListenUDP("udp", udpAddress)
	if err != nil {
		return fmt.Errorf("starting DNS listener: %w", err)
	}

	dnsServer.mutex.Lock()
	dnsServer.udpConnection = udpConnection
	dnsServer.mutex.Unlock()

	go func() {
		<-ctx.Done()
		udpConnection.Close()
	}()

	packetBuffer := make([]byte, 512)
	for {
		bytesRead, remoteAddress, err := udpConnection.ReadFromUDP(packetBuffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go dnsServer.handleDNSQuery(ctx, udpConnection, remoteAddress, packetBuffer[:bytesRead])
	}
}

// ListenPort returns the actual port the DNS server is listening on. This is
// useful in tests where port 0 is used for automatic assignment.
func (dnsServer *DNSServer) ListenPort() int {
	dnsServer.mutex.Lock()
	defer dnsServer.mutex.Unlock()
	if dnsServer.udpConnection == nil {
		return 0
	}
	return dnsServer.udpConnection.LocalAddr().(*net.UDPAddr).Port
}

// handleDNSQuery processes a single DNS query packet. It extracts the query
// name, checks if it matches a service in the ccattler.local domain, resolves
// the VIP, and sends back a DNS response with an A record.
func (dnsServer *DNSServer) handleDNSQuery(ctx context.Context, connection *net.UDPConn, remoteAddress *net.UDPAddr, queryPacket []byte) {
	if len(queryPacket) < 12 {
		return
	}

	transactionID := binary.BigEndian.Uint16(queryPacket[0:2])
	queryName, queryNameEndOffset := parseDNSQuestionName(queryPacket, 12)
	if queryName == "" {
		return
	}

	// Extract the service name by stripping the domain suffix.
	queryNameLower := strings.ToLower(queryName)
	domainSuffix := "." + dnsServer.domain
	if !strings.HasSuffix(queryNameLower, domainSuffix) {
		dnsServer.sendNXDomainResponse(connection, remoteAddress, transactionID, queryPacket[12:queryNameEndOffset+4])
		return
	}
	serviceName := queryNameLower[:len(queryNameLower)-len(domainSuffix)]

	virtualIP, err := dnsServer.resolver.ResolveVIP(ctx, serviceName)
	if err != nil || virtualIP == "" {
		dnsServer.sendNXDomainResponse(connection, remoteAddress, transactionID, queryPacket[12:queryNameEndOffset+4])
		return
	}

	parsedIP := net.ParseIP(virtualIP).To4()
	if parsedIP == nil {
		log.Printf("dns: invalid VIP for service %s: %s", serviceName, virtualIP)
		return
	}

	responsePacket := buildDNSARecordResponse(transactionID, queryPacket[12:queryNameEndOffset+4], parsedIP)
	connection.WriteToUDP(responsePacket, remoteAddress)
}

// sendNXDomainResponse sends a DNS NXDOMAIN (name not found) response.
func (dnsServer *DNSServer) sendNXDomainResponse(connection *net.UDPConn, remoteAddress *net.UDPAddr, transactionID uint16, questionSection []byte) {
	responseHeader := make([]byte, 12)
	binary.BigEndian.PutUint16(responseHeader[0:2], transactionID)
	// Flags: response (0x8000) | authoritative (0x0400) | NXDOMAIN rcode (0x0003)
	binary.BigEndian.PutUint16(responseHeader[2:4], 0x8403)
	binary.BigEndian.PutUint16(responseHeader[4:6], 1) // QDCOUNT = 1
	binary.BigEndian.PutUint16(responseHeader[6:8], 0) // ANCOUNT = 0

	responsePacket := append(responseHeader, questionSection...)
	connection.WriteToUDP(responsePacket, remoteAddress)
}

// parseDNSQuestionName extracts the domain name from a DNS question section
// starting at the given offset. Returns the name as a dotted string and the
// offset past the last label byte (before QTYPE/QCLASS).
func parseDNSQuestionName(packet []byte, startOffset int) (string, int) {
	var nameLabels []string
	currentOffset := startOffset

	for currentOffset < len(packet) {
		labelLength := int(packet[currentOffset])
		if labelLength == 0 {
			currentOffset++
			break
		}
		currentOffset++
		if currentOffset+labelLength > len(packet) {
			return "", 0
		}
		nameLabels = append(nameLabels, string(packet[currentOffset:currentOffset+labelLength]))
		currentOffset += labelLength
	}

	return strings.Join(nameLabels, "."), currentOffset
}

// buildDNSARecordResponse constructs a complete DNS response packet with a
// single A record pointing to the given IPv4 address.
func buildDNSARecordResponse(transactionID uint16, questionSection []byte, ipv4Address net.IP) []byte {
	responseHeader := make([]byte, 12)
	binary.BigEndian.PutUint16(responseHeader[0:2], transactionID)
	// Flags: response (0x8000) | authoritative (0x0400) | no error (0x0000)
	binary.BigEndian.PutUint16(responseHeader[2:4], 0x8400)
	binary.BigEndian.PutUint16(responseHeader[4:6], 1) // QDCOUNT = 1
	binary.BigEndian.PutUint16(responseHeader[6:8], 1) // ANCOUNT = 1

	// Answer section: pointer to name in question (0xC00C), type A (1),
	// class IN (1), TTL 30s, data length 4, IPv4 address.
	answerRecord := make([]byte, 16)
	binary.BigEndian.PutUint16(answerRecord[0:2], 0xC00C)  // name pointer to offset 12
	binary.BigEndian.PutUint16(answerRecord[2:4], 1)        // type A
	binary.BigEndian.PutUint16(answerRecord[4:6], 1)        // class IN
	binary.BigEndian.PutUint32(answerRecord[6:10], 30)      // TTL 30 seconds
	binary.BigEndian.PutUint16(answerRecord[10:12], 4)      // RDLENGTH = 4
	copy(answerRecord[12:16], ipv4Address)                   // RDATA = IPv4 address

	responsePacket := append(responseHeader, questionSection...)
	responsePacket = append(responsePacket, answerRecord...)
	return responsePacket
}
