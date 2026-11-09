// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/types"
)

// DefaultSecretMaterialDirectory is where delivered secret material lives on a
// node (Flow 27). /run is tmpfs on systemd hosts, so material never reaches
// disk and is gone after a reboot.
const DefaultSecretMaterialDirectory = "/run/ccattler/secrets" //nolint:gosec // G101: directory path, not a credential

// secretMaterialDirectoryMode keeps the per-instance directory root-only on the host.
const secretMaterialDirectoryMode = 0o700

// secretMaterialFileMode makes files readable by any container user. They are
// only reachable through the root-only parent directory or the container's own
// read-only bind mount, so host users still cannot read them.
const secretMaterialFileMode = 0o444

// secretValuesSubdirectory holds one file per delivered secret value.
const secretValuesSubdirectory = "values"

// secretConnectionsSubdirectory holds assembled connection URL files.
const secretConnectionsSubdirectory = "connections"

// secretFileNameSlashEncoding replaces "/" in hierarchical secret names on disk.
const secretFileNameSlashEncoding = "%2F"

// stagingSuffixRandomBytes is the number of random bytes (hex-encoded) that make
// a staging directory name unique.
const stagingSuffixRandomBytes = 4

// secretMaterialStagingMarker marks a directory being written; it is renamed
// into place only when complete, so a crash never leaves partial material.
const secretMaterialStagingMarker = ".staging-"

// SecretMaterialStore manages per-instance secret material on tmpfs. The agent
// writes delivered values once per boot and reuses them for container restarts.
type SecretMaterialStore struct {
	baseDirectory           string // baseDirectory is the tmpfs root, e.g. /run/ccattler/secrets.
	requireMemoryFilesystem bool   // requireMemoryFilesystem refuses to write unless baseDirectory is tmpfs or ramfs.
}

// NewSecretMaterialStore creates a store rooted at baseDirectory.
func NewSecretMaterialStore(baseDirectory string) *SecretMaterialStore {
	return &SecretMaterialStore{baseDirectory: baseDirectory}
}

// RequireMemoryFilesystem makes every write fail unless the base directory is
// on tmpfs or ramfs, so plaintext never reaches disk on a host where /run is
// disk-backed. cca agent enables it; tests use a plain temporary directory.
func (materialStore *SecretMaterialStore) RequireMemoryFilesystem() {
	materialStore.requireMemoryFilesystem = true
}

// encodeSecretFileName maps a validated secret name to one file name: "/"
// (hierarchical names such as "payments/db-password") becomes "%2F". Valid
// names never contain "%", so the mapping is reversible.
func encodeSecretFileName(secretName string) (string, error) {
	if validationError := types.ValidateSecretName(secretName); validationError != nil {
		return "", validationError
	}
	return strings.ReplaceAll(secretName, "/", secretFileNameSlashEncoding), nil
}

// decodeSecretFileName reverses encodeSecretFileName.
func decodeSecretFileName(fileName string) string {
	return strings.ReplaceAll(fileName, secretFileNameSlashEncoding, "/")
}

