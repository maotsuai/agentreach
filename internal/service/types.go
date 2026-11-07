// Package service owns approved immutable service specifications and lifecycle.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/terminal"
)

// Protocol and retention budgets are hard ceilings without eviction.
const (
	MaxServices     = 32
	MaxFrame        = 64 << 10
	MaxStore        = 4 << 20
	MaxRead         = 16 << 10
	MaxPublications = 16
)

// Mount is one exact bind or captured local volume approval.
type Mount struct {
	Type     string                `json:"type"`
	Source   string                `json:"source"`
	Target   string                `json:"target"`
	ReadOnly bool                  `json:"read_only"`
	Volume   *docker.ServiceVolume `json:"volume,omitempty"`
}

// Resources bounds sidecars; workspace services inherit their existing container limits.
type Resources struct {
	Memory   int64 `json:"memory"`
	NanoCPUs int64 `json:"nano_cpus"`
	PIDs     int64 `json:"pids"`
}

// Health permits only a declared TCP port or origin-relative HTTP path.
type Health struct {
	Protocol  string `json:"protocol"`
	Port      uint16 `json:"port"`
	Path      string `json:"path,omitempty"`
	TimeoutMS int    `json:"timeout_ms"`
}

// Publication is an explicit fixed IPv4 loopback binding.
type Publication struct {
	GuestPort uint16 `json:"guest_port"`
	HostPort  uint16 `json:"host_port"`
	Listen    string `json:"listen"`
}

// Spec is closed, with no Compose, shell, secrets, env_file or daemon arguments.
// Entrypoint and image healthchecks are explicitly overridden; image volumes
// require exact approved mounts. Env includes every effective image environment value.
type Spec struct {
	Schema     int               `json:"schema"`
	ProjectID  string            `json:"project_id"`
	Driver     string            `json:"driver"`
	Image      string            `json:"image,omitempty"`
	Argv       []string          `json:"argv"`
	Cwd        string            `json:"cwd"`
	Env        map[string]string `json:"env"`
	Mounts     []Mount           `json:"mounts"`
	NetworkID  string            `json:"network_id"`
	User       string            `json:"user"`
	Resources  Resources         `json:"resources"`
	Health     Health            `json:"health"`
	Restart    string            `json:"restart"`
	LogBytes   int64             `json:"log_bytes"`
	GuestPorts []uint16          `json:"guest_ports"`
	Publish    []Publication     `json:"publish"`
}

