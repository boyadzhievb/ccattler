package lang

import "testing"

func TestParseMinimalService(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 3
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	svc := file.Services[0]
	if svc.Name != "web" {
		t.Errorf("name: got %s, want web", svc.Name)
	}
	if svc.Image != "nginx:1.28" {
		t.Errorf("image: got %s, want nginx:1.28", svc.Image)
	}
	if svc.Instances != 3 {
		t.Errorf("instances: got %d, want 3", svc.Instances)
	}
}

func TestParseServiceWithExpose(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 3
    expose 8080
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	svc := file.Services[0]
	if len(svc.Ports) != 1 || svc.Ports[0] != 8080 {
		t.Errorf("ports: got %v, want [8080]", svc.Ports)
	}
}

func TestParseServiceWithResources(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 3
    resources {
        cpu 500m
        memory 512Mi
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	svc := file.Services[0]
	if svc.Resources == nil {
		t.Fatal("expected resources block")
	}
	if svc.Resources.CPU != "500m" {
		t.Errorf("cpu: got %s, want 500m", svc.Resources.CPU)
	}
	if svc.Resources.Memory != "512Mi" {
		t.Errorf("memory: got %s, want 512Mi", svc.Resources.Memory)
	}
}

func TestParseServiceWithHealth(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
    health {
        http /health
        every 10s
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	svc := file.Services[0]
	if svc.Health == nil {
		t.Fatal("expected health block")
	}
	if svc.Health.Method != "http" {
		t.Errorf("method: got %s, want http", svc.Health.Method)
	}
	if svc.Health.Path != "/health" {
		t.Errorf("path: got %s, want /health", svc.Health.Path)
	}
	if svc.Health.Interval != "10s" {
		t.Errorf("interval: got %s, want 10s", svc.Health.Interval)
	}
}

func TestParseFullService(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 3
    expose 8080

    health {
        http /health
        every 10s
    }

    resources {
        cpu 500m
        memory 512Mi
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	svc := file.Services[0]
	if svc.Name != "web" {
		t.Errorf("name: %s", svc.Name)
	}
	if svc.Image != "nginx:1.28" {
		t.Errorf("image: %s", svc.Image)
	}
	if svc.Instances != 3 {
		t.Errorf("instances: %d", svc.Instances)
	}
	if len(svc.Ports) != 1 || svc.Ports[0] != 8080 {
		t.Errorf("ports: %v", svc.Ports)
	}
	if svc.Resources == nil || svc.Resources.CPU != "500m" {
		t.Errorf("resources: %+v", svc.Resources)
	}
	if svc.Health == nil || svc.Health.Path != "/health" {
		t.Errorf("health: %+v", svc.Health)
	}
}

func TestParseMultipleServices(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 3
}

service api {
    image myapp:latest
    instances 2
    expose 9090
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(file.Services))
	}
	if file.Services[0].Name != "web" {
		t.Errorf("first service: %s", file.Services[0].Name)
	}
	if file.Services[1].Name != "api" {
		t.Errorf("second service: %s", file.Services[1].Name)
	}
	if file.Services[1].Instances != 2 {
		t.Errorf("api instances: %d", file.Services[1].Instances)
	}
}

func TestParseWithComments(t *testing.T) {
	input := `# Frontend service
service web {
    image nginx:1.28
    instances 3
    # main port
    expose 8080
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	if file.Services[0].Ports[0] != 8080 {
		t.Errorf("port: %d", file.Services[0].Ports[0])
	}
}

func TestParseEmptyFile(t *testing.T) {
	file, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 0 {
		t.Fatalf("expected 0 services, got %d", len(file.Services))
	}
}

func TestParseErrorMissingBrace(t *testing.T) {
	input := `service web
    image nginx:1.28
}`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for missing opening brace")
	}
}

func TestParseErrorUnknownField(t *testing.T) {
	input := `service web {
    image nginx:1.28
    bogus 42
}`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestParseErrorUnknownDeclaration(t *testing.T) {
	_, err := Parse(`foobar {}`)
	if err == nil {
		t.Fatal("expected error for unknown declaration")
	}
}

func TestParseVolumeDeclaration(t *testing.T) {
	input := `volume database {
    size 100Gi
    persistent true
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(file.Volumes))
	}
	volumeDecl := file.Volumes[0]
	if volumeDecl.Name != "database" {
		t.Errorf("name: got %s, want database", volumeDecl.Name)
	}
	if volumeDecl.Size != "100Gi" {
		t.Errorf("size: got %s, want 100Gi", volumeDecl.Size)
	}
	if !volumeDecl.Persistent {
		t.Error("persistent: got false, want true")
	}
}

