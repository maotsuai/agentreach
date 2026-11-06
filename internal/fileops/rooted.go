package fileops

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"sync"

	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/rooted"
	"github.com/bojieli/agentreach/internal/transport"
	"github.com/bojieli/agentreach/internal/workspace"
)

// RootBinding is shared by all helper members of one managed connection.
// Reopening or growing the pool may not silently acquire another directory inode.
type RootBinding struct {
	mu           sync.Mutex
	identity     string
	root, digest string
	failure      error
}

func (b *RootBinding) check(root, digest, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	if id == "" {
		b.failure = fmt.Errorf("unsupported: helper returned no root identity")
	} else if b.identity == "" {
		b.identity = id
		b.root = root
		b.digest = digest
	} else if id != b.identity || root != b.root || digest != b.digest {
		b.failure = fmt.Errorf("identity_changed: helper workspace root was replaced")
	}
	return b.failure
}
func (b *RootBinding) ready() error { b.mu.Lock(); defer b.mu.Unlock(); return b.failure }

// Identity returns the negotiated root instance or its permanent failure.
func (b *RootBinding) Identity() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.identity, b.failure
}

type rootConfig struct {
	root, digest string
	binding      *RootBinding
}

func (c *rootConfig) validate(h map[string]any) error {
	if h["capability"] != rooted.Capability || h["protocol"] != float64(rooted.Version) || h["root"] != c.root || h["record_digest"] != c.digest || h["version"] != reach.Version {
		return fmt.Errorf("unsupported: managed operations require reach-helper %s with %s at %s; prepare it explicitly in the Guest", reach.Version, rooted.Capability, rooted.HelperPath)
	}
	id, _ := h["root_identity"].(string)
	return c.binding.check(c.root, c.digest, id)
}

// NewRooted takes its path namespace from the trusted entrypoint. It never builds,
// installs, probes another tier or falls back to a shell.
func NewRooted(ctx context.Context, t transport.Transport, m workspace.Mapper, namespace workspace.Namespace, digest string, binding *RootBinding, effects ...Effect) (FileOps, error) {
	if namespace != workspace.Guest && namespace != workspace.Protocol {
		return nil, fmt.Errorf("path_denied: rooted FileOps requires an explicit trusted path namespace")
	}
	if namespace == workspace.Protocol {
		if e := m.ValidateProtocolAliases(); e != nil {
			return nil, e
		}
	} else {
		m.HostAliases = nil
	}
	cmd := "exec " + transport.ShellQuote(rooted.HelperPath) + " serve-rooted " + transport.ShellQuote(m.Root) + " " + transport.ShellQuote(digest)
	ops, e := startHandler(ctx, t, nil, reach.TierHelper, "managed rooted helper", cmd, "")
	if e != nil {
		return nil, fmt.Errorf("unsupported: %w", e)
	}
	p, ok := ops.(*handlerOps)
	if !ok {
		_ = ops.Close()
		return nil, fmt.Errorf("unsupported: unexpected rooted handler")
	}
	p.rooted = &rootConfig{m.Root, digest, binding}
	if _, _, e = p.roundTrip(ctx, map[string]any{"op": "ping"}, nil); e != nil {
		_ = p.Close()
		return nil, fmt.Errorf("unsupported: managed rooted helper unavailable; explicitly prepare %s: %w", rooted.HelperPath, e)
	}
	r := &rootedOps{handlerOps: p, mapper: m, namespace: namespace}
	if len(effects) > 0 {
		r.effect = effects[0]
	}
	return r, nil
}

// Effect commits authorization/intent before the supplied Guest mutation.
type Effect func(context.Context, string, any, func() error) error

type rootedOps struct {
	*handlerOps
	mapper    workspace.Mapper
	namespace workspace.Namespace
	effect    Effect
}

func (p *rootedOps) change(c context.Context, op string, payload any, fn func() error) error {
	if p.effect != nil {
		return p.effect(c, op, payload, fn)
	}
	return fn()
}

