// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

// File is the root of the AST — a list of top-level declarations.
type File struct {
	Services         []ServiceDecl         // top-level service blocks in the source file
	Volumes          []VolumeDecl          // top-level volume blocks in the source file
	Tenants          []TenantDecl          // top-level tenant blocks in the source file
	CloudIdentities  []CloudIdentityDecl   // top-level cloud_identity blocks in the source file
	CredentialBroker *CredentialBrokerDecl // optional top-level credential_broker block
	Cloud            *CloudDecl            // optional top-level cloud block for cloud provider config
	Roles            []RoleDecl            // top-level role blocks defining capability bundles
	Grants           []GrantDecl           // top-level grant statements binding roles to principals
	Groups           []GroupDecl           // top-level group blocks defining member collections
	ServiceGroups    []ServiceGroupDecl    // top-level group blocks defining co-scheduled service groups
	Policies         []PolicyDecl          // top-level policy blocks defining ABAC conditions
	Networks         []NetworkDecl         // top-level network blocks defining allow/deny rules
	BaseDir          string                // directory of the source file, used to resolve "from" paths
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
	Name            string                     // unique service identifier from the block header
	Owner           string                     // owning tenant (empty = derived from hierarchical name)
	Image           string                     // container image reference (e.g. "nginx:1.27")
	Instances       int                        // desired number of running instances
	Ports           []int                      // exposed port numbers declared via "expose"
	ExternalPorts   []ExternalPortDecl         // ports exposed externally via cloud load balancer
	Resources       *ResourcesDecl             // optional CPU/memory resource constraints
	Health          *HealthDecl                // optional health check configuration
	Scale           *ScaleDecl                 // optional autoscaling policy
	Placement       *PlacementDecl             // optional placement constraints
	Update          *UpdateDecl                // optional rolling update strategy
	Config          *ConfigDecl                // optional config block (env vars, config files)
	Secrets         []SecretDecl               // optional secret mount declarations
	VolumeMounts    []VolumeMountDecl          // optional volume mount bindings
	CloudIdentities []CloudIdentityBindingDecl // optional cloud identity bindings
	InitSteps       []InitStepDecl             // optional ordered initialization steps
	Startup         *ProbeDecl                 // optional startup probe (gates liveness/readiness)
	Liveness        *ProbeDecl                 // optional liveness probe (triggers restart on failure)
	Readiness       *ProbeDecl                 // optional readiness probe (controls endpoint membership)
	Disruption      *DisruptionDecl            // optional disruption budget for drain/rollout safety
	Stateful        bool                       // true if service uses ordinal instance IDs and ordered startup
	Line            int                        // source line number for error reporting
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
	Timeout  string // max time per check (e.g. "5s")
}

// ScaleDecl holds autoscaling configuration for a service.
type ScaleDecl struct {
	Horizontal *HorizontalScaleDecl // optional horizontal scaling policy
	Vertical   *VerticalScaleDecl   // optional vertical scaling policy
}

// HorizontalScaleDecl holds horizontal autoscaling bounds and target metrics.
type HorizontalScaleDecl struct {
	Min               int                // minimum instance count floor (0 enables warm-zero)
	Max               int                // maximum instance count ceiling
	Targets           []ScaleTargetDecl  // metric thresholds that drive scaling decisions
	Events            []EventScaleDecl   // event-driven scaling sources
	Schedule          *ScheduleDecl      // optional time-based scaling minimum
	Stabilization     *StabilizationDecl // optional stabilization windows
	IdleTimeout       string             // warm-zero: idle duration before scale-to-zero (e.g. "5m")
	ActivationTimeout string             // warm-zero: max cold-start wait for proxy (e.g. "60s")
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
	Architecture string               // required CPU architecture (e.g. "amd64")
	ZonePolicy   string               // "spread" for zone-aware spreading or a specific zone name
	Require      []PlacementMatchDecl // hard node label requirements
	Prefer       []PlacementMatchDecl // soft node label preferences
	Accept       []string             // accept node restrictions (tolerate restricted nodes)
}

// PlacementMatchDecl holds a label=value pair for placement require or prefer rules.
type PlacementMatchDecl struct {
	Label string // node label name (e.g. "region", "gpu", "ssd")
	Value string // required or preferred value (e.g. "us-east", "true")
}

