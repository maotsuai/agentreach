// Package project owns explicitly registered, immutable execution bindings.
package project

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/state"
)

// Schema identifies immutable project specs and bindings.
const Schema = 1

var idRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ValidID permits bounded, path-independent project identifiers.
func ValidID(id string) bool { return idRE.MatchString(id) }

// VolumeClaim binds a native external mount to a Guest-invisible sentinel.
type VolumeClaim struct {
	Path           string `json:"path"`
	UUID           string `json:"uuid"`
	Name           string `json:"name"`
	Sentinel       string `json:"sentinel"`
	SentinelSHA256 string `json:"sentinel_sha256"`
}

// MountApproval is one exact canonical Host bind and its Guest destination.
type MountApproval struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// NamedVolumeApproval approves a local volume without driver options.
type NamedVolumeApproval struct {
	Name     string `json:"name"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// Workspace identifies the mount used as the Guest's working directory.
type Workspace struct {
	Mode      string `json:"mode"`
	GuestPath string `json:"guest_path"`
	HostPath  string `json:"host_path,omitempty"`
}

// Spec is the explicit, strict registration request supplied by the operator.
type Spec struct {
	Schema         int                   `json:"schema"`
	ID             string                `json:"id"`
	Provider       string                `json:"provider"`
	Docker         docker.Connection     `json:"docker"`
	Container      string                `json:"container"`
	Workspace      Workspace             `json:"workspace"`
	EngineDataPath string                `json:"engine_data_path"`
	OrbStackConfig string                `json:"orbstack_config"`
	Volumes        []VolumeClaim         `json:"volumes"`
	BindMounts     []MountApproval       `json:"bind_mounts"`
	NamedVolumes   []NamedVolumeApproval `json:"named_volumes"`
}

// Binding captures the exact approved daemon, container and running generation.
type Binding struct {
	Schema         int                      `json:"schema"`
	Revision       uint64                   `json:"revision"`
	Generation     uint64                   `json:"generation"`
	ContainerID    string                   `json:"container_id"`
	ImageID        string                   `json:"image_id"`
	StartedAt      string                   `json:"started_at"`
	DaemonID       string                   `json:"daemon_id"`
	SnapshotSHA256 string                   `json:"snapshot_sha256"`
	NamedVolumes   map[string]docker.Volume `json:"named_volumes"`
	RegisteredAt   time.Time                `json:"registered_at"`
}

// Record is the immutable trusted project authorization saved on the Host.
type Record struct {
	Spec    Spec    `json:"spec"`
	Binding Binding `json:"binding"`
}

// ReadSpec rejects ambiguous JSON, unknown fields and unsupported schemas.
func ReadSpec(path string) (Spec, error) {
	var s Spec
	if err := state.Read(path, &s); err != nil {
		return s, err
	}
	return s, s.Validate()
}

// Validate checks the registration's supported shape before runtime inspection.
func (s Spec) Validate() error {
	if s.Schema != Schema || !ValidID(s.ID) || s.Provider != "orbstack-docker" || s.Container == "" {
		return docker.Fail("policy_denied", "unsupported project schema/provider, invalid id or missing container")
	}
	if s.Workspace.Mode != "shared-project" && s.Workspace.Mode != "guest-owned" {
		return docker.Fail("policy_denied", "workspace mode must be shared-project or guest-owned")
	}
	if s.Workspace.GuestPath == "/" || !path.IsAbs(s.Workspace.GuestPath) || path.Clean(s.Workspace.GuestPath) != s.Workspace.GuestPath {
		return docker.Fail("policy_denied", "workspace guest_path must be an absolute non-root directory")
	}
	if len(s.Volumes) == 0 || !filepath.IsAbs(s.EngineDataPath) || !filepath.IsAbs(s.OrbStackConfig) || filepath.Base(s.OrbStackConfig) != "vmconfig.json" || filepath.Base(filepath.Dir(s.OrbStackConfig)) != ".orbstack" {
		return docker.Fail("policy_denied", "external volume claims, engine_data_path and native .orbstack/vmconfig.json are required")
	}
	if !filepath.IsAbs(s.Docker.ControlHome) || s.OrbStackConfig != filepath.Join(s.Docker.ControlHome, ".orbstack", "vmconfig.json") {
		return docker.Fail("policy_denied", "orbstack_config must be the fixed control_home/.orbstack/vmconfig.json")
	}
	if s.Workspace.Mode == "shared-project" && (!filepath.IsAbs(s.Workspace.HostPath) || filepath.Clean(s.Workspace.HostPath) != s.Workspace.HostPath) || s.Workspace.Mode == "guest-owned" && s.Workspace.HostPath != "" {
		return docker.Fail("policy_denied", "workspace host_path does not match its mode")
	}
	return nil
}

// Home resolves the trusted reach directory without creating it.
func Home() (string, error) {
	if base := os.Getenv("REACH_HOME"); base != "" {
		if !filepath.IsAbs(base) {
			return "", fmt.Errorf("REACH_HOME must be absolute")
		}
		return filepath.Clean(base), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reach"), nil
}
func recordPath(id string) (string, error) {
	if !ValidID(id) {
		return "", docker.Fail("policy_denied", "invalid project id")
	}
	base, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "projects", id+".json"), nil
}

// Load reads an existing binding; it never creates or repairs state.
func Load(id string) (Record, error) {
	var r Record
	p, err := recordPath(id)
	if err != nil {
		return r, err
	}
	if err := hostpath.Canonical(p, false); err != nil {
		return r, err
	}
	if err := hostpath.PrivateFile(p); err != nil {
		return r, docker.Fail("state_corrupt", "unsafe registered project file: %v", err)
	}
	if err := state.Read(p, &r); err != nil {
		return r, err
	}
	if err := r.Spec.Validate(); err != nil {
		return r, err
	}
	if r.Spec.ID != id || r.Binding.Schema != Schema || r.Binding.Revision != 1 || r.Binding.Generation != 1 || len(r.Binding.ContainerID) != 64 || r.Binding.ImageID == "" || r.Binding.DaemonID == "" || r.Binding.StartedAt == "" || len(r.Binding.SnapshotSHA256) != 64 {
		return r, docker.Fail("state_corrupt", "invalid project binding; explicit repair is required")
	}
	return r, nil
}

// Register writes a newly captured binding exactly once. P0 has no rebind API.
func Register(ctx context.Context, r Record) error {
	p, err := recordPath(r.Spec.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	unlock, err := state.Lock(ctx, p+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(p); err == nil {
		return docker.Fail("identity_changed", "project already registered; P0 never overwrites or rebinds it")
	} else if !os.IsNotExist(err) {
		return err
	}
	return state.Write(p, r)
}