func (p *rootedOps) paths(op, a, b string) (workspace.Result, error) {
	if e := p.rooted.binding.ready(); e != nil {
		return workspace.Result{}, e
	}
	return p.mapper.Map(workspace.PathRequest{Operation: op, Namespace: p.namespace, Path: a, SecondPath: b})
}
func (p *rootedOps) Read(c context.Context, a string, o, n int64) ([]byte, error) {
	r, e := p.paths("read", a, "")
	if e != nil {
		return nil, e
	}
	if o < 0 || n < 0 || n > rooted.MaxFrame {
		return nil, fmt.Errorf("invalid managed read bounds")
	}
	want := n
	if want == 0 {
		want = rooted.MaxFrame + 1
	}
	b, e := p.handlerOps.Read(c, r.Relative, o, want)
	if len(b) > rooted.MaxFrame {
		return nil, fmt.Errorf("managed read exceeds content limit")
	}
	return b, e
}
func (p *rootedOps) Write(c context.Context, a string, b []byte, m fs.FileMode) error {
	r, e := p.paths("write", a, "")
	if e != nil {
		return e
	}
	return p.change(c, "write", []any{r.Guest, b, m}, func() error { return p.handlerOps.Write(c, r.Relative, b, m) })
}
func (p *rootedOps) Stat(c context.Context, a string) (*reach.FileInfo, error) {
	r, e := p.paths("stat", a, "")
	if e != nil {
		return nil, e
	}
	return p.handlerOps.Stat(c, r.Relative)
}
func (p *rootedOps) List(c context.Context, a string) ([]reach.FileInfo, error) {
	r, e := p.paths("list", a, "")
	if e != nil {
		return nil, e
	}
	return p.handlerOps.List(c, r.Relative)
}
func (p *rootedOps) Mkdir(c context.Context, a string, m fs.FileMode) error {
	r, e := p.paths("mkdir", a, "")
	if e != nil {
		return e
	}
	return p.change(c, "mkdir", []any{r.Guest, m}, func() error { return p.handlerOps.Mkdir(c, r.Relative, m) })
}
func (p *rootedOps) Remove(c context.Context, a string, b bool) error {
	r, e := p.paths("remove", a, "")
	if e != nil {
		return e
	}
	return p.change(c, "remove", []any{r.Guest, b}, func() error { return p.handlerOps.Remove(c, r.Relative, b) })
}
func (p *rootedOps) Rename(c context.Context, a, b string) error {
	r, e := p.paths("rename", a, b)
	if e != nil {
		return e
	}
	return p.change(c, "rename", []any{r.Guest, r.SecondGuest}, func() error { return p.handlerOps.Rename(c, r.Relative, r.SecondRelative) })
}
func (p *rootedOps) Hash(c context.Context, a string) (string, error) {
	r, e := p.paths("hash", a, "")
	if e != nil {
		return "", e
	}
	return p.handlerOps.Hash(c, r.Relative)
}
func decodeField(h map[string]any, key string, out any) error {
	b, e := json.Marshal(h[key])
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func (p *rootedOps) Search(c context.Context, q reach.SearchRequest) ([]reach.Match, error) {
	r, e := p.paths("search", q.Root, "")
	if e != nil {
		return nil, e
	}
	q.Root = ""
	h, _, e := p.roundTrip(c, map[string]any{"op": "search", "path": r.Relative, "search": q}, nil)
	if e != nil {
		return nil, e
	}
	var out []reach.Match
	e = decodeField(h, "matches", &out)
	return out, e
}
func (p *rootedOps) Glob(c context.Context, a, b string) ([]string, error) {
	r, e := p.paths("glob", a, "")
	if e != nil {
		return nil, e
	}
	h, _, e := p.roundTrip(c, map[string]any{"op": "glob", "path": r.Relative, "pattern": b}, nil)
	if e != nil {
		return nil, e
	}
	var out []string
	e = decodeField(h, "paths", &out)
	return out, e
}
func (p *rootedOps) Canonicalize(c context.Context, a string) (string, error) {
	r, e := p.paths("canonicalize", a, "")
	if e != nil {
		return "", e
	}
	h, _, e := p.roundTrip(c, map[string]any{"op": "canonicalize", "path": r.Relative}, nil)
	if e != nil {
		return "", e
	}
	s, _ := h["path"].(string)
	v, e := p.mapper.Map(workspace.PathRequest{Operation: "canonicalize", Namespace: workspace.Guest, Path: s})
	return v.Guest, e
}
func (p *rootedOps) Copy(c context.Context, a, b string, recursive bool) error {
	r, e := p.paths("copy", a, b)
	if e != nil {
		return e
	}
	return p.change(c, "copy", []any{r.Guest, r.SecondGuest, recursive}, func() error {
		_, _, e = p.roundTrip(c, map[string]any{"op": "copy", "from": r.Relative, "to": r.SecondRelative, "recursive": recursive}, nil)
		return e
	})
}

// ExtendedOps covers operations which legacy FileOps implements through shell tools.
type ExtendedOps interface {
	Canonicalize(context.Context, string) (string, error)
	Copy(context.Context, string, string, bool) error
}

// Canonicalize uses the rooted protocol without a shell realpath escape.
func Canonicalize(c context.Context, ops FileOps, p string) (string, error) {
	if x, ok := ops.(ExtendedOps); ok {
		return x.Canonicalize(c, p)
	}
	return "", fmt.Errorf("unsupported: canonicalize requires rooted file operations")
}

// Copy keeps both endpoints inside the rooted helper.
func Copy(c context.Context, ops FileOps, a, b string, r bool) error {
	if x, ok := ops.(ExtendedOps); ok {
		return x.Copy(c, a, b, r)
	}
	return fmt.Errorf("unsupported: copy requires rooted file operations")
}
func (g *guardedOps) Canonicalize(c context.Context, p string) (string, error) {
	if e := g.ready(c); e != nil {
		return "", e
	}
	return Canonicalize(c, g.FileOps, p)
}
func (g *guardedOps) Copy(c context.Context, a, b string, r bool) error {
	if e := g.ready(c); e != nil {
		return e
	}
	return Copy(c, g.FileOps, a, b, r)
}
func (p *poolOps) Canonicalize(c context.Context, a string) (string, error) {
	o, e := p.get(c)
	if e != nil {
		return "", e
	}
	defer p.put(o)
	return Canonicalize(c, o, a)
}
func (p *poolOps) Copy(c context.Context, a, b string, r bool) error {
	o, e := p.get(c)
	if e != nil {
		return e
	}
	defer p.put(o)
	return Copy(c, o, a, b, r)
}
