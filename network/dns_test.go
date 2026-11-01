// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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
	binary.BigEndian.PutUint16(header[4:6], 1)      // QDCOUNT = 1

	// Question: labels + null + QTYPE(A=1) + QCLASS(IN=1)
	var questionSection []byte
	for _, label := range splitDomainIntoLabels(domainName) {
		questionSection = append(questionSection, byte(len(label))) //nolint:gosec // test uses small values
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

// splitDomainIntoLabels splits "web.cca.local" into ["web", "ccattler", "local"].
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
// "web.cca.local" returns the service's VIP as an A record.
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
	queryPacket := buildDNSARecordQuery(0x1234, "web.cca.local")
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
	queryPacket := buildDNSARecordQuery(0x5678, "unknown.cca.local")
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
// domains outside *.cca.local return NXDOMAIN.
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

// buildDNSQuery constructs a DNS query packet for the given domain, query type,
// and transaction ID. queryType: 1=A, 28=AAAA, 5=CNAME, 255=ANY.
func buildDNSQuery(transactionID uint16, domainName string, queryType uint16) []byte {
	header := make([]byte, 12)
	binary.BigEndian.PutUint16(header[0:2], transactionID)
	binary.BigEndian.PutUint16(header[2:4], 0x0100)
	binary.BigEndian.PutUint16(header[4:6], 1)

	var questionSection []byte
	for _, label := range splitDomainIntoLabels(domainName) {
		questionSection = append(questionSection, byte(len(label))) //nolint:gosec
		questionSection = append(questionSection, []byte(label)...)
	}
	questionSection = append(questionSection, 0)

	typeAndClass := make([]byte, 4)
	binary.BigEndian.PutUint16(typeAndClass[0:2], queryType)
	binary.BigEndian.PutUint16(typeAndClass[2:4], 1) // IN
	questionSection = append(questionSection, typeAndClass...)

	return append(header, questionSection...)
}

// startTestDNSServer creates a DNS server with the given facts and returns its address.
func startTestDNSServer(t *testing.T, ctx context.Context, facts map[string]string) string {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { factStore.Close() })

	for factKey, factValue := range facts {
		factStore.Put(ctx, factKey, []byte(factValue))
	}

	resolver := NewStoreBackedResolver(factStore)
	dnsServer := NewDNSServer(resolver, "127.0.0.1:0")
	go dnsServer.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	return "127.0.0.1:" + intToString(dnsServer.ListenPort())
}

// TestDNSServerAAAAQueryReturnsEmptyResponse verifies that AAAA queries get a
// valid empty response (NOERROR, ANCOUNT=0) instead of a malformed A record.
func TestDNSServerAAAAQueryReturnsEmptyResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverAddress := startTestDNSServer(t, ctx, map[string]string{
		types.KeyNetworkVIPService("web"): "10.200.0.1",
	})

	queryPacket := buildDNSQuery(0xAAAA, "web.cca.local", 28) // AAAA
	responsePacket, err := sendDNSQueryAndReadResponse(serverAddress, queryPacket)
	if err != nil {
		t.Fatal(err)
	}

	responseFlags := binary.BigEndian.Uint16(responsePacket[2:4])
	responseCode := responseFlags & 0x000F
	if responseCode != 0 {
		t.Errorf("RCODE = %d, want 0 (NOERROR) for AAAA query", responseCode)
	}

	answerCount := binary.BigEndian.Uint16(responsePacket[6:8])
	if answerCount != 0 {
		t.Errorf("ANCOUNT = %d, want 0 for AAAA query", answerCount)
	}
}

// TestDNSServerAAAAFollowedByAQuery verifies that a resolver sending both AAAA
// and A queries gets the correct result — the scenario that broke Airflow.
func TestDNSServerAAAAFollowedByAQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverAddress := startTestDNSServer(t, ctx, map[string]string{
		types.KeyNetworkVIPService("metadata-db"): "10.200.0.5",
	})

	// Send AAAA first (like glibc resolver does).
	aaaaQuery := buildDNSQuery(0x0001, "metadata-db.cca.local", 28)
	aaaaResponse, err := sendDNSQueryAndReadResponse(serverAddress, aaaaQuery)
	if err != nil {
		t.Fatal("AAAA query failed:", err)
	}
	aaaaAnswerCount := binary.BigEndian.Uint16(aaaaResponse[6:8])
	if aaaaAnswerCount != 0 {
		t.Errorf("AAAA ANCOUNT = %d, want 0", aaaaAnswerCount)
	}

	// Then A query (should succeed).
	aQuery := buildDNSARecordQuery(0x0002, "metadata-db.cca.local")
	aResponse, err := sendDNSQueryAndReadResponse(serverAddress, aQuery)
	if err != nil {
		t.Fatal("A query failed after AAAA:", err)
	}
	aAnswerCount := binary.BigEndian.Uint16(aResponse[6:8])
	if aAnswerCount != 1 {
		t.Fatalf("A ANCOUNT = %d, want 1", aAnswerCount)
	}
	responseIP := net.IP(aResponse[len(aResponse)-4:])
	if responseIP.String() != "10.200.0.5" {
		t.Errorf("A record IP = %s, want 10.200.0.5", responseIP)
	}
}

