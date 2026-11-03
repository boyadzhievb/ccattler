// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// node_debug_proxy.go serves /api/node/debug/{node}/{endpoint}: the only way
// users reach an agent's debug API (Flow 24). The API server authorizes the
// caller by capability, then forwards over mTLS with its own certificate.
package api

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/types"
)

// nodeDebugPathPrefix is the route prefix for the agent debug proxy.
const nodeDebugPathPrefix = "/api/node/debug/"

// nodeDebugEndpointRule is the HTTP method and capability required for one
// proxied debug endpoint.
type nodeDebugEndpointRule struct {
	method     string              // method is the only HTTP method the endpoint accepts.
	capability security.Capability // capability is checked at cluster scope before forwarding.
}

// nodeDebugEndpointRules lists every endpoint the proxy forwards. Paths not
// listed here are rejected, never forwarded.
var nodeDebugEndpointRules = map[string]nodeDebugEndpointRule{
	"containers":  {method: http.MethodGet, capability: security.CapabilityNodeRead},
	"images":      {method: http.MethodGet, capability: security.CapabilityNodeRead},
	"stats":       {method: http.MethodGet, capability: security.CapabilityNodeRead},
	"logs":        {method: http.MethodGet, capability: security.CapabilityWorkloadRead},
	"exec":        {method: http.MethodPost, capability: security.CapabilityWorkloadExec},
	"images/pull": {method: http.MethodPost, capability: security.CapabilityNodeManage},
}

// SetAgentDebugClientTLS sets the client TLS config (API server certificate
// and cluster CA roots) used to reach agent debug APIs over mTLS. Without it
// only loopback http:// debug addresses (dev mode) can be proxied.
func (apiServer *Server) SetAgentDebugClientTLS(clientTLSConfig *tls.Config) {
	apiServer.agentDebugClientTLS = clientTLSConfig
}

// handleNodeDebugProxy authorizes the caller for the requested debug endpoint,
// looks up the node's published debug URL, and reverse-proxies the request to
// the agent. Log streams are flushed as they arrive.
func (apiServer *Server) handleNodeDebugProxy(responseWriter http.ResponseWriter, request *http.Request) {
	nodeID, endpointName, pathValid := parseNodeDebugPath(request.URL.Path)
	endpointRule, endpointKnown := nodeDebugEndpointRules[endpointName]
	if !pathValid || !endpointKnown {
		writeJSONError(responseWriter, "unknown debug endpoint", http.StatusNotFound)
		return
	}
	if request.Method != endpointRule.method {
		writeJSONError(responseWriter, endpointRule.method+" only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, endpointRule.capability, security.ScopeCluster) {
		return
	}
	if validateError := types.ValidateNodeID(nodeID); validateError != nil {
		writeJSONError(responseWriter, validateError.Error(), http.StatusBadRequest)
		return
	}

	debugAddressFact, getError := apiServer.factStore.Get(request.Context(), types.KeyObservedNodeDebugAddress(nodeID))
	if getError != nil || len(debugAddressFact.Value) == 0 {
		writeJSONError(responseWriter, fmt.Sprintf("node %q has no debug API address — is the agent running with --debug-listen?", nodeID), http.StatusNotFound)
		return
	}
	agentBaseURL, agentTransport, targetError := apiServer.resolveAgentDebugTarget(nodeID, string(debugAddressFact.Value))
	if targetError != nil {
		writeJSONError(responseWriter, targetError.Error(), http.StatusServiceUnavailable)
		return
	}
	defer agentTransport.CloseIdleConnections()

	if request.Method == http.MethodPost {
		request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	}
	buildNodeDebugReverseProxy(nodeID, agentBaseURL, endpointName, agentTransport).ServeHTTP(responseWriter, request)
}

// parseNodeDebugPath splits "/api/node/debug/{node}/{endpoint}" into the node
// ID and endpoint name. Returns false when either part is missing.
func parseNodeDebugPath(requestPath string) (string, string, bool) {
	relativePath := strings.TrimPrefix(requestPath, nodeDebugPathPrefix)
	nodeID, endpointName, hasSeparator := strings.Cut(relativePath, "/")
	if !hasSeparator || nodeID == "" || endpointName == "" {
		return "", "", false
	}
	return nodeID, endpointName, true
}

// buildNodeDebugReverseProxy returns a reverse proxy that sends the request to
// <agentBaseURL>/debug/<endpoint> with the original query. The caller's
// credentials are stripped: the agent authenticates the API server only.
func buildNodeDebugReverseProxy(nodeID string, agentBaseURL *url.URL, endpointName string, agentTransport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			proxyRequest.Out.URL.Scheme = agentBaseURL.Scheme
			proxyRequest.Out.URL.Host = agentBaseURL.Host
			proxyRequest.Out.URL.Path = "/debug/" + endpointName
			proxyRequest.Out.URL.RawPath = ""
			proxyRequest.Out.Host = agentBaseURL.Host
			proxyRequest.Out.Header.Del("Authorization")
			proxyRequest.Out.Header.Del("Cookie")
		},
		Transport:     agentTransport,
		FlushInterval: -1,
		ErrorHandler: func(responseWriter http.ResponseWriter, _ *http.Request, proxyError error) {
			logging.Default().Warn("agent debug proxy failed", "node", nodeID, "error", proxyError.Error())
			writeJSONError(responseWriter, fmt.Sprintf("agent debug API on node %q unreachable: %v", nodeID, proxyError), http.StatusBadGateway)
		},
	}
}

