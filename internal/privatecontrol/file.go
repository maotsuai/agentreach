// Package privatecontrol stores bounded private Host resource records. It is
// separate from the authoritative policy ledger and never repairs corrupt data.
package privatecontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/state"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File bounds and validates one private Host record.
type File struct {
	Root, Path string
	Max        int
	Check      func() error
	Write      func(string, any) error
}
type envelope struct {
	Schema   int             `json:"schema"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Dir validates a private canonical directory, allowing a missing suffix.
func Dir(p string) error {
	if e := hostpath.Canonical(p, true); e != nil {
		return e
	}
	fi, e := os.Lstat(p)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !fi.IsDir() || !Private(fi) {
		return fmt.Errorf("state_corrupt: unsafe private directory %s", p)
	}
	return nil
}

// Safe checks ownership, links and all private directory components.
func (f File) Safe() error {
	if f.Check != nil {
		if e := f.Check(); e != nil {
			return e
		}
	}
	rel, e := filepath.Rel(f.Root, f.Path)
	if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !filepath.IsAbs(f.Root) || !filepath.IsAbs(f.Path) || filepath.Clean(f.Root) != f.Root || filepath.Clean(f.Path) != f.Path {
		return fmt.Errorf("policy_denied: private record root")
	}
	for p := filepath.Dir(f.Path); ; p = filepath.Dir(p) {
		if e = Dir(p); e != nil {
			return e
		}
		if p == f.Root {
			break
		}
	}
	if e = hostpath.Canonical(f.Path, true); e != nil {
		return e
	}
	if e = hostpath.PrivateFile(f.Path); e != nil {
		return e
	}
	fi, e := os.Lstat(f.Path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !Private(fi) {
		return fmt.Errorf("state_corrupt: private record permissions")
	}
	return nil
}
func (f File) Read(v any) error {
	if e := f.Safe(); e != nil {
		return e
	}
	h, e := os.Open(f.Path)
	if e != nil {
		return e
	}
	defer func() { _ = h.Close() }()
	fi, e := h.Stat()
	if e != nil {
		return e
	}
	now, e := os.Lstat(f.Path)
	if e != nil || !os.SameFile(fi, now) || !Private(fi) || fi.Size() > int64(f.Max) {
		return fmt.Errorf("state_corrupt: private record identity/budget")
	}
	b, e := io.ReadAll(io.LimitReader(h, int64(f.Max)+1))
	if e != nil {
		return e
	}
	if len(b) > f.Max {
		return fmt.Errorf("request_budget: private record")
	}
	var env envelope
	if e = state.Decode(b, &env); e != nil {
		return e
	}
	// Marshal canonicalises indentation added by state.Write, preserving the
	// checksum across encoders without accepting duplicate/unknown keys.
	var compact any
	d := json.NewDecoder(bytes.NewReader(env.Payload))
	d.UseNumber()
	if e = d.Decode(&compact); e != nil {
		return e
	}
	canonical, _ := json.Marshal(compact)
	if env.Schema != 1 || env.Checksum != sum(canonical) {
		return fmt.Errorf("state_corrupt: private record schema/checksum")
	}
	if e = state.Decode(env.Payload, v); e != nil {
		return e
	}
	return f.Safe()
}

// Save atomically replaces a bounded checksummed envelope.
func (f File) Save(v any) error {
	if e := f.Safe(); e != nil {
		return e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	var raw any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(&raw); e != nil {
		return e
	}
	b, e = json.Marshal(raw)
	if e != nil {
		return e
	}
	env := envelope{1, b, sum(b)}
	encoded, e := json.MarshalIndent(env, "", "  ")
	if e != nil {
		return e
	}
	if len(encoded) > f.Max {
		return fmt.Errorf("request_budget: private record exhausted")
	}
	write := f.Write
	if write == nil {
		write = state.Write
	}
	return write(f.Path, env)
}

// Lock serialises record writers with a bounded, non-stealing directory lock.
func (f File) Lock(ctx context.Context) (func(), error) {
	if e := f.Safe(); e != nil {
		return nil, e
	}
	if e := hostpath.Canonical(f.Path+".lock", true); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(filepath.Dir(f.Path), 0700); e != nil {
		return nil, e
	}
	if e := f.Safe(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return state.Lock(ctx, f.Path+".lock")
}
