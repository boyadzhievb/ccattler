package lang

// File is the root of the AST — a list of top-level declarations.
type File struct {
	Services []ServiceDecl // top-level service blocks in the source file
	Volumes  []VolumeDecl  // top-level volume blocks in the source file
}

// ServiceDecl represents a parsed "service" block in the DSL.
type ServiceDecl struct {
	Name         string           // unique service identifier from the block header
	Image        string           // container image reference (e.g. "nginx:1.27")
	Instances    int              // desired number of running instances
	Ports        []int            // exposed port numbers declared via "expose"
	Resources    *ResourcesDecl   // optional CPU/memory resource constraints
	Health       *HealthDecl      // optional health check configuration
	VolumeMounts []VolumeMountDecl // optional volume mount bindings
	Line         int              // source line number for error reporting
}

// VolumeDecl represents a parsed top-level "volume" block in the DSL.
type VolumeDecl struct {
	Name       string // unique volume identifier from the block header
	Size       string // declared storage size (e.g. "100Gi")
	Persistent bool   // true if the volume's data survives instance deletion
	Line       int    // source line number for error reporting
}

// VolumeMountDecl represents a volume mount binding inside a service block.
// It connects a named volume to a filesystem path inside the service's instances.
type VolumeMountDecl struct {
	VolumeName string // VolumeName is the name of the volume to mount
	MountPath  string // MountPath is the filesystem path inside the instance
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
