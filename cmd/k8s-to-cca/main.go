// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// k8s-to-cca converts Kubernetes YAML manifests to CCattler DSL syntax.
// It reads one or more K8s resource documents from a YAML file and outputs
// equivalent .cca declarations to stdout.
//
// Usage: k8s-to-cca <kubernetes-yaml-file>
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/lang"
	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: k8s-to-cca <kubernetes-yaml-file>")
		os.Exit(1)
	}
	inputFilePath := os.Args[1]

	yamlContent, readError := os.ReadFile(inputFilePath) //nolint:gosec // CLI reads user-specified files
	if readError != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", inputFilePath, readError)
		os.Exit(1)
	}

	dslFile, convertError := convertKubernetesYAMLToDSL(yamlContent)
	if convertError != nil {
		fmt.Fprintf(os.Stderr, "error: conversion failed: %v\n", convertError)
		os.Exit(1)
	}

	dslOutput := lang.Format(dslFile)
	fmt.Print(dslOutput)
}

// kubernetesResource is the minimal structure needed to identify and route a
// Kubernetes resource from a multi-document YAML stream.
type kubernetesResource struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   kubernetesMetadata     `yaml:"metadata"`
	Spec       map[string]interface{} `yaml:"spec"`
}

// kubernetesMetadata holds the name and namespace from K8s resource metadata.
type kubernetesMetadata struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// convertKubernetesYAMLToDSL parses a multi-document K8s YAML stream and
// builds an equivalent CCattler DSL AST.
func convertKubernetesYAMLToDSL(yamlContent []byte) (*lang.File, error) {
	resources, parseError := parseKubernetesDocuments(yamlContent)
	if parseError != nil {
		return nil, parseError
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("no Kubernetes resources found in input")
	}

	dslFile := &lang.File{}
	deployments := filterResourcesByKind(resources, "Deployment")
	services := filterResourcesByKind(resources, "Service")
	servicePortMap := buildKubernetesServicePortMap(services)

	for _, deployment := range deployments {
		serviceDecl, convertErr := convertDeploymentToServiceDecl(deployment, servicePortMap)
		if convertErr != nil {
			return nil, fmt.Errorf("deployment %q: %w", deployment.Metadata.Name, convertErr)
		}
		dslFile.Services = append(dslFile.Services, serviceDecl)
	}

	return dslFile, nil
}