func TestParseServiceWithVolumeMount(t *testing.T) {
	input := `service postgres {
    image postgres:16
    instances 1
    volume pgdata /var/lib/postgresql/data
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	serviceDecl := file.Services[0]
	if len(serviceDecl.VolumeMounts) != 1 {
		t.Fatalf("expected 1 volume mount, got %d", len(serviceDecl.VolumeMounts))
	}
	if serviceDecl.VolumeMounts[0].VolumeName != "pgdata" {
		t.Errorf("volume name: got %s, want pgdata", serviceDecl.VolumeMounts[0].VolumeName)
	}
	if serviceDecl.VolumeMounts[0].MountPath != "/var/lib/postgresql/data" {
		t.Errorf("mount path: got %s, want /var/lib/postgresql/data", serviceDecl.VolumeMounts[0].MountPath)
	}
}

func TestParseVolumeAndServiceTogether(t *testing.T) {
	input := `volume pgdata {
    size 50Gi
    persistent true
}

service postgres {
    image postgres:16
    instances 1
    volume pgdata /var/lib/postgresql/data
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(file.Volumes))
	}
	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	if file.Volumes[0].Name != "pgdata" {
		t.Errorf("volume name: got %s, want pgdata", file.Volumes[0].Name)
	}
	if file.Services[0].VolumeMounts[0].VolumeName != "pgdata" {
		t.Errorf("service volume mount: got %s, want pgdata", file.Services[0].VolumeMounts[0].VolumeName)
	}
}

func TestParseVolumeErrorUnknownField(t *testing.T) {
	input := `volume pgdata {
    bogus 42
}`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for unknown volume field")
	}
}

