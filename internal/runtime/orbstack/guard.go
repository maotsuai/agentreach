// Package orbstack verifies an approved, existing runtime. It never starts,
// creates, pulls, rebuilds or repairs containers or storage.
package orbstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
)

// VolumeChecker verifies native mount-table and volume metadata.
type VolumeChecker func(context.Context, project.VolumeClaim) error

// Guard validates one immutable project binding before each new operation.
type Guard struct {
	Record           project.Record
	Docker           *docker.Client
	CheckVolume      VolumeChecker
	CheckPathVolume  func(context.Context, string, project.VolumeClaim) error
	CheckEngineData  func(context.Context, string, string) error
	CheckControlHome func(context.Context, string) error
}

// New installs read-only native volume and Docker inspectors.
func New(r project.Record) *Guard {
	return &Guard{Record: r, Docker: &docker.Client{Connection: r.Spec.Docker}, CheckVolume: CheckNativeVolume}
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, string(filepath.Separator))+string(filepath.Separator))
}

func (g *Guard) storage(ctx context.Context) error {
	s := g.Record.Spec
	if err := s.Validate(); err != nil {
		return err
	}
	homeCheck := g.CheckControlHome
	if homeCheck == nil {
		homeCheck = CheckNativeControlHome
	}
	if err := homeCheck(ctx, s.Docker.ControlHome); err != nil {
		return err
	}
	engineCheck := g.CheckEngineData
	if engineCheck == nil {
		engineCheck = CheckNativeEngineData
	}
	if err := engineCheck(ctx, s.OrbStackConfig, s.EngineDataPath); err != nil {
		return err
	}
	checker := g.CheckVolume
	if checker == nil {
		checker = CheckNativeVolume
	}
	pathChecker := g.CheckPathVolume
	if pathChecker == nil {
		pathChecker = CheckNativePathVolume
	}
	for _, v := range s.Volumes {
		if !filepath.IsAbs(v.Path) || filepath.Clean(v.Path) != v.Path || v.Path == "/" || v.UUID == "" || v.Name == "" || !filepath.IsAbs(v.Sentinel) || filepath.Clean(v.Sentinel) != v.Sentinel || !within(v.Sentinel, v.Path) || v.Sentinel == v.Path || len(v.SentinelSHA256) != 64 {
			return docker.Fail("policy_denied", "invalid external volume claim")
		}
		if err := checker(ctx, v); err != nil {
			return err
		}
		if err := pathChecker(ctx, v.Sentinel, v); err != nil {
			return err
		}
		fi, err := os.Lstat(v.Sentinel)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
			return docker.Fail("storage_unavailable", "volume sentinel missing or unsafe: %s", v.Sentinel)
		}
		if err := hostpath.PrivateFile(v.Sentinel); err != nil {
			return docker.Fail("policy_denied", "unsafe sentinel identity: %v", err)
		}
		if resolved, err := filepath.EvalSymlinks(v.Sentinel); err != nil || resolved != v.Sentinel {
			return docker.Fail("storage_unavailable", "noncanonical sentinel")
		}
		b, err := os.ReadFile(v.Sentinel)
		if err != nil {
			return docker.Fail("storage_unavailable", "read sentinel: %v", err)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != v.SentinelSHA256 {
			return docker.Fail("storage_unavailable", "volume sentinel changed")
		}
	}
	control, err := project.Home()
	if err != nil {
		return err
	}
	protected := []string{s.EngineDataPath, control, s.Docker.CLI, s.Docker.ConfigDir, s.OrbStackConfig}
	// Existing control subdirectories and files must not redirect later state,
	// auth or settings writes through a symlink into a Guest-writable bind.
	for _, part := range []string{"sessions", "projects", "conf", "bin", "codex-home", "security", "terminals", "services", "ports", "service-backends"} {
		protected = append(protected, filepath.Join(control, part))
	}
	for _, p := range protected {
		if err := hostpath.Canonical(p, true); err != nil {
			return docker.Fail("policy_denied", "unsafe trusted control path: %v", err)
		}
	}
	for _, part := range []string{".ssh", ".claude", ".codex", ".aws", ".gnupg", ".docker", ".orbstack"} {
		protected = append(protected, filepath.Join(s.Docker.ControlHome, part))
	}
	paths := append([]string{s.EngineDataPath}, func() []string {
		var out []string
		for _, m := range s.BindMounts {
			out = append(out, m.Source)
		}
		return out
	}()...)
	for _, p := range paths {
		if err := hostpath.Canonical(p, false); err != nil {
			return docker.Fail("storage_unavailable", "path missing or not canonical: %s: %v", p, err)
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() && (!fi.Mode().IsRegular() || p == s.EngineDataPath) {
			return docker.Fail("policy_denied", "storage/bind path must be a directory or regular file: %s", p)
		}
		covered := false
		for _, v := range s.Volumes {
			contains, err := hostpath.Contains(v.Path, p)
			if err != nil {
				return docker.Fail("storage_unavailable", "inspect volume ancestry: %v", err)
			}
			if contains {
				if err := pathChecker(ctx, p, v); err != nil {
					return err
				}
				covered = true
			}
		}
		if !covered {
			return docker.Fail("policy_denied", "path lacks an external volume claim: %s", p)
		}
	}
	for _, m := range s.BindMounts {
		for _, p := range protected {
			overlap, err := hostpath.Overlaps(m.Source, p)
			if err != nil || overlap {
				return docker.Fail("policy_denied", "bind source overlaps or cannot verify trusted control/engine path %s: %v", p, err)
			}
		}
		for _, v := range s.Volumes {
			overlap, err := hostpath.Overlaps(m.Source, v.Sentinel)
			if err != nil || overlap {
				return docker.Fail("policy_denied", "volume sentinel must not be visible to the Guest: %v", err)
			}
		}
	}
	return nil
}

var numericUser = regexp.MustCompile(`^[1-9][0-9]*(?::[0-9]+)?$`)

func safeSource(p, home string) bool {
	control, _ := project.Home()
	if p == "/" || p == "/Volumes" || p == "/Users" || p == home || within(p, control) || within(control, p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if slices.Contains([]string{".ssh", ".claude", ".codex", ".aws", ".gnupg", ".docker", ".orbstack"}, part) || strings.Contains(part, "docker.sock") || strings.Contains(part, "ssh-agent") || strings.Contains(part, "ssh_auth_sock") {
			return false
		}
	}
	// Resolve reserved component aliases on this filesystem. On a case-sensitive
	// volume, an unrelated .SSH remains a different object from .ssh.
	for current := p; filepath.Dir(current) != current; current = filepath.Dir(current) {
		actual, err := os.Stat(current)
		if err != nil {
			return false
		}
		for _, reserved := range []string{".ssh", ".claude", ".codex", ".aws", ".gnupg", ".docker", ".orbstack"} {
			candidate, err := os.Stat(filepath.Join(filepath.Dir(current), reserved))
			if err != nil && !os.IsNotExist(err) {
				return false
			}
			if err == nil && os.SameFile(actual, candidate) {
				return false
			}
		}
	}
	return true
}

func (g *Guard) safe(ctx context.Context, c docker.Container) (map[string]docker.Volume, error) {
	s := g.Record.Spec
	if !c.State.Running || c.State.Paused || c.State.Restarting {
		return nil, docker.Fail("runtime_unavailable", "container is not running normally")
	}
	if !numericUser.MatchString(c.Config.User) || c.Config.WorkingDir != s.Workspace.GuestPath {
		return nil, docker.Fail("policy_denied", "require explicit nonzero numeric UID and approved working directory")
	}
	if c.Config.Labels["dev.agentreach.managed"] != "true" || c.Config.Labels["dev.agentreach.project"] != s.ID {
		return nil, docker.Fail("identity_changed", "project labels do not match")
	}
	var h docker.HostConfig
	if err := json.Unmarshal(c.HostConfig, &h); err != nil {
		return nil, docker.Fail("policy_denied", "invalid HostConfig")
	}
	if h.Privileged || h.NetworkMode == "host" || strings.HasPrefix(h.NetworkMode, "container:") || h.PidMode != "" || h.IpcMode == "host" || strings.HasPrefix(h.IpcMode, "container:") || h.UsernsMode == "host" || h.CgroupnsMode == "host" || h.UTSMode != "" || len(h.Tmpfs) > 0 || len(h.DeviceCgroupRules) > 0 || len(h.CapAdd) > 0 || len(h.Devices) > 0 || len(h.DeviceRequests) > 0 || !slices.Contains(h.CapDrop, "ALL") {
		return nil, docker.Fail("policy_denied", "unsafe container privileges/namespaces/devices/capabilities")
	}
	if h.PublishAllPorts || len(h.PortBindings) != 0 {
		return nil, docker.Fail("policy_denied", "P0 managed workspaces cannot publish Host ports")
	}
	for _, opt := range h.SecurityOpt {
		if opt != "no-new-privileges" && opt != "no-new-privileges=true" {
			return nil, docker.Fail("policy_denied", "unapproved security option")
		}
	}
	if !slices.Contains(h.SecurityOpt, "no-new-privileges") && !slices.Contains(h.SecurityOpt, "no-new-privileges=true") {
		return nil, docker.Fail("policy_denied", "no-new-privileges is required")
	}
	if len(c.Mounts) != len(s.BindMounts)+len(s.NamedVolumes) {
		return nil, docker.Fail("policy_denied", "unapproved mount count")
	}
	seen := map[string]bool{}
	volumes := map[string]docker.Volume{}
	workspaceMount := false
	for _, m := range c.Mounts {
		if seen[m.Destination] || m.Destination == "/" || !path.IsAbs(m.Destination) || path.Clean(m.Destination) != m.Destination || strings.Contains(m.Destination, "docker.sock") || strings.Contains(strings.ToLower(m.Destination), "ssh-agent") {
			return nil, docker.Fail("policy_denied", "unsafe/duplicate mount destination")
		}
		seen[m.Destination] = true
		approved := false
		switch m.Type {
		case "bind":
			if !safeSource(m.Source, s.Docker.ControlHome) || m.Propagation != "rprivate" && m.Propagation != "" {
				return nil, docker.Fail("policy_denied", "unsafe bind source/propagation: %s", m.Source)
			}
			for _, a := range s.BindMounts {
				if a.Source == m.Source && a.Target == m.Destination && a.ReadOnly == !m.RW {
					approved = true
				}
			}
			if m.Destination == s.Workspace.GuestPath && s.Workspace.Mode == "shared-project" && m.Source == s.Workspace.HostPath {
				workspaceMount = true
			}
		case "volume":
			for _, a := range s.NamedVolumes {
				if a.Name == m.Name && a.Target == m.Destination && a.ReadOnly == !m.RW {
					approved = true
				}
			}
			v, err := g.Docker.Volume(ctx, m.Name)
			if err != nil {
				return nil, err
			}
			if v.Driver != "local" || len(v.Options) != 0 || m.Driver != "local" || v.Name != m.Name {
				return nil, docker.Fail("policy_denied", "named volume has unsafe driver/options")
			}
			volumes[m.Name] = v
			if m.Destination == s.Workspace.GuestPath && s.Workspace.Mode == "guest-owned" {
				workspaceMount = true
			}
		default:
			return nil, docker.Fail("policy_denied", "unsupported mount type: %s", m.Type)
		}
		if !approved {
			return nil, docker.Fail("policy_denied", "unapproved mount: %s", m.Destination)
		}
	}
	if !workspaceMount {
		return nil, docker.Fail("policy_denied", "workspace lacks its approved mount")
	}
	return volumes, nil
}

func snapshot(c docker.Container) string {
	var host any
	_ = json.Unmarshal(c.HostConfig, &host)
	mounts := slices.Clone(c.Mounts)
	slices.SortFunc(mounts, func(a, b docker.Mount) int { return strings.Compare(a.Destination, b.Destination) })
	b, _ := json.Marshal(struct {
		User, Dir string
		Labels    map[string]string
		Mounts    []docker.Mount
		Host      any
	}{c.Config.User, c.Config.WorkingDir, c.Config.Labels, mounts, host})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Capture validates an existing container before the explicit registration.
func (g *Guard) Capture(ctx context.Context) (project.Binding, error) {
	if err := g.storage(ctx); err != nil {
		return project.Binding{}, err
	}
	if err := g.Docker.CheckConnection(ctx); err != nil {
		return project.Binding{}, err
	}
	daemonID, err := g.Docker.DaemonID(ctx)
	if err != nil {
		return project.Binding{}, err
	}
	c, err := g.Docker.Inspect(ctx, g.Record.Spec.Container)
	if err != nil {
		return project.Binding{}, err
	}
	vs, err := g.safe(ctx, c)
	if err != nil {
		return project.Binding{}, err
	}
	if len(c.ID) != 64 || c.Image == "" || c.State.StartedAt == "" {
		return project.Binding{}, docker.Fail("identity_changed", "incomplete container identity")
	}
	return project.Binding{Schema: project.Schema, Revision: 1, Generation: 1, ContainerID: c.ID, ImageID: c.Image, StartedAt: c.State.StartedAt, DaemonID: daemonID, SnapshotSHA256: snapshot(c), NamedVolumes: vs, RegisteredAt: time.Now()}, nil
}

// Check is ensure_ready: it performs inspections only and never repairs state.
func (g *Guard) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := g.storage(ctx); err != nil {
		return err
	}
	if err := g.Docker.CheckConnection(ctx); err != nil {
		return err
	}
	b := g.Record.Binding
	daemonID, err := g.Docker.DaemonID(ctx)
	if err != nil {
		return err
	}
	if daemonID != b.DaemonID {
		return docker.Fail("identity_changed", "Docker daemon ID changed")
	}
	c, err := g.Docker.Inspect(ctx, b.ContainerID)
	if err != nil {
		return err
	}
	if c.ID != b.ContainerID || c.Image != b.ImageID {
		return docker.Fail("identity_changed", "registered container/image no longer matches; rebind is not automatic")
	}
	if c.State.StartedAt != b.StartedAt {
		return docker.Fail("generation_mismatch", "container restarted; old channels cannot be resumed")
	}
	vs, err := g.safe(ctx, c)
	if err != nil {
		return err
	}
	if snapshot(c) != b.SnapshotSHA256 || !reflect.DeepEqual(vs, b.NamedVolumes) {
		return docker.Fail("identity_changed", "container configuration or volume identity changed")
	}
	return nil
}

// Description is safe to expose without a Docker operation.
func (g *Guard) Description() string {
	return fmt.Sprintf("%s@%s generation %d", g.Record.Spec.ID, g.Record.Binding.ContainerID, g.Record.Binding.Generation)
}
