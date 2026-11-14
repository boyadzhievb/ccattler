// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// referenced_files.go reads, on the client, the local files a configuration
// names, so cca apply and cca diff can send their contents to the API server,
// which never reads a client path (ValidateNoLocalFileReferences).
package lang

import (
	"fmt"

	"github.com/boyadzhievb/ccattler/types"
)

// ClientReferencedFiles holds what cca apply/diff read from local disk for one
// configuration before calling the API server.
type ClientReferencedFiles struct {
	Files          map[string]string // declared path → contents of gate tls cert and config "from" files (the apply body's files map)
	InlineGateKeys map[string]string // gate name → private key PEM from a tls key file; sent only to PUT /api/gate-key, never in the apply body
}

// ReadClientReferencedFiles parses input and reads every file it references
// from disk, relative paths against baseDir. A tls key file goes to
// InlineGateKeys, never to Files. Errors name the gate or config file.
func ReadClientReferencedFiles(input string, baseDir string) (ClientReferencedFiles, error) {
	file, parseError := Parse(input)
	if parseError != nil {
		return ClientReferencedFiles{}, parseError
	}
	diskSource := referencedFileSource{baseDir: baseDir}
	sourceLines := splitSourceLines(input)
	referenced := ClientReferencedFiles{Files: map[string]string{}, InlineGateKeys: map[string]string{}}
	for _, routeDecl := range file.Routes {
		if routeDecl.TLS == nil || routeDecl.TLS.Mode != types.RouteTLSModeManual {
			continue
		}
		if routeDecl.TLS.CertFile != "" {
			certificatePEM, readError := readRouteTLSFile(routeDecl, "cert", routeDecl.TLS.CertFile, diskSource, sourceLines)
			if readError != nil {
				return ClientReferencedFiles{}, readError
			}
			referenced.Files[routeDecl.TLS.CertFile] = certificatePEM
		}
		if routeDecl.TLS.KeySecret == "" && routeDecl.TLS.KeyFile != "" {
			privateKeyPEM, readError := readRouteTLSFile(routeDecl, "key", routeDecl.TLS.KeyFile, diskSource, sourceLines)
			if readError != nil {
				return ClientReferencedFiles{}, readError
			}
			referenced.InlineGateKeys[routeDecl.Name] = privateKeyPEM
		}
	}
	if configError := readServiceConfigFromFiles(file, diskSource, referenced.Files); configError != nil {
		return ClientReferencedFiles{}, configError
	}
	return referenced, nil
}

// readServiceConfigFromFiles adds the contents of every service config "from"
// file to files, keyed by the declared path.
func readServiceConfigFromFiles(file *File, diskSource referencedFileSource, files map[string]string) error {
	for _, serviceDecl := range file.Services {
		if serviceDecl.Config == nil {
			continue
		}
		for _, configFile := range serviceDecl.Config.ConfigFiles {
			if configFile.FromFile == "" {
				continue
			}
			fileBytes, readError := diskSource.readReferencedFile(configFile.FromFile)
			if readError != nil {
				return fmt.Errorf("service %q: config file %q: cannot read %q: %w", serviceDecl.Name, configFile.Path, configFile.FromFile, readError)
			}
			files[configFile.FromFile] = string(fileBytes)
		}
	}
	return nil
}
