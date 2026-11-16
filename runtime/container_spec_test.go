package runtime

import (
	"context"
	"slices"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// specTestSecretHostPath is a granted secret file on the node's memory filesystem (Flow 27).
const specTestSecretHostPath = "/run/ccattler/secrets/inst-1/values/db-password" //nolint:gosec // G101: a filesystem path, not a credential

// specTestSecretContainerPath is where the container reads that secret.
const specTestSecretContainerPath = "/run/cca-secrets/db-password" //nolint:gosec // G101: a filesystem path, not a credential

// generateTestContainerSpec applies spec options to containerd's default
// Linux spec, as containerd.WithNewSpec does, without containerd or an image.
func generateTestContainerSpec(t *testing.T, ociOptions []oci.SpecOpts) *oci.Spec {
	t.Helper()
	namespacedContext := namespaces.WithNamespace(context.Background(), "spec-test")
	generatedSpec, generateError := oci.GenerateSpec(namespacedContext, nil, &containers.Container{ID: "spec-test"}, ociOptions...)
	if generateError != nil {
		t.Fatalf("generate spec: %v", generateError)
	}
	return generatedSpec
}

// assertNoMountOnRun fails when anything is mounted over /run, which would hide the image's /run.
func assertNoMountOnRun(t *testing.T, generatedSpec *oci.Spec) {
	t.Helper()
	for _, containerMount := range generatedSpec.Mounts {
		if containerMount.Destination == "/run" {
			t.Errorf("spec mounts %s on /run (%v); the image's /run must stay visible", containerMount.Type, containerMount.Options)
		}
	}
}

// assertReadOnlySecretMount fails unless the granted secret is a read-only bind mount from the node.
func assertReadOnlySecretMount(t *testing.T, generatedSpec *oci.Spec) {
	t.Helper()
	index := slices.IndexFunc(generatedSpec.Mounts, func(containerMount specs.Mount) bool {
		return containerMount.Destination == specTestSecretContainerPath
	})
	if index < 0 {
		t.Fatalf("no mount at %s", specTestSecretContainerPath)
	}
	secretMount := generatedSpec.Mounts[index]
	if secretMount.Type != "bind" || secretMount.Source != specTestSecretHostPath {
		t.Errorf("secret mount = %s from %s, want bind from %s", secretMount.Type, secretMount.Source, specTestSecretHostPath)
	}
	for _, requiredOption := range []string{"ro", "nosuid", "nodev", "noexec"} {
		if !slices.Contains(secretMount.Options, requiredOption) {
			t.Errorf("secret mount options %v lack %q", secretMount.Options, requiredOption)
		}
	}
}

// TestContainerSpecKeepsImageRun checks a main container sees the image's /run
// and still gets its secret as a read-only bind mount (spec container-filesystem).
func TestContainerSpecKeepsImageRun(t *testing.T) {
	containerdRuntime := &ContainerdRuntime{}
	containerSpec := Spec{
		ID:           "inst-1",
		SecretMounts: map[string]string{specTestSecretContainerPath: specTestSecretHostPath},
	}
	generatedSpec := generateTestContainerSpec(t, containerdRuntime.buildOCISpecOptions(containerSpec, ""))
	assertNoMountOnRun(t, generatedSpec)
	assertReadOnlySecretMount(t, generatedSpec)
}

// TestInitContainerSpecKeepsImageRun checks an init container sees the image's
// /run as well (spec container-filesystem).
func TestInitContainerSpecKeepsImageRun(t *testing.T) {
	containerdRuntime := &ContainerdRuntime{}
	execSpec := ExecSpec{
		Command:      "true",
		SecretMounts: map[string]string{specTestSecretContainerPath: specTestSecretHostPath},
	}
	generatedSpec := generateTestContainerSpec(t, containerdRuntime.buildInitContainerSpecOptions(execSpec, "cca-init-spec-test"))
	assertNoMountOnRun(t, generatedSpec)
	assertReadOnlySecretMount(t, generatedSpec)
}