// resolveAgentDebugTarget parses the node's published debug URL and returns it
// with a transport for it. https:// uses mTLS pinned to the node identity;
// http:// (or no scheme, from older agents) is allowed only for loopback hosts.
func (apiServer *Server) resolveAgentDebugTarget(nodeID string, publishedAddress string) (*url.URL, *http.Transport, error) {
	if !strings.Contains(publishedAddress, "://") {
		publishedAddress = "http://" + publishedAddress
	}
	agentBaseURL, parseError := url.Parse(publishedAddress)
	if parseError != nil || agentBaseURL.Host == "" {
		return nil, nil, fmt.Errorf("node %q published an invalid debug address", nodeID)
	}

	switch agentBaseURL.Scheme {
	case "https":
		if apiServer.agentDebugClientTLS == nil {
			return nil, nil, fmt.Errorf("agent debug proxy needs server TLS credentials")
		}
		return agentBaseURL, &http.Transport{TLSClientConfig: buildNodePinnedTLSConfig(apiServer.agentDebugClientTLS, nodeID)}, nil
	case "http":
		if !isLoopbackHost(agentBaseURL.Hostname()) {
			return nil, nil, fmt.Errorf("node %q debug API is plain HTTP on a non-loopback address; restart the agent with TLS credentials", nodeID)
		}
		return agentBaseURL, &http.Transport{}, nil
	default:
		return nil, nil, fmt.Errorf("node %q published an unsupported debug address scheme %q", nodeID, agentBaseURL.Scheme)
	}
}

// buildNodePinnedTLSConfig returns a client TLS config that accepts only a
// certificate chaining to the cluster CA whose CN is expectedNodeID. Hostname
// checks are replaced by this identity check because the address comes from
// the store; any extended key usage is accepted because enrolled node
// certificates are client-auth only (Flow 11).
func buildNodePinnedTLSConfig(baseClientTLS *tls.Config, expectedNodeID string) *tls.Config {
	pinnedTLSConfig := baseClientTLS.Clone()
	pinnedTLSConfig.MinVersion = tls.VersionTLS13
	pinnedTLSConfig.InsecureSkipVerify = true //nolint:gosec // G402: chain and node identity are verified in VerifyConnection
	clusterRoots := baseClientTLS.RootCAs
	pinnedTLSConfig.VerifyConnection = func(connectionState tls.ConnectionState) error {
		return verifyNodeCertificate(connectionState.PeerCertificates, clusterRoots, expectedNodeID)
	}
	return pinnedTLSConfig
}

// verifyNodeCertificate checks that the peer chain verifies against the
// cluster roots and that the leaf CN equals the expected node ID.
func verifyNodeCertificate(peerCertificates []*x509.Certificate, clusterRoots *x509.CertPool, expectedNodeID string) error {
	if len(peerCertificates) == 0 {
		return fmt.Errorf("agent presented no certificate")
	}
	intermediatePool := x509.NewCertPool()
	for _, intermediateCertificate := range peerCertificates[1:] {
		intermediatePool.AddCert(intermediateCertificate)
	}
	leafCertificate := peerCertificates[0]
	if _, verifyError := leafCertificate.Verify(x509.VerifyOptions{
		Roots:         clusterRoots,
		Intermediates: intermediatePool,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); verifyError != nil {
		return fmt.Errorf("agent certificate not signed by the cluster CA: %w", verifyError)
	}
	if leafCertificate.Subject.CommonName != expectedNodeID {
		return fmt.Errorf("agent certificate is for %q, expected node %q", leafCertificate.Subject.CommonName, expectedNodeID)
	}
	return nil
}

// isLoopbackHost reports whether a URL host name is a loopback address.
func isLoopbackHost(hostName string) bool {
	if hostName == "localhost" {
		return true
	}
	hostIP := net.ParseIP(hostName)
	return hostIP != nil && hostIP.IsLoopback()
}
