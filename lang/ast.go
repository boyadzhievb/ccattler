package lang

// File is the root of the AST — a list of top-level declarations.
type File struct {
	Services []ServiceDecl // top-level service blocks in the source file
	Volumes  []VolumeDecl  // top-level volume blocks in the source file
	Tenants  []TenantDecl  // top-level tenant blocks in the source file
}

// TenantDecl represents a parsed "tenant" block in the DSL.
type TenantDecl struct {
	Name   string     // unique tenant identifier
	Quota  *QuotaDecl // optional resource quota limits
	Weight int        // scheduling weight for fair scheduling (0 = unset)
	Line   int        // source line number for error reporting
}

// QuotaDecl holds resource quota limits for a tenant.
type QuotaDecl struct {
	CPU       int    // maximum CPU in millicores (0 = unlimited)
	Memory    string // maximum memory (e.g. "256Gi", empty = unlimited)
	Instances int    // maximum instance count (0 = unlimited)
	Volumes   int    // maximum volume count (0 = unlimited)
	Storage   string // maximum storage (e.g. "10Ti", empty = unlimited)
}

// ServiceDecl represents a parsed "service" block in the DSL.
type ServiceDecl struct {
	Name         string            // unique service identifier from the block header
	Owner        string            // owning tenant (empty = derived from hierarchical name)
	Image        string            // container image reference (e.g. "nginx:1.27")
	Instances    int               // desired number of running instances
	Ports        []int             // exposed port numbers declared via "expose"
	Resources    *ResourcesDecl    // optional CPU/memory resource constraints
	Health       *HealthDecl       // optional health check configuration
	Scale        *ScaleDecl        // optional autoscaling policy
	Placement    *PlacementDecl    // optional placement constraints
	Update       *UpdateDecl       // optional rolling update strategy
	Config       *ConfigDecl       // optional config block (env vars, config files)
	Secrets      []SecretDecl      // optional secret mount declarations
	VolumeMounts []VolumeMountDecl // optional volume mount bindings
	Line         int               // source line number for error reporting
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

// ScaleDecl holds autoscaling configuration for a service.
type ScaleDecl struct {
	Horizontal *HorizontalScaleDecl // optional horizontal scaling policy
	Vertical   *VerticalScaleDecl   // optional vertical scaling policy
}

// HorizontalScaleDecl holds horizontal autoscaling bounds and target metrics.
type HorizontalScaleDecl struct {
	Min           int                // minimum instance count floor
	Max           int                // maximum instance count ceiling
	Targets       []ScaleTargetDecl  // metric thresholds that drive scaling decisions
	Events        []EventScaleDecl   // event-driven scaling sources
	Schedule      *ScheduleDecl      // optional time-based scaling minimum
	Stabilization *StabilizationDecl // optional stabilization windows
}

// ScaleTargetDecl represents a single "target metric = value" entry in a
// horizontal scaling block.
type ScaleTargetDecl struct {
	Metric string // metric name (e.g. "cpu", "memory", "requests_per_second")
	Value  int    // target threshold value (e.g. 60 for 60%)
}

// EventScaleDecl represents an event-driven scaling source with a target
// messages-per-instance threshold.
type EventScaleDecl struct {
	Source string // event source name (e.g. "payments.pending")
	Target int    // target messages per instance (e.g. 20)
}

// ScheduleDecl represents a time-based scaling rule.
type ScheduleDecl struct {
	Days    string // when the rule applies (e.g. "weekdays", "everyday")
	Start   string // start time in HH:MM format
	End     string // end time in HH:MM format
	Minimum int    // minimum instance count during the active window
}

// StabilizationDecl holds the asymmetric stabilization window durations that
// prevent autoscaling oscillation.
type StabilizationDecl struct {
	ScaleUp   string // scale-up stabilization window (e.g. "60s")
	ScaleDown string // scale-down stabilization window (e.g. "300s")
}

// VerticalScaleDecl holds vertical autoscaling resource bounds.
type VerticalScaleDecl struct {
	CPUMin    string // minimum CPU allocation (e.g. "250m")
	CPUMax    string // maximum CPU allocation (e.g. "4000m")
	MemoryMin string // minimum memory allocation (e.g. "512Mi")
	MemoryMax string // maximum memory allocation (e.g. "8Gi")
}

// PlacementDecl holds placement constraints for a service.
type PlacementDecl struct {
	Architecture string // required CPU architecture (e.g. "amd64")
	ZonePolicy   string // "spread" for zone-aware spreading or a specific zone name
}

// UpdateDecl holds the rolling update strategy for a service.
type UpdateDecl struct {
	MaxUnavailable int // maximum instances that can be unavailable during update
	MaxExtra       int // maximum extra instances allowed during surge
}

// ConfigDecl holds configuration declarations for a service — environment
// variables and mounted config files.
type ConfigDecl struct {
	EnvVars     []EnvVarDecl     // environment variables to set in the instance
	ConfigFiles []ConfigFileDecl // config files to mount into the instance
}

// EnvVarDecl represents a single "env KEY VALUE" entry in a config block.
type EnvVarDecl struct {
	Name  string // environment variable name (e.g. "DATABASE_URL")
	Value string // environment variable value (may reference a secret)
}

// ConfigFileDecl represents a "file PATH CONTENT" entry in a config block.
type ConfigFileDecl struct {
	Path    string // filesystem path to mount the config file at
	Content string // file contents (inline or template reference)
}

// SecretDecl holds a secret reference for a service. Secrets are delivered as
// mounted files and lifecycle-managed by the node agent.
type SecretDecl struct {
	Name      string // secret name in the encrypted store
	MountPath string // filesystem path to mount the secret at (default: /run/secrets/{name})
}
