// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// secretDeliveryRequestTimeout bounds one delivery request so an unreachable
// API server cannot stall the reconcile loop.
const secretDeliveryRequestTimeout = 5 * time.Second

// secretDeliveryMaxResponseBytes caps the delivery response size.
const secretDeliveryMaxResponseBytes = 4 << 20

// nodeSecretDeliveryPath mirrors api.NodeSecretDeliveryPath (the agent does
// not import the api package).
const nodeSecretDeliveryPath = "/api/node/secrets" //nolint:gosec // G101: URL path, not a credential

// SecretWaitReason is the typed reason an instance is in waiting-for-secrets.
type SecretWaitReason string

const (
	// SecretWaitAPIUnreachable means the API server could not be reached or failed.
	SecretWaitAPIUnreachable SecretWaitReason = "api_unreachable"
	// SecretWaitNotAuthorized means delivery was refused (grant, placement, node).
	SecretWaitNotAuthorized SecretWaitReason = "not_authorized"
	// SecretWaitSecretMissing means a granted secret has no stored value.
	SecretWaitSecretMissing SecretWaitReason = "secret_missing"
	// SecretWaitDeliveryNotConfigured means the agent has no mTLS identity or
	// API server address, so it cannot request delivery at all.
	SecretWaitDeliveryNotConfigured SecretWaitReason = "delivery_not_configured"
)

// SecretDeliveryError is returned when delivery did not produce material.
type SecretDeliveryError struct {
	Reason      SecretWaitReason // Reason is the typed wait reason.
	SecretNames []string         // SecretNames lists refused or missing secrets (names only).
	Detail      string           // Detail is a human-readable explanation.
}

// Error formats the delivery failure.
func (deliveryError *SecretDeliveryError) Error() string {
	if len(deliveryError.SecretNames) == 0 {
		return fmt.Sprintf("%s: %s", deliveryError.Reason, deliveryError.Detail)
	}
	return fmt.Sprintf("%s: %s (%s)", deliveryError.Reason, deliveryError.Detail, strings.Join(deliveryError.SecretNames, ", "))
}

// SecretDeliveryClient fetches the secret material of an instance placed on
// this node from the control plane (Flow 27).
type SecretDeliveryClient interface {
	// FetchInstanceSecrets returns secret name → value, or a *SecretDeliveryError.
	FetchInstanceSecrets(ctx context.Context, instanceID string) (map[string][]byte, error)
}

// HTTPSecretDeliveryClient calls POST /api/node/secrets over mTLS with the
// node certificate; the server derives the node ID from the certificate.
type HTTPSecretDeliveryClient struct {
	httpClient *http.Client // httpClient carries the node mTLS configuration.
	apiBaseURL string       // apiBaseURL is e.g. "https://10.0.0.5:9770".
}

// NewHTTPSecretDeliveryClient creates a client for the API server at
// apiAddress (host:port or https URL) using the node's mTLS configuration.
func NewHTTPSecretDeliveryClient(apiAddress string, nodeTLSConfig *tls.Config) *HTTPSecretDeliveryClient {
	apiBaseURL := strings.TrimRight(apiAddress, "/")
	if !strings.HasPrefix(apiBaseURL, "https://") {
		apiBaseURL = "https://" + strings.TrimPrefix(apiBaseURL, "http://")
	}
	return &HTTPSecretDeliveryClient{
		httpClient: &http.Client{
			Timeout:   secretDeliveryRequestTimeout,
			Transport: &http.Transport{TLSClientConfig: nodeTLSConfig.Clone()},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // never follow a redirect with the node certificate
			},
		},
		apiBaseURL: apiBaseURL,
	}
}

// FetchInstanceSecrets requests the instance's material. Network failures,
// rate limiting and server errors map to api_unreachable (retried with backoff).
func (deliveryClient *HTTPSecretDeliveryClient) FetchInstanceSecrets(ctx context.Context, instanceID string) (map[string][]byte, error) {
	requestBody, _ := json.Marshal(map[string]string{"instance_id": instanceID})
	request, requestError := http.NewRequestWithContext(ctx, http.MethodPost, deliveryClient.apiBaseURL+nodeSecretDeliveryPath, bytes.NewReader(requestBody))
	if requestError != nil {
		return nil, &SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: requestError.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	response, sendError := deliveryClient.httpClient.Do(request)
	if sendError != nil {
		return nil, &SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: sendError.Error()}
	}
	defer func() { _ = response.Body.Close() }()
	responseBytes, readError := io.ReadAll(io.LimitReader(response.Body, secretDeliveryMaxResponseBytes))
	if readError != nil {
		return nil, &SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: readError.Error()}
	}
	return decodeSecretDeliveryResponse(response.StatusCode, responseBytes)
}

// decodeSecretDeliveryResponse maps the HTTP status and body to material or a
// typed SecretDeliveryError.
func decodeSecretDeliveryResponse(statusCode int, responseBytes []byte) (map[string][]byte, error) {
	switch statusCode {
	case http.StatusOK:
		var delivered struct {
			Secrets map[string][]byte `json:"secrets"` // Secrets maps name to plaintext.
		}
		if decodeError := json.Unmarshal(responseBytes, &delivered); decodeError != nil {
			return nil, &SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: "invalid delivery response"}
		}
		if delivered.Secrets == nil {
			delivered.Secrets = map[string][]byte{}
		}
		return delivered.Secrets, nil
	case http.StatusForbidden, http.StatusConflict:
		var refusal struct {
			Error   string   `json:"error"`   // Error is the refusal text.
			Reason  string   `json:"reason"`  // Reason is the typed refusal reason.
			Secrets []string `json:"secrets"` // Secrets lists refused secret names.
		}
		_ = json.Unmarshal(responseBytes, &refusal)
		waitReason := SecretWaitNotAuthorized
		if statusCode == http.StatusConflict || refusal.Reason == string(SecretWaitSecretMissing) {
			waitReason = SecretWaitSecretMissing
		}
		return nil, &SecretDeliveryError{Reason: waitReason, SecretNames: refusal.Secrets, Detail: refusal.Error}
	default:
		return nil, &SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: fmt.Sprintf("delivery returned HTTP %d", statusCode)}
	}
}
