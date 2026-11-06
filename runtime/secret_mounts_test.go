// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"strings"
	"testing"
)

// Test mount paths (paths, not credentials).
const (
	testMountPathA = "/run/secrets/a"                    //nolint:gosec // mount path, not a credential
	testMountPathB = "/run/secrets/b"                    //nolint:gosec // mount path, not a credential
	testHostPathA  = "/run/ccattler/secrets/i1/values/a" //nolint:gosec // mount path, not a credential
	testHostPathB  = "/run/ccattler/secrets/i1/values/b" //nolint:gosec // mount path, not a credential
)

func TestBuildSecretMountArgumentsSortedReadOnly(testContext *testing.T) {
	arguments, buildError := buildSecretMountArguments(map[string]string{
		testMountPathB: testHostPathB,
		testMountPathA: testHostPathA,
	})
	if buildError != nil {
		testContext.Fatalf("buildSecretMountArguments: %v", buildError)
	}
	expected := "-v /run/ccattler/secrets/i1/values/a:/run/secrets/a:ro -v /run/ccattler/secrets/i1/values/b:/run/secrets/b:ro"
	if strings.Join(arguments, " ") != expected {
		testContext.Fatalf("arguments = %v", arguments)
	}
}

func TestSecretMountValidationRejectsUnsafePaths(testContext *testing.T) {
	for _, secretMounts := range []map[string]string{
		{"relative/path": testHostPathA},
		{testMountPathA: "/run/ccattler/secrets/../../etc/shadow"},
		{testMountPathA: "/run/x:/etc"},
		{testMountPathA: "relative"},
	} {
		if _, buildError := buildSecretMountArguments(secretMounts); buildError == nil {
			testContext.Fatalf("expected rejection for %v", secretMounts)
		}
	}
}

func TestBuildRunArgumentsIncludesSecretMounts(testContext *testing.T) {
	containerRuntime := NewContainerRuntime()
	spec := Spec{ID: "i1", Image: "nginx:latest", SecretMounts: map[string]string{testMountPathA: testHostPathA}}
	arguments, _, buildError := containerRuntime.buildRunArguments(spec, "test-container", nil)
	if buildError != nil {
		testContext.Fatalf("buildRunArguments: %v", buildError)
	}
	if !strings.Contains(strings.Join(arguments, " "), "-v /run/ccattler/secrets/i1/values/a:/run/secrets/a:ro") {
		testContext.Fatalf("secret mount missing from %v", arguments)
	}
	spec.SecretMounts = map[string]string{testMountPathA: "/tmp/../etc/shadow"}
	if _, _, buildError = containerRuntime.buildRunArguments(spec, "test-container", nil); buildError == nil {
		testContext.Fatalf("expected an error for an unsafe secret mount")
	}
}

func TestBuildSecretOCIMountsReadOnlyBindMounts(testContext *testing.T) {
	ociMounts := buildSecretOCIMounts(map[string]string{testMountPathA: testHostPathA})
	if len(ociMounts) != 1 || ociMounts[0].Source != testHostPathA || ociMounts[0].Destination != testMountPathA {
		testContext.Fatalf("unexpected mounts %+v", ociMounts)
	}
	if !strings.Contains(strings.Join(ociMounts[0].Options, ","), "ro") {
		testContext.Fatalf("mount must be read-only: %v", ociMounts[0].Options)
	}
	if invalidMounts := buildSecretOCIMounts(map[string]string{"/a": "rel"}); invalidMounts != nil {
		testContext.Fatalf("invalid mounts must be skipped, got %+v", invalidMounts)
	}
}
