// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeReferencedTestFile writes contents to name inside directory.
func writeReferencedTestFile(t *testing.T, directory string, name string, contents string) {
	t.Helper()
	if writeError := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); writeError != nil {
		t.Fatal(writeError)
	}
}

// TestReadClientReferencedFilesSeparatesKeysFromFiles: cert and config "from"
// files go to Files under their declared (relative) path, read against
// baseDir; the tls key goes only to InlineGateKeys, never into Files.
func TestReadClientReferencedFilesSeparatesKeysFromFiles(t *testing.T) {
	baseDirectory := t.TempDir()
	writeReferencedTestFile(t, baseDirectory, "cert.pem", "certificate-placeholder")
	writeReferencedTestFile(t, baseDirectory, "key.pem", "private-key-placeholder")
	writeReferencedTestFile(t, baseDirectory, "app.conf", "listen 8080\n")
	configuration := `service web {
    image nginx:1.27
    config {
        file "/etc/web/app.conf" from "app.conf"
    }
}

gate shop {
    host "shop.example.com"
    service web port 8080
    tls {
        cert "cert.pem"
        key "key.pem"
    }
}
`
	referenced, readError := ReadClientReferencedFiles(configuration, baseDirectory)
	if readError != nil {
		t.Fatal(readError)
	}
	if referenced.Files["cert.pem"] != "certificate-placeholder" || referenced.Files["app.conf"] != "listen 8080\n" {
		t.Errorf("Files = %v, want cert.pem and app.conf contents", referenced.Files)
	}
	if _, keyInFiles := referenced.Files["key.pem"]; keyInFiles || len(referenced.Files) != 2 {
		t.Errorf("Files = %v, must hold exactly the cert and the config file, never the key", referenced.Files)
	}
	if referenced.InlineGateKeys["shop"] != "private-key-placeholder" || len(referenced.InlineGateKeys) != 1 {
		t.Errorf("InlineGateKeys = %v, want shop's key only", referenced.InlineGateKeys)
	}
}

// TestReadClientReferencedFilesKeySecretReadsNoKey: a gate whose key is a key
// secret sends only its certificate; no key file is read.
func TestReadClientReferencedFilesKeySecretReadsNoKey(t *testing.T) {
	baseDirectory := t.TempDir()
	writeReferencedTestFile(t, baseDirectory, "cert.pem", "certificate-placeholder")
	configuration := `gate shop {
    host "shop.example.com"
    service web port 8080
    tls {
        cert "cert.pem"
        key secret "shop-key"
    }
}
`
	referenced, readError := ReadClientReferencedFiles(configuration, baseDirectory)
	if readError != nil {
		t.Fatal(readError)
	}
	if len(referenced.InlineGateKeys) != 0 || len(referenced.Files) != 1 {
		t.Errorf("got Files %v InlineGateKeys %v, want only the certificate", referenced.Files, referenced.InlineGateKeys)
	}
}

// TestReadClientReferencedFilesMissingFileNamesIt: a missing referenced file
// fails before anything is sent, naming the service and the path.
func TestReadClientReferencedFilesMissingFileNamesIt(t *testing.T) {
	configuration := `service web {
    image nginx:1.27
    config {
        file "/etc/web/app.conf" from "missing.conf"
    }
}
`
	_, readError := ReadClientReferencedFiles(configuration, t.TempDir())
	if readError == nil || !strings.Contains(readError.Error(), `"web"`) || !strings.Contains(readError.Error(), "missing.conf") {
		t.Errorf("error = %v, want one naming service web and missing.conf", readError)
	}
}
