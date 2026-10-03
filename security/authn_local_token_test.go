package security

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalTokenAuthenticatorAcceptsCorrectToken(t *testing.T) {
	originalDirectory, _ := os.Getwd()
	temporaryDirectory := t.TempDir()
	os.Chdir(temporaryDirectory) //nolint:errcheck // test helper
	defer os.Chdir(originalDirectory) //nolint:errcheck // restore

	tokenAuthenticator, createError := NewLocalTokenAuthenticator()
	if createError != nil {
		t.Fatal(createError)
	}

	tokenValue, readError := ReadLocalToken()
	if readError != nil {
		t.Fatal(readError)
	}

	request, _ := http.NewRequest("GET", "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+tokenValue)

	result, authError := tokenAuthenticator.Authenticate(request)
	if authError != nil {
		t.Fatalf("expected no error, got: %v", authError)
	}
	if result == nil {
		t.Fatal("expected non-nil result for correct token")
	}
	if result.Principal != localTokenPrincipal {
		t.Errorf("principal: got %q, want %q", result.Principal, localTokenPrincipal)
	}
	if result.PrincipalKind != PrincipalKindUser {
		t.Errorf("principal kind: got %q, want %q", result.PrincipalKind, PrincipalKindUser)
	}
}

func TestLocalTokenAuthenticatorRejectsWrongToken(t *testing.T) {
	originalDirectory, _ := os.Getwd()
	temporaryDirectory := t.TempDir()
	os.Chdir(temporaryDirectory) //nolint:errcheck // test helper
	defer os.Chdir(originalDirectory) //nolint:errcheck // restore

	tokenAuthenticator, createError := NewLocalTokenAuthenticator()
	if createError != nil {
		t.Fatal(createError)
	}

	request, _ := http.NewRequest("GET", "/api/status", nil)
	request.Header.Set("Authorization", "Bearer wrong-token-value")

	_, authError := tokenAuthenticator.Authenticate(request)
	if authError == nil {
		t.Fatal("expected error for wrong token, got nil")
	}
	_ = tokenAuthenticator // suppress unused
}

func TestLocalTokenAuthenticatorRejectsNoHeader(t *testing.T) {
	originalDirectory, _ := os.Getwd()
	temporaryDirectory := t.TempDir()
	os.Chdir(temporaryDirectory) //nolint:errcheck // test helper
	defer os.Chdir(originalDirectory) //nolint:errcheck // restore

	tokenAuthenticator, createError := NewLocalTokenAuthenticator()
	if createError != nil {
		t.Fatal(createError)
	}

	request, _ := http.NewRequest("GET", "/api/status", nil)

	result, authError := tokenAuthenticator.Authenticate(request)
	if authError != nil {
		t.Fatalf("expected nil error for absent header, got: %v", authError)
	}
	if result != nil {
		t.Fatal("expected nil result when no Authorization header is set")
	}
}

func TestLocalTokenFilePermissions(t *testing.T) {
	originalDirectory, _ := os.Getwd()
	temporaryDirectory := t.TempDir()
	os.Chdir(temporaryDirectory) //nolint:errcheck // test helper
	defer os.Chdir(originalDirectory) //nolint:errcheck // restore

	_, createError := NewLocalTokenAuthenticator()
	if createError != nil {
		t.Fatal(createError)
	}

	tokenFilePath := filepath.Join(localTokenDirectoryName, localTokenFileName)
	fileInfo, statError := os.Stat(tokenFilePath)
	if statError != nil {
		t.Fatalf("stat token file: %v", statError)
	}

	fileMode := fileInfo.Mode().Perm()
	if fileMode != 0600 {
		t.Errorf("token file permissions: got %04o, want 0600", fileMode)
	}
}

func TestLocalTokenAuthenticatorRejectsEmptyBearerToken(t *testing.T) {
	originalDirectory, _ := os.Getwd()
	temporaryDirectory := t.TempDir()
	os.Chdir(temporaryDirectory) //nolint:errcheck // test helper
	defer os.Chdir(originalDirectory) //nolint:errcheck // restore

	tokenAuthenticator, createError := NewLocalTokenAuthenticator()
	if createError != nil {
		t.Fatal(createError)
	}

	request, _ := http.NewRequest("GET", "/api/status", nil)
	request.Header.Set("Authorization", "Bearer ")

	_, authError := tokenAuthenticator.Authenticate(request)
	if authError == nil {
		t.Fatal("expected error for empty bearer token")
	}
}

func TestSpoofedXCCattlerUserHeaderRejected(t *testing.T) {
	originalDirectory, _ := os.Getwd()
	temporaryDirectory := t.TempDir()
	os.Chdir(temporaryDirectory) //nolint:errcheck // test helper
	defer os.Chdir(originalDirectory) //nolint:errcheck // restore

	tokenAuthenticator, createError := NewLocalTokenAuthenticator()
	if createError != nil {
		t.Fatal(createError)
	}

	// Attacker sends only X-CCattler-User, no bearer token.
	request, _ := http.NewRequest("GET", "/api/status", nil)
	request.Header.Set("X-CCattler-User", "admin")

	result, authError := tokenAuthenticator.Authenticate(request)
	if authError != nil {
		t.Fatalf("expected nil error (unrecognized, not rejected), got: %v", authError)
	}
	if result != nil {
		t.Fatal("X-CCattler-User header should NOT authenticate — expected nil result")
	}
}

func TestLoadLocalTokenAuthenticator(t *testing.T) {
	originalDirectory, _ := os.Getwd()
	temporaryDirectory := t.TempDir()
	os.Chdir(temporaryDirectory) //nolint:errcheck // test helper
	defer os.Chdir(originalDirectory) //nolint:errcheck // restore

	originalAuthenticator, createError := NewLocalTokenAuthenticator()
	if createError != nil {
		t.Fatal(createError)
	}

	loadedAuthenticator, loadError := LoadLocalTokenAuthenticator(originalAuthenticator.TokenFilePath())
	if loadError != nil {
		t.Fatalf("load: %v", loadError)
	}

	tokenValue, _ := ReadLocalToken()
	request, _ := http.NewRequest("GET", "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+tokenValue)

	result, authError := loadedAuthenticator.Authenticate(request)
	if authError != nil {
		t.Fatalf("loaded authenticator should accept token: %v", authError)
	}
	if result == nil || result.Principal != localTokenPrincipal {
		t.Errorf("loaded authenticator returned wrong principal: %v", result)
	}
}
