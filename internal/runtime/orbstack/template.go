package orbstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/bojieli/agentreach/internal/runtime/docker"
)

// SnapshotTemplate retains only the fields already covered by the P0 snapshot.
// Command/environment are separately approved by the closed provisioning spec.
type SnapshotTemplate struct {
	User, Dir string
	Labels    map[string]string
	Mounts    []docker.Mount
	Host      any
}

func template(c docker.Container) SnapshotTemplate {
	var host any
	_ = json.Unmarshal(c.HostConfig, &host)
	mounts := slices.Clone(c.Mounts)
	slices.SortFunc(mounts, func(a, b docker.Mount) int { return strings.Compare(a.Destination, b.Destination) })
	return SnapshotTemplate{c.Config.User, c.Config.WorkingDir, c.Config.Labels, mounts, host}
}

// Digest uses exactly the existing snapshot wire shape and JSON semantics.
func (s SnapshotTemplate) Digest() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ForCreate approves a new exact snapshot without rewriting any old binding.
func (s SnapshotTemplate) ForCreate() (string, error) {
	return s.withOOMKillDefault(false)
}

// ForRunning covers Docker's other observed default after starting a workspace.
func (s SnapshotTemplate) ForRunning() (string, error) {
	return s.withOOMKillDefault(nil)
}

func (s SnapshotTemplate) withOOMKillDefault(value any) (string, error) {
	b, e := json.Marshal(s)
	if e != nil {
		return "", e
	}
	var copyTemplate SnapshotTemplate
	if e = json.Unmarshal(b, &copyTemplate); e != nil {
		return "", e
	}
	host, ok := copyTemplate.Host.(map[string]any)
	if !ok {
		return "", docker.Fail("state_corrupt", "invalid retained runtime template")
	}
	if v := host["OomKillDisable"]; v != nil && v != false {
		return "", docker.Fail("policy_denied", "unsupported OOM-kill configuration")
	}
	host["OomKillDisable"] = value
	return copyTemplate.Digest(), nil
}

// CaptureTemplate is a read-only recovery operation. It accepts only the
// approved image/mount profile and the supported non-secret JSON log settings.
func (g *Guard) CaptureTemplate(ctx context.Context, id string) (*SnapshotTemplate, error) {
	if !docker.FullID(id) {
		return nil, docker.Fail("policy_denied", "full template source ID required")
	}
	if e := g.CheckRecoveryBase(ctx); e != nil {
		return nil, e
	}
	c, e := g.Docker.Inspect(ctx, id)
	if e != nil {
		return nil, e
	}
	vs, e := g.safeConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	if c.ID != id || c.Image != g.Record.Binding.ImageID || serviceDigestVolumes(vs) != serviceDigestVolumes(g.Record.Binding.NamedVolumes) {
		return nil, docker.Fail("identity_changed", "template image/volume identity")
	}
	var h struct {
		LogConfig struct {
			Type   string
			Config map[string]string
		}
	}
	if e = json.Unmarshal(c.HostConfig, &h); e != nil {
		return nil, e
	}
	if h.LogConfig.Type != "json-file" || len(h.LogConfig.Config) != 2 || h.LogConfig.Config["max-file"] != "1" || h.LogConfig.Config["max-size"] == "" {
		return nil, docker.Fail("unsupported", "runtime template supports bounded json-file logging only")
	}
	t := template(c)
	for k, v := range t.Labels {
		if k != "agentreach.task" && k != "dev.agentreach.managed" && k != "dev.agentreach.project" || len(v) > 128 {
			return nil, docker.Fail("unsupported", "unrecognised runtime template labels")
		}
	}
	return &t, nil
}

func serviceDigestVolumes(v map[string]docker.Volume) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
