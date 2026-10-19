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

// TestConvertResourceRequests verifies that CPU and memory requests are
// extracted from the container spec.
func TestConvertResourceRequests(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: with-resources
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: app
          image: myapp:v1
          resources:
            requests:
              cpu: "500m"
              memory: "256Mi"
            limits:
              cpu: "1000m"
              memory: "512Mi"
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	serviceDecl := dslFile.Services[0]
	if serviceDecl.Resources == nil {
		t.Fatal("expected resources, got nil")
	}
	if serviceDecl.Resources.CPU != "500m" {
		t.Errorf("expected CPU 500m, got %q", serviceDecl.Resources.CPU)
	}
	if serviceDecl.Resources.Memory != "256Mi" {
		t.Errorf("expected Memory 256Mi, got %q", serviceDecl.Resources.Memory)
	}
}

// TestConvertHealthCheck verifies that a livenessProbe httpGet is converted
// to a CCattler health block.
func TestConvertHealthCheck(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: with-health
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: app
          image: myapp:v1
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
            periodSeconds: 10
            timeoutSeconds: 3
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	serviceDecl := dslFile.Services[0]
	if serviceDecl.Health == nil {
		t.Fatal("expected health check, got nil")
	}
	if serviceDecl.Health.Method != "http" {
		t.Errorf("expected method http, got %q", serviceDecl.Health.Method)
	}
	if serviceDecl.Health.Path != "/healthz" {
		t.Errorf("expected path /healthz, got %q", serviceDecl.Health.Path)
	}
	if serviceDecl.Health.Interval != "10s" {
		t.Errorf("expected interval 10s, got %q", serviceDecl.Health.Interval)
	}
	if serviceDecl.Health.Timeout != "3s" {
		t.Errorf("expected timeout 3s, got %q", serviceDecl.Health.Timeout)
	}
}

// TestConvertRollingUpdate verifies that a Deployment RollingUpdate strategy
// is converted to a CCattler update block.
func TestConvertRollingUpdate(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: with-rolling
spec:
  replicas: 4
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 2
  template:
    spec:
      containers:
        - name: app
          image: myapp:v1
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	serviceDecl := dslFile.Services[0]
	if serviceDecl.Update == nil {
		t.Fatal("expected update strategy, got nil")
	}
	if serviceDecl.Update.MaxUnavailable != 1 {
		t.Errorf("expected max_unavailable 1, got %d", serviceDecl.Update.MaxUnavailable)
	}
	if serviceDecl.Update.MaxExtra != 2 {
		t.Errorf("expected max_extra 2, got %d", serviceDecl.Update.MaxExtra)
	}
}

// TestConvertLabelsToTags verifies that Deployment metadata labels (excluding
// standard K8s labels) are converted to CCattler tags.
func TestConvertLabelsToTags(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tagged-app
  labels:
    app: tagged-app
    team: payments
    tier: backend
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: app
          image: myapp:v1
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	serviceDecl := dslFile.Services[0]
	if len(serviceDecl.Tags) != 2 {
		t.Fatalf("expected 2 tags (team, tier; app skipped), got %d: %v",
			len(serviceDecl.Tags), serviceDecl.Tags)
	}
	tagMap := make(map[string]string)
	for _, tag := range serviceDecl.Tags {
		tagMap[tag.Key] = tag.Value
	}
	if tagMap["team"] != "payments" {
		t.Errorf("expected tag team=payments, got %q", tagMap["team"])
	}
	if tagMap["tier"] != "backend" {
		t.Errorf("expected tag tier=backend, got %q", tagMap["tier"])
	}
}

// TestConvertSecretKeyRefEnvVar verifies that secretKeyRef env vars produce
// a descriptive placeholder value instead of an empty string.
func TestConvertSecretKeyRefEnvVar(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: secret-env-app
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: app
          image: myapp:v1
          env:
            - name: DB_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: db-credentials
                  key: password
            - name: PLAIN_VAR
              value: "hello"
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	serviceDecl := dslFile.Services[0]
	if serviceDecl.Config == nil || len(serviceDecl.Config.EnvVars) != 2 {
		t.Fatalf("expected 2 env vars, got %v", serviceDecl.Config)
	}
	dbPasswordEnv := serviceDecl.Config.EnvVars[0]
	if dbPasswordEnv.Name != "DB_PASSWORD" {
		t.Errorf("expected env name DB_PASSWORD, got %q", dbPasswordEnv.Name)
	}
	if dbPasswordEnv.Value != "secret:db-credentials/password" {
		t.Errorf("expected value 'secret:db-credentials/password', got %q", dbPasswordEnv.Value)
	}
	plainEnv := serviceDecl.Config.EnvVars[1]
	if plainEnv.Value != "hello" {
		t.Errorf("expected value 'hello', got %q", plainEnv.Value)
	}
}