// Request is the strict public service operation envelope.
type Request struct {
	RequestID string `json:"request_id,omitempty"`
	ServiceID string `json:"service_id,omitempty"`
	Spec      *Spec  `json:"spec,omitempty"`
	MaxBytes  int    `json:"max_bytes,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

// Record retains immutable desired state and exact observed resource identities.
type Record struct {
	ID              string             `json:"service_id"`
	Binding         policy.Binding     `json:"binding"`
	RequestID       string             `json:"request_id"`
	RequestDigest   string             `json:"request_digest"`
	Spec            Spec               `json:"spec"`
	Digest          string             `json:"spec_digest"`
	Generation      uint64             `json:"generation"`
	State           string             `json:"state"`
	Ready           bool               `json:"ready"`
	OperationID     string             `json:"operation_id,omitempty"`
	Pending         *policy.Pending    `json:"pending,omitempty"`
	ContainerID     string             `json:"container_id,omitempty"`
	StartedAt       string             `json:"started_at,omitempty"`
	Snapshot        string             `json:"snapshot,omitempty"`
	TerminalID      string             `json:"terminal_id,omitempty"`
	Tool            docker.ComposeTool `json:"compose_tool"`
	RenderDigest    string             `json:"render_digest,omitempty"`
	ExecutionDigest string             `json:"execution_digest,omitempty"`
	Endpoint        docker.Endpoint    `json:"endpoint"`
	Reason          string             `json:"reason,omitempty"`
	Descendants     string             `json:"descendants,omitempty"`
	FixedIDs        []string           `json:"fixed_exposure_ids"`
}

// Result retains service and operation identity even when dispatch is uncertain.
type Result struct {
	Service  *Record `json:"service,omitempty"`
	Data     []byte  `json:"data,omitempty"`
	Cursor   string  `json:"next_cursor,omitempty"`
	Complete bool    `json:"complete,omitempty"`
}

// Summary is bounded list metadata; inspect returns the complete specification.
type Summary struct {
	ID              string `json:"service_id"`
	Driver          string `json:"driver"`
	Digest          string `json:"spec_digest"`
	ExecutionDigest string `json:"execution_digest,omitempty"`
	Generation      uint64 `json:"generation"`
	State           string `json:"state"`
	Ready           bool   `json:"last_observed_ready"`
	OperationID     string `json:"operation_id,omitempty"`
	ContainerID     string `json:"container_id,omitempty"`
}

// BackendIdentityError identifies the resource that retired this connection.
type BackendIdentityError struct {
	ServiceID string
	Cause     error
}

func (e *BackendIdentityError) Error() string {
	return fmt.Sprintf("identity_changed: service %s: %v", e.ServiceID, e.Cause)
}
func (e *BackendIdentityError) Unwrap() error { return e.Cause }

// Digest hashes a canonical typed resource or execution specification.
func Digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Decode rejects oversized, ambiguous and invalid UTF-8 requests.
func Decode(b []byte) (Request, error) {
	var q Request
	if len(b) > MaxFrame || !utf8.Valid(b) {
		return q, fmt.Errorf("request_budget: service request")
	}
	e := policy.VerifyEncoded(b, &q)
	return q, e
}

var userRE = regexp.MustCompile(`^[1-9][0-9]{0,8}:[1-9][0-9]{0,8}$`)
var envRE = regexp.MustCompile(`^[A-Z_][A-Z_0-9]{0,63}$`)
var fullIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate freezes the closed specification and all resource budgets.
func (s Spec) Validate(project string) error {
	if !fullIDRE.MatchString(s.NetworkID) || s.Driver == "sidecar" && (!strings.HasPrefix(s.Image, "sha256:") || !fullIDRE.MatchString(strings.TrimPrefix(s.Image, "sha256:"))) {
		return fmt.Errorf("policy_denied: immutable image/network identity required")
	}
	if s.Schema != 1 || s.ProjectID != project || s.Driver != "sidecar" && s.Driver != "workspace" || !path.IsAbs(s.Cwd) || path.Clean(s.Cwd) != s.Cwd || !userRE.MatchString(s.User) || s.Restart != "no" {
		return fmt.Errorf("policy_denied: service schema/binding/driver/cwd/user/restart")
	}
	if len(s.Argv) == 0 || len(s.Argv) > 128 || s.Argv[0] == "" || len(s.Env) > 64 || len(s.Mounts) > 16 || len(s.GuestPorts) == 0 || len(s.GuestPorts) > 16 || len(s.Publish) > 16 || s.LogBytes < 1 || s.LogBytes > 8<<20 {
		return fmt.Errorf("request_budget: service specification")
	}
	b, _ := json.Marshal(s)
	if len(b) > MaxFrame/4 {
		return fmt.Errorf("request_budget: service specification")
	}
	for _, a := range s.Argv {
		if !utf8.ValidString(a) || strings.ContainsRune(a, 0) {
			return fmt.Errorf("policy_denied: invalid argv")
		}
	}
	for k, v := range s.Env {
		if !envRE.MatchString(k) || !utf8.ValidString(v) || strings.ContainsRune(v, 0) || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("policy_denied: service environment")
		}
		for _, s := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "AUTH", "PRIVATE_KEY", "COOKIE"} {
			if strings.Contains(k, s) {
				return fmt.Errorf("unsupported: Host secrets")
			}
		}
	}
	if s.Resources.Memory < 16<<20 || s.Resources.Memory > 8<<30 || s.Resources.NanoCPUs < 100000000 || s.Resources.NanoCPUs > 8000000000 || s.Resources.PIDs < 16 || s.Resources.PIDs > 512 {
		return fmt.Errorf("request_budget: service resources")
	}
	ports := map[uint16]bool{}
	for _, p := range s.GuestPorts {
		if p == 0 || ports[p] {
			return fmt.Errorf("policy_denied: guest ports")
		}
		ports[p] = true
	}
	if s.Health.Protocol != "tcp" && s.Health.Protocol != "http" || !ports[s.Health.Port] || s.Health.TimeoutMS < 100 || s.Health.TimeoutMS > 10000 {
		return fmt.Errorf("policy_denied: readiness")
	}
	if s.Health.Protocol == "http" {
		if _, e := policy.RelativePath(s.Health.Path); e != nil {
			return e
		}
	} else if s.Health.Path != "" {
		return fmt.Errorf("policy_denied: TCP readiness path")
	}
	seen := map[uint16]bool{}
	for _, p := range s.Publish {
		if !ports[p.GuestPort] || p.HostPort == 0 || p.Listen != "127.0.0.1" || seen[p.HostPort] {
			return fmt.Errorf("policy_denied: fixed loopback publication")
		}
		seen[p.HostPort] = true
	}
	targets := map[string]bool{}
	for _, m := range s.Mounts {
		if !utf8.ValidString(m.Source) || !utf8.ValidString(m.Target) || strings.ContainsRune(m.Source, 0) || strings.ContainsRune(m.Target, 0) {
			return fmt.Errorf("policy_denied: mount encoding")
		}
		if m.Type != "bind" && m.Type != "volume" || m.Source == "" || !path.IsAbs(m.Target) || path.Clean(m.Target) != m.Target || m.Target == "/" || targets[m.Target] {
			return fmt.Errorf("policy_denied: service mounts")
		}
		targets[m.Target] = true
	}
	if s.Driver == "workspace" && (s.Image != "" || len(s.Mounts) != 0 || len(s.Publish) != 0) {
		return fmt.Errorf("policy_denied: workspace cannot mutate image/mounts/publication")
	}
	if s.Driver == "workspace" && len(s.Argv)+len(s.Env)+1 > 128 {
		return fmt.Errorf("request_budget: expanded workspace argv")
	}
	if strings.ContainsRune(s.Cwd, 0) || !utf8.ValidString(s.Cwd) || s.Driver == "workspace" && strings.ContainsAny(s.Cwd, "\r\n") {
		return fmt.Errorf("policy_denied: service cwd encoding")
	}
	for _, m := range s.Mounts {
		if m.Type == "bind" && m.Volume != nil {
			return fmt.Errorf("policy_denied: bind cannot carry volume identity")
		}
	}
	return nil
}
func validateRequest(op string, q Request) error {
	c := q
	switch op {
	case "plan", "start":
		if !terminal.ValidID(q.RequestID) || q.Spec == nil {
			return fmt.Errorf("policy_denied: service request identity/spec")
		}
		c.RequestID = ""
		c.Spec = nil
	case "inspect", "stop":
		if !terminal.ValidID(q.ServiceID) {
			return fmt.Errorf("policy_denied: service ID")
		}
		c.ServiceID = ""
	case "logs":
		if !terminal.ValidID(q.ServiceID) || q.MaxBytes < 1 || q.MaxBytes > MaxRead {
			return fmt.Errorf("request_budget: service logs")
		}
		c.ServiceID = ""
		c.MaxBytes = 0
		c.Cursor = ""
	case "list":
	default:
		return fmt.Errorf("policy_denied: unknown service operation")
	}
	if c.RequestID != "" || c.ServiceID != "" || c.Spec != nil || c.MaxBytes != 0 || c.Cursor != "" {
		return fmt.Errorf("policy_denied: unexpected service parameters")
	}
	return nil
}

// Port reports membership in the immutable approved guest port set.
func (r Record) Port(p uint16) bool { return slices.Contains(r.Spec.GuestPorts, p) }