// parseKubernetesDocuments splits a multi-document YAML stream (separated by
// "---") into individually parsed resource structs.
func parseKubernetesDocuments(yamlContent []byte) ([]kubernetesResource, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(yamlContent))
	var resources []kubernetesResource

	for {
		var resource kubernetesResource
		decodeError := decoder.Decode(&resource)
		if decodeError == io.EOF {
			break
		}
		if decodeError != nil {
			return nil, fmt.Errorf("YAML parse error: %w", decodeError)
		}
		if resource.Kind == "" {
			continue
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

// filterResourcesByKind returns only resources matching the given K8s kind.
func filterResourcesByKind(resources []kubernetesResource, kind string) []kubernetesResource {
	var filtered []kubernetesResource
	for _, resource := range resources {
		if resource.Kind == kind {
			filtered = append(filtered, resource)
		}
	}
	return filtered
}

// buildKubernetesServicePortMap builds a map from K8s Service name to its
// exposed port numbers, so Deployment conversion can incorporate Service ports.
func buildKubernetesServicePortMap(services []kubernetesResource) map[string][]int {
	servicePortMap := make(map[string][]int)
	for _, service := range services {
		portsRaw, hasPorts := service.Spec["ports"]
		if !hasPorts {
			continue
		}
		portsList, isList := portsRaw.([]interface{})
		if !isList {
			continue
		}
		for _, portEntry := range portsList {
			portEntryMap, isMap := portEntry.(map[string]interface{})
			if !isMap {
				continue
			}
			portNumber := extractIntFromYAML(portEntryMap, "port")
			if portNumber > 0 {
				servicePortMap[service.Metadata.Name] = append(
					servicePortMap[service.Metadata.Name], portNumber,
				)
			}
		}
	}
	return servicePortMap
}

// convertDeploymentToServiceDecl converts a K8s Deployment resource to a
// CCattler ServiceDecl, including image, replicas, ports, env vars, and
// secret volume mounts.
func convertDeploymentToServiceDecl(
	deployment kubernetesResource,
	servicePortMap map[string][]int,
) (lang.ServiceDecl, error) {
	serviceDecl := lang.ServiceDecl{
		Name: sanitizeServiceName(deployment.Metadata.Name),
	}

	replicaCount := extractIntFromYAML(deployment.Spec, "replicas")
	if replicaCount > 0 {
		serviceDecl.Instances = replicaCount
	}

	templateSpec, templateError := extractPodTemplateSpec(deployment.Spec)
	if templateError != nil {
		return serviceDecl, templateError
	}

	containers := extractContainerList(templateSpec)
	if len(containers) == 0 {
		return serviceDecl, fmt.Errorf("no containers found in pod template")
	}

	primaryContainer := containers[0]
	serviceDecl.Image = extractStringFromYAML(primaryContainer, "image")

	containerPorts := extractContainerPorts(primaryContainer)
	servicePorts := servicePortMap[deployment.Metadata.Name]
	serviceDecl.Ports = mergePortLists(containerPorts, servicePorts)

	envVars := extractEnvironmentVariables(primaryContainer)
	if len(envVars) > 0 {
		serviceDecl.Config = &lang.ConfigDecl{EnvVars: envVars}
	}

	volumeMounts := extractVolumeMounts(primaryContainer)
	volumes := extractVolumes(templateSpec)
	serviceDecl.Secrets = resolveSecretVolumeMounts(volumeMounts, volumes)

	return serviceDecl, nil
}

// extractPodTemplateSpec navigates spec.template.spec in a Deployment to
// reach the pod-level specification.
func extractPodTemplateSpec(deploymentSpec map[string]interface{}) (map[string]interface{}, error) {
	templateRaw, hasTemplate := deploymentSpec["template"]
	if !hasTemplate {
		return nil, fmt.Errorf("missing spec.template")
	}
	templateMap, isMap := templateRaw.(map[string]interface{})
	if !isMap {
		return nil, fmt.Errorf("spec.template is not a map")
	}
	specRaw, hasSpec := templateMap["spec"]
	if !hasSpec {
		return nil, fmt.Errorf("missing spec.template.spec")
	}
	specMap, isMap := specRaw.(map[string]interface{})
	if !isMap {
		return nil, fmt.Errorf("spec.template.spec is not a map")
	}
	return specMap, nil
}

// extractContainerList returns the list of containers from a pod spec.
func extractContainerList(podSpec map[string]interface{}) []map[string]interface{} {
	containersRaw, hasContainers := podSpec["containers"]
	if !hasContainers {
		return nil
	}
	containersList, isList := containersRaw.([]interface{})
	if !isList {
		return nil
	}
	var containers []map[string]interface{}
	for _, containerRaw := range containersList {
		containerMap, isMap := containerRaw.(map[string]interface{})
		if isMap {
			containers = append(containers, containerMap)
		}
	}
	return containers
}

// extractContainerPorts reads the ports array from a container spec and returns
// the containerPort values.
func extractContainerPorts(container map[string]interface{}) []int {
	portsRaw, hasPorts := container["ports"]
	if !hasPorts {
		return nil
	}
	portsList, isList := portsRaw.([]interface{})
	if !isList {
		return nil
	}
	var ports []int
	for _, portEntry := range portsList {
		portMap, isMap := portEntry.(map[string]interface{})
		if !isMap {
			continue
		}
		containerPort := extractIntFromYAML(portMap, "containerPort")
		if containerPort > 0 {
			ports = append(ports, containerPort)
		}
	}
	return ports
}

// extractEnvironmentVariables reads the env array from a container spec and
// converts each entry to a DSL EnvVarDecl.
func extractEnvironmentVariables(container map[string]interface{}) []lang.EnvVarDecl {
	envRaw, hasEnv := container["env"]
	if !hasEnv {
		return nil
	}
	envList, isList := envRaw.([]interface{})
	if !isList {
		return nil
	}
	var envVars []lang.EnvVarDecl
	for _, envEntry := range envList {
		envMap, isMap := envEntry.(map[string]interface{})
		if !isMap {
			continue
		}
		envName := extractStringFromYAML(envMap, "name")
		envValue := extractStringFromYAML(envMap, "value")
		if envName != "" {
			envVars = append(envVars, lang.EnvVarDecl{Name: envName, Value: envValue})
		}
	}
	return envVars
}

// volumeMountEntry pairs a K8s volume name with its mount path from a container.
type volumeMountEntry struct {
	name      string // volume reference name from the container spec
	mountPath string // filesystem path inside the container
}

// volumeEntry pairs a K8s volume name with its source details from a pod spec.
type volumeEntry struct {
	name       string // volume name matching volumeMount references
	secretName string // K8s Secret name (non-empty for secret-type volumes)
}

// extractVolumeMounts reads the volumeMounts array from a container spec.
func extractVolumeMounts(container map[string]interface{}) []volumeMountEntry {
	mountsRaw, hasMounts := container["volumeMounts"]
	if !hasMounts {
		return nil
	}
	mountsList, isList := mountsRaw.([]interface{})
	if !isList {
		return nil
	}
	var mounts []volumeMountEntry
	for _, mountEntry := range mountsList {
		mountMap, isMap := mountEntry.(map[string]interface{})
		if !isMap {
			continue
		}
		mounts = append(mounts, volumeMountEntry{
			name:      extractStringFromYAML(mountMap, "name"),
			mountPath: extractStringFromYAML(mountMap, "mountPath"),
		})
	}
	return mounts
}

// extractVolumes reads the volumes array from a pod spec and identifies
// secret-backed volumes.
func extractVolumes(podSpec map[string]interface{}) []volumeEntry {
	volumesRaw, hasVolumes := podSpec["volumes"]
	if !hasVolumes {
		return nil
	}
	volumesList, isList := volumesRaw.([]interface{})
	if !isList {
		return nil
	}
	var volumes []volumeEntry
	for _, volumeRaw := range volumesList {
		volumeMap, isMap := volumeRaw.(map[string]interface{})
		if !isMap {
			continue
		}
		entry := volumeEntry{name: extractStringFromYAML(volumeMap, "name")}
		secretRaw, hasSecret := volumeMap["secret"]
		if hasSecret {
			secretMap, isSecretMap := secretRaw.(map[string]interface{})
			if isSecretMap {
				entry.secretName = extractStringFromYAML(secretMap, "secretName")
			}
		}
		volumes = append(volumes, entry)
	}
	return volumes
}

// resolveSecretVolumeMounts matches container volumeMounts against pod volumes
// to identify secret-backed mounts and converts them to DSL SecretDecls.
func resolveSecretVolumeMounts(
	mounts []volumeMountEntry,
	volumes []volumeEntry,
) []lang.SecretDecl {
	volumesByName := make(map[string]volumeEntry)
	for _, volume := range volumes {
		volumesByName[volume.name] = volume
	}

	var secretDecls []lang.SecretDecl
	for _, mount := range mounts {
		volume, hasVolume := volumesByName[mount.name]
		if !hasVolume || volume.secretName == "" {
			continue
		}
		secretDecls = append(secretDecls, lang.SecretDecl{
			Name:      volume.secretName,
			MountPath: mount.mountPath,
		})
	}
	return secretDecls
}

// mergePortLists combines container ports and service ports into a
// deduplicated sorted list. Container ports take precedence.
func mergePortLists(containerPorts []int, servicePorts []int) []int {
	portSet := make(map[int]bool)
	for _, port := range containerPorts {
		portSet[port] = true
	}
	for _, port := range servicePorts {
		portSet[port] = true
	}
	var merged []int
	for port := range portSet {
		merged = append(merged, port)
	}
	return merged
}

// extractStringFromYAML safely extracts a string value from a YAML map.
func extractStringFromYAML(yamlMap map[string]interface{}, key string) string {
	valueRaw, hasKey := yamlMap[key]
	if !hasKey {
		return ""
	}
	valueString, isString := valueRaw.(string)
	if isString {
		return valueString
	}
	return fmt.Sprintf("%v", valueRaw)
}

// extractIntFromYAML safely extracts an integer value from a YAML map,
// handling both int and float64 representations from the YAML decoder.
func extractIntFromYAML(yamlMap map[string]interface{}, key string) int {
	valueRaw, hasKey := yamlMap[key]
	if !hasKey {
		return 0
	}
	switch typedValue := valueRaw.(type) {
	case int:
		return typedValue
	case float64:
		return int(typedValue)
	case string:
		parsed, parseErr := strconv.Atoi(typedValue)
		if parseErr != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// sanitizeServiceName converts a K8s resource name to a valid CCattler service
// name by replacing disallowed characters with hyphens.
func sanitizeServiceName(kubernetesName string) string {
	return strings.ReplaceAll(kubernetesName, "_", "-")
}