// UpdateDecl holds the rolling update strategy for a service.
type UpdateDecl struct {
	MaxUnavailable int // maximum instances that can be unavailable during update
	MaxExtra       int // maximum extra instances allowed during surge
}

// DisruptionDecl holds the disruption budget for a service. During node drains
// and rolling updates, the system guarantees that at least MinAvailable
// instances remain running. If MaxUnavailable is set instead, it caps how many
// instances may be simultaneously unavailable. Only one of the two fields
// should be non-zero.
type DisruptionDecl struct {
	MinAvailable   int // minimum instances that must remain running (0 = unset)
	MaxUnavailable int // maximum instances that may be unavailable (0 = unset)
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

// ConfigFileDecl represents a "file PATH CONTENT" or "file PATH from LOCAL" entry
// in a config block. When FromFile is set, the content is loaded from a local file
// at apply time rather than being provided inline.
type ConfigFileDecl struct {
	Path     string // filesystem path to mount the config file at
	Content  string // file contents when provided inline
	FromFile string // local file path to load content from at apply time
}

// InitStepDecl represents a single "init { ... }" block in a service declaration.
// Init steps run sequentially before the main workload starts. Each step defines
// an exec command with optional timeout and retry configuration.
type InitStepDecl struct {
	Exec    string // command to execute (e.g. "migrate-db")
	Timeout string // maximum time for the step to complete (e.g. "30s")
	Retry   int    // number of retry attempts on failure (0 = no retries)
}

// ProbeDecl holds configuration for a startup, liveness, or readiness probe.
// Probes produce observations — they never restart containers directly.
type ProbeDecl struct {
	Method           string // "http", "tcp", or "exec"
	Path             string // URL path for HTTP probes (e.g. "/health/ready")
	Port             int    // TCP port to probe (0 = derive from service expose)
	Interval         string // time between checks (e.g. "5s")
	Timeout          string // max time per check (e.g. "1s")
	FailureThreshold int    // consecutive failures before state change (default 3)
	SuccessThreshold int    // consecutive successes before state change (default 1)
	InitialDelay     string // delay before first probe after startup (e.g. "0s")
}

// SecretDecl holds a secret reference for a service. Secrets are delivered as
// mounted files and lifecycle-managed by the node agent.
type SecretDecl struct {
	Name      string // secret name in the encrypted store
	MountPath string // filesystem path to mount the secret at (default: /run/secrets/{name})
}

// CloudIdentityDecl represents a top-level "cloud_identity" block declaring a
// cloud IAM identity that workloads can assume via OIDC federation.
type CloudIdentityDecl struct {
	Name           string // unique identity name (e.g. "payments-s3")
	Provider       string // cloud provider: "aws", "gcp", or "azure"
	Role           string // AWS IAM role ARN
	ServiceAccount string // GCP service account email
	Pool           string // GCP workload identity pool
	ClientID       string // Azure AD application client ID
	TenantID       string // Azure AD tenant ID
	Line           int    // source line number for error reporting
}

// CloudIdentityBindingDecl represents a "cloud_identity" binding inside a
// service block. It binds a named cloud identity to the service's instances,
// specifying how credentials are delivered.
type CloudIdentityBindingDecl struct {
	IdentityName string // name of the top-level cloud_identity to bind
	MountPath    string // filesystem path for credential files (e.g. "/var/run/cloud-creds")
	DeliverMode  string // "credentials" (provider-specific file) or "token" (raw JWT)
}

// CredentialBrokerDecl represents the top-level "credential_broker" block
// configuring the OIDC issuer and credential lifecycle parameters.
type CredentialBrokerDecl struct {
	OIDCIssuer    string // OIDC issuer URL (e.g. "https://ccattler.example.com")
	CredentialTTL string // credential lifetime (e.g. "1h")
	RefreshBefore string // refresh window before expiry (e.g. "15m")
	Line          int    // source line number for error reporting
}

// ExternalPortDecl represents an "expose external" port declaration in a
// service block. It marks a port for cloud load balancer exposure.
type ExternalPortDecl struct {
	Port     int    // port number to expose externally
	Protocol string // protocol: "tcp" (default) or "http"
}

// CloudDecl represents the top-level "cloud" block configuring the cloud
// provider for node lifecycle management, load balancers, and VPC routes.
type CloudDecl struct {
	Provider      string // cloud provider: "aws", "gcp", or "azure"
	Region        string // cloud region (e.g. "us-east-1", "us-central1")
	Credentials   string // cloud_identity name for provider authentication
	InstanceType  string // default instance type for provisioned nodes
	ProjectID     string // GCP project ID or Azure subscription ID
	ResourceGroup string // Azure resource group name
	VPCNetwork    string // VPC/VNet network name for route management
	RouteTable    string // VPC route table ID (AWS)
	Line          int    // source line number for error reporting
}

// RoleDecl represents a parsed "role" block in the DSL. A role groups a set
// of capabilities and optional scopes that can be granted to principals.
//
//	role developer {
//	    allow workload.read
//	    allow workload.update
//	    scope team/payments
//	}
type RoleDecl struct {
	Name         string   // unique role identifier (e.g. "developer", "operator")
	Capabilities []string // capability names granted by this role (e.g. "workload.read")
	Scopes       []string // optional scope restrictions (e.g. "team/payments"); empty means cluster
	Line         int      // source line number for error reporting
}

// GrantDecl represents a parsed "grant" statement in the DSL. It binds a
// named role to a principal kind and name.
//
//	grant developer to group developers
//	grant operator to user alice@example.com
type GrantDecl struct {
	RoleName      string // name of the role to grant (must match a RoleDecl.Name)
	PrincipalKind string // kind of principal: "group", "user", "node", or "service"
	PrincipalName string // identifier of the principal (e.g. "developers", "alice@example.com")
	Line          int    // source line number for error reporting
}

// GroupDecl represents a parsed "group" block in the DSL. A group is a named
// collection of members that can be referenced in grant statements.
//
//	group developers {
//	    member alice@example.com
//	    member bob@example.com
//	}
type GroupDecl struct {
	Name    string   // unique group identifier (e.g. "developers")
	Members []string // member identifiers (e.g. user emails or service names)
	Line    int      // source line number for error reporting
}

// ServiceGroupDecl represents a parsed service "group" block in the DSL. A
// service group co-schedules multiple services on the same node and optionally
// shares network namespace or volumes between them.
//
//	group frontend {
//	    process proxy
//	    process web
//	    share network
//	    share volume cache
//	}
type ServiceGroupDecl struct {
	Name          string   // unique service group identifier (e.g. "frontend")
	Processes     []string // service names that belong to this group
	ShareNetwork  bool     // whether group members share a network namespace
	SharedVolumes []string // volume names shared across group members
	Line          int      // source line number for error reporting
}

// PolicyDecl represents a parsed "policy" block in the DSL. A policy defines
// an ABAC rule that conditionally grants a capability when all conditions
// evaluate to true.
//
//	policy team-isolation {
//	    allow service.update
//	    when subject.team == resource.team
//	}
type PolicyDecl struct {
	Name       string                // unique policy identifier (e.g. "team-isolation")
	Capability string                // capability granted when conditions pass (e.g. "service.update")
	Conditions []PolicyConditionDecl // all conditions must match (AND semantics)
	Line       int                   // source line number for error reporting
}

// PolicyConditionDecl represents a single "when" clause inside a policy block.
// Field is a dotted path (e.g. "subject.team"), Operator is a comparison
// operator (==, !=, in, not_in), and Value is either a literal string or a
// field reference.
type PolicyConditionDecl struct {
	Field    string // left-hand side: dotted path (e.g. "subject.team")
	Operator string // comparison operator: "==", "!=", "in", "not_in"
	Value    string // right-hand side: literal or field reference (e.g. "resource.team")
	Line     int    // source line number for error reporting
}

// NetworkDecl represents a parsed "network" block containing allow/deny rules.
// Example DSL:
//
//	network {
//	    allow frontend/web -> payments/checkout port 443
//	    deny frontend/web -> payments/database
//	}
type NetworkDecl struct {
	Rules []NetworkRuleDecl // ordered list of allow/deny rules
	Line  int               // source line number for error reporting
}

// NetworkRuleDecl represents a single allow or deny rule inside a network block.
// The rule specifies identity-based traffic control between two services,
// optionally restricted to a specific port.
type NetworkRuleDecl struct {
	Action string // "allow" or "deny"
	Source string // source service identity (e.g. "frontend/web")
	Target string // target service identity (e.g. "payments/checkout")
	Port   int    // target port (0 means any port)
	Line   int    // source line number for error reporting
}
