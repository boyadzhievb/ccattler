package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// CredentialBrokerController watches cloud identity bindings and running
// instances, issues and refreshes cloud credentials, and garbage-collects
// credentials for stopped instances.
type CredentialBrokerController struct {
	tokenIssuer    *security.WorkloadTokenIssuer
	adapters       map[string]security.CloudProviderAdapter
	credentialTTL  time.Duration
	refreshBefore  time.Duration
}

// NewCredentialBrokerController creates a credential broker with the given
// token issuer and default TTL/refresh settings.
func NewCredentialBrokerController(tokenIssuer *security.WorkloadTokenIssuer) *CredentialBrokerController {
	return &CredentialBrokerController{
		tokenIssuer:   tokenIssuer,
		adapters:      make(map[string]security.CloudProviderAdapter),
		credentialTTL: 1 * time.Hour,
		refreshBefore: 15 * time.Minute,
	}
}

// RegisterAdapter adds a cloud provider adapter. Multiple providers can be
// registered (e.g. both AWS and GCP for multi-cloud workloads).
func (brokerController *CredentialBrokerController) RegisterAdapter(adapter security.CloudProviderAdapter) {
	brokerController.adapters[adapter.ProviderName()] = adapter
}

// SetCredentialTTL overrides the default credential lifetime.
func (brokerController *CredentialBrokerController) SetCredentialTTL(credentialTTL time.Duration) {
	brokerController.credentialTTL = credentialTTL
}

// SetRefreshBefore overrides the default pre-expiry refresh window.
func (brokerController *CredentialBrokerController) SetRefreshBefore(refreshBefore time.Duration) {
	brokerController.refreshBefore = refreshBefore
}

// Name returns the controller name for logging and metrics.
func (brokerController *CredentialBrokerController) Name() string {
	return "credential-broker"
}

// Watch returns the fact prefixes this controller monitors.
func (brokerController *CredentialBrokerController) Watch() []string {
	return []string{
		types.ScanDesiredCloudIdentities,
		types.ScanDesiredCredentialBroker,
		types.ScanDerivedCredentials,
		types.ScanObservedInstances,
		"desired/service/",
	}
}

// Reconcile examines cloud identity bindings, running instances, and credential
// state to issue, refresh, or garbage-collect cloud credentials.
func (brokerController *CredentialBrokerController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	cloudIdentities := parseCloudIdentities(facts)
	serviceBindings := parseServiceCloudBindings(facts)
	instanceServices := parseInstanceServices(facts)
	runningInstances := parseRunningInstances(facts)
	credentialStates := parseCredentialStates(facts)
	brokerConfig := parseBrokerConfig(facts)

	if brokerConfig.credentialTTL > 0 {
		brokerController.credentialTTL = brokerConfig.credentialTTL
	}
	if brokerConfig.refreshBefore > 0 {
		brokerController.refreshBefore = brokerConfig.refreshBefore
	}

	var changes []Change

	for instanceID, serviceName := range instanceServices {
		if !runningInstances[instanceID] {
			continue
		}

		bindings := serviceBindings[serviceName]
		for _, bindingIdentity := range bindings {
			identityConfig, exists := cloudIdentities[bindingIdentity]
			if !exists {
				continue
			}

			credentialKey := instanceID + "/" + bindingIdentity
			existingState := credentialStates[credentialKey]

			if existingState.state == "active" && !brokerController.needsRefresh(existingState.expiresAt) {
				continue
			}

			adapter, adapterExists := brokerController.adapters[identityConfig.Provider]
			if !adapterExists {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedCredentialState(instanceID, bindingIdentity),
					Value: []byte("error"),
				})
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedCredentialError(instanceID, bindingIdentity),
					Value: []byte(fmt.Sprintf("no adapter for provider %q", identityConfig.Provider)),
				})
				continue
			}

			spiffeID := fmt.Sprintf("spiffe://ccattler/%s/%s", serviceName, instanceID)
			jwtToken, mintError := brokerController.tokenIssuer.MintWorkloadToken(spiffeID, identityConfig.Provider, brokerController.credentialTTL)
			if mintError != nil {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedCredentialState(instanceID, bindingIdentity),
					Value: []byte("error"),
				})
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedCredentialError(instanceID, bindingIdentity),
					Value: []byte(mintError.Error()),
				})
				continue
			}

			credential, exchangeError := adapter.ExchangeToken(ctx, jwtToken, identityConfig)
			if exchangeError != nil {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedCredentialState(instanceID, bindingIdentity),
					Value: []byte("error"),
				})
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedCredentialError(instanceID, bindingIdentity),
					Value: []byte(exchangeError.Error()),
				})
				continue
			}

			issuedAt := time.Now()
			changes = append(changes,
				Change{Type: store.OpPut, Key: types.KeyDerivedCredentialState(instanceID, bindingIdentity), Value: []byte("active")},
				Change{Type: store.OpPut, Key: types.KeyDerivedCredentialIssuedAt(instanceID, bindingIdentity), Value: []byte(issuedAt.Format(time.RFC3339))},
				Change{Type: store.OpPut, Key: types.KeyDerivedCredentialExpiresAt(instanceID, bindingIdentity), Value: []byte(credential.ExpiresAt.Format(time.RFC3339))},
			)

			if existingState.errorMessage != "" {
				changes = append(changes, Change{
					Type: store.OpDelete,
					Key:  types.KeyDerivedCredentialError(instanceID, bindingIdentity),
				})
			}
		}
	}

	// Garbage collection: delete credentials for instances that no longer exist
	// or are no longer running.
	for credentialKey, credentialState := range credentialStates {
		parts := strings.SplitN(credentialKey, "/", 2)
		if len(parts) != 2 {
			continue
		}
		instanceID := parts[0]
		identityName := parts[1]

		if _, instanceExists := instanceServices[instanceID]; !instanceExists || !runningInstances[instanceID] {
			if credentialState.state != "" {
				changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyDerivedCredentialState(instanceID, identityName)})
			}
			if credentialState.expiresAt != "" {
				changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyDerivedCredentialExpiresAt(instanceID, identityName)})
			}
			if credentialState.issuedAt != "" {
				changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyDerivedCredentialIssuedAt(instanceID, identityName)})
			}
			if credentialState.errorMessage != "" {
				changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyDerivedCredentialError(instanceID, identityName)})
			}
		}
	}

	return changes, nil
}

