// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"strings"
	"testing"
)

// TestConvertDeploymentWithEnvAndSecrets verifies that a Deployment with env
// vars, ports, and a secret volume mount produces the expected DSL output.
func TestConvertDeploymentWithEnvAndSecrets(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: auth-manager
spec:
  replicas: 1
  selector:
    matchLabels:
      app: auth-manager
  template:
    metadata:
      labels:
        app: auth-manager
    spec:
      containers:
        - name: auth-manager
          image: auth-manager:latest
          env:
            - name: PORT
              value: "8888"
            - name: DB_HOST
              value: "postgres.local"
          ports:
            - containerPort: 8888
          volumeMounts:
            - name: firebase-secret
              mountPath: "/config"
              readOnly: true
      volumes:
        - name: firebase-secret
          secret:
            secretName: firebase-service-account
---
apiVersion: v1
kind: Service
metadata:
  name: auth-manager
spec:
  type: ClusterIP
  selector:
    app: auth-manager
  ports:
    - protocol: TCP
      port: 8888
      targetPort: 8888
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}

	if len(dslFile.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(dslFile.Services))
	}

	serviceDecl := dslFile.Services[0]
	if serviceDecl.Name != "auth-manager" {
		t.Errorf("expected name auth-manager, got %q", serviceDecl.Name)
	}
	if serviceDecl.Image != "auth-manager:latest" {
		t.Errorf("expected image auth-manager:latest, got %q", serviceDecl.Image)
	}
	if serviceDecl.Instances != 1 {
		t.Errorf("expected 1 instance, got %d", serviceDecl.Instances)
	}
	if len(serviceDecl.Ports) != 1 || serviceDecl.Ports[0] != 8888 {
		t.Errorf("expected ports [8888], got %v", serviceDecl.Ports)
	}
	if serviceDecl.Config == nil || len(serviceDecl.Config.EnvVars) != 2 {
		t.Fatalf("expected 2 env vars, got %v", serviceDecl.Config)
	}
	if serviceDecl.Config.EnvVars[0].Name != "PORT" || serviceDecl.Config.EnvVars[0].Value != "8888" {
		t.Errorf("expected env PORT=8888, got %s=%s",
			serviceDecl.Config.EnvVars[0].Name, serviceDecl.Config.EnvVars[0].Value)
	}
	if len(serviceDecl.Secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(serviceDecl.Secrets))
	}
	if serviceDecl.Secrets[0].Name != "firebase-service-account" {
		t.Errorf("expected secret firebase-service-account, got %q", serviceDecl.Secrets[0].Name)
	}
	if serviceDecl.Secrets[0].MountPath != "/config" {
		t.Errorf("expected mount /config, got %q", serviceDecl.Secrets[0].MountPath)
	}
}

// TestConvertMultipleDeployments verifies that multiple Deployments in a single
// YAML file produce separate service declarations.
func TestConvertMultipleDeployments(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: frontend
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: web
          image: nginx:1.28
          ports:
            - containerPort: 80
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: backend
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: api
          image: api-server:v2
          ports:
            - containerPort: 3000
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	if len(dslFile.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(dslFile.Services))
	}
	if dslFile.Services[0].Name != "frontend" {
		t.Errorf("expected first service frontend, got %q", dslFile.Services[0].Name)
	}
	if dslFile.Services[0].Instances != 3 {
		t.Errorf("expected 3 frontend instances, got %d", dslFile.Services[0].Instances)
	}
	if dslFile.Services[1].Name != "backend" {
		t.Errorf("expected second service backend, got %q", dslFile.Services[1].Name)
	}
	if dslFile.Services[1].Instances != 2 {
		t.Errorf("expected 2 backend instances, got %d", dslFile.Services[1].Instances)
	}
}

// TestConvertServiceAccountIgnored verifies that ServiceAccount resources are
// gracefully skipped (no DSL equivalent).
func TestConvertServiceAccountIgnored(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-sa
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: app
          image: myapp:latest
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	if len(dslFile.Services) != 1 {
		t.Fatalf("expected 1 service (ServiceAccount ignored), got %d", len(dslFile.Services))
	}
	if dslFile.Services[0].Name != "app" {
		t.Errorf("expected service app, got %q", dslFile.Services[0].Name)
	}
}

// TestConvertEmptyYAML verifies that empty or comment-only YAML produces an error.
func TestConvertEmptyYAML(t *testing.T) {
	t.Parallel()
	_, convertError := convertKubernetesYAMLToDSL([]byte("# just a comment\n"))
	if convertError == nil {
		t.Fatal("expected error for empty YAML, got nil")
	}
	if !strings.Contains(convertError.Error(), "no Kubernetes resources") {
		t.Errorf("unexpected error: %v", convertError)
	}
}

// TestConvertMultiPortDeployment verifies that a Deployment with multiple
// container ports produces multiple expose entries.
func TestConvertMultiPortDeployment(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: multi-port-app
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: app
          image: multi:v1
          ports:
            - containerPort: 8080
            - containerPort: 9090
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	serviceDecl := dslFile.Services[0]
	if len(serviceDecl.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d: %v", len(serviceDecl.Ports), serviceDecl.Ports)
	}
	portSet := make(map[int]bool)
	for _, port := range serviceDecl.Ports {
		portSet[port] = true
	}
	if !portSet[8080] || !portSet[9090] {
		t.Errorf("expected ports 8080 and 9090, got %v", serviceDecl.Ports)
	}
}
