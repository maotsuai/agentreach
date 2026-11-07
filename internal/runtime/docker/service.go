package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ServiceContainer deliberately does not change the P0 workspace snapshot.
type ServiceContainer struct {
	Container
	Config          json.RawMessage
	Path            string
	Args            []string
	NetworkSettings struct{ Networks map[string]Endpoint }
}

// Endpoint identifies one effective container network attachment.
type Endpoint struct{ NetworkID, EndpointID, IPAddress string }

// Image contains immutable image defaults expanded during service planning.
type Image struct {
	ID     string `json:"Id"`
	Config struct {
		Entrypoint, Cmd, Env []string
		User, WorkingDir     string
		Volumes              map[string]json.RawMessage
		Healthcheck          json.RawMessage
	}
}

// Network identifies an existing explicitly approved bridge.
type Network struct {
	ID           string `json:"Id"`
	Name, Driver string
	Internal     bool
	Labels       map[string]string
}

// ServiceVolume adds creation identity without changing the P0 volume snapshot.
type ServiceVolume struct {
	Volume
	CreatedAt string
}

// ServiceVolume observes an existing project-owned local volume without creating it.
func (c *Client) ServiceVolume(ctx context.Context, name string) (ServiceVolume, error) {
	if !volumeNamePattern.MatchString(name) {
		return ServiceVolume{}, Fail("policy_denied", "invalid service volume")
	}
	b, e := c.command(ctx, true, "volume", "inspect", name)
	if e != nil {
		return ServiceVolume{}, e
	}
	var v []ServiceVolume
	if e = json.Unmarshal(b, &v); e != nil || len(v) != 1 || v[0].Name != name || v[0].CreatedAt == "" {
		return ServiceVolume{}, Fail("identity_changed", "service volume identity unavailable")
	}
	return v[0], nil
}

// InspectService is read-only; names are never accepted at a mutation boundary.
func (c *Client) InspectService(ctx context.Context, id string) (ServiceContainer, error) {
	var v []ServiceContainer
	if !fullID(id) {
		return ServiceContainer{}, Fail("policy_denied", "full container ID required")
	}
	b, e := c.command(ctx, true, "inspect", "--type", "container", id)
	if e != nil {
		if strings.TrimSpace(string(b)) == "[]\nError response from daemon: No such container: "+id {
			return ServiceContainer{}, Fail("identity_changed", "service container no longer exists")
		}
		return ServiceContainer{}, e
	}
	if e = json.Unmarshal(b, &v); e != nil || len(v) != 1 || v[0].ID != id {
		return ServiceContainer{}, Fail("identity_changed", "service inspect")
	}
	return v[0], nil
}

// Image inspects a locally available immutable image without pulling it.
func (c *Client) Image(ctx context.Context, id string) (Image, error) {
	var v []Image
	if !strings.HasPrefix(id, "sha256:") || !fullID(strings.TrimPrefix(id, "sha256:")) {
		return Image{}, Fail("policy_denied", "immutable image ID required")
	}
	b, e := c.command(ctx, true, "image", "inspect", id)
	if e != nil {
		if strings.TrimSpace(string(b)) == "[]\nError response from daemon: No such image: "+id {
			return Image{}, Fail("image_unavailable", "approved image is not locally available")
		}
		return Image{}, e
	}
	if e = json.Unmarshal(b, &v); e != nil || len(v) != 1 || v[0].ID != id {
		return Image{}, Fail("identity_changed", "image inspect")
	}
	return v[0], nil
}

// Network inspects an exact bridge identity without attaching resources.
func (c *Client) Network(ctx context.Context, id string) (Network, error) {
	var v []Network
	if !fullID(id) {
		return Network{}, Fail("policy_denied", "full network ID required")
	}
	b, e := c.command(ctx, true, "network", "inspect", id)
	if e != nil {
		return Network{}, e
	}
	if e = json.Unmarshal(b, &v); e != nil || len(v) != 1 || v[0].ID != id || v[0].Driver != "bridge" {
		return Network{}, Fail("policy_denied", "explicit bridge network required")
	}
	return v[0], nil
}

