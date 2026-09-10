package lang

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestCompileMinimalService(t *testing.T) {
	file, _ := Parse(`service web {
    image nginx:1.28
    instances 3
}`)
	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if compiledFactMap[types.KeyDesiredServiceImage("web")] != "nginx:1.28" {
		t.Errorf("image: %s", compiledFactMap[types.KeyDesiredServiceImage("web")])
	}
	if compiledFactMap[types.KeyDesiredServiceInstances("web")] != "3" {
		t.Errorf("instances: %s", compiledFactMap[types.KeyDesiredServiceInstances("web")])
	}
	if compiledFactMap[types.KeyEffectiveServiceInstances("web")] != "3" {
		t.Errorf("effective instances: %s", compiledFactMap[types.KeyEffectiveServiceInstances("web")])
	}
	if compiledFactMap[types.KeyIntentUserServiceInstances("web")] != "3" {
		t.Errorf("intent: %s", compiledFactMap[types.KeyIntentUserServiceInstances("web")])
	}
}

func TestCompileServiceWithPort(t *testing.T) {
	file, _ := Parse(`service web {
    image nginx:1.28
    instances 1
    expose 8080
}`)
	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if _, ok := compiledFactMap[types.KeyDesiredServiceExpose("web", 8080)]; !ok {
		t.Error("missing expose fact")
	}
}

func TestCompileServiceWithResources(t *testing.T) {
	file, _ := Parse(`service web {
    image nginx:1.28
    instances 1
    resources {
        cpu 500m
        memory 512Mi
    }
}`)
	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if compiledFactMap[types.KeyDesiredServiceResourcesCPU("web")] != "500m" {
		t.Errorf("cpu: %s", compiledFactMap[types.KeyDesiredServiceResourcesCPU("web")])
	}
	if compiledFactMap[types.KeyDesiredServiceResourcesMemory("web")] != "512Mi" {
		t.Errorf("memory: %s", compiledFactMap[types.KeyDesiredServiceResourcesMemory("web")])
	}
}

func TestCompileMultipleServices(t *testing.T) {
	file, _ := Parse(`service web {
    image nginx:1.28
    instances 3
}
service api {
    image myapp:latest
    instances 2
}`)
	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if compiledFactMap[types.KeyDesiredServiceImage("web")] != "nginx:1.28" {
		t.Error("missing web image")
	}
	if compiledFactMap[types.KeyDesiredServiceImage("api")] != "myapp:latest" {
		t.Error("missing api image")
	}
}

func TestCompileErrorNoImage(t *testing.T) {
	file, _ := Parse(`service web {
    instances 3
}`)
	_, err := Compile(file)
	if err == nil {
		t.Fatal("expected error for missing image")
	}
}

