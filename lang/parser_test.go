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
