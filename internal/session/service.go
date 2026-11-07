package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/ports"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/service"
)

// Services uses a separate persistent backend namespace; public terminals can
// neither discover these identities nor write interactive input to them.
func (s *Session) Services(check func(context.Context) error) (*service.Manager, error) {
	r, e := s.ManagedProject()
	if e != nil {
		return nil, e
	}
	base, e := project.Home()
	if e != nil {
		return nil, e
	}
	p, e := s.Policy(check)
	if e != nil {
		return nil, e
	}
	t, e := s.terminals(check, filepath.Join(base, "service-backends"))
	if e != nil {
		return nil, e
	}
	b := policy.Binding{ProjectID: s.Managed.ProjectID, RecordDigest: s.Managed.RecordSHA256, Generation: r.Binding.Generation}
	pc := &ports.Client{Config: ports.Config{Base: base, Binding: b, Check: check, Paths: s.CheckControlPaths, Policy: p}}
	driver := &service.DockerDriver{Client: &docker.Client{Connection: r.Spec.Docker}, Project: r, Base: base, Paths: s.CheckControlPaths}
	m := service.New(service.Config{Base: base, Binding: b, Policy: p, Check: check, Paths: s.CheckControlPaths, Docker: driver, Terminal: t, Ports: pc})
	pc.Resolve = m.Target
	pc.ResolveIdentity = m.IdentityTarget
	pc.Start = func() error {
		self, e := os.Executable()
		if e != nil {
			return e
		}
		self, e = filepath.EvalSymlinks(self)
		if e != nil {
			return e
		}
		fi, e := os.Stat(self)
		if e != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("policy_denied: Host manager executable")
		}
		if e = s.CheckControlPaths(); e != nil {
			return e
		}
		for _, mount := range r.Spec.BindMounts {
			if !mount.ReadOnly {
				yes, e := hostpath.Contains(mount.Source, self)
				if e != nil || yes {
					return fmt.Errorf("policy_denied: Host manager executable overlaps Guest bind")
				}
			}
		}
		cmd := exec.CommandContext(context.Background(), self, "hostd", "--session", s.Name)
		cmd.Dir = base
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "REACH_HOME=" + base, "REACH_SESSION=" + s.Name, "REACH_MANAGED=1", "REACH_MANAGED_PROJECT=" + b.ProjectID, "REACH_MANAGED_RECORD=" + b.RecordDigest}
		detachHostManager(cmd)
		if e = cmd.Start(); e != nil {
			return e
		}
		go func() { _ = cmd.Wait() }()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			c, cancel := context.WithTimeout(context.Background(), time.Second)
			e := pc.Ping(c)
			cancel()
			if e == nil {
				return nil
			}
			time.Sleep(25 * time.Millisecond)
		}
		return fmt.Errorf("exposure_unavailable: Host manager did not become ready")
	}
	return m, nil
}
