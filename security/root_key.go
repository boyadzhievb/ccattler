// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultSecretRootKeyPath is where the control plane keeps the secret root
// key (Flow 29). Every API replica holds the same file; agents never do.
const DefaultSecretRootKeyPath = "/etc/ccattler/secret-root.key" //nolint:gosec // G101: file path, not a credential

// SecretRootKeySize is the root key length in bytes (AES-256).
const SecretRootKeySize = 32

// secretRootKeyFileMode is the mode the root key is written with: readable by
// its owner (root) only.
const secretRootKeyFileMode fs.FileMode = 0o400

// secretRootKeyDirectoryMode is the mode of a parent directory created for a
// new root key file.
const secretRootKeyDirectoryMode fs.FileMode = 0o700

// secretRootKeyForbiddenModeBits are the permission bits a root key file must
// not have: any group or world access.
const secretRootKeyForbiddenModeBits fs.FileMode = 0o077

// ErrSecretRootKeyMissing is returned when the root key file does not exist.
// The server never generates the key itself (no silent generation, Flow 29).
var ErrSecretRootKeyMissing = errors.New("secret root key file not found")

// GenerateSecretRootKeyFile writes a fresh random root key to filePath with
// mode 0400. It refuses to overwrite an existing file, so running it twice
// can never replace the key that protects the keyring.
func GenerateSecretRootKeyFile(filePath string) error {
	if mkdirError := os.MkdirAll(filepath.Dir(filePath), secretRootKeyDirectoryMode); mkdirError != nil {
		return fmt.Errorf("create directory for %s: %w", filePath, mkdirError)
	}
	rootKey := make([]byte, SecretRootKeySize)
	if _, randomError := rand.Read(rootKey); randomError != nil {
		return fmt.Errorf("generate secret root key: %w", randomError)
	}
	keyFile, openError := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, secretRootKeyFileMode) //nolint:gosec // G304: operator-supplied key path
	if openError != nil {
		if errors.Is(openError, fs.ErrExist) {
			return fmt.Errorf("secret root key %s already exists; it is never overwritten", filePath)
		}
		return fmt.Errorf("create %s: %w", filePath, openError)
	}
	if _, writeError := keyFile.Write(rootKey); writeError != nil {
		_ = keyFile.Close()
		_ = os.Remove(filePath)
		return fmt.Errorf("write %s: %w", filePath, writeError)
	}
	return keyFile.Close()
}

// ErrLegacySecretMasterKeyMissing is returned when the legacy master key file
// does not exist.
var ErrLegacySecretMasterKeyMissing = errors.New("legacy secret master key file not found")

// LoadSecretRootKeyFile reads and validates the root key: the file must exist,
// be a regular file without group or world permissions, and hold exactly
// SecretRootKeySize bytes. Any violation is an error, never a fallback.
func LoadSecretRootKeyFile(filePath string) ([]byte, error) {
	return loadPrivateSecretKeyFile(filePath, "secret root key", ErrSecretRootKeyMissing)
}

// LoadLegacySecretMasterKeyFile reads and validates the legacy master key
// (secret-master.key) with the same checks as the root key.
func LoadLegacySecretMasterKeyFile(filePath string) ([]byte, error) {
	return loadPrivateSecretKeyFile(filePath, "legacy secret master key", ErrLegacySecretMasterKeyMissing)
}

// loadPrivateSecretKeyFile reads a 32-byte key file that must be a regular
// file without group or world permissions; missingError wraps a missing file.
func loadPrivateSecretKeyFile(filePath string, keyDescription string, missingError error) ([]byte, error) {
	fileInfo, statError := os.Stat(filePath)
	if errors.Is(statError, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", missingError, filePath)
	}
	if statError != nil {
		return nil, fmt.Errorf("stat %s %s: %w", keyDescription, filePath, statError)
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%s %s is not a regular file", keyDescription, filePath)
	}
	if permissionBits := fileInfo.Mode().Perm(); permissionBits&secretRootKeyForbiddenModeBits != 0 {
		return nil, fmt.Errorf("%s %s has mode %04o; it must not be group- or world-accessible (chmod 400)", keyDescription, filePath, permissionBits)
	}
	keyBytes, readError := os.ReadFile(filePath) //nolint:gosec // G304: operator-supplied key path
	if readError != nil {
		return nil, fmt.Errorf("read %s %s: %w", keyDescription, filePath, readError)
	}
	if len(keyBytes) != SecretRootKeySize {
		return nil, fmt.Errorf("%s %s has %d bytes, want %d", keyDescription, filePath, len(keyBytes), SecretRootKeySize)
	}
	return keyBytes, nil
}

// ValidateSecretRootKeyDistinctFromLegacy fails when the root key is
// byte-identical to the legacy master key: the legacy key was copied to every
// worker before M174 and must never protect the keyring (decision R1).
func ValidateSecretRootKeyDistinctFromLegacy(rootKey []byte, legacyKey []byte) error {
	if len(legacyKey) > 0 && subtle.ConstantTimeCompare(rootKey, legacyKey) == 1 {
		return errors.New("secret root key is identical to the legacy secret-master.key; generate a fresh root key")
	}
	return nil
}
