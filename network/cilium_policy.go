// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build cilium

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

const (
	// ciliumPolicyPrefix stores Cilium network policy entries in the kvstore.
	ciliumPolicyPrefix = ciliumStatePrefix + "policy/v1/"
)

// CiliumPolicyTranslator reads CCattler compiled network policy rules
// (derived/network/rule/{nodeID}/{index}) and writes corresponding Cilium
// identity-based policy entries to the kvstore. Cilium agents watch these
// keys and enforce policy at the eBPF level.
type CiliumPolicyTranslator struct {
	// factStore is the CCattler fact store (shared etcd instance).
	factStore store.StateStore
	// clusterName is the Cilium cluster identifier.
	clusterName string
	// writtenPolicyKeys tracks keys written for cleanup.
	writtenPolicyKeys map[string]bool
}

// NewCiliumPolicyTranslator creates a policy translator that reads CCattler
// compiled network rules and writes Cilium-compatible policy entries.
func NewCiliumPolicyTranslator(factStore store.StateStore, clusterName string) *CiliumPolicyTranslator {
	return &CiliumPolicyTranslator{
		factStore:         factStore,
		clusterName:       clusterName,
		writtenPolicyKeys: make(map[string]bool),
	}
}

// ciliumPolicyEntry represents a Cilium network policy rule in the kvstore.
// Cilium uses identity-based policy: rules reference numeric security
// identities rather than IP addresses.
type ciliumPolicyEntry struct {
	SourceIdentity      int    `json:"source_identity"`
	DestinationIdentity int    `json:"destination_identity"`
	DestinationPort     int    `json:"destination_port"`
	Protocol            string `json:"protocol"`
	Action              string `json:"action"`
}

// ReconcilePolicies reads CCattler compiled network rules from the fact store
// and translates them into Cilium identity-based policy entries. Each compiled
// rule has the format "sourceIP:targetIP:port:action" — this translator
// resolves IPs back to service identities and writes Cilium policy keys.
func (ciliumPolicyTranslator *CiliumPolicyTranslator) ReconcilePolicies(ctx context.Context, identityResolver func(ipAddress string) int) error {
	compiledRuleFacts, scanError := ciliumPolicyTranslator.factStore.Scan(ctx, "derived/network/rule/")
	if scanError != nil {
		return fmt.Errorf("scan compiled network rules: %w", scanError)
	}

	desiredPolicyKeys := make(map[string]bool)

	for _, ruleFact := range compiledRuleFacts {
		ruleFields := strings.SplitN(string(ruleFact.Value), ":", 4)
		if len(ruleFields) != 4 {
			logging.Default().Warn("cilium: malformed compiled rule", "key", ruleFact.Key, "value", string(ruleFact.Value))
			continue
		}

		sourceIP := ruleFields[0]
		targetIP := ruleFields[1]
		portString := ruleFields[2]
		action := ruleFields[3]

		sourceIdentityID := identityResolver(sourceIP)
		destinationIdentityID := identityResolver(targetIP)
		if sourceIdentityID == 0 || destinationIdentityID == 0 {
			continue
		}

		destinationPort := 0
		if portString != "" && portString != "0" {
			parsedPort, parseError := strconv.Atoi(portString)
			if parseError != nil {
				logging.Default().Warn("cilium: invalid port in compiled rule", "key", ruleFact.Key, "port", portString)
				continue
			}
			destinationPort = parsedPort
		}

		ciliumAction := "allow"
		if action == "deny" {
			ciliumAction = "deny"
		}

		policyEntry := ciliumPolicyEntry{
			SourceIdentity:      sourceIdentityID,
			DestinationIdentity: destinationIdentityID,
			DestinationPort:     destinationPort,
			Protocol:            "TCP",
			Action:              ciliumAction,
		}

		policyKey := fmt.Sprintf("%s%s/%d-%d-%d",
			ciliumPolicyPrefix, ciliumPolicyTranslator.clusterName,
			sourceIdentityID, destinationIdentityID, destinationPort)
		desiredPolicyKeys[policyKey] = true

		policyJSON, marshalError := json.Marshal(policyEntry)
		if marshalError != nil {
			logging.Default().Error("cilium: failed to marshal policy entry",
				"key", policyKey, "error", marshalError.Error())
			continue
		}

		if _, putError := ciliumPolicyTranslator.factStore.Put(ctx, policyKey, policyJSON); putError != nil {
			logging.Default().Error("cilium: failed to write policy entry",
				"key", policyKey, "error", putError.Error())
		}
		ciliumPolicyTranslator.writtenPolicyKeys[policyKey] = true
	}

	// Remove stale policy keys.
	for existingKey := range ciliumPolicyTranslator.writtenPolicyKeys {
		if !desiredPolicyKeys[existingKey] {
			if deleteError := ciliumPolicyTranslator.factStore.Delete(ctx, existingKey); deleteError != nil {
				logging.Default().Error("cilium: failed to delete stale policy key",
					"key", existingKey, "error", deleteError.Error())
			}
			delete(ciliumPolicyTranslator.writtenPolicyKeys, existingKey)
		}
	}

	return nil
}

// CleanupPolicies removes all Cilium policy keys written by this translator.
func (ciliumPolicyTranslator *CiliumPolicyTranslator) CleanupPolicies(ctx context.Context) error {
	var lastError error
	for policyKey := range ciliumPolicyTranslator.writtenPolicyKeys {
		if deleteError := ciliumPolicyTranslator.factStore.Delete(ctx, policyKey); deleteError != nil {
			logging.Default().Error("cilium: policy cleanup failed",
				"key", policyKey, "error", deleteError.Error())
			lastError = deleteError
		}
	}
	ciliumPolicyTranslator.writtenPolicyKeys = make(map[string]bool)
	return lastError
}
