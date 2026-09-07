package lang

// File is the root of the AST — a list of top-level declarations.
type File struct {
	Services []ServiceDecl // top-level service blocks in the source file
}

// ServiceDecl represents a parsed "service" block in the DSL.
type ServiceDecl struct {
	Name      string         // unique service identifier from the block header
	Image     string         // container image reference (e.g. "nginx:1.27")
	Instances int            // desired number of running instances
	Ports     []int          // exposed port numbers declared via "expose"
	Resources *ResourcesDecl // optional CPU/memory resource constraints
	Health    *HealthDecl    // optional health check configuration
	Line      int            // source line number for error reporting
}

// ResourcesDecl holds CPU and memory resource constraints for a service.
type ResourcesDecl struct {
	CPU    string // CPU request in Kubernetes-style units (e.g. "500m")
	Memory string // memory request in Kubernetes-style units (e.g. "512Mi")
}

// HealthDecl holds health check configuration for a service.
type HealthDecl struct {
	Method   string // "http" or "tcp"
	Path     string // URL path for HTTP checks (e.g. "/health")
	Interval string // time between checks (e.g. "10s")
}
