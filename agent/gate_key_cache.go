// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/types"
)

// DefaultGateKeyCacheDirectory is the tmpfs directory holding delivered gate
// TLS pairs (Flow 28, R4). It survives an agent restart but not a reboot.
const DefaultGateKeyCacheDirectory = "/run/ccattler/gate-keys" //nolint:gosec // G101: directory path, not a credential

// Gate-key cache file names and modes inside {cache}/{gate}/.
const (
	gateCertificateFileName = "certificate.pem" // gateCertificateFileName holds the gate certificate.
	gatePrivateKeyFileName  = "private-key.pem" // gatePrivateKeyFileName holds the matching private key.
	gateCertificateFileMode = 0o444             // gateCertificateFileMode: the certificate is public.
	gatePrivateKeyFileMode  = 0o400             // gatePrivateKeyFileMode: the key is readable by root only.
)

// DeliveredGateKeyPair is one gate's pair as returned by POST /api/node/gate-keys.
type DeliveredGateKeyPair struct {
	Gate           string `json:"gate"`            // Gate is the gate name.
	Host           string `json:"host"`            // Host is the gate's declared host.
	CertificatePEM string `json:"certificate_pem"` // CertificatePEM is the gate certificate.
	PrivateKeyPEM  string `json:"private_key_pem"` // PrivateKeyPEM is the matching private key.
}

// GateKeyDelivery is the decoded gate-key delivery response.
type GateKeyDelivery struct {
	Gates   []DeliveredGateKeyPair `json:"gates"`   // Gates are the available pairs.
	Pending []string               `json:"pending"` // Pending lists TLS gates without a valid pair yet.
}

// GateKeyCache keeps delivered gate TLS pairs in memory for the gate proxy and
// on tmpfs so a restarted agent serves TLS while the API is down. Certificate
// and key always come from the same delivery; they are never paired with facts.
type GateKeyCache struct {
	baseDirectory           string                      // baseDirectory is the tmpfs root, e.g. /run/ccattler/gate-keys.
	requireMemoryFilesystem bool                        // requireMemoryFilesystem refuses writes unless baseDirectory is tmpfs or ramfs.
	mutex                   sync.RWMutex                // mutex guards certificates and pairPEMs.
	certificates            map[string]*tls.Certificate // certificates maps gate name to its parsed pair.
	pairPEMs                map[string]string           // pairPEMs maps gate name to cert+key PEM, to skip unchanged rewrites.
}

// NewGateKeyCache creates an empty cache rooted at baseDirectory.
func NewGateKeyCache(baseDirectory string) *GateKeyCache {
	return &GateKeyCache{
		baseDirectory: baseDirectory,
		certificates:  make(map[string]*tls.Certificate),
		pairPEMs:      make(map[string]string),
	}
}

// RequireMemoryFilesystem makes writes fail unless the base directory is on
// tmpfs or ramfs. cca agent enables it; tests use a temporary directory.
func (gateKeyCache *GateKeyCache) RequireMemoryFilesystem() {
	gateKeyCache.requireMemoryFilesystem = true
}

// GateCertificate returns the cached pair of a gate, or nil. It implements
// network.GateCertificateSource for the gate proxy's SNI lookup.
func (gateKeyCache *GateKeyCache) GateCertificate(gateName string) *tls.Certificate {
	gateKeyCache.mutex.RLock()
	defer gateKeyCache.mutex.RUnlock()
	return gateKeyCache.certificates[gateName]
}

// CachedGateNames returns the names of the gates with a cached pair, sorted.
func (gateKeyCache *GateKeyCache) CachedGateNames() []string {
	gateKeyCache.mutex.RLock()
	defer gateKeyCache.mutex.RUnlock()
	gateNames := make([]string, 0, len(gateKeyCache.certificates))
	for gateName := range gateKeyCache.certificates {
		gateNames = append(gateNames, gateName)
	}
	sort.Strings(gateNames)
	return gateNames
}

// Load reads the pairs left by a previous agent run on this boot. Invalid or
// mismatched pairs and interrupted writes are removed. Returns the count loaded.
func (gateKeyCache *GateKeyCache) Load() int {
	directoryEntries, readError := os.ReadDir(gateKeyCache.baseDirectory)
	if readError != nil {
		return 0
	}
	gateKeyCache.mutex.Lock()
	defer gateKeyCache.mutex.Unlock()
	for _, directoryEntry := range directoryEntries {
		entryPath := filepath.Join(gateKeyCache.baseDirectory, directoryEntry.Name())
		if !directoryEntry.IsDir() || validateMaterialPathSegment(directoryEntry.Name()) != nil {
			_ = os.RemoveAll(entryPath)
			continue
		}
		certificatePEM, certificateError := os.ReadFile(filepath.Join(entryPath, gateCertificateFileName)) //nolint:gosec // G304: path built from a validated gate name under the cache root
		privateKeyPEM, keyError := os.ReadFile(filepath.Join(entryPath, gatePrivateKeyFileName))           //nolint:gosec // G304: path built from a validated gate name under the cache root
		if certificateError != nil || keyError != nil {
			_ = os.RemoveAll(entryPath)
			continue
		}
		certificate, pairError := tls.X509KeyPair(certificatePEM, privateKeyPEM)
		if pairError != nil {
			logging.Default().Warn("gate-key cache: dropping invalid cached pair", "gate", directoryEntry.Name())
			_ = os.RemoveAll(entryPath)
			continue
		}
		gateKeyCache.certificates[directoryEntry.Name()] = &certificate
		gateKeyCache.pairPEMs[directoryEntry.Name()] = string(certificatePEM) + string(privateKeyPEM)
	}
	return len(gateKeyCache.certificates)
}

