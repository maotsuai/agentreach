package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/rooted"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/transport"
	"github.com/bojieli/agentreach/internal/workspace"
)

// Managed pins a session to the full immutable trusted project record.
type Managed struct {
	ProjectID    string `json:"project_id"`
	RecordSHA256 string `json:"record_sha256"`
}

func recordDigest(r project.Record) string {
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Bind creates the persisted reference without changing the project record.
func Bind(r project.Record) *Managed {
	return &Managed{ProjectID: r.Spec.ID, RecordSHA256: recordDigest(r)}
}

// ManagedProject refuses a changed project or a downgraded target/mode.
func (s *Session) ManagedProject() (project.Record, error) {
	if s.Managed == nil {
		return project.Record{}, docker.Fail("policy_denied", "session is not managed")
	}
	r, err := project.Load(s.Managed.ProjectID)
	if err != nil {
		return r, err
	}
	if recordDigest(r) != s.Managed.RecordSHA256 || s.Target.Kind != KindDocker || s.Target.Container != r.Binding.ContainerID || s.Target.Workspace != r.Spec.Workspace.GuestPath || s.Mode != ModeExec {
		return r, docker.Fail("identity_changed", "session no longer matches its approved project")
	}
	return r, nil
}

// CheckManaged validates managed runtimes; legacy sessions retain their path.
func (s *Session) CheckManaged(ctx context.Context) error {
	if s.Managed == nil {
		return nil
	}
	current, err := Load(s.Name)
	if err != nil {
		return docker.Fail("identity_changed", "managed session is no longer available: %v", err)
	}
	if current.Managed == nil || *current.Managed != *s.Managed || current.Mode != s.Mode || !current.Created.Equal(s.Created) || current.Target == nil || *current.Target != *s.Target {
		return docker.Fail("identity_changed", "managed session was removed, downgraded or rebound")
	}
	r, err := s.ManagedProject()
	if err != nil {
		return err
	}
	if err := s.CheckControlPaths(); err != nil {
		return err
	}
	return orbstack.New(r).Check(ctx)
}

// ManagedEnvironment carries the approved binding through harness callbacks.
// Caller-supplied values cannot substitute another project or record.
func (s *Session) ManagedEnvironment(env []string) []string {
	var out []string
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "REACH_MANAGED" || key == "REACH_MANAGED_PROJECT" || key == "REACH_MANAGED_RECORD" {
			continue
		}
		out = append(out, entry)
	}
	if s.Managed != nil {
		out = append(out, "REACH_MANAGED=1", "REACH_MANAGED_PROJECT="+s.Managed.ProjectID, "REACH_MANAGED_RECORD="+s.Managed.RecordSHA256)
	}
	return out
}
func (s *Session) checkManagedContext() error {
	if os.Getenv("REACH_MANAGED") != "1" {
		return nil
	}
	if s.Managed == nil || os.Getenv("REACH_MANAGED_PROJECT") == "" || os.Getenv("REACH_MANAGED_RECORD") == "" || s.Managed.ProjectID != os.Getenv("REACH_MANAGED_PROJECT") || s.Managed.RecordSHA256 != os.Getenv("REACH_MANAGED_RECORD") || s.Name != os.Getenv("REACH_SESSION") {
		return docker.Fail("identity_changed", "managed child requires its original session/project binding")
	}
	return nil
}