// validateMaterialPathSegment rejects names that could escape the instance
// directory (empty, ".", "..", path separators, staging prefix).
func validateMaterialPathSegment(pathSegment string) error {
	if pathSegment == "" || pathSegment == "." || pathSegment == ".." || strings.ContainsAny(pathSegment, `/\`) || strings.HasPrefix(pathSegment, secretMaterialStagingMarker) {
		return fmt.Errorf("invalid secret material name %q", pathSegment)
	}
	return nil
}

// instanceDirectory returns the material directory of an instance.
func (materialStore *SecretMaterialStore) instanceDirectory(instanceID string) string {
	return filepath.Join(materialStore.baseDirectory, instanceID)
}

// SecretValuePath returns the host path of one delivered secret value.
func (materialStore *SecretMaterialStore) SecretValuePath(instanceID string, secretName string) string {
	fileName, _ := encodeSecretFileName(secretName)
	return filepath.Join(materialStore.instanceDirectory(instanceID), secretValuesSubdirectory, fileName)
}

// WriteInstanceSecrets replaces the instance's material with the delivered
// values. It writes a staging directory and renames it into place.
func (materialStore *SecretMaterialStore) WriteInstanceSecrets(instanceID string, secretValues map[string][]byte) error {
	if validationError := validateMaterialPathSegment(instanceID); validationError != nil {
		return validationError
	}
	if prepareError := materialStore.prepareBaseDirectory(); prepareError != nil {
		return prepareError
	}
	randomSuffix := make([]byte, stagingSuffixRandomBytes)
	_, _ = rand.Read(randomSuffix)
	stagingDirectory := filepath.Join(materialStore.baseDirectory, secretMaterialStagingMarker+instanceID+"-"+hex.EncodeToString(randomSuffix))
	if writeError := writeSecretValuesDirectory(stagingDirectory, secretValues); writeError != nil {
		_ = os.RemoveAll(stagingDirectory)
		return writeError
	}
	finalDirectory := materialStore.instanceDirectory(instanceID)
	_ = os.RemoveAll(finalDirectory)
	if renameError := os.Rename(stagingDirectory, finalDirectory); renameError != nil {
		_ = os.RemoveAll(stagingDirectory)
		return fmt.Errorf("install secret material for %s: %w", instanceID, renameError)
	}
	return nil
}

// writeSecretValuesDirectory creates directory/values/{name} for every value.
func writeSecretValuesDirectory(directory string, secretValues map[string][]byte) error {
	valuesDirectory := filepath.Join(directory, secretValuesSubdirectory)
	if mkdirError := os.MkdirAll(valuesDirectory, secretMaterialDirectoryMode); mkdirError != nil {
		return fmt.Errorf("create secret values directory: %w", mkdirError)
	}
	if chmodError := os.Chmod(directory, secretMaterialDirectoryMode); chmodError != nil {
		return fmt.Errorf("restrict secret material directory: %w", chmodError)
	}
	for secretName, secretValue := range secretValues {
		fileName, validationError := encodeSecretFileName(secretName)
		if validationError != nil {
			return validationError
		}
		if writeError := os.WriteFile(filepath.Join(valuesDirectory, fileName), secretValue, secretMaterialFileMode); writeError != nil {
			return fmt.Errorf("write secret %s: %w", secretName, writeError)
		}
	}
	return nil
}

// ReadInstanceSecrets returns the delivered values of an instance and whether
// material exists from this boot.
func (materialStore *SecretMaterialStore) ReadInstanceSecrets(instanceID string) (map[string][]byte, bool) {
	if validateMaterialPathSegment(instanceID) != nil {
		return nil, false
	}
	valuesDirectory := filepath.Join(materialStore.instanceDirectory(instanceID), secretValuesSubdirectory)
	directoryEntries, readError := os.ReadDir(valuesDirectory)
	if readError != nil {
		return nil, false
	}
	secretValues := make(map[string][]byte, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		secretValue, fileError := os.ReadFile(filepath.Join(valuesDirectory, directoryEntry.Name())) //nolint:gosec // G304: path built from validated names under the material root
		if fileError != nil {
			return nil, false
		}
		secretValues[decodeSecretFileName(directoryEntry.Name())] = secretValue
	}
	return secretValues, true
}

// ReadInstanceSecret returns one delivered value; used by the legacy secret API.
func (materialStore *SecretMaterialStore) ReadInstanceSecret(instanceID string, secretName string) ([]byte, error) {
	if validateMaterialPathSegment(instanceID) != nil || types.ValidateSecretName(secretName) != nil {
		return nil, errors.New("invalid secret name")
	}
	return os.ReadFile(materialStore.SecretValuePath(instanceID, secretName)) //nolint:gosec // G304: path built from validated names under the material root
}

// WriteConnectionFile writes an assembled connection URL for the instance and
// returns its host path. Connection URLs are files only (decision 5).
func (materialStore *SecretMaterialStore) WriteConnectionFile(instanceID string, connectionName string, connectionURL string) (string, error) {
	if validateMaterialPathSegment(instanceID) != nil || validateMaterialPathSegment(connectionName) != nil {
		return "", fmt.Errorf("invalid connection name %q", connectionName)
	}
	connectionsDirectory := filepath.Join(materialStore.instanceDirectory(instanceID), secretConnectionsSubdirectory)
	if mkdirError := os.MkdirAll(connectionsDirectory, secretMaterialDirectoryMode); mkdirError != nil {
		return "", fmt.Errorf("create connections directory: %w", mkdirError)
	}
	connectionPath := filepath.Join(connectionsDirectory, connectionName)
	_ = os.Remove(connectionPath)
	if writeError := os.WriteFile(connectionPath, []byte(connectionURL), secretMaterialFileMode); writeError != nil {
		return "", fmt.Errorf("write connection %s: %w", connectionName, writeError)
	}
	return connectionPath, nil
}

// prepareBaseDirectory creates the base directory, restricts it to root
// (0700), and verifies it is memory-backed when that is required.
func (materialStore *SecretMaterialStore) prepareBaseDirectory() error {
	return prepareMaterialDirectory(materialStore.baseDirectory, materialStore.requireMemoryFilesystem)
}

// prepareMaterialDirectory creates a root-only (0700) directory for secret
// material and, when requireMemory is set, verifies it is on tmpfs or ramfs.
// Shared by instance secrets (Flow 27) and the gate-key cache (Flow 28).
func prepareMaterialDirectory(directory string, requireMemory bool) error {
	if mkdirError := os.MkdirAll(directory, secretMaterialDirectoryMode); mkdirError != nil {
		return fmt.Errorf("create secret material directory: %w", mkdirError)
	}
	if chmodError := os.Chmod(directory, secretMaterialDirectoryMode); chmodError != nil {
		return fmt.Errorf("restrict secret material directory: %w", chmodError)
	}
	if !requireMemory {
		return nil
	}
	if memoryBacked, checkError := isMemoryFilesystem(directory); checkError != nil || !memoryBacked {
		return fmt.Errorf("refusing to write secrets: %s is not on tmpfs or ramfs (%v)", directory, checkError)
	}
	return nil
}

// RemoveInstance deletes the material of an instance.
func (materialStore *SecretMaterialStore) RemoveInstance(instanceID string) {
	if validateMaterialPathSegment(instanceID) != nil {
		return
	}
	_ = os.RemoveAll(materialStore.instanceDirectory(instanceID))
}

// ListInstances returns the instance IDs that have material, sorted, and
// removes leftover staging directories from an interrupted write.
func (materialStore *SecretMaterialStore) ListInstances() []string {
	directoryEntries, readError := os.ReadDir(materialStore.baseDirectory)
	if readError != nil {
		return nil
	}
	instanceIDs := make([]string, 0, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		if strings.HasPrefix(directoryEntry.Name(), secretMaterialStagingMarker) {
			_ = os.RemoveAll(filepath.Join(materialStore.baseDirectory, directoryEntry.Name()))
			continue
		}
		if directoryEntry.IsDir() {
			instanceIDs = append(instanceIDs, directoryEntry.Name())
		}
	}
	sort.Strings(instanceIDs)
	return instanceIDs
}