// Apply installs a delivery: new or changed pairs are written and served,
// pending gates keep their cached pair, and gates in neither list are removed.
// Returns the first write error; other gates are still applied.
func (gateKeyCache *GateKeyCache) Apply(delivery *GateKeyDelivery) error {
	if prepareError := prepareMaterialDirectory(gateKeyCache.baseDirectory, gateKeyCache.requireMemoryFilesystem); prepareError != nil {
		return prepareError
	}
	keptGateNames := make(map[string]bool, len(delivery.Gates)+len(delivery.Pending))
	for _, pendingGateName := range delivery.Pending {
		keptGateNames[pendingGateName] = true
	}
	var firstWriteError error
	for _, deliveredPair := range delivery.Gates {
		keptGateNames[deliveredPair.Gate] = true
		if installError := gateKeyCache.installPair(deliveredPair); installError != nil && firstWriteError == nil {
			firstWriteError = installError
		}
	}
	for _, cachedGateName := range gateKeyCache.CachedGateNames() {
		if !keptGateNames[cachedGateName] {
			gateKeyCache.removeGate(cachedGateName)
		}
	}
	return firstWriteError
}

// installPair validates one delivered pair and, when it differs from the
// cached one, writes it to {cache}/{gate}/ and serves it.
func (gateKeyCache *GateKeyCache) installPair(deliveredPair DeliveredGateKeyPair) error {
	if types.ValidateResourceName(deliveredPair.Gate) != nil || validateMaterialPathSegment(deliveredPair.Gate) != nil {
		return fmt.Errorf("invalid gate name %q", deliveredPair.Gate)
	}
	pairPEM := deliveredPair.CertificatePEM + deliveredPair.PrivateKeyPEM
	gateKeyCache.mutex.RLock()
	unchanged := gateKeyCache.pairPEMs[deliveredPair.Gate] == pairPEM
	gateKeyCache.mutex.RUnlock()
	if unchanged {
		return nil
	}
	certificate, pairError := tls.X509KeyPair([]byte(deliveredPair.CertificatePEM), []byte(deliveredPair.PrivateKeyPEM))
	if pairError != nil {
		return fmt.Errorf("gate %s: delivered pair invalid: %w", deliveredPair.Gate, pairError)
	}
	if writeError := gateKeyCache.writePairFiles(deliveredPair); writeError != nil {
		return writeError
	}
	gateKeyCache.mutex.Lock()
	gateKeyCache.certificates[deliveredPair.Gate] = &certificate
	gateKeyCache.pairPEMs[deliveredPair.Gate] = pairPEM
	gateKeyCache.mutex.Unlock()
	return nil
}

// writePairFiles writes both files into a staging directory and renames it
// into place, so a crash never leaves a certificate without its key.
func (gateKeyCache *GateKeyCache) writePairFiles(deliveredPair DeliveredGateKeyPair) error {
	randomSuffix := make([]byte, stagingSuffixRandomBytes)
	_, _ = rand.Read(randomSuffix)
	stagingDirectory := filepath.Join(gateKeyCache.baseDirectory, secretMaterialStagingMarker+deliveredPair.Gate+"-"+hex.EncodeToString(randomSuffix))
	if mkdirError := os.Mkdir(stagingDirectory, secretMaterialDirectoryMode); mkdirError != nil {
		return fmt.Errorf("gate %s: create cache directory: %w", deliveredPair.Gate, mkdirError)
	}
	certificateError := os.WriteFile(filepath.Join(stagingDirectory, gateCertificateFileName), []byte(deliveredPair.CertificatePEM), gateCertificateFileMode)
	keyError := os.WriteFile(filepath.Join(stagingDirectory, gatePrivateKeyFileName), []byte(deliveredPair.PrivateKeyPEM), gatePrivateKeyFileMode)
	if certificateError != nil || keyError != nil {
		_ = os.RemoveAll(stagingDirectory)
		return fmt.Errorf("gate %s: write cache files: %v %v", deliveredPair.Gate, certificateError, keyError)
	}
	finalDirectory := filepath.Join(gateKeyCache.baseDirectory, deliveredPair.Gate)
	_ = os.RemoveAll(finalDirectory)
	if renameError := os.Rename(stagingDirectory, finalDirectory); renameError != nil {
		_ = os.RemoveAll(stagingDirectory)
		return fmt.Errorf("gate %s: install cache files: %w", deliveredPair.Gate, renameError)
	}
	return nil
}

// removeGate drops a gate's pair from memory and from tmpfs.
func (gateKeyCache *GateKeyCache) removeGate(gateName string) {
	gateKeyCache.mutex.Lock()
	delete(gateKeyCache.certificates, gateName)
	delete(gateKeyCache.pairPEMs, gateName)
	gateKeyCache.mutex.Unlock()
	if validateMaterialPathSegment(gateName) == nil && !strings.HasPrefix(gateName, ".") {
		_ = os.RemoveAll(filepath.Join(gateKeyCache.baseDirectory, gateName))
	}
}
