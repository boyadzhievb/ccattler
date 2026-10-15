// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// store_config.go provides shared store configuration resolution for all CLI
// commands. Instead of requiring --store and --endpoints on every invocation,
// the resolver checks multiple sources in priority order.
package main

import (
	"os"
	"strings"
)

const (
	endpointsConfigPath   = "/etc/ccattler/endpoints"
	endpointsEnvVariable  = "CCA_ENDPOINTS"
	defaultEtcdEndpoints  = "localhost:2379"
	defaultStoreKeyPrefix = "/ccattler/"

	// defaultEtcdCertPath is the standard location for the node TLS certificate
	// used for etcd client authentication when endpoints use https://.
	defaultEtcdCertPath = "/etc/ccattler/pki/node.pem"
	// defaultEtcdKeyPath is the standard location for the node TLS private key.
	defaultEtcdKeyPath = "/etc/ccattler/pki/node-key.pem"
	// defaultEtcdCACertPath is the standard location for the cluster CA certificate.
	defaultEtcdCACertPath = "/etc/ccattler/pki/ca.pem"
)

// storeDefaults holds resolved store configuration that can be used as defaults
// by any command's argument parser.
type storeDefaults struct {
	// storeBackend is "etcd" when endpoints are available, "memory" otherwise.
	storeBackend string
	// etcdEndpoints is the resolved comma-separated endpoint list.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within etcd.
	storeKeyPrefix string
	// etcdCertPath is the TLS client certificate for etcd, auto-detected for https.
	etcdCertPath string
	// etcdKeyPath is the TLS client key for etcd, auto-detected for https.
	etcdKeyPath string
	// etcdCACertPath is the TLS CA certificate for verifying etcd, auto-detected for https.
	etcdCACertPath string
}

// resolveStoreDefaults determines the store backend and endpoints by checking
// multiple sources in priority order:
//
//  1. CCA_ENDPOINTS environment variable
//  2. /etc/ccattler/endpoints file (written by "cca join")
//  3. Fall back to memory store (no endpoints available)
//
// Command-line flags (--endpoints, --store) parsed later override these defaults.
func resolveStoreDefaults() storeDefaults {
	defaults := storeDefaults{
		storeBackend:   "memory",
		etcdEndpoints:  defaultEtcdEndpoints,
		storeKeyPrefix: defaultStoreKeyPrefix,
	}

	if envEndpoints := os.Getenv(endpointsEnvVariable); envEndpoints != "" {
		defaults.storeBackend = "etcd"
		defaults.etcdEndpoints = envEndpoints
		populateEtcdTLSDefaults(&defaults)
		return defaults
	}

	fileContent, readError := os.ReadFile(endpointsConfigPath)
	if readError == nil {
		trimmedEndpoints := strings.TrimSpace(string(fileContent))
		if trimmedEndpoints != "" {
			defaults.storeBackend = "etcd"
			defaults.etcdEndpoints = trimmedEndpoints
			populateEtcdTLSDefaults(&defaults)
			return defaults
		}
	}

	return defaults
}

// populateEtcdTLSDefaults sets TLS cert paths from the canonical location when
// the resolved endpoints use https:// and the cert files exist on disk.
func populateEtcdTLSDefaults(defaults *storeDefaults) {
	if !strings.Contains(defaults.etcdEndpoints, "https://") {
		return
	}
	if fileExists(defaultEtcdCertPath) && fileExists(defaultEtcdKeyPath) && fileExists(defaultEtcdCACertPath) {
		defaults.etcdCertPath = defaultEtcdCertPath
		defaults.etcdKeyPath = defaultEtcdKeyPath
		defaults.etcdCACertPath = defaultEtcdCACertPath
	}
}

// resolveStoreDefaultsForDaemon returns defaults appropriate for long-running
// daemon commands (server, agent) that always use etcd.
func resolveStoreDefaultsForDaemon() storeDefaults {
	defaults := resolveStoreDefaults()
	defaults.storeBackend = "etcd"
	return defaults
}

// writeEndpointsConfig writes the etcd endpoints to the system-wide config file
// so subsequent CLI commands can auto-detect the store without flags.
func writeEndpointsConfig(endpoints string) error {
	if mkdirError := os.MkdirAll("/etc/ccattler", 0750); mkdirError != nil {
		return mkdirError
	}
	return os.WriteFile(endpointsConfigPath, []byte(endpoints+"\n"), 0600)
}