// CheckControlPaths validates the actual directories and files this managed
// session will use, including legal not-yet-created suffixes. Host same-UID
// adversarial concurrent mutation of the control root is outside this contract.
func (s *Session) CheckControlPaths() error {
	if s.Managed == nil {
		return nil
	}
	base, err := project.Home()
	if err != nil {
		return err
	}
	for _, rel := range []string{"", "sessions", "projects", "conf", "bin", "codex-home", "security", "terminals", "services", "ports", "service-backends", filepath.Join("terminals", s.Managed.ProjectID+".json"), filepath.Join("terminals", s.Managed.ProjectID+".json.lock"), filepath.Join("security", s.Managed.ProjectID+".json"), filepath.Join("security", s.Managed.ProjectID+".json.lock"), filepath.Join("sessions", s.Name+".json"), filepath.Join("sessions", s.Name+".cwd"), filepath.Join("projects", s.Managed.ProjectID+".json"), filepath.Join("conf", s.Name+".claude-settings.json"), filepath.Join("codex-home", s.Name), filepath.Join("codex-home", s.Name, "auth.json"), filepath.Join("codex-home", s.Name, "config.toml"), filepath.Join("codex-home", s.Name, "environments.toml")} {
		if err := hostpath.Canonical(filepath.Join(base, rel), true); err != nil {
			return fmt.Errorf("policy_denied: unsafe managed control path: %w", err)
		}
	}
	for _, rel := range []string{filepath.Join("terminals", s.Managed.ProjectID+".json"), filepath.Join("security", s.Managed.ProjectID+".json"), filepath.Join("sessions", s.Name+".json"), filepath.Join("sessions", s.Name+".cwd"), filepath.Join("projects", s.Managed.ProjectID+".json"), filepath.Join("conf", s.Name+".claude-settings.json"), filepath.Join("codex-home", s.Name, "auth.json"), filepath.Join("codex-home", s.Name, "config.toml"), filepath.Join("codex-home", s.Name, "environments.toml")} {
		if err := hostpath.PrivateFile(filepath.Join(base, rel)); err != nil {
			return fmt.Errorf("policy_denied: unsafe managed control file: %w", err)
		}
	}
	return nil
}

// ProbeManaged initialises a new, explicitly registered binding before its
// session record exists. Ordinary dispatches never use this bootstrap guard.
func (s *Session) ProbeManaged(ctx context.Context, registered project.Record) error {
	if s.Managed == nil || *s.Managed != *Bind(registered) {
		return docker.Fail("identity_changed", "invalid managed bootstrap binding")
	}
	if _, err := Load(s.Name); !errors.Is(err, os.ErrNotExist) {
		return docker.Fail("identity_changed", "managed bootstrap requires an unused session slot: %v", err)
	}
	r, err := s.ManagedProject()
	if err != nil {
		return err
	}
	if err := s.CheckControlPaths(); err != nil {
		return err
	}
	check := orbstack.New(r).Check
	t, err := s.managedTransport(r, check)
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	return s.probeWithTransport(ctx, t, check)
}

// PathMapper uses only approved shared roots. A harness may add its trusted control cwd separately.
func (s *Session) PathMapper() (workspace.Mapper, error) {
	r, e := s.ManagedProject()
	if e != nil {
		return workspace.Mapper{}, e
	}
	return managedPathMapper(r)
}

// managedPathMapper validates the registered Protocol capability before any
// transport, including bootstrap probes and fixed-Guest consumers, is created.
func managedPathMapper(r project.Record) (workspace.Mapper, error) {
	m := workspace.Mapper{Root: r.Spec.Workspace.GuestPath}
	if r.Spec.Workspace.HostPath != "" {
		m.HostAliases = []string{r.Spec.Workspace.HostPath}
	}
	if e := m.ValidateProtocolAliases(); e != nil {
		return workspace.Mapper{}, e
	}
	return m, nil
}

// Command builds the one managed launcher. Its caller must leave ExecRequest.Dir empty afterwards.
func (s *Session) Command(req reach.ExecRequest) (string, error) {
	if s.Managed == nil {
		return transport.BuildCommand(req), nil
	}
	m, e := s.PathMapper()
	if e != nil {
		return "", e
	}
	cwd := req.Dir
	if cwd == "" {
		cwd = m.Root
	}
	r, e := m.Map(workspace.PathRequest{Operation: "cwd", Namespace: workspace.Guest, Path: cwd})
	if e != nil {
		return "", e
	}
	id, e := s.rootBinding.Identity()
	if e != nil {
		return "", e
	}
	return rooted.Command(m.Root, r.Relative, req, id)
}