// ComposeTool pins the resolved plugin and executable content discovered by the
// fixed Docker CLI. It is part of the approved service rendering identity.
type ComposeTool struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}

// ComposeTool fingerprints the first fixed plugin before any plugin execution.
func (c *Client) ComposeTool(_ context.Context) (ComposeTool, error) {
	// Check the actual first plugin candidate before executing any metadata.
	// Extra plugin directories could otherwise execute Guest-controlled code
	// while merely discovering the supposedly trusted plugin.
	b, e := os.ReadFile(filepath.Join(c.Connection.ConfigDir, "config.json"))
	if e != nil {
		return ComposeTool{}, e
	}
	var config struct {
		Extra []string `json:"cliPluginsExtraDirs"`
	}
	if e = json.Unmarshal(b, &config); e != nil || len(config.Extra) != 0 {
		return ComposeTool{}, Fail("policy_denied", "Compose requires no extra plugin search directories")
	}
	alias := filepath.Join(c.Connection.ConfigDir, "cli-plugins", "docker-compose")
	{
		resolved, e := filepath.EvalSymlinks(alias)
		if e != nil {
			return ComposeTool{}, e
		}
		fi, e := os.Stat(resolved)
		if e != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0022 != 0 || fi.Mode().Perm()&0111 == 0 || fi.Size() > 256<<20 {
			return ComposeTool{}, Fail("policy_denied", "unsafe Compose executable")
		}
		data, e := os.ReadFile(resolved)
		if e != nil {
			return ComposeTool{}, e
		}
		sum := sha256.Sum256(data)
		return ComposeTool{resolved, hex.EncodeToString(sum[:]), "v5.5.0"}, nil
	}
}

var composeName = regexp.MustCompile(`^reach-[a-f0-9]{32}$`)

// Compose performs only render/create/id queries against one private generated
// file. There is no caller-selected subcommand, build, pull, up or down.
func (c *Client) Compose(ctx context.Context, tool ComposeTool, name, file, op string) ([]byte, error) {
	now, e := c.ComposeTool(ctx)
	if e != nil {
		return nil, e
	}
	if now != tool {
		return nil, Fail("identity_changed", "Compose executable changed")
	}
	version, e := c.command(ctx, true, "compose", "version", "--short")
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(string(version)) != "5.5.0" {
		return nil, Fail("unsupported", "Compose version must be explicitly validated")
	}
	if !composeName.MatchString(name) || !filepath.IsAbs(file) || filepath.Base(file) != "compose.json" {
		return nil, Fail("policy_denied", "generated Compose identity")
	}
	args := []string{"compose", "--project-directory", filepath.Dir(file), "--env-file", "/dev/null", "-p", name, "-f", file}
	switch op {
	case "render":
		args = append(args, "config", "--format", "json", "--no-interpolate")
	case "create":
		args = append(args, "create", "--no-build", "--pull", "never", "app")
	case "id":
		args = append(args, "ps", "-aq", "--no-trunc", "app")
	default:
		return nil, Fail("policy_denied", "unknown Compose action")
	}
	return c.commandAt(ctx, true, filepath.Dir(file), args...)
}

// ChangeService accepts only exact identities; ownership/configuration must be
// verified by the service driver immediately before calling this primitive.
func (c *Client) ChangeService(ctx context.Context, id, op string) ([]byte, error) {
	if !fullID(id) {
		return nil, Fail("policy_denied", "full service container ID required")
	}
	switch op {
	case "start":
		return c.command(ctx, true, "start", id)
	case "stop":
		return c.command(ctx, true, "stop", "--time", "5", id)
	default:
		return nil, Fail("policy_denied", "unknown service mutation")
	}
}

// ServiceLogs reads a bounded number of lines from one exact container.
func (c *Client) ServiceLogs(ctx context.Context, id string, n int) ([]byte, error) {
	if !fullID(id) || n < 1 || n > 100 {
		return nil, fmt.Errorf("request_budget: service logs")
	}
	return c.command(ctx, true, "logs", "--tail", fmt.Sprint(n), id)
}