// TestDNSServerUnsupportedQueryTypesReturnEmpty verifies that CNAME, MX, ANY
// and other non-A query types return valid empty responses.
func TestDNSServerUnsupportedQueryTypesReturnEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverAddress := startTestDNSServer(t, ctx, map[string]string{
		types.KeyNetworkVIPService("web"): "10.200.0.1",
	})

	unsupportedTypes := []struct {
		name      string
		queryType uint16
	}{
		{"AAAA", 28},
		{"CNAME", 5},
		{"MX", 15},
		{"ANY", 255},
		{"SRV", 33},
	}

	for _, queryTypeEntry := range unsupportedTypes {
		t.Run(queryTypeEntry.name, func(t *testing.T) {
			queryPacket := buildDNSQuery(0xBBBB, "web.cca.local", queryTypeEntry.queryType)
			responsePacket, err := sendDNSQueryAndReadResponse(serverAddress, queryPacket)
			if err != nil {
				t.Fatalf("%s query failed: %v", queryTypeEntry.name, err)
			}

			responseFlags := binary.BigEndian.Uint16(responsePacket[2:4])
			responseCode := responseFlags & 0x000F
			if responseCode != 0 {
				t.Errorf("%s RCODE = %d, want 0 (NOERROR)", queryTypeEntry.name, responseCode)
			}

			answerCount := binary.BigEndian.Uint16(responsePacket[6:8])
			if answerCount != 0 {
				t.Errorf("%s ANCOUNT = %d, want 0", queryTypeEntry.name, answerCount)
			}
		})
	}
}

// TestDNSServerHostnameFallback verifies that when a VIP doesn't exist for a
// name but a hostname DNS entry does, the DNS server returns the hostname IP.
func TestDNSServerHostnameFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverAddress := startTestDNSServer(t, ctx, map[string]string{
		types.KeyNetworkDNSHostname("worker-abc123"): "10.100.1.5",
	})

	queryPacket := buildDNSARecordQuery(0xDDDD, "worker-abc123.cca.local")
	responsePacket, err := sendDNSQueryAndReadResponse(serverAddress, queryPacket)
	if err != nil {
		t.Fatal(err)
	}

	answerCount := binary.BigEndian.Uint16(responsePacket[6:8])
	if answerCount != 1 {
		t.Fatalf("ANCOUNT = %d, want 1 for hostname fallback", answerCount)
	}

	responseIP := net.IP(responsePacket[len(responsePacket)-4:])
	if responseIP.String() != "10.100.1.5" {
		t.Errorf("hostname IP = %s, want 10.100.1.5", responseIP)
	}
}

// TestDNSServerVIPTakesPrecedenceOverHostname verifies that when both a VIP and
// hostname exist for the same name, the VIP is returned.
func TestDNSServerVIPTakesPrecedenceOverHostname(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverAddress := startTestDNSServer(t, ctx, map[string]string{
		types.KeyNetworkVIPService("web"):  "10.200.0.1",
		types.KeyNetworkDNSHostname("web"): "10.100.1.99",
	})

	queryPacket := buildDNSARecordQuery(0xEEEE, "web.cca.local")
	responsePacket, err := sendDNSQueryAndReadResponse(serverAddress, queryPacket)
	if err != nil {
		t.Fatal(err)
	}

	answerCount := binary.BigEndian.Uint16(responsePacket[6:8])
	if answerCount != 1 {
		t.Fatalf("ANCOUNT = %d, want 1", answerCount)
	}

	responseIP := net.IP(responsePacket[len(responsePacket)-4:])
	if responseIP.String() != "10.200.0.1" {
		t.Errorf("IP = %s, want VIP 10.200.0.1 (not hostname IP)", responseIP)
	}
}

// TestDNSServerConcurrentQueries verifies that multiple concurrent A and AAAA
// queries are handled correctly without panics or data races.
func TestDNSServerConcurrentQueries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverAddress := startTestDNSServer(t, ctx, map[string]string{
		types.KeyNetworkVIPService("web"):            "10.200.0.1",
		types.KeyNetworkVIPService("api"):            "10.200.0.2",
		types.KeyNetworkDNSHostname("worker-abc123"): "10.100.1.5",
	})

	type queryResult struct {
		err error
	}

	resultChannel := make(chan queryResult, 30)

	for queryIndex := 0; queryIndex < 10; queryIndex++ {
		go func() {
			query := buildDNSARecordQuery(0x1111, "web.cca.local")
			_, err := sendDNSQueryAndReadResponse(serverAddress, query)
			resultChannel <- queryResult{err: err}
		}()
		go func() {
			query := buildDNSQuery(0x2222, "api.cca.local", 28) // AAAA
			_, err := sendDNSQueryAndReadResponse(serverAddress, query)
			resultChannel <- queryResult{err: err}
		}()
		go func() {
			query := buildDNSARecordQuery(0x3333, "worker-abc123.cca.local")
			_, err := sendDNSQueryAndReadResponse(serverAddress, query)
			resultChannel <- queryResult{err: err}
		}()
	}

	failureCount := 0
	for resultIndex := 0; resultIndex < 30; resultIndex++ {
		result := <-resultChannel
		if result.err != nil {
			failureCount++
		}
	}
	if failureCount > 0 {
		t.Errorf("%d/30 concurrent DNS queries failed", failureCount)
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
