package network

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// buildDNSARecordQuery constructs a minimal DNS query packet for an A record
// lookup of the given domain name. Used by DNS server tests.
func buildDNSARecordQuery(transactionID uint16, domainName string) []byte {
	// Header: 12 bytes
	header := make([]byte, 12)
	binary.BigEndian.PutUint16(header[0:2], transactionID)
	binary.BigEndian.PutUint16(header[2:4], 0x0100) // standard query, recursion desired
	binary.BigEndian.PutUint16(header[4:6], 1)       // QDCOUNT = 1

	// Question: labels + null + QTYPE(A=1) + QCLASS(IN=1)
	var questionSection []byte
	for _, label := range splitDomainIntoLabels(domainName) {
		questionSection = append(questionSection, byte(len(label)))
		questionSection = append(questionSection, []byte(label)...)
	}
	questionSection = append(questionSection, 0) // null terminator

	// QTYPE = A (1), QCLASS = IN (1)
	typeAndClass := make([]byte, 4)
	binary.BigEndian.PutUint16(typeAndClass[0:2], 1) // A
	binary.BigEndian.PutUint16(typeAndClass[2:4], 1) // IN
	questionSection = append(questionSection, typeAndClass...)

	return append(header, questionSection...)
}

// splitDomainIntoLabels splits "web.ccattler.local" into ["web", "ccattler", "local"].
func splitDomainIntoLabels(domainName string) []string {
	var labels []string
	current := ""
	for _, character := range domainName {
		if character == '.' {
			if current != "" {
				labels = append(labels, current)
				current = ""
			}
		} else {
			current += string(character)
		}
	}
	if current != "" {
		labels = append(labels, current)
	}
	return labels
}

// sendDNSQueryAndReadResponse sends a DNS query to the given address and
// returns the response packet.
func sendDNSQueryAndReadResponse(serverAddress string, queryPacket []byte) ([]byte, error) {
	udpAddress, _ := net.ResolveUDPAddr("udp", serverAddress)
	connection, err := net.DialUDP("udp", nil, udpAddress)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(2 * time.Second))

	_, err = connection.Write(queryPacket)
	if err != nil {
		return nil, err
	}

	responseBuffer := make([]byte, 512)
	bytesRead, err := connection.Read(responseBuffer)
	if err != nil {
		return nil, err
	}
	return responseBuffer[:bytesRead], nil
}

// TestDNSServerResolvesServiceToVIP verifies that querying for
// "web.ccattler.local" returns the service's VIP as an A record.
func TestDNSServerResolvesServiceToVIP(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factStore.Put(ctx, types.KeyNetworkVIPService("web"), []byte("10.200.0.1"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	dnsServer := NewDNSServer(storeBackedResolver, "127.0.0.1:0")

	go dnsServer.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	serverAddress := "127.0.0.1:" + intToString(dnsServer.ListenPort())
	queryPacket := buildDNSARecordQuery(0x1234, "web.ccattler.local")
	responsePacket, err := sendDNSQueryAndReadResponse(serverAddress, queryPacket)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's a response with ANCOUNT=1.
	if len(responsePacket) < 12 {
		t.Fatal("response too short")
	}
	responseTransactionID := binary.BigEndian.Uint16(responsePacket[0:2])
	if responseTransactionID != 0x1234 {
		t.Errorf("transaction ID = 0x%04x, want 0x1234", responseTransactionID)
	}
	answerCount := binary.BigEndian.Uint16(responsePacket[6:8])
	if answerCount != 1 {
		t.Fatalf("ANCOUNT = %d, want 1", answerCount)
	}

	// Extract the A record IP from the last 4 bytes of the response.
	responseIP := net.IP(responsePacket[len(responsePacket)-4:])
	if responseIP.String() != "10.200.0.1" {
		t.Errorf("A record IP = %s, want 10.200.0.1", responseIP)
	}
}

// TestDNSServerReturnsNXDOMAINForUnknownService verifies that querying for
// a service that has no VIP returns an NXDOMAIN response.
func TestDNSServerReturnsNXDOMAINForUnknownService(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storeBackedResolver := NewStoreBackedResolver(factStore)
	dnsServer := NewDNSServer(storeBackedResolver, "127.0.0.1:0")

	go dnsServer.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	serverAddress := "127.0.0.1:" + intToString(dnsServer.ListenPort())
	queryPacket := buildDNSARecordQuery(0x5678, "unknown.ccattler.local")
	responsePacket, err := sendDNSQueryAndReadResponse(serverAddress, queryPacket)
	if err != nil {
		t.Fatal(err)
	}

	// Check RCODE = NXDOMAIN (3) in the flags field.
	responseFlags := binary.BigEndian.Uint16(responsePacket[2:4])
	responseCode := responseFlags & 0x000F
	if responseCode != 3 {
		t.Errorf("RCODE = %d, want 3 (NXDOMAIN)", responseCode)
	}

	answerCount := binary.BigEndian.Uint16(responsePacket[6:8])
	if answerCount != 0 {
		t.Errorf("ANCOUNT = %d, want 0 for NXDOMAIN", answerCount)
	}
}

// TestDNSServerReturnsNXDOMAINForWrongDomain verifies that queries for
// domains outside *.ccattler.local return NXDOMAIN.
func TestDNSServerReturnsNXDOMAINForWrongDomain(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factStore.Put(ctx, types.KeyNetworkVIPService("web"), []byte("10.200.0.1"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	dnsServer := NewDNSServer(storeBackedResolver, "127.0.0.1:0")

	go dnsServer.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	serverAddress := "127.0.0.1:" + intToString(dnsServer.ListenPort())
	queryPacket := buildDNSARecordQuery(0x9ABC, "web.example.com")
	responsePacket, err := sendDNSQueryAndReadResponse(serverAddress, queryPacket)
	if err != nil {
		t.Fatal(err)
	}

	responseFlags := binary.BigEndian.Uint16(responsePacket[2:4])
	responseCode := responseFlags & 0x000F
	if responseCode != 3 {
		t.Errorf("RCODE = %d, want 3 (NXDOMAIN) for wrong domain", responseCode)
	}
}

// intToString converts an integer to its string representation without
// importing strconv in this test file.
func intToString(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	remaining := value
	for remaining > 0 {
		digits = string(rune('0'+remaining%10)) + digits
		remaining /= 10
	}
	return digits
}
