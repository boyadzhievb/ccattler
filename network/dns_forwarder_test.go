// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// startMockDNSUpstream creates a UDP server that replies to every query with a
// single A record pointing to the given IP. Returns the listener address and a
// channel that receives every query name seen.
func startMockDNSUpstream(t *testing.T, responseIP net.IP, responseTTL uint32) (string, chan string) {
	t.Helper()
	udpAddress, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	connection, listenError := net.ListenUDP("udp", udpAddress)
	if listenError != nil {
		t.Fatalf("mock upstream listen: %v", listenError)
	}
	queriesReceived := make(chan string, 64)
	t.Cleanup(func() { connection.Close() })

	go func() {
		buffer := make([]byte, 512)
		for {
			bytesRead, remoteAddress, readError := connection.ReadFromUDP(buffer)
			if readError != nil {
				return
			}
			queryPacket := make([]byte, bytesRead)
			copy(queryPacket, buffer[:bytesRead])

			queryName, questionEndOffset := parseDNSQuestionName(queryPacket, 12)
			if queryName != "" {
				queriesReceived <- queryName
			}

			response := buildMockDNSResponse(queryPacket, questionEndOffset, responseIP, responseTTL)
			_, _ = connection.WriteToUDP(response, remoteAddress)
		}
	}()

	return connection.LocalAddr().String(), queriesReceived
}

// buildMockDNSResponse constructs a DNS response with one A record.
func buildMockDNSResponse(queryPacket []byte, questionEndOffset int, responseIP net.IP, ttlSeconds uint32) []byte {
	header := make([]byte, 12)
	copy(header[0:2], queryPacket[0:2])
	binary.BigEndian.PutUint16(header[2:4], 0x8180) // response, no error
	binary.BigEndian.PutUint16(header[4:6], 1)      // QDCOUNT
	binary.BigEndian.PutUint16(header[6:8], 1)      // ANCOUNT

	questionSection := queryPacket[12 : questionEndOffset+4]

	var answerSection []byte
	answerSection = append(answerSection, 0xC0, 0x0C) // pointer to question name
	answerSection = append(answerSection, 0, 1)       // TYPE A
	answerSection = append(answerSection, 0, 1)       // CLASS IN
	ttlBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(ttlBytes, ttlSeconds)
	answerSection = append(answerSection, ttlBytes...)
	answerSection = append(answerSection, 0, 4) // RDLENGTH
	answerSection = append(answerSection, responseIP.To4()...)

	packet := make([]byte, 0, len(header)+len(questionSection)+len(answerSection))
	packet = append(packet, header...)
	packet = append(packet, questionSection...)
	packet = append(packet, answerSection...)
	return packet
}

