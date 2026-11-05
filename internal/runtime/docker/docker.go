// Package docker drives a fixed Docker connection without invoking a shell.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Error includes a stable runtime failure category.
type Error struct {
	Kind   string
	Detail string
}

func (e *Error) Error() string { return e.Kind + ": " + e.Detail }

// Fail constructs a categorised runtime error.
func Fail(kind, format string, args ...any) error { return &Error{kind, fmt.Sprintf(format, args...)} }

// Connection pins the Docker executable, control identity and daemon endpoint.
type Connection struct {
	CLI       string `json:"cli"`
	Context   string `json:"context"`
	Endpoint  string `json:"endpoint"`
	ConfigDir string `json:"config_dir"`
	// ControlHome is the trusted Host identity, independent of a harness's
	// throwaway HOME. Docker never inherits that temporary identity.
	ControlHome string `json:"control_home"`
}

// Runner executes one Docker argv with an exact environment; tests inject it.
type Runner func(context.Context, string, []string, []string) ([]byte, error)

// Client only exposes the inspections required for managed runtime validation.
type Client struct {
	Connection Connection
	Run        Runner
}

// Command invokes a fixed executable directly, without a Host shell.
func Command(ctx context.Context, cli string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	// OrbStack's resolved docker-tools is a multicall executable; selecting its
	// Docker entrypoint must not depend on a mutable symlink or basename.
	cmd.Args[0] = "docker"
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, Fail("engine_unavailable", "%v: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Validate requires resolved trusted executable and directory paths.
func (c Connection) Validate() error {
	if !filepath.IsAbs(c.CLI) || !filepath.IsAbs(c.ConfigDir) || !filepath.IsAbs(c.ControlHome) || c.Context != "orbstack" || !strings.HasPrefix(c.Endpoint, "unix:///") {
		return Fail("policy_denied", "require absolute Docker CLI/config_dir/control_home, context orbstack and a unix endpoint")
	}
	resolved, err := filepath.EvalSymlinks(c.CLI)
	if err != nil {
		return Fail("engine_unavailable", "Docker CLI: %v", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o022 != 0 || fi.Mode().Perm()&0o111 == 0 {
		return Fail("policy_denied", "Docker CLI must be a regular executable without group/other write permission")
	}
	if resolved != c.CLI {
		return Fail("policy_denied", "cli must name its resolved executable: %s", resolved)
	}
	for _, p := range []string{c.ConfigDir, c.ControlHome} {
		resolved, err := filepath.EvalSymlinks(p)
		if fi, statErr := os.Stat(p); err != nil || statErr != nil || resolved != p || !fi.IsDir() {
			return Fail("engine_unavailable", "Docker control directory is missing/noncanonical: %s", p)
		}
	}
	return nil
}

// Environment rejects connection overrides and removes all ambient DOCKER_*
// values from execution. The approved --config/--host determine the target.
func (c Connection) Environment() ([]string, error) {
	for _, entry := range os.Environ() {
		k, v, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(k, "DOCKER_") || v == "" {
			continue
		}
		allowed := k == "DOCKER_CONTEXT" && v == c.Context || k == "DOCKER_HOST" && v == c.Endpoint || k == "DOCKER_CONFIG" && filepath.Clean(v) == filepath.Clean(c.ConfigDir)
		if !allowed {
			return nil, Fail("context_mismatch", "conflicting environment variable %s", k)
		}
	}
	return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + c.ControlHome, "LANG=C", "LC_ALL=C"}, nil
}

func (c *Client) command(ctx context.Context, pinned bool, args ...string) ([]byte, error) {
	env, err := c.Connection.Environment()
	if err != nil {
		return nil, err
	}
	argv := []string{"--config", c.Connection.ConfigDir}
	if pinned {
		argv = append(argv, "--host", c.Connection.Endpoint)
	}
	argv = append(argv, args...)
	run := c.Run
	if run == nil {
		run = Command
	}
	return run(ctx, c.Connection.CLI, argv, env)
}

// CheckConnection verifies the ambient context before any pinned operation.
func (c *Client) CheckConnection(ctx context.Context) error {
	if err := c.Connection.Validate(); err != nil {
		return err
	}
	out, err := c.command(ctx, false, "context", "show")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != c.Connection.Context {
		return Fail("context_mismatch", "ambient Docker context is not %s", c.Connection.Context)
	}
	out, err = c.command(ctx, false, "context", "inspect", c.Connection.Context)
	if err != nil {
		return err
	}
	var contexts []struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(out, &contexts); err != nil || len(contexts) != 1 {
		return Fail("context_mismatch", "invalid Docker context inspect response")
	}
	if contexts[0].Endpoints["docker"].Host != c.Connection.Endpoint {
		return Fail("context_mismatch", "approved endpoint changed")
	}
	return nil
}

// Mount is Docker's effective mount description, not its requested bind syntax.
type Mount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Driver      string `json:"Driver,omitempty"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
	Propagation string `json:"Propagation"`
}

// HostConfig contains security properties checked at registration and dispatch.
type HostConfig struct {
	Privileged        bool
	NetworkMode       string
	PidMode           string
	IpcMode           string
	UsernsMode        string
	CgroupnsMode      string
	UTSMode           string
	Tmpfs             map[string]string
	DeviceCgroupRules []string
	CapAdd            []string
	CapDrop           []string
	SecurityOpt       []string
	Devices           []json.RawMessage
	DeviceRequests    []json.RawMessage
	PortBindings      map[string][]json.RawMessage
	PublishAllPorts   bool
}

// Container is the subset needed for runtime identity and isolation checks.
type Container struct {
	ID    string `json:"Id"`
	Image string
	State struct {
		Running    bool
		Paused     bool
		Restarting bool
		StartedAt  string
	}
	Config struct {
		User       string
		WorkingDir string
		Labels     map[string]string
	}
	Mounts     []Mount
	HostConfig json.RawMessage
}

// Inspect queries the pinned endpoint, using a name only during registration.
func (c *Client) Inspect(ctx context.Context, id string) (Container, error) {
	out, err := c.command(ctx, true, "inspect", "--type", "container", id)
	if err != nil {
		// This exact C-locale response was measured against the approved CLI.
		// Names and other daemon/connection errors retain their original cause.
		if fullID(id) && strings.TrimSpace(string(out)) == "[]\nError response from daemon: No such container: "+id {
			return Container{}, Fail("identity_changed", "registered container %s no longer exists; explicitly register a new project binding", id)
		}
		return Container{}, err
	}
	var v []Container
	if err := json.Unmarshal(out, &v); err != nil || len(v) != 1 || v[0].ID == "" {
		return Container{}, Fail("identity_changed", "invalid container inspect response")
	}
	return v[0], nil
}

// Volume captures the local driver's options and identity labels.
type Volume struct {
	Name    string
	Driver  string
	Options map[string]string
	Labels  map[string]string
}

// Volume inspects a named volume without creating it.
func (c *Client) Volume(ctx context.Context, name string) (Volume, error) {
	out, err := c.command(ctx, true, "volume", "inspect", name)
	if err != nil {
		return Volume{}, err
	}
	var v []Volume
	if err := json.Unmarshal(out, &v); err != nil || len(v) != 1 {
		return Volume{}, Fail("policy_denied", "invalid volume inspect response")
	}
	return v[0], nil
}

// DaemonID requires an identified OrbStack Engine at the pinned endpoint.
func (c *Client) DaemonID(ctx context.Context) (string, error) {
	out, err := c.command(ctx, true, "info", "--format", "{{json .}}")
	if err != nil {
		return "", err
	}
	var info struct {
		ID              string
		OperatingSystem string
	}
	if err := json.Unmarshal(out, &info); err != nil || info.ID == "" || !strings.Contains(info.OperatingSystem, "OrbStack") {
		return "", Fail("identity_changed", "daemon identity is missing or is not OrbStack")
	}
	return info.ID, nil
}

// ExecArgs uses the verified endpoint rather than a mutable context mapping.
func (c Connection) ExecArgs() []string {
	return []string{"--config", c.ConfigDir, "--host", c.Endpoint}
}

func fullID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