func TestApplyEndToEnd(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	input := `service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}`
	err := Apply(context.Background(), factStore, input)
	if err != nil {
		t.Fatal(err)
	}

	// Verify facts in the store.
	svc, err := types.ReadService(context.Background(), factStore, "web")
	if err != nil {
		t.Fatal(err)
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
	if svc.CPU != "500m" {
		t.Errorf("cpu: %s", svc.CPU)
	}

	// Verify effective instances were set (drives instance controller).
	f, err := factStore.Get(context.Background(), types.KeyEffectiveServiceInstances("web"))
	if err != nil {
		t.Fatal(err)
	}
	if string(f.Value) != "3" {
		t.Errorf("effective instances: %s", f.Value)
	}
}

func TestCompileServiceWithHealth(t *testing.T) {
	file, _ := Parse(`service web {
    image nginx:1.28
    instances 1
    expose 8080
    health {
        http /health
        every 10s
    }
}`)
	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if compiledFactMap[types.KeyDesiredServiceHealthMethod("web")] != "http" {
		t.Errorf("health method: %s", compiledFactMap[types.KeyDesiredServiceHealthMethod("web")])
	}
	if compiledFactMap[types.KeyDesiredServiceHealthPath("web")] != "/health" {
		t.Errorf("health path: %s", compiledFactMap[types.KeyDesiredServiceHealthPath("web")])
	}
	if compiledFactMap[types.KeyDesiredServiceHealthInterval("web")] != "10s" {
		t.Errorf("health interval: %s", compiledFactMap[types.KeyDesiredServiceHealthInterval("web")])
	}
}

func TestApplyParseError(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	err := Apply(context.Background(), factStore, `service web { bogus 42 }`)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompileVolumeDeclaration(t *testing.T) {
	file, _ := Parse(`volume pgdata {
    size 100Gi
    persistent true
}`)
	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if _, ok := compiledFactMap[types.KeyDesiredVolume("pgdata")]; !ok {
		t.Error("missing volume marker fact")
	}
	if compiledFactMap[types.KeyDesiredVolumeSize("pgdata")] != "100Gi" {
		t.Errorf("size: %s", compiledFactMap[types.KeyDesiredVolumeSize("pgdata")])
	}
	if compiledFactMap[types.KeyDesiredVolumePersistent("pgdata")] != "true" {
		t.Errorf("persistent: %s", compiledFactMap[types.KeyDesiredVolumePersistent("pgdata")])
	}
}

func TestCompileServiceWithVolumeMount(t *testing.T) {
	file, _ := Parse(`service postgres {
    image postgres:16
    instances 1
    volume pgdata /var/lib/postgresql/data
}`)
	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if compiledFactMap[types.KeyDesiredServiceVolume("postgres", "pgdata")] != "/var/lib/postgresql/data" {
		t.Errorf("volume mount: %s", compiledFactMap[types.KeyDesiredServiceVolume("postgres", "pgdata")])
	}
}

func TestCompileServiceWithConfig(t *testing.T) {
	file, err := Parse(`service web {
    image nginx:1.28
    instances 1
    config {
        env DATABASE_URL "postgres://localhost/db"
        env LOG_LEVEL debug
        file "/etc/app/config.yaml" "server:\n  port: 8080"
    }
}`)
	if err != nil {
		t.Fatal(err)
	}

	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if compiledFactMap[types.KeyDesiredServiceConfigEnv("web", "DATABASE_URL")] != "postgres://localhost/db" {
		t.Errorf("env DATABASE_URL: %s", compiledFactMap[types.KeyDesiredServiceConfigEnv("web", "DATABASE_URL")])
	}
	if compiledFactMap[types.KeyDesiredServiceConfigEnv("web", "LOG_LEVEL")] != "debug" {
		t.Errorf("env LOG_LEVEL: %s", compiledFactMap[types.KeyDesiredServiceConfigEnv("web", "LOG_LEVEL")])
	}
	configFileValue := compiledFactMap[types.KeyDesiredServiceConfigFile("web", "/etc/app/config.yaml")]
	if configFileValue == "" {
		t.Error("config file not found")
	}
}

func TestCompileServiceWithSecrets(t *testing.T) {
	file, err := Parse(`service web {
    image nginx:1.28
    instances 1
    secret db_password
    secret api_key "/etc/secrets/api.key"
}`)
	if err != nil {
		t.Fatal(err)
	}

	facts, err := Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	compiledFactMap := factMap(facts)
	if compiledFactMap[types.KeyDesiredServiceSecret("web", "db_password")] != "/run/secrets/db_password" {
		t.Errorf("secret db_password mount: %s", compiledFactMap[types.KeyDesiredServiceSecret("web", "db_password")])
	}
	if compiledFactMap[types.KeyDesiredServiceSecret("web", "api_key")] == "" {
		t.Error("secret api_key not found")
	}
}

func TestApplyWithConfig(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	ctx := context.Background()
	input := `service web {
    image nginx:1.28
    instances 1
    config {
        env PORT "8080"
    }
    secret tls_cert
}`
	if err := Apply(ctx, memoryStore, input); err != nil {
		t.Fatal(err)
	}

	envFact, err := memoryStore.Get(ctx, types.KeyDesiredServiceConfigEnv("web", "PORT"))
	if err != nil {
		t.Fatal("env PORT not written")
	}
	if string(envFact.Value) != "8080" {
		t.Fatalf("expected 8080, got %s", string(envFact.Value))
	}

	secretFact, err := memoryStore.Get(ctx, types.KeyDesiredServiceSecret("web", "tls_cert"))
	if err != nil {
		t.Fatal("secret tls_cert not written")
	}
	if string(secretFact.Value) != "/run/secrets/tls_cert" {
		t.Fatalf("expected /run/secrets/tls_cert, got %s", string(secretFact.Value))
	}
}

func TestCompileInitSteps(t *testing.T) {
	file := &File{
		Services: []ServiceDecl{
			{
				Name:      "api",
				Image:     "my-api:1.4",
				Instances: 3,
				InitSteps: []InitStepDecl{
					{Exec: "migrate-db", Timeout: "30s"},
					{Exec: "generate-config", Timeout: "10s", Retry: 5},
				},
			},
		},
	}

	facts, compileError := Compile(file)
	if compileError != nil {
		t.Fatalf("unexpected error: %v", compileError)
	}

	lookup := factMap(facts)

	if lookup[types.KeyDesiredServiceInitStep("api", 0)] != "" {
		t.Fatalf("expected empty marker for init step 0, got %q", lookup[types.KeyDesiredServiceInitStep("api", 0)])
	}
	if _, exists := lookup[types.KeyDesiredServiceInitStep("api", 0)]; !exists {
		t.Fatal("init step 0 marker not found")
	}
	if lookup[types.KeyDesiredServiceInitStepExec("api", 0)] != "migrate-db" {
		t.Fatalf("expected exec 'migrate-db', got %q", lookup[types.KeyDesiredServiceInitStepExec("api", 0)])
	}
	if lookup[types.KeyDesiredServiceInitStepTimeout("api", 0)] != "30s" {
		t.Fatalf("expected timeout '30s', got %q", lookup[types.KeyDesiredServiceInitStepTimeout("api", 0)])
	}
	if _, exists := lookup[types.KeyDesiredServiceInitStepRetry("api", 0)]; exists {
		t.Fatal("retry should not be set for step 0 (retry=0)")
	}
	if lookup[types.KeyDesiredServiceInitStepExec("api", 1)] != "generate-config" {
		t.Fatalf("expected exec 'generate-config', got %q", lookup[types.KeyDesiredServiceInitStepExec("api", 1)])
	}
	if lookup[types.KeyDesiredServiceInitStepRetry("api", 1)] != "5" {
		t.Fatalf("expected retry '5', got %q", lookup[types.KeyDesiredServiceInitStepRetry("api", 1)])
	}
}

// TestCompileServiceWithProbes verifies that startup, liveness, and readiness
// probe declarations are compiled into the correct desired-state probe facts.
func TestCompileServiceWithProbes(t *testing.T) {
	file := &File{
		Services: []ServiceDecl{
			{
				Name:      "web",
				Image:     "nginx:1.28",
				Instances: 1,
				Startup: &ProbeDecl{
					Method:           "http",
					Path:             "/health/startup",
					Port:             8080,
					Interval:         "2s",
					Timeout:          "1s",
					FailureThreshold: 30,
					SuccessThreshold: 1,
					InitialDelay:     "5s",
				},
				Liveness: &ProbeDecl{
					Method:           "tcp",
					Port:             8080,
					Interval:         "10s",
					Timeout:          "2s",
					FailureThreshold: 3,
					SuccessThreshold: 1,
				},
				Readiness: &ProbeDecl{
					Method:           "http",
					Path:             "/ready",
					Port:             8080,
					Interval:         "5s",
					FailureThreshold: 3,
					SuccessThreshold: 2,
				},
			},
		},
	}

	facts, compileError := Compile(file)
	if compileError != nil {
		t.Fatalf("unexpected error: %v", compileError)
	}

	lookup := factMap(facts)

	if lookup[types.KeyDesiredServiceProbeMethod("web", "startup")] != "http" {
		t.Errorf("startup method: got %q", lookup[types.KeyDesiredServiceProbeMethod("web", "startup")])
	}
	if lookup[types.KeyDesiredServiceProbePath("web", "startup")] != "/health/startup" {
		t.Errorf("startup path: got %q", lookup[types.KeyDesiredServiceProbePath("web", "startup")])
	}
	if lookup[types.KeyDesiredServiceProbePort("web", "startup")] != "8080" {
		t.Errorf("startup port: got %q", lookup[types.KeyDesiredServiceProbePort("web", "startup")])
	}
	if lookup[types.KeyDesiredServiceProbeInterval("web", "startup")] != "2s" {
		t.Errorf("startup interval: got %q", lookup[types.KeyDesiredServiceProbeInterval("web", "startup")])
	}
	if lookup[types.KeyDesiredServiceProbeTimeout("web", "startup")] != "1s" {
		t.Errorf("startup timeout: got %q", lookup[types.KeyDesiredServiceProbeTimeout("web", "startup")])
	}
	if lookup[types.KeyDesiredServiceProbeFailureThreshold("web", "startup")] != "30" {
		t.Errorf("startup failure_threshold: got %q", lookup[types.KeyDesiredServiceProbeFailureThreshold("web", "startup")])
	}
	if lookup[types.KeyDesiredServiceProbeInitialDelay("web", "startup")] != "5s" {
		t.Errorf("startup initial_delay: got %q", lookup[types.KeyDesiredServiceProbeInitialDelay("web", "startup")])
	}

	if lookup[types.KeyDesiredServiceProbeMethod("web", "liveness")] != "tcp" {
		t.Errorf("liveness method: got %q", lookup[types.KeyDesiredServiceProbeMethod("web", "liveness")])
	}
	if lookup[types.KeyDesiredServiceProbePort("web", "liveness")] != "8080" {
		t.Errorf("liveness port: got %q", lookup[types.KeyDesiredServiceProbePort("web", "liveness")])
	}

	if lookup[types.KeyDesiredServiceProbeMethod("web", "readiness")] != "http" {
		t.Errorf("readiness method: got %q", lookup[types.KeyDesiredServiceProbeMethod("web", "readiness")])
	}
	if lookup[types.KeyDesiredServiceProbePath("web", "readiness")] != "/ready" {
		t.Errorf("readiness path: got %q", lookup[types.KeyDesiredServiceProbePath("web", "readiness")])
	}
	if lookup[types.KeyDesiredServiceProbeSuccessThreshold("web", "readiness")] != "2" {
		t.Errorf("readiness success_threshold: got %q", lookup[types.KeyDesiredServiceProbeSuccessThreshold("web", "readiness")])
	}
}

func factMap(facts []Fact) map[string]string {
	factLookup := make(map[string]string)
	for _, compiledFact := range facts {
		factLookup[compiledFact.Key] = compiledFact.Value
	}
	return factLookup
}