func TestDNSForwarder_RouteCCALocalToUpstream(t *testing.T) {
	ccaUpstreamAddress, ccaQueries := startMockDNSUpstream(t, net.IPv4(10, 200, 0, 1), 30)
	systemUpstreamAddress, systemQueries := startMockDNSUpstream(t, net.IPv4(8, 8, 8, 8), 60)

	forwarder := NewDNSForwarder("127.0.0.1:0", ccaUpstreamAddress, systemUpstreamAddress)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = forwarder.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	forwarderAddress := net.JoinHostPort("127.0.0.1", formatPort(forwarder.ListenPort()))

	queryPacket := buildDNSARecordQuery(0x1234, "metadata-db.cca.local")
	responsePacket := sendUDPQuery(t, forwarderAddress, queryPacket)

	if len(responsePacket) < 12 {
		t.Fatal("response too short")
	}

	select {
	case queryName := <-ccaQueries:
		if queryName != "metadata-db.cca.local" {
			t.Errorf("cca upstream received wrong query: %s", queryName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cca upstream did not receive query")
	}

	select {
	case queryName := <-systemQueries:
		t.Errorf("system upstream should not receive cca.local queries, got: %s", queryName)
	default:
	}

	txid := binary.BigEndian.Uint16(responsePacket[0:2])
	if txid != 0x1234 {
		t.Errorf("response txid = %#04x, want 0x1234", txid)
	}
}

func TestDNSForwarder_RouteNonCCAToSystemUpstream(t *testing.T) {
	ccaUpstreamAddress, ccaQueries := startMockDNSUpstream(t, net.IPv4(10, 200, 0, 1), 30)
	systemUpstreamAddress, systemQueries := startMockDNSUpstream(t, net.IPv4(8, 8, 8, 8), 60)

	forwarder := NewDNSForwarder("127.0.0.1:0", ccaUpstreamAddress, systemUpstreamAddress)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = forwarder.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	forwarderAddress := net.JoinHostPort("127.0.0.1", formatPort(forwarder.ListenPort()))

	queryPacket := buildDNSARecordQuery(0x5678, "example.com")
	sendUDPQuery(t, forwarderAddress, queryPacket)

	select {
	case queryName := <-systemQueries:
		if queryName != "example.com" {
			t.Errorf("system upstream received wrong query: %s", queryName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("system upstream did not receive query")
	}

	select {
	case queryName := <-ccaQueries:
		t.Errorf("cca upstream should not receive non-cca queries, got: %s", queryName)
	default:
	}
}

func TestDNSForwarder_CacheHit(t *testing.T) {
	ccaUpstreamAddress, ccaQueries := startMockDNSUpstream(t, net.IPv4(10, 200, 0, 1), 30)
	systemUpstreamAddress, _ := startMockDNSUpstream(t, net.IPv4(8, 8, 8, 8), 60)

	forwarder := NewDNSForwarder("127.0.0.1:0", ccaUpstreamAddress, systemUpstreamAddress)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = forwarder.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	forwarderAddress := net.JoinHostPort("127.0.0.1", formatPort(forwarder.ListenPort()))

	queryPacket1 := buildDNSARecordQuery(0x0001, "redis.cca.local")
	sendUDPQuery(t, forwarderAddress, queryPacket1)

	select {
	case <-ccaQueries:
	case <-time.After(2 * time.Second):
		t.Fatal("first query not forwarded")
	}

	queryPacket2 := buildDNSARecordQuery(0x0002, "redis.cca.local")
	responsePacket := sendUDPQuery(t, forwarderAddress, queryPacket2)

	txid := binary.BigEndian.Uint16(responsePacket[0:2])
	if txid != 0x0002 {
		t.Errorf("cached response txid = %#04x, want 0x0002 (txid should be patched)", txid)
	}

	select {
	case <-ccaQueries:
		t.Error("second query should have been served from cache, but upstream received it")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDNSForwarder_CacheExpiry(t *testing.T) {
	ccaUpstreamAddress, ccaQueries := startMockDNSUpstream(t, net.IPv4(10, 200, 0, 1), 1)
	systemUpstreamAddress, _ := startMockDNSUpstream(t, net.IPv4(8, 8, 8, 8), 60)

	forwarder := NewDNSForwarder("127.0.0.1:0", ccaUpstreamAddress, systemUpstreamAddress)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = forwarder.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	forwarderAddress := net.JoinHostPort("127.0.0.1", formatPort(forwarder.ListenPort()))

	queryPacket := buildDNSARecordQuery(0x0010, "webserver.cca.local")
	sendUDPQuery(t, forwarderAddress, queryPacket)

	select {
	case <-ccaQueries:
	case <-time.After(2 * time.Second):
		t.Fatal("first query not forwarded")
	}

	time.Sleep(1100 * time.Millisecond)

	queryPacket2 := buildDNSARecordQuery(0x0011, "webserver.cca.local")
	sendUDPQuery(t, forwarderAddress, queryPacket2)

	select {
	case <-ccaQueries:
	case <-time.After(2 * time.Second):
		t.Fatal("second query should have been forwarded after cache expiry")
	}
}

func TestExtractTTLFromDNSResponse(t *testing.T) {
	queryPacket := buildDNSARecordQuery(0xABCD, "test.cca.local")
	_, questionEndOffset := parseDNSQuestionName(queryPacket, 12)
	response := buildMockDNSResponse(queryPacket, questionEndOffset, net.IPv4(10, 0, 0, 1), 15)

	ttlDuration := extractTTLFromDNSResponse(response)
	if ttlDuration != 15*time.Second {
		t.Errorf("TTL = %v, want 15s", ttlDuration)
	}
}

func TestExtractTTLFromDNSResponse_CapsAtMax(t *testing.T) {
	queryPacket := buildDNSARecordQuery(0xABCD, "test.cca.local")
	_, questionEndOffset := parseDNSQuestionName(queryPacket, 12)
	response := buildMockDNSResponse(queryPacket, questionEndOffset, net.IPv4(10, 0, 0, 1), 3600)

	ttlDuration := extractTTLFromDNSResponse(response)
	if ttlDuration != dnsCacheMaxTTL {
		t.Errorf("TTL = %v, want %v (capped)", ttlDuration, dnsCacheMaxTTL)
	}
}

func TestParseSystemResolvers_SkipsSelf(t *testing.T) {
	tempDir := t.TempDir()
	resolvPath := filepath.Join(tempDir, "resolv.conf")
	content := "nameserver 192.168.1.10\nnameserver 8.8.8.8\n"
	if writeError := os.WriteFile(resolvPath, []byte(content), 0600); writeError != nil {
		t.Fatal(writeError)
	}

	result := ParseSystemResolvers(resolvPath, "192.168.1.10")
	if result != "8.8.8.8:53" {
		t.Errorf("got %q, want %q", result, "8.8.8.8:53")
	}
}

func TestParseSystemResolvers_ReturnsFirstNonExcluded(t *testing.T) {
	tempDir := t.TempDir()
	resolvPath := filepath.Join(tempDir, "resolv.conf")
	content := "nameserver 10.0.0.1\nnameserver 8.8.4.4\n"
	if writeError := os.WriteFile(resolvPath, []byte(content), 0600); writeError != nil {
		t.Fatal(writeError)
	}

	result := ParseSystemResolvers(resolvPath, "192.168.1.99")
	if result != "10.0.0.1:53" {
		t.Errorf("got %q, want %q", result, "10.0.0.1:53")
	}
}

func TestParseSystemResolvers_FallbackOnMissing(t *testing.T) {
	result := ParseSystemResolvers("/nonexistent/resolv.conf", "")
	if result != "127.0.0.1:53" {
		t.Errorf("got %q, want %q", result, "127.0.0.1:53")
	}
}

// sendUDPQuery sends a DNS query to the given address and reads the response.
func sendUDPQuery(t *testing.T, address string, queryPacket []byte) []byte {
	t.Helper()
	udpAddress, _ := net.ResolveUDPAddr("udp", address)
	connection, dialError := net.DialUDP("udp", nil, udpAddress)
	if dialError != nil {
		t.Fatalf("dial %s: %v", address, dialError)
	}
	defer connection.Close()

	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, writeError := connection.Write(queryPacket); writeError != nil {
		t.Fatalf("write query: %v", writeError)
	}

	responseBuffer := make([]byte, 512)
	bytesRead, readError := connection.Read(responseBuffer)
	if readError != nil {
		t.Fatalf("read response: %v", readError)
	}
	return responseBuffer[:bytesRead]
}

// formatPort converts an int port to a string for net.JoinHostPort.
func formatPort(port int) string {
	return strconv.Itoa(port)
}
