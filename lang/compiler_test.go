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

	m := factMap(facts)
	if m[types.KeyDesiredServiceImage("web")] != "nginx:1.28" {
		t.Errorf("image: %s", m[types.KeyDesiredServiceImage("web")])
	}
	if m[types.KeyDesiredServiceInstances("web")] != "3" {
		t.Errorf("instances: %s", m[types.KeyDesiredServiceInstances("web")])
	}
	if m[types.KeyEffectiveServiceInstances("web")] != "3" {
		t.Errorf("effective instances: %s", m[types.KeyEffectiveServiceInstances("web")])
	}
	if m[types.KeyIntentUserServiceInstances("web")] != "3" {
		t.Errorf("intent: %s", m[types.KeyIntentUserServiceInstances("web")])
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

	m := factMap(facts)
	if _, ok := m[types.KeyDesiredServiceExpose("web", 8080)]; !ok {
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

	m := factMap(facts)
	if m[types.KeyDesiredServiceResourcesCPU("web")] != "500m" {
		t.Errorf("cpu: %s", m[types.KeyDesiredServiceResourcesCPU("web")])
	}
	if m[types.KeyDesiredServiceResourcesMemory("web")] != "512Mi" {
		t.Errorf("memory: %s", m[types.KeyDesiredServiceResourcesMemory("web")])
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

	m := factMap(facts)
	if m[types.KeyDesiredServiceImage("web")] != "nginx:1.28" {
		t.Error("missing web image")
	}
	if m[types.KeyDesiredServiceImage("api")] != "myapp:latest" {
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
	s := store.NewMemoryStore()
	defer s.Close()

	input := `service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}`
	err := Apply(context.Background(), s, input)
	if err != nil {
		t.Fatal(err)
	}

	// Verify facts in the store.
	svc, err := types.ReadService(context.Background(), s, "web")
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
	f, err := s.Get(context.Background(), types.KeyEffectiveServiceInstances("web"))
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

	m := factMap(facts)
	if m[types.KeyDesiredServiceHealthMethod("web")] != "http" {
		t.Errorf("health method: %s", m[types.KeyDesiredServiceHealthMethod("web")])
	}
	if m[types.KeyDesiredServiceHealthPath("web")] != "/health" {
		t.Errorf("health path: %s", m[types.KeyDesiredServiceHealthPath("web")])
	}
	if m[types.KeyDesiredServiceHealthInterval("web")] != "10s" {
		t.Errorf("health interval: %s", m[types.KeyDesiredServiceHealthInterval("web")])
	}
}

func TestApplyParseError(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	err := Apply(context.Background(), s, `service web { bogus 42 }`)
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

	m := factMap(facts)
	if _, ok := m[types.KeyDesiredVolume("pgdata")]; !ok {
		t.Error("missing volume marker fact")
	}
	if m[types.KeyDesiredVolumeSize("pgdata")] != "100Gi" {
		t.Errorf("size: %s", m[types.KeyDesiredVolumeSize("pgdata")])
	}
	if m[types.KeyDesiredVolumePersistent("pgdata")] != "true" {
		t.Errorf("persistent: %s", m[types.KeyDesiredVolumePersistent("pgdata")])
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

	m := factMap(facts)
	if m[types.KeyDesiredServiceVolume("postgres", "pgdata")] != "/var/lib/postgresql/data" {
		t.Errorf("volume mount: %s", m[types.KeyDesiredServiceVolume("postgres", "pgdata")])
	}
}

func factMap(facts []Fact) map[string]string {
	m := make(map[string]string)
	for _, f := range facts {
		m[f.Key] = f.Value
	}
	return m
}
