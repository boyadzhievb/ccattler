// Shared utility functions used by multiple cca CLI commands.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

// statusAPIListenAddress is the address the HTTP status API binds to when running
// in live mode (run, run-container, demo). The status and metric commands query this.
const statusAPIListenAddress = "127.0.0.1:9770"

// configureLogger sets up the process-wide structured logger from CLI flags.
func configureLogger(logLevel string, logFormat string) {
	level := logging.LevelInfo
	if logLevel != "" {
		level = logging.ParseLevel(logLevel)
	}
	format := logging.FormatHuman
	if logFormat == "json" {
		format = logging.FormatJSON
	}
	logging.SetDefault(logging.New(os.Stderr, level, format))
}

// createStateStoreFromServerConfig builds the appropriate StateStore for
// the server or agent command configuration. When etcdCertPath, etcdKeyPath,
// and etcdCACertPath are all provided, a TLS config is built for the etcd
// client connection.
func createStateStoreFromServerConfig(storeBackend, etcdEndpoints, storeKeyPrefix, etcdCertPath, etcdKeyPath, etcdCACertPath string) (store.StateStore, error) {
	if storeBackend == "etcd" {
		endpointList := strings.Split(etcdEndpoints, ",")

		var etcdTLSConfig *tls.Config
		if etcdCertPath != "" && etcdKeyPath != "" && etcdCACertPath != "" {
			clientCertificate, loadError := tls.LoadX509KeyPair(etcdCertPath, etcdKeyPath)
			if loadError != nil {
				return nil, fmt.Errorf("loading etcd client certificate: %w", loadError)
			}

			caCertPEM, readError := os.ReadFile(etcdCACertPath) //nolint:gosec // reads user-specified config file
			if readError != nil {
				return nil, fmt.Errorf("reading etcd CA certificate: %w", readError)
			}

			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCertPEM) {
				return nil, fmt.Errorf("etcd CA certificate file contains no valid certificates")
			}

			etcdTLSConfig = &tls.Config{
				Certificates: []tls.Certificate{clientCertificate},
				RootCAs:      caCertPool,
				MinVersion:   tls.VersionTLS13,
			}

			for _, endpoint := range endpointList {
				if strings.HasPrefix(endpoint, "http://") {
					return nil, fmt.Errorf("etcd endpoint %q uses http:// but TLS is configured; use https:// or omit the scheme", endpoint)
				}
			}
		}

		return store.NewEtcdStore(store.EtcdStoreConfig{
			Endpoints:   endpointList,
			KeyPrefix:   storeKeyPrefix,
			DialTimeout: 5 * time.Second,
			TLSConfig:   etcdTLSConfig,
		})
	}
	return store.NewMemoryStore(), nil
}

// fileExists returns true if the given path exists and is a regular file.
func fileExists(filePath string) bool {
	fileInfo, statError := os.Stat(filePath)
	return statError == nil && !fileInfo.IsDir()
}

// detectLocalIPAddresses returns the non-loopback IPv4 addresses of this machine.
func detectLocalIPAddresses() []net.IP {
	var localAddresses []net.IP
	networkInterfaces, interfaceError := net.Interfaces()
	if interfaceError != nil {
		return localAddresses
	}
	for _, networkInterface := range networkInterfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		interfaceAddresses, addressError := networkInterface.Addrs()
		if addressError != nil {
			continue
		}
		for _, interfaceAddress := range interfaceAddresses {
			var ipAddress net.IP
			switch typedAddress := interfaceAddress.(type) {
			case *net.IPNet:
				ipAddress = typedAddress.IP
			case *net.IPAddr:
				ipAddress = typedAddress.IP
			}
			if ipAddress != nil && ipAddress.To4() != nil && !ipAddress.IsLoopback() {
				localAddresses = append(localAddresses, ipAddress)
			}
		}
	}
	return localAddresses
}

// annotateErrorWithFileName sets the File field on a ParseError if the error
// is of that type. This adds the source filename to diagnostic output.
func annotateErrorWithFileName(originalError error, fileName string) error {
	if parseError, isParseError := originalError.(*lang.ParseError); isParseError {
		parseError.File = fileName
	}
	return originalError
}
