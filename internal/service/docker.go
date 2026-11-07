package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/privatecontrol"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
	"github.com/bojieli/agentreach/internal/state"
)

// DockerDriver is constructed only from a verified immutable project record.
type DockerDriver struct {
	Client  *docker.Client
	Project project.Record
	Base    string
	Paths   func() error
}

// RecoveryIdentity checks a retained sidecar's complete configuration and
// execution identity without depending on the old workspace being alive.
// The caller must separately prove ownership from the original durable intent.
func (d *DockerDriver) RecoveryIdentity(ctx context.Context, r Record) error {
	if r.Spec.Driver != "sidecar" || r.Snapshot == "" || r.StartedAt == "" {
		return fmt.Errorf("identity_changed: incomplete retained sidecar identity")
	}
	if err := d.Paths(); err != nil {
		return err
	}
	c, err := d.Client.InspectService(ctx, r.ContainerID)
	if err != nil {
		return err
	}
	if c.State.StartedAt != r.StartedAt {
		return docker.Fail("generation_mismatch", "orphan sidecar restarted")
	}
	return d.verify(ctx, r, c)
}

func (d *DockerDriver) prepare(ctx context.Context, s Spec) (Spec, docker.ComposeTool, error) {
	s.Mounts = slices.Clone(s.Mounts)
	c, e := d.Client.InspectService(ctx, d.Project.Binding.ContainerID)
	if e != nil {
		return s, docker.ComposeTool{}, e
	}
	n, e := d.Client.Network(ctx, s.NetworkID)
	if e != nil {
		return s, docker.ComposeTool{}, e
	}
	if n.Labels["dev.agentreach.project"] != s.ProjectID {
		return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: project-specific network required")
	}
	found := false
	for _, endpoint := range c.NetworkSettings.Networks {
		if endpoint.NetworkID == s.NetworkID {
			found = true
		}
	}
	if !found || len(c.NetworkSettings.Networks) != 1 {
		return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: workspace is not bound to approved network")
	}
	if s.Driver == "workspace" {
		var h struct{ Memory, NanoCpus, PidsLimit int64 }
		if e = json.Unmarshal(c.HostConfig, &h); e != nil {
			return s, docker.ComposeTool{}, e
		}
		if s.Resources != (Resources{h.Memory, h.NanoCpus, h.PidsLimit}) || s.LogBytes != 8<<20 {
			return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: workspace service inherits container resources and 8 MiB backend log")
		}
		if s.User != c.Container.Config.User {
			var cfg struct{ User string }
			_ = json.Unmarshal(c.Config, &cfg)
			if cfg.User != s.User {
				return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: workspace user")
			}
		}
		return s, docker.ComposeTool{}, nil
	}
	image, e := d.Client.Image(ctx, s.Image)
	if e != nil {
		return s, docker.ComposeTool{}, e
	}
	for target := range image.Config.Volumes {
		covered := false
		for _, mount := range s.Mounts {
			if mount.Target == target {
				covered = true
			}
		}
		if !covered {
			return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: image declares unapproved anonymous volume")
		}
	}
	env := map[string]string{}
	for _, entry := range image.Config.Env {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: image environment")
		}
		env[k] = v
	}
	for k, v := range s.Env {
		env[k] = v
	}
	s.Env = env
	for i := range s.Mounts {
		m := &s.Mounts[i]
		approved := false
		if m.Type == "bind" {
			if e = hostpath.Canonical(m.Source, false); e != nil {
				return s, docker.ComposeTool{}, e
			}
			for _, a := range d.Project.Spec.BindMounts {
				if a.Source == m.Source && a.Target == m.Target && a.ReadOnly == m.ReadOnly {
					approved = true
				}
			}
		} else {
			v, e := d.Client.ServiceVolume(ctx, m.Source)
			if e != nil {
				return s, docker.ComposeTool{}, e
			}
			if v.Driver != "local" || len(v.Options) != 0 || v.Labels["dev.agentreach.project"] != s.ProjectID {
				return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: explicit project-owned local service volume required")
			}
			if m.Volume != nil && !reflect.DeepEqual(*m.Volume, v) {
				return s, docker.ComposeTool{}, docker.Fail("identity_changed", "service volume changed")
			}
			m.Volume = &v
			approved = true
		}
		if !approved {
			return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: mount is outside immutable workspace approvals")
		}
	}
	alias := filepath.Join(d.Client.Connection.ConfigDir, "cli-plugins", "docker-compose")
	resolved, e := filepath.EvalSymlinks(alias)
	if e != nil {
		return s, docker.ComposeTool{}, e
	}
	for _, p := range []string{alias, resolved} {
		for _, m := range d.Project.Spec.BindMounts {
			if !m.ReadOnly {
				yes, e := hostpath.Contains(m.Source, p)
				if e != nil || yes {
					return s, docker.ComposeTool{}, fmt.Errorf("policy_denied: Guest-writable Compose plugin")
				}
			}
		}
	}
	tool, e := d.Client.ComposeTool(ctx)
	if e != nil {
		return s, tool, e
	}
	for _, p := range []string{tool.Path, d.Client.Connection.CLI} {
		for _, m := range d.Project.Spec.BindMounts {
			if !m.ReadOnly {
				yes, e := hostpath.Contains(m.Source, p)
				if e != nil || yes {
					return s, tool, fmt.Errorf("policy_denied: Guest-writable Compose execution chain")
				}
			}
		}
	}
	return s, tool, s.Validate(d.Project.Spec.ID)
}
func (d *DockerDriver) dir(id string) string {
	return filepath.Join(d.Base, "services", d.Project.Spec.ID, id)
}
func (d *DockerDriver) file(r Record) string { return filepath.Join(d.dir(r.ID), "compose.json") }
func escape(s string) string                 { return strings.ReplaceAll(s, "$", "$$") }
func (d *DockerDriver) document(ctx context.Context, r Record) (map[string]any, error) {
	n, e := d.Client.Network(ctx, r.Spec.NetworkID)
	if e != nil {
		return nil, e
	}
	argv := make([]string, len(r.Spec.Argv))
	for i, a := range r.Spec.Argv {
		argv[i] = escape(a)
	}
	env := map[string]string{}
	for k, v := range r.Spec.Env {
		env[k] = escape(v)
	}
	app := map[string]any{"image": r.Spec.Image, "pull_policy": "never", "entrypoint": []string{}, "command": argv, "working_dir": escape(r.Spec.Cwd), "user": r.Spec.User, "environment": env, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges"}, "init": true, "restart": "no", "mem_limit": r.Spec.Resources.Memory, "memswap_limit": r.Spec.Resources.Memory, "cpus": float64(r.Spec.Resources.NanoCPUs) / 1e9, "pids_limit": r.Spec.Resources.PIDs, "healthcheck": map[string]any{"disable": true}, "logging": map[string]any{"driver": "json-file", "options": map[string]string{"max-size": strconv.FormatInt(r.Spec.LogBytes, 10), "max-file": "1"}}, "labels": map[string]string{"dev.agentreach.managed": "true", "dev.agentreach.project": r.Binding.ProjectID, "dev.agentreach.service": r.ID, "dev.agentreach.spec": r.Digest}, "networks": []string{"project"}}
	app["shm_size"] = int64(16 << 20)
	mounts := []any{}
	volumes := map[string]any{}
	for i, m := range r.Spec.Mounts {
		v := map[string]any{"type": m.Type, "source": escape(m.Source), "target": escape(m.Target), "read_only": m.ReadOnly}
		if m.Type == "bind" {
			v["bind"] = map[string]bool{"create_host_path": false}
		} else {
			key := fmt.Sprintf("v%d", i)
			v["source"] = key
			v["volume"] = map[string]bool{"nocopy": true}
			volumes[key] = map[string]any{"external": true, "name": m.Source}
		}
		mounts = append(mounts, v)
	}
	if len(mounts) > 0 {
		app["volumes"] = mounts
	}
	ps := []any{}
	for _, p := range r.Spec.Publish {
		ps = append(ps, map[string]any{"target": p.GuestPort, "published": strconv.Itoa(int(p.HostPort)), "host_ip": "127.0.0.1", "protocol": "tcp"})
	}
	if len(ps) > 0 {
		app["ports"] = ps
	}
	doc := map[string]any{"name": "reach-" + r.ID, "services": map[string]any{"app": app}, "networks": map[string]any{"project": map[string]any{"external": true, "name": n.Name}}}
	if len(volumes) > 0 {
		doc["volumes"] = volumes
	}
	return doc, nil
}
func (d *DockerDriver) render(ctx context.Context, r Record, create bool) (string, error) {
	if e := d.Paths(); e != nil {
		return "", e
	}
	for _, p := range []string{filepath.Dir(d.dir(r.ID)), d.dir(r.ID)} {
		if e := privatecontrol.Dir(p); e != nil {
			return "", e
		}
	}
	doc, e := d.document(ctx, r)
	if e != nil {
		return "", e
	}
	file := d.file(r)
	if create {
		if e = os.MkdirAll(d.dir(r.ID), 0700); e != nil {
			return "", e
		}
		if e = privatecontrol.Dir(d.dir(r.ID)); e != nil {
			return "", e
		}
		if _, e = os.Lstat(file); !os.IsNotExist(e) {
			return "", fmt.Errorf("state_corrupt: generated Compose already exists")
		}
		if e = state.Write(file, doc); e != nil {
			return "", e
		}
	}
	if e = hostpath.Canonical(file, false); e != nil {
		return "", e
	}
	fi, e := os.Lstat(file)
	if e != nil || !privatecontrol.Private(fi) {
		return "", fmt.Errorf("state_corrupt: generated Compose permissions")
	}
	var actual map[string]any
	if e = state.Read(file, &actual); e != nil {
		return "", e
	}
	if Digest(actual) != Digest(doc) {
		return "", docker.Fail("identity_changed", "generated Compose changed")
	}
	b, e := d.Client.Compose(ctx, r.Tool, "reach-"+r.ID, file, "render")
	if e != nil {
		return "", e
	}
	var rendered any
	if e = state.Decode(b, &rendered); e != nil {
		return "", e
	}
	return Digest(rendered), nil
}
func (d *DockerDriver) verify(ctx context.Context, r Record, c docker.ServiceContainer) error {
	if c.ID != r.ContainerID || c.Image != r.Spec.Image {
		return docker.Fail("identity_changed", "service container/image")
	}
	var cfg struct {
		User, WorkingDir     string
		Cmd, Entrypoint, Env []string
		Labels               map[string]string
		Healthcheck          struct{ Test []string }
	}
	if e := json.Unmarshal(c.Config, &cfg); e != nil {
		return e
	}
	if cfg.User != r.Spec.User || cfg.WorkingDir != r.Spec.Cwd || !slices.Equal(cfg.Cmd, r.Spec.Argv) || len(cfg.Entrypoint) != 0 || !slices.Equal(cfg.Healthcheck.Test, []string{"NONE"}) || cfg.Labels["dev.agentreach.service"] != r.ID || cfg.Labels["dev.agentreach.spec"] != r.Digest || cfg.Labels["dev.agentreach.project"] != r.Binding.ProjectID {
		return docker.Fail("identity_changed", "effective service command/configuration")
	}
	env := map[string]string{}
	for _, entry := range cfg.Env {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || env[k] != "" {
			return docker.Fail("identity_changed", "service env")
		}
		env[k] = v
	}
	if !reflect.DeepEqual(env, r.Spec.Env) {
		return docker.Fail("identity_changed", "effective service environment")
	}
	var h struct {
		docker.HostConfig
		Memory, MemorySwap, NanoCpus, PidsLimit int64
		ShmSize                                 int64
		Init                                    bool
		RestartPolicy                           struct{ Name string }
		LogConfig                               struct {
			Type   string
			Config map[string]string
		}
		Binds, VolumesFrom, Links, ExtraHosts, GroupAdd []string
		Runtime                                         string
		ReadonlyRootfs, AutoRemove                      bool
		OomKillDisable                                  bool
	}
	if e := json.Unmarshal(c.HostConfig, &h); e != nil {
		return e
	}
	if h.Privileged || h.PublishAllPorts || h.PidMode != "" || h.UTSMode != "" || h.IpcMode != "private" || h.UsernsMode != "" || h.CgroupnsMode != "private" || len(h.CapAdd) != 0 || !slices.Equal(h.CapDrop, []string{"ALL"}) || len(h.SecurityOpt) != 1 || h.SecurityOpt[0] != "no-new-privileges=true" && h.SecurityOpt[0] != "no-new-privileges" || len(h.Devices)+len(h.DeviceRequests)+len(h.DeviceCgroupRules)+len(h.Tmpfs)+len(h.Binds)+len(h.VolumesFrom)+len(h.Links)+len(h.ExtraHosts)+len(h.GroupAdd) != 0 || h.Memory != r.Spec.Resources.Memory || h.MemorySwap != r.Spec.Resources.Memory || h.NanoCpus != r.Spec.Resources.NanoCPUs || h.PidsLimit != r.Spec.Resources.PIDs || !h.Init || h.RestartPolicy.Name != "no" || h.AutoRemove || h.Runtime != "runc" {
		return docker.Fail("identity_changed", "effective service HostConfig")
	}
	if h.LogConfig.Type != "json-file" || !reflect.DeepEqual(h.LogConfig.Config, map[string]string{"max-size": strconv.FormatInt(r.Spec.LogBytes, 10), "max-file": "1"}) {
		return docker.Fail("identity_changed", "effective service log limits")
	}
	if h.ShmSize != 16<<20 {
		return docker.Fail("identity_changed", "effective shared memory limit")
	}
	if h.OomKillDisable {
		return docker.Fail("identity_changed", "service OOM killing disabled")
	}
	for _, mount := range r.Spec.Mounts {
		if mount.Type == "volume" {
			v, e := d.Client.ServiceVolume(ctx, mount.Source)
			if e != nil {
				return e
			}
			if mount.Volume == nil || !reflect.DeepEqual(v, *mount.Volume) {
				return docker.Fail("identity_changed", "approved service volume changed")
			}
		}
	}
	ports := map[string][]json.RawMessage{}
	for _, p := range r.Spec.Publish {
		b, _ := json.Marshal(struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		}{"127.0.0.1", strconv.Itoa(int(p.HostPort))})
		ports[strconv.Itoa(int(p.GuestPort))+"/tcp"] = append(ports[strconv.Itoa(int(p.GuestPort))+"/tcp"], b)
	}
	if len(ports) != len(h.PortBindings) {
		return docker.Fail("identity_changed", "unexpected service publication")
	}
	for k, v := range ports {
		var a, b any
		aa, _ := json.Marshal(v)
		bb, _ := json.Marshal(h.PortBindings[k])
		_ = json.Unmarshal(aa, &a)
		_ = json.Unmarshal(bb, &b)
		if !reflect.DeepEqual(a, b) {
			return docker.Fail("identity_changed", "non-loopback or changed service publication")
		}
	}
	if len(c.Mounts) != len(r.Spec.Mounts) {
		return docker.Fail("identity_changed", "unapproved effective mount")
	}
	for _, m := range c.Mounts {
		found := false
		for _, a := range r.Spec.Mounts {
			if m.Type == a.Type && m.Destination == a.Target && m.RW == !a.ReadOnly && (a.Type == "bind" && m.Source == a.Source || a.Type == "volume" && m.Name == a.Source) {
				found = true
			}
		}
		if !found {
			return docker.Fail("identity_changed", "effective service mount mismatch")
		}
	}
	n, e := d.Client.Network(ctx, r.Spec.NetworkID)
	if e != nil {
		return e
	}
	if len(c.NetworkSettings.Networks) != 1 {
		return docker.Fail("identity_changed", "service network count")
	}
	ep, ok := c.NetworkSettings.Networks[n.Name]
	if !ok || ep.NetworkID != "" && ep.NetworkID != n.ID || h.NetworkMode != n.Name && h.NetworkMode != n.ID {
		return docker.Fail("identity_changed", "effective service network")
	}
	if r.Snapshot != "" && r.Snapshot != snapshot(c) {
		return docker.Fail("identity_changed", "service effective snapshot changed")
	}
	return nil
}
func snapshot(c docker.ServiceContainer) string {
	var cfg, host any
	_ = json.Unmarshal(c.Config, &cfg)
	_ = json.Unmarshal(c.HostConfig, &host)
	// Engine 29.8.2 changes the optional OomKillDisable value from false to
	// null on start. Both mean enabled OOM killing; true is rejected above.
	if h, ok := host.(map[string]any); ok && h["OomKillDisable"] == nil {
		h["OomKillDisable"] = false
	}
	return Digest(struct {
		Image        string
		Config, Host any
		Mounts       []docker.Mount
		Path         string
		Args         []string
	}{c.Image, cfg, host, c.Mounts, c.Path, c.Args})
}
func (d *DockerDriver) endpoint(ctx context.Context, r Record, c docker.ServiceContainer) (docker.Endpoint, error) {
	n, e := d.Client.Network(ctx, r.Spec.NetworkID)
	if e != nil {
		return docker.Endpoint{}, e
	}
	ep := c.NetworkSettings.Networks[n.Name]
	if ep.NetworkID != r.Spec.NetworkID || ep.EndpointID == "" || net.ParseIP(ep.IPAddress).To4() == nil {
		return ep, docker.Fail("identity_changed", "service endpoint")
	}
	return ep, nil
}

// portReady retains the primary health contract and checks TCP reachability of
// every other approved port before exposing that exact endpoint.
func portReady(ctx context.Context, r Record, port uint16) bool {
	if !r.Ready || !r.Port(port) {
		return false
	}
	if port == r.Spec.Health.Port {
		return true // observe already probed this port under the service lock
	}
	r.Spec.Health = Health{Protocol: "tcp", Port: port, TimeoutMS: r.Spec.Health.TimeoutMS}
	return probe(ctx, r)
}
func probe(ctx context.Context, r Record) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.Spec.Health.TimeoutMS)*time.Millisecond)
	defer cancel()
	address := net.JoinHostPort(r.Endpoint.IPAddress, strconv.Itoa(int(r.Spec.Health.Port)))
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}
	if r.Spec.Health.Protocol == "tcp" {
		c, e := dial(ctx, "", "")
		if e != nil {
			return false
		}
		_ = c.Close()
		return true
	}
	transport := &http.Transport{DialContext: dial, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	p, e := policy.RelativePath(r.Spec.Health.Path)
	if e != nil {
		return false
	}
	u := url.URL{Scheme: "http", Host: address, Path: p}
	q, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return false
	}
	response, e := client.Do(q)
	if e != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 400
}