// needsRefresh returns true if a credential's expiry is within the refresh window.
func (brokerController *CredentialBrokerController) needsRefresh(expiresAtString string) bool {
	if expiresAtString == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtString)
	if err != nil {
		return true
	}
	return time.Now().Add(brokerController.refreshBefore).After(expiresAt)
}

type credentialState struct {
	state        string
	expiresAt    string
	issuedAt     string
	errorMessage string
}

type brokerConfigParsed struct {
	credentialTTL time.Duration
	refreshBefore time.Duration
}

func parseCloudIdentities(facts []store.Fact) map[string]security.CloudIdentityConfig {
	identities := make(map[string]security.CloudIdentityConfig)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredCloudIdentities) {
		remainder := strings.TrimPrefix(fact.Key, types.ScanDesiredCloudIdentities)
		parts := strings.SplitN(remainder, "/", 2)
		identityName := parts[0]

		identity := identities[identityName]
		identity.Name = identityName

		if len(parts) == 1 {
			identities[identityName] = identity
			continue
		}

		switch parts[1] {
		case "provider":
			identity.Provider = string(fact.Value)
		case "role":
			identity.Role = string(fact.Value)
		case "service_account":
			identity.ServiceAccount = string(fact.Value)
		case "pool":
			identity.Pool = string(fact.Value)
		case "client_id":
			identity.ClientID = string(fact.Value)
		case "tenant_id":
			identity.TenantID = string(fact.Value)
		}
		identities[identityName] = identity
	}
	return identities
}

func parseServiceCloudBindings(facts []store.Fact) map[string][]string {
	bindings := make(map[string][]string)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, "desired/service/") {
			continue
		}
		remainder := strings.TrimPrefix(fact.Key, "desired/service/")
		// format: {service}/cloud_identity/{identity}
		if cloudIdentityIndex := strings.Index(remainder, "/cloud_identity/"); cloudIdentityIndex > 0 {
			serviceName := remainder[:cloudIdentityIndex]
			identityPart := remainder[cloudIdentityIndex+len("/cloud_identity/"):]
			if !strings.Contains(identityPart, "/") {
				bindings[serviceName] = appendUnique(bindings[serviceName], identityPart)
			}
		}
	}
	return bindings
}

func parseInstanceServices(facts []store.Fact) map[string]string {
	instanceServices := make(map[string]string)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, "observed/instance/") {
			continue
		}
		remainder := strings.TrimPrefix(fact.Key, "observed/instance/")
		parts := strings.SplitN(remainder, "/", 2)
		if len(parts) == 2 && parts[1] == "service" {
			instanceServices[parts[0]] = string(fact.Value)
		}
	}
	return instanceServices
}

func parseRunningInstances(facts []store.Fact) map[string]bool {
	running := make(map[string]bool)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, "observed/instance/") {
			continue
		}
		remainder := strings.TrimPrefix(fact.Key, "observed/instance/")
		parts := strings.SplitN(remainder, "/", 2)
		if len(parts) == 2 && parts[1] == "state" && string(fact.Value) == string(types.InstanceRunning) {
			running[parts[0]] = true
		}
	}
	return running
}

func parseCredentialStates(facts []store.Fact) map[string]credentialState {
	states := make(map[string]credentialState)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedCredentials) {
		remainder := strings.TrimPrefix(fact.Key, types.ScanDerivedCredentials)
		// format: {instance}/{identity}/{field}
		parts := strings.SplitN(remainder, "/", 3)
		if len(parts) != 3 {
			continue
		}
		credentialKey := parts[0] + "/" + parts[1]
		state := states[credentialKey]
		switch parts[2] {
		case "state":
			state.state = string(fact.Value)
		case "expires_at":
			state.expiresAt = string(fact.Value)
		case "issued_at":
			state.issuedAt = string(fact.Value)
		case "error":
			state.errorMessage = string(fact.Value)
		}
		states[credentialKey] = state
	}
	return states
}

func parseBrokerConfig(facts []store.Fact) brokerConfigParsed {
	var config brokerConfigParsed
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredCredentialBroker) {
		remainder := strings.TrimPrefix(fact.Key, types.ScanDesiredCredentialBroker)
		switch remainder {
		case "credential_ttl":
			if duration, err := time.ParseDuration(string(fact.Value)); err == nil {
				config.credentialTTL = duration
			}
		case "refresh_before":
			if duration, err := time.ParseDuration(string(fact.Value)); err == nil {
				config.refreshBefore = duration
			}
		}
	}
	return config
}

func appendUnique(slice []string, value string) []string {
	for _, existing := range slice {
		if existing == value {
			return slice
		}
	}
	return append(slice, value)
}