func TestParseInitSteps(t *testing.T) {
	input := `service api {
    image my-api:1.4
    instances 3

    init {
        exec "migrate-db"
        timeout 30s
    }

    init {
        exec "generate-config"
        timeout 10s
        retry 3
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	serviceDecl := file.Services[0]
	if len(serviceDecl.InitSteps) != 2 {
		t.Fatalf("expected 2 init steps, got %d", len(serviceDecl.InitSteps))
	}
	if serviceDecl.InitSteps[0].Exec != "migrate-db" {
		t.Errorf("step 0 exec: got %q, want %q", serviceDecl.InitSteps[0].Exec, "migrate-db")
	}
	if serviceDecl.InitSteps[0].Timeout != "30s" {
		t.Errorf("step 0 timeout: got %q, want %q", serviceDecl.InitSteps[0].Timeout, "30s")
	}
	if serviceDecl.InitSteps[0].Retry != 0 {
		t.Errorf("step 0 retry: got %d, want 0", serviceDecl.InitSteps[0].Retry)
	}
	if serviceDecl.InitSteps[1].Exec != "generate-config" {
		t.Errorf("step 1 exec: got %q, want %q", serviceDecl.InitSteps[1].Exec, "generate-config")
	}
	if serviceDecl.InitSteps[1].Retry != 3 {
		t.Errorf("step 1 retry: got %d, want 3", serviceDecl.InitSteps[1].Retry)
	}
}

func TestParseInitStepRequiresExec(t *testing.T) {
	input := `service api {
    image my-api:1.4
    instances 1

    init {
        timeout 30s
    }
}`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for init block without exec")
	}
}

// TestParseServiceWithStartupProbe verifies that a startup probe block with HTTP
// method and all configurable fields is correctly parsed into a ProbeDecl.
func TestParseServiceWithStartupProbe(t *testing.T) {
	input := `service web {
    image nginx:1.27
    instances 1
    startup {
        http /health
        port 8080
        every 2s
        timeout 1s
        failure_threshold 30
        success_threshold 1
        initial_delay 5s
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	serviceDecl := file.Services[0]
	if serviceDecl.Startup == nil {
		t.Fatal("expected startup probe block")
	}
	startupProbe := serviceDecl.Startup
	if startupProbe.Method != "http" {
		t.Errorf("method: got %s, want http", startupProbe.Method)
	}
	if startupProbe.Path != "/health" {
		t.Errorf("path: got %s, want /health", startupProbe.Path)
	}
	if startupProbe.Port != 8080 {
		t.Errorf("port: got %d, want 8080", startupProbe.Port)
	}
	if startupProbe.Interval != "2s" {
		t.Errorf("interval: got %s, want 2s", startupProbe.Interval)
	}
	if startupProbe.Timeout != "1s" {
		t.Errorf("timeout: got %s, want 1s", startupProbe.Timeout)
	}
	if startupProbe.FailureThreshold != 30 {
		t.Errorf("failure_threshold: got %d, want 30", startupProbe.FailureThreshold)
	}
	if startupProbe.SuccessThreshold != 1 {
		t.Errorf("success_threshold: got %d, want 1", startupProbe.SuccessThreshold)
	}
	if startupProbe.InitialDelay != "5s" {
		t.Errorf("initial_delay: got %s, want 5s", startupProbe.InitialDelay)
	}
}

// TestParseServiceWithLivenessProbe verifies that a liveness probe block with TCP
// method and a subset of fields is correctly parsed into a ProbeDecl.
func TestParseServiceWithLivenessProbe(t *testing.T) {
	input := `service web {
    image nginx:1.27
    instances 1
    liveness {
        tcp
        port 8080
        every 10s
        timeout 2s
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	serviceDecl := file.Services[0]
	if serviceDecl.Liveness == nil {
		t.Fatal("expected liveness probe block")
	}
	livenessProbe := serviceDecl.Liveness
	if livenessProbe.Method != "tcp" {
		t.Errorf("method: got %s, want tcp", livenessProbe.Method)
	}
	if livenessProbe.Path != "" {
		t.Errorf("path: got %s, want empty string for tcp probe", livenessProbe.Path)
	}
	if livenessProbe.Port != 8080 {
		t.Errorf("port: got %d, want 8080", livenessProbe.Port)
	}
	if livenessProbe.Interval != "10s" {
		t.Errorf("interval: got %s, want 10s", livenessProbe.Interval)
	}
	if livenessProbe.Timeout != "2s" {
		t.Errorf("timeout: got %s, want 2s", livenessProbe.Timeout)
	}
}

// TestParseServiceWithReadinessProbe verifies that a readiness probe block with HTTP
// method and minimal fields is correctly parsed into a ProbeDecl.
func TestParseServiceWithReadinessProbe(t *testing.T) {
	input := `service web {
    image nginx:1.27
    instances 1
    readiness {
        http /ready
        port 8080
        every 5s
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	serviceDecl := file.Services[0]
	if serviceDecl.Readiness == nil {
		t.Fatal("expected readiness probe block")
	}
	readinessProbe := serviceDecl.Readiness
	if readinessProbe.Method != "http" {
		t.Errorf("method: got %s, want http", readinessProbe.Method)
	}
	if readinessProbe.Path != "/ready" {
		t.Errorf("path: got %s, want /ready", readinessProbe.Path)
	}
	if readinessProbe.Port != 8080 {
		t.Errorf("port: got %d, want 8080", readinessProbe.Port)
	}
	if readinessProbe.Interval != "5s" {
		t.Errorf("interval: got %s, want 5s", readinessProbe.Interval)
	}
}

// TestParseServiceWithAllThreeProbes verifies that a service can declare startup,
// liveness, and readiness probe blocks together, each parsed into its own ProbeDecl.
func TestParseServiceWithAllThreeProbes(t *testing.T) {
	input := `service web {
    image nginx:1.27
    instances 1
    startup {
        http /health
        port 8080
        every 2s
        timeout 1s
        failure_threshold 30
        initial_delay 5s
    }
    liveness {
        tcp
        port 8080
        every 10s
        timeout 2s
    }
    readiness {
        http /ready
        port 8080
        every 5s
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	serviceDecl := file.Services[0]

	// Verify startup probe
	if serviceDecl.Startup == nil {
		t.Fatal("expected startup probe block")
	}
	if serviceDecl.Startup.Method != "http" {
		t.Errorf("startup method: got %s, want http", serviceDecl.Startup.Method)
	}
	if serviceDecl.Startup.Path != "/health" {
		t.Errorf("startup path: got %s, want /health", serviceDecl.Startup.Path)
	}
	if serviceDecl.Startup.Port != 8080 {
		t.Errorf("startup port: got %d, want 8080", serviceDecl.Startup.Port)
	}
	if serviceDecl.Startup.Interval != "2s" {
		t.Errorf("startup interval: got %s, want 2s", serviceDecl.Startup.Interval)
	}
	if serviceDecl.Startup.InitialDelay != "5s" {
		t.Errorf("startup initial_delay: got %s, want 5s", serviceDecl.Startup.InitialDelay)
	}

	// Verify liveness probe
	if serviceDecl.Liveness == nil {
		t.Fatal("expected liveness probe block")
	}
	if serviceDecl.Liveness.Method != "tcp" {
		t.Errorf("liveness method: got %s, want tcp", serviceDecl.Liveness.Method)
	}
	if serviceDecl.Liveness.Port != 8080 {
		t.Errorf("liveness port: got %d, want 8080", serviceDecl.Liveness.Port)
	}
	if serviceDecl.Liveness.Interval != "10s" {
		t.Errorf("liveness interval: got %s, want 10s", serviceDecl.Liveness.Interval)
	}

	// Verify readiness probe
	if serviceDecl.Readiness == nil {
		t.Fatal("expected readiness probe block")
	}
	if serviceDecl.Readiness.Method != "http" {
		t.Errorf("readiness method: got %s, want http", serviceDecl.Readiness.Method)
	}
	if serviceDecl.Readiness.Path != "/ready" {
		t.Errorf("readiness path: got %s, want /ready", serviceDecl.Readiness.Path)
	}
	if serviceDecl.Readiness.Port != 8080 {
		t.Errorf("readiness port: got %d, want 8080", serviceDecl.Readiness.Port)
	}
	if serviceDecl.Readiness.Interval != "5s" {
		t.Errorf("readiness interval: got %s, want 5s", serviceDecl.Readiness.Interval)
	}
}

// TestParseProbeDefaultThresholds verifies that FailureThreshold defaults to 3
// and SuccessThreshold defaults to 1 when neither is explicitly set in the probe block.
func TestParseProbeDefaultThresholds(t *testing.T) {
	input := `service web {
    image nginx:1.27
    instances 1
    liveness {
        http /health
        port 8080
        every 10s
    }
}`
	file, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	serviceDecl := file.Services[0]
	if serviceDecl.Liveness == nil {
		t.Fatal("expected liveness probe block")
	}
	livenessProbe := serviceDecl.Liveness
	if livenessProbe.FailureThreshold != 3 {
		t.Errorf("failure_threshold default: got %d, want 3", livenessProbe.FailureThreshold)
	}
	if livenessProbe.SuccessThreshold != 1 {
		t.Errorf("success_threshold default: got %d, want 1", livenessProbe.SuccessThreshold)
	}
}

// TestParseServiceWithExecProbe verifies that a probe block with the exec
// method parses the command string correctly.
func TestParseServiceWithExecProbe(t *testing.T) {
	file, err := Parse(`service worker {
    image worker:2.1
    instances 1
    liveness {
        exec "healthcheck --deep"
        every 30s
        timeout 5s
        failure_threshold 5
    }
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	serviceDecl := file.Services[0]
	if serviceDecl.Liveness == nil {
		t.Fatal("expected liveness probe to be parsed")
	}
	livenessProbe := serviceDecl.Liveness
	if livenessProbe.Method != "exec" {
		t.Errorf("method: got %q, want exec", livenessProbe.Method)
	}
	if livenessProbe.Path != "healthcheck --deep" {
		t.Errorf("path (exec command): got %q, want %q", livenessProbe.Path, "healthcheck --deep")
	}
	if livenessProbe.Interval != "30s" {
		t.Errorf("interval: got %q, want 30s", livenessProbe.Interval)
	}
	if livenessProbe.Timeout != "5s" {
		t.Errorf("timeout: got %q, want 5s", livenessProbe.Timeout)
	}
	if livenessProbe.FailureThreshold != 5 {
		t.Errorf("failure_threshold: got %d, want 5", livenessProbe.FailureThreshold)
	}
}

func TestParseCloudIdentityAWS(t *testing.T) {
	file, err := Parse(`cloud_identity payments_s3 {
    provider aws
    role "arn:aws:iam::123456789012:role/payments-s3-access"
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(file.CloudIdentities) != 1 {
		t.Fatalf("expected 1 cloud identity, got %d", len(file.CloudIdentities))
	}
	cloudIdentityDecl := file.CloudIdentities[0]
	if cloudIdentityDecl.Name != "payments_s3" {
		t.Errorf("name: got %q, want payments_s3", cloudIdentityDecl.Name)
	}
	if cloudIdentityDecl.Provider != "aws" {
		t.Errorf("provider: got %q, want aws", cloudIdentityDecl.Provider)
	}
	if cloudIdentityDecl.Role != "arn:aws:iam::123456789012:role/payments-s3-access" {
		t.Errorf("role: got %q", cloudIdentityDecl.Role)
	}
}

func TestParseCloudIdentityGCP(t *testing.T) {
	file, err := Parse(`cloud_identity analytics_bq {
    provider gcp
    service_account "analytics@project.iam.gserviceaccount.com"
    pool "projects/123/locations/global/workloadIdentityPools/ccattler"
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	cloudIdentityDecl := file.CloudIdentities[0]
	if cloudIdentityDecl.Provider != "gcp" {
		t.Errorf("provider: got %q, want gcp", cloudIdentityDecl.Provider)
	}
	if cloudIdentityDecl.ServiceAccount != "analytics@project.iam.gserviceaccount.com" {
		t.Errorf("service_account: got %q", cloudIdentityDecl.ServiceAccount)
	}
	if cloudIdentityDecl.Pool != "projects/123/locations/global/workloadIdentityPools/ccattler" {
		t.Errorf("pool: got %q", cloudIdentityDecl.Pool)
	}
}

func TestParseCloudIdentityAzure(t *testing.T) {
	file, err := Parse(`cloud_identity storage_blob {
    provider azure
    client_id "11111111-2222-3333-4444-555555555555"
    tenant_id "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	cloudIdentityDecl := file.CloudIdentities[0]
	if cloudIdentityDecl.Provider != "azure" {
		t.Errorf("provider: got %q, want azure", cloudIdentityDecl.Provider)
	}
	if cloudIdentityDecl.ClientID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("client_id: got %q", cloudIdentityDecl.ClientID)
	}
	if cloudIdentityDecl.TenantID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("tenant_id: got %q", cloudIdentityDecl.TenantID)
	}
}

func TestParseCredentialBroker(t *testing.T) {
	file, err := Parse(`credential_broker {
    oidc_issuer "https://ccattler.example.com"
    credential_ttl "1h"
    refresh_before "15m"
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if file.CredentialBroker == nil {
		t.Fatal("expected credential_broker to be parsed")
	}
	brokerDecl := file.CredentialBroker
	if brokerDecl.OIDCIssuer != "https://ccattler.example.com" {
		t.Errorf("oidc_issuer: got %q", brokerDecl.OIDCIssuer)
	}
	if brokerDecl.CredentialTTL != "1h" {
		t.Errorf("credential_ttl: got %q, want 1h", brokerDecl.CredentialTTL)
	}
	if brokerDecl.RefreshBefore != "15m" {
		t.Errorf("refresh_before: got %q, want 15m", brokerDecl.RefreshBefore)
	}
}

func TestParseCloudIdentityBindingInService(t *testing.T) {
	file, err := Parse(`service payments {
    image payments:1.0
    instances 2
    cloud_identity payments_s3 {
        mount_path "/var/run/cloud-creds"
        deliver token
    }
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	serviceDecl := file.Services[0]
	if len(serviceDecl.CloudIdentities) != 1 {
		t.Fatalf("expected 1 cloud identity binding, got %d", len(serviceDecl.CloudIdentities))
	}
	binding := serviceDecl.CloudIdentities[0]
	if binding.IdentityName != "payments_s3" {
		t.Errorf("identity_name: got %q, want payments_s3", binding.IdentityName)
	}
	if binding.MountPath != "/var/run/cloud-creds" {
		t.Errorf("mount_path: got %q", binding.MountPath)
	}
	if binding.DeliverMode != "token" {
		t.Errorf("deliver_mode: got %q, want token", binding.DeliverMode)
	}
}

func TestParseCloudIdentityBindingDefaultDeliverMode(t *testing.T) {
	file, err := Parse(`service payments {
    image payments:1.0
    instances 1
    cloud_identity payments_s3 {
        mount_path "/var/run/cloud-creds"
    }
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	binding := file.Services[0].CloudIdentities[0]
	if binding.DeliverMode != "credentials" {
		t.Errorf("default deliver_mode: got %q, want credentials", binding.DeliverMode)
	}
}

func TestParseCloudIdentityUnknownField(t *testing.T) {
	_, err := Parse(`cloud_identity test_id {
    provider aws
    bogus_field "value"
}`)
	if err == nil {
		t.Fatal("expected error for unknown cloud_identity field")
	}
}

func TestParseCredentialBrokerUnknownField(t *testing.T) {
	_, err := Parse(`credential_broker {
    oidc_issuer "https://example.com"
    unknown_field "value"
}`)
	if err == nil {
		t.Fatal("expected error for unknown credential_broker field")
	}
}

func TestParseCloudBlock(t *testing.T) {
	file, err := Parse(`cloud {
    provider aws
    region "us-east-1"
    instance_type "m5.large"
    credentials infra_identity
    route_table "rtb-abc123"
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if file.Cloud == nil {
		t.Fatal("expected cloud block to be parsed")
	}
	cloudDecl := file.Cloud
	if cloudDecl.Provider != "aws" {
		t.Errorf("provider: got %q, want aws", cloudDecl.Provider)
	}
	if cloudDecl.Region != "us-east-1" {
		t.Errorf("region: got %q, want us-east-1", cloudDecl.Region)
	}
	if cloudDecl.InstanceType != "m5.large" {
		t.Errorf("instance_type: got %q, want m5.large", cloudDecl.InstanceType)
	}
	if cloudDecl.Credentials != "infra_identity" {
		t.Errorf("credentials: got %q, want infra_identity", cloudDecl.Credentials)
	}
	if cloudDecl.RouteTable != "rtb-abc123" {
		t.Errorf("route_table: got %q, want rtb-abc123", cloudDecl.RouteTable)
	}
}

func TestParseCloudBlockGCP(t *testing.T) {
	file, err := Parse(`cloud {
    provider gcp
    region "us-central1"
    project_id "my-project"
    vpc_network "default"
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	cloudDecl := file.Cloud
	if cloudDecl.Provider != "gcp" {
		t.Errorf("provider: got %q, want gcp", cloudDecl.Provider)
	}
	if cloudDecl.ProjectID != "my-project" {
		t.Errorf("project_id: got %q, want my-project", cloudDecl.ProjectID)
	}
	if cloudDecl.VPCNetwork != "default" {
		t.Errorf("vpc_network: got %q, want default", cloudDecl.VPCNetwork)
	}
}

func TestParseCloudBlockAzure(t *testing.T) {
	file, err := Parse(`cloud {
    provider azure
    region "eastus"
    project_id "sub-123"
    resource_group "my-rg"
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	cloudDecl := file.Cloud
	if cloudDecl.Provider != "azure" {
		t.Errorf("provider: got %q, want azure", cloudDecl.Provider)
	}
	if cloudDecl.ResourceGroup != "my-rg" {
		t.Errorf("resource_group: got %q, want my-rg", cloudDecl.ResourceGroup)
	}
}

func TestParseCloudBlockUnknownField(t *testing.T) {
	_, err := Parse(`cloud {
    provider aws
    unknown_field "value"
}`)
	if err == nil {
		t.Fatal("expected error for unknown cloud field")
	}
}

func TestParseExposeExternal(t *testing.T) {
	file, err := Parse(`service web {
    image nginx:1.27
    instances 3
    expose external 443 http
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	serviceDecl := file.Services[0]
	if len(serviceDecl.ExternalPorts) != 1 {
		t.Fatalf("expected 1 external port, got %d", len(serviceDecl.ExternalPorts))
	}
	if serviceDecl.ExternalPorts[0].Port != 443 {
		t.Errorf("expected external port 443, got %d", serviceDecl.ExternalPorts[0].Port)
	}
	if serviceDecl.ExternalPorts[0].Protocol != "http" {
		t.Errorf("expected protocol http, got %q", serviceDecl.ExternalPorts[0].Protocol)
	}
}

func TestParseExposeExternalDefaultProtocol(t *testing.T) {
	file, err := Parse(`service redis {
    image redis:7
    instances 1
    expose external 6379
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if file.Services[0].ExternalPorts[0].Protocol != "tcp" {
		t.Errorf("expected default protocol tcp, got %q", file.Services[0].ExternalPorts[0].Protocol)
	}
}

func TestParseExposeAndExposeExternal(t *testing.T) {
	file, err := Parse(`service api {
    image api:1.0
    instances 2
    expose 8080
    expose external 443 http
}`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	serviceDecl := file.Services[0]
	if len(serviceDecl.Ports) != 1 || serviceDecl.Ports[0] != 8080 {
		t.Errorf("expected internal port 8080, got %v", serviceDecl.Ports)
	}
	if len(serviceDecl.ExternalPorts) != 1 || serviceDecl.ExternalPorts[0].Port != 443 {
		t.Errorf("expected external port 443, got %v", serviceDecl.ExternalPorts)
	}
}
