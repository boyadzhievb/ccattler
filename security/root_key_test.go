// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateSecretRootKeyFileWritesPrivateKey verifies a generated root key
// has the right size and mode and loads back unchanged.
func TestGenerateSecretRootKeyFileWritesPrivateKey(t *testing.T) {
	rootKeyPath := filepath.Join(t.TempDir(), "nested", "secret-root.key")
	if generateError := GenerateSecretRootKeyFile(rootKeyPath); generateError != nil {
		t.Fatalf("generate: %v", generateError)
	}
	fileInfo, statError := os.Stat(rootKeyPath)
	if statError != nil {
		t.Fatalf("stat: %v", statError)
	}
	if fileInfo.Mode().Perm() != secretRootKeyFileMode {
		t.Errorf("mode = %04o, want %04o", fileInfo.Mode().Perm(), secretRootKeyFileMode)
	}
	loadedKey, loadError := LoadSecretRootKeyFile(rootKeyPath)
	if loadError != nil {
		t.Fatalf("load: %v", loadError)
	}
	if len(loadedKey) != SecretRootKeySize {
		t.Errorf("key size = %d, want %d", len(loadedKey), SecretRootKeySize)
	}
}

// TestGenerateSecretRootKeyFileNeverOverwrites verifies a second generation
// fails and leaves the existing key untouched.
func TestGenerateSecretRootKeyFileNeverOverwrites(t *testing.T) {
	rootKeyPath := filepath.Join(t.TempDir(), "secret-root.key")
	if generateError := GenerateSecretRootKeyFile(rootKeyPath); generateError != nil {
		t.Fatalf("first generate: %v", generateError)
	}
	originalKey, _ := LoadSecretRootKeyFile(rootKeyPath)
	if generateError := GenerateSecretRootKeyFile(rootKeyPath); generateError == nil {
		t.Fatal("second generate succeeded, want refusal")
	}
	currentKey, _ := LoadSecretRootKeyFile(rootKeyPath)
	if !bytes.Equal(originalKey, currentKey) {
		t.Error("existing root key was changed")
	}
}

// TestLoadSecretRootKeyFileRejectsInvalidFiles verifies every startup error
// case: missing, directory, group/world-readable and wrong size.
func TestLoadSecretRootKeyFileRejectsInvalidFiles(t *testing.T) {
	temporaryDirectory := t.TempDir()
	writeKeyFile := func(name string, content []byte, mode os.FileMode) string {
		keyPath := filepath.Join(temporaryDirectory, name)
		if writeError := os.WriteFile(keyPath, content, mode); writeError != nil {
			t.Fatalf("write %s: %v", name, writeError)
		}
		if chmodError := os.Chmod(keyPath, mode); chmodError != nil {
			t.Fatalf("chmod %s: %v", name, chmodError)
		}
		return keyPath
	}
	validKey := bytes.Repeat([]byte{7}, SecretRootKeySize)
	symlinkPath := filepath.Join(temporaryDirectory, "link.key")
	if symlinkError := os.Symlink(writeKeyFile("target.key", validKey, 0o400), symlinkPath); symlinkError != nil {
		t.Fatal(symlinkError)
	}
	testCases := []struct {
		name            string
		keyPath         string
		expectedMessage string
	}{
		{"missing", filepath.Join(temporaryDirectory, "absent.key"), "not found"},
		{"directory", temporaryDirectory, "not a regular file"},
		{"group readable", writeKeyFile("group.key", validKey, 0o440), "group- or world-accessible"},
		{"world readable", writeKeyFile("world.key", validKey, 0o404), "group- or world-accessible"},
		{"too short", writeKeyFile("short.key", validKey[:16], 0o400), "has 16 bytes"},
		{"too long", writeKeyFile("long.key", append(validKey, 1), 0o400), "has 33 bytes"},
		{"symlink", symlinkPath, "symlinks are refused"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, loadError := LoadSecretRootKeyFile(testCase.keyPath)
			if loadError == nil || !strings.Contains(loadError.Error(), testCase.expectedMessage) {
				t.Fatalf("error = %v, want one containing %q", loadError, testCase.expectedMessage)
			}
		})
	}
	if _, loadError := LoadSecretRootKeyFile(filepath.Join(temporaryDirectory, "absent.key")); !errors.Is(loadError, ErrSecretRootKeyMissing) {
		t.Errorf("missing file error = %v, want ErrSecretRootKeyMissing", loadError)
	}
}

// TestValidateSecretRootKeyDistinctFromLegacy verifies a root key equal to
// the legacy key is refused and a missing legacy key is allowed.
func TestValidateSecretRootKeyDistinctFromLegacy(t *testing.T) {
	rootKey := bytes.Repeat([]byte{1}, SecretRootKeySize)
	if validateError := ValidateSecretRootKeyDistinctFromLegacy(rootKey, bytes.Clone(rootKey)); validateError == nil {
		t.Error("identical keys accepted")
	}
	if validateError := ValidateSecretRootKeyDistinctFromLegacy(rootKey, bytes.Repeat([]byte{2}, SecretRootKeySize)); validateError != nil {
		t.Errorf("distinct keys refused: %v", validateError)
	}
	if validateError := ValidateSecretRootKeyDistinctFromLegacy(rootKey, nil); validateError != nil {
		t.Errorf("no legacy key refused: %v", validateError)
	}
}
