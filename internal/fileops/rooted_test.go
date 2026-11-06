package fileops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/rooted"
	"github.com/bojieli/agentreach/internal/transport"
	"github.com/bojieli/agentreach/internal/workspace"
)

type rootTestTransport struct {
	root, digest, mode string
	mu                 sync.Mutex
	ops                []string
	opens              int
}

func (t *rootTestTransport) Run(context.Context, reach.ExecRequest) (reach.ExecResult, error) {
	return reach.ExecResult{}, fmt.Errorf("test: unexpected shell fallback")
}
func (t *rootTestTransport) Describe() string { return "rooted test transport" }
func (t *rootTestTransport) Close() error     { return nil }
func (t *rootTestTransport) Open(_ context.Context, command string) (transport.Stream, error) {
	want := "exec " + transport.ShellQuote(rooted.HelperPath) + " serve-rooted " + transport.ShellQuote(t.root) + " " + transport.ShellQuote(t.digest)
	if command != want {
		return transport.Stream{}, fmt.Errorf("unexpected command: %s", command)
	}
	t.mu.Lock()
	t.opens++
	t.mu.Unlock()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		var e error
		if t.mode == "legacy" {
			in := bufio.NewReader(inR)
			var n uint32
			_ = binary.Read(in, binary.BigEndian, &n)
			b := make([]byte, n)
			_, _ = io.ReadFull(in, b)
			_ = binary.Read(in, binary.BigEndian, &n)
			var h map[string]any
			_ = json.Unmarshal(b, &h)
			reply, _ := json.Marshal(map[string]any{"id": h["id"], "ok": true, "version": reach.Version, "helper": true})
			_ = binary.Write(outW, binary.BigEndian, uint32(len(reply)))
			_, _ = outW.Write(reply)
			_ = binary.Write(outW, binary.BigEndian, uint32(0))
		} else {
			e = rooted.Serve(t.root, t.digest, io.TeeReader(inR, &requestRecorder{tr: t}), outW)
		}
		_ = outW.Close()
		_ = inR.Close()
		done <- e
	}()
	closeFn := func() error { _ = inW.Close(); _ = outR.Close(); return nil }
	return transport.Stream{Stdin: inW, Stdout: outR, Stderr: strings.NewReader(""), Close: closeFn, Wait: func() (int, error) { return 0, <-done }}, nil
}

type requestRecorder struct {
	tr  *rootTestTransport
	buf []byte
}

func (r *requestRecorder) Write(b []byte) (int, error) {
	r.buf = append(r.buf, b...)
	for len(r.buf) >= 4 {
		n := int(binary.BigEndian.Uint32(r.buf))
		if len(r.buf) < n+8 {
			break
		}
		payload := int(binary.BigEndian.Uint32(r.buf[4+n:]))
		if len(r.buf) < n+8+payload {
			break
		}
		var h map[string]any
		_ = json.Unmarshal(r.buf[4:4+n], &h)
		op, _ := h["op"].(string)
		r.tr.mu.Lock()
		r.tr.ops = append(r.tr.ops, op)
		r.tr.mu.Unlock()
		r.buf = r.buf[n+8+payload:]
	}
	return len(b), nil
}
func rootedFixture(t *testing.T) (*rootTestTransport, workspace.Mapper, *RootBinding) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root with space")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	return &rootTestTransport{root: root, digest: "record-digest"}, workspace.Mapper{Root: root, HostAliases: []string{"/approved/project"}}, &RootBinding{}
}
func TestRootedClientAllOperationsAndNoFallback(t *testing.T) {
	tr, m, b := rootedFixture(t)
	ops, e := NewRooted(context.Background(), tr, m, workspace.Protocol, tr.digest, b)
	if e != nil {
		t.Fatal(e)
	}
	defer ops.Close()
	c := context.Background()
	if e = ops.Mkdir(c, "子 dir", 0700); e != nil {
		t.Fatal(e)
	}
	if e = ops.Write(c, "/approved/project/子 dir/one", []byte("needle"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = ops.Rename(c, "子 dir/one", "子 dir/two"); e != nil {
		t.Fatal(e)
	}
	if e = Copy(c, ops, "子 dir", "copy", true); e != nil {
		t.Fatal(e)
	}
	if data, e := ops.Read(c, "copy/two", 0, 0); e != nil || string(data) != "needle" {
		t.Fatalf("read %q %v", data, e)
	}
	if _, e = ops.Stat(c, "copy/two"); e != nil {
		t.Fatal(e)
	}
	if _, e = ops.List(c, "copy"); e != nil {
		t.Fatal(e)
	}
	if _, e = ops.Hash(c, "copy/two"); e != nil {
		t.Fatal(e)
	}
	if _, e = Canonicalize(c, ops, "copy/two"); e != nil {
		t.Fatal(e)
	}
	if v, e := ops.Search(c, reach.SearchRequest{Root: "copy", Pattern: "needle"}); e != nil || len(v) != 1 {
		t.Fatalf("search %v %v", v, e)
	}
	if v, e := ops.Glob(c, "copy", "*"); e != nil || len(v) < 1 {
		t.Fatalf("glob %v %v", v, e)
	}
	before := len(tr.ops)
	for _, fn := range []func() error{func() error { return ops.Write(c, "/etc/out", nil, 0600) }, func() error { return ops.Rename(c, "copy/two", "../outside") }, func() error { return Copy(c, ops, "copy/two", "/etc/out", false) }, func() error { return ops.Remove(c, ".", true) }} {
		if e = fn(); e == nil {
			t.Fatal("outside request accepted")
		}
	}
	if len(tr.ops) != before {
		t.Fatal("denied request dispatched")
	}
	if e = ops.Remove(c, "copy", true); e != nil {
		t.Fatal(e)
	}
}
func TestRootedClientRejectsLegacyBeforeAnyFileRequest(t *testing.T) {
	tr, m, b := rootedFixture(t)
	tr.mode = "legacy"
	if _, e := NewRooted(context.Background(), tr, m, workspace.Protocol, tr.digest, b); e == nil || !strings.Contains(e.Error(), "unsupported") {
		t.Fatalf("legacy helper: %v", e)
	}
	if tr.opens != 1 {
		t.Fatal("fallback/reinstall")
	}
}
func TestRootedReopenAndPoolBindRootIdentity(t *testing.T) {
	for _, kind := range []string{"reopen", "pool"} {
		t.Run(kind, func(t *testing.T) {
			tr, m, b := rootedFixture(t)
			c := context.Background()
			ops, e := NewRooted(c, tr, m, workspace.Protocol, tr.digest, b)
			if e != nil {
				t.Fatal(e)
			}
			defer ops.Close()
			if e = ops.Write(c, "seed", []byte("old"), 0600); e != nil {
				t.Fatal(e)
			}
			if e = os.Rename(tr.root, tr.root+"-old"); e != nil {
				t.Fatal(e)
			}
			if e = os.Mkdir(tr.root, 0700); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(tr.root+"/seed", []byte("new"), 0600); e != nil {
				t.Fatal(e)
			}
			before := len(tr.ops)
			if kind == "reopen" {
				p := ops.(*rootedOps)
				p.closeStream()
				p.mu.Lock()
				p.broken = fmt.Errorf("test disconnected")
				p.mu.Unlock()
				e = ops.Write(c, "seed", []byte("bad"), 0600)
			} else {
				_, e = NewRooted(c, tr, m, workspace.Protocol, tr.digest, b)
			}
			if e == nil || !strings.Contains(e.Error(), "identity_changed") {
				t.Fatalf("root was rebound: %v", e)
			}
			for _, op := range tr.ops[before:] {
				if op != "ping" {
					t.Fatalf("file op before root identity handshake: %s", op)
				}
			}
			if _, e = ops.Read(c, "seed", 0, 0); e == nil {
				t.Fatal("old member revived after pool mismatch")
			}
			data, e := os.ReadFile(tr.root + "/seed")
			if e != nil || !bytes.Equal(data, []byte("new")) {
				t.Fatal("rejected write touched replacement root")
			}
		})
	}
}
func TestRootedGuardRetiresWarmOps(t *testing.T) {
	tr, m, b := rootedFixture(t)
	ops, e := NewRooted(context.Background(), tr, m, workspace.Protocol, tr.digest, b)
	if e != nil {
		t.Fatal(e)
	}
	ready := true
	g := Guard(ops, func(context.Context) error {
		if !ready {
			return fmt.Errorf("storage_unavailable")
		}
		return nil
	})
	c := context.Background()
	if e = g.Write(c, "seed", []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	ready = false
	if _, e = g.Read(c, "seed", 0, 0); e == nil {
		t.Fatal("failed guard allowed warm read")
	}
	ready = true
	before := len(tr.ops)
	if e = g.Write(c, "seed", []byte("bad"), 0600); e == nil {
		t.Fatal("closed ops reopened")
	}
	if len(tr.ops) != before {
		t.Fatal("closed ops dispatched")
	}
	_ = g.Close()
}

func TestRootedHandshakeAllBindingFieldsRequired(t *testing.T) {
	for _, field := range []string{"capability", "protocol", "root", "record_digest", "version", "root_identity"} {
		h := map[string]any{"capability": rooted.Capability, "protocol": float64(rooted.Version), "root": "/workspace/project", "record_digest": "digest", "version": reach.Version, "root_identity": "device:inode"}
		delete(h, field)
		c := rootConfig{root: "/workspace/project", digest: "digest", binding: &RootBinding{}}
		if e := c.validate(h); e == nil {
			t.Fatalf("accepted handshake missing %s", field)
		}
	}
}

func TestRootedTrustedNamespacePreventsRepeatedMapping(t *testing.T) {
	for _, direction := range []string{"host-parent", "host-child"} {
		t.Run(direction, func(t *testing.T) {
			tr, m, b := rootedFixture(t)
			alias := filepath.Dir(m.Root)
			suffix := "note"
			wrong := m.Root + "/" + filepath.Base(m.Root) + "/note"
			if direction == "host-child" {
				alias = m.Root + "/host"
				suffix = "host/note"
				wrong = m.Root + "/note"
			}
			m.HostAliases = []string{alias}
			intended := m.Root + "/" + suffix
			for p, value := range map[string]string{intended: "intended-old", wrong: "unrelated-old"} {
				if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(p, []byte(value), 0600); e != nil {
					t.Fatal(e)
				}
			}

			if _, e := NewRooted(context.Background(), tr, m, workspace.Protocol, tr.digest, b); e == nil || !strings.Contains(e.Error(), "path_denied") {
				t.Fatalf("ambiguous Protocol constructor: %v", e)
			}
			if tr.opens != 0 {
				t.Fatal("rejected Protocol constructor opened a helper")
			}
			data, e := os.ReadFile(intended)
			if e != nil || string(data) != "intended-old" {
				t.Fatalf("rejection changed intended path: %q %v", data, e)
			}
			guest, e := NewRooted(context.Background(), tr, m, workspace.Guest, tr.digest, b)
			if e != nil {
				t.Fatal(e)
			}
			defer guest.Close()

			// This lower-level constructor has only a fixed Guest namespace,
			// not the rejected Protocol capability. Keep the B1 regression:
			// an already-authorised Guest path cannot be interpreted again.
			if e = guest.Write(context.Background(), intended, []byte("rpc-mapped"), 0600); e != nil {
				t.Fatal(e)
			}
			data, e = os.ReadFile(intended)
			if e != nil || string(data) != "rpc-mapped" {
				t.Fatalf("mapped Guest destination changed: %q %v", data, e)
			}
			data, e = os.ReadFile(wrong)
			if e != nil || string(data) != "unrelated-old" {
				t.Fatalf("double mapping touched unrelated path: %q %v", data, e)
			}
		})
	}
}
func TestRootedRequiresTrustedNamespaceBeforeOpening(t *testing.T) {
	tr, m, b := rootedFixture(t)
	for _, ns := range []workspace.Namespace{"", workspace.FileURI, "model-approved"} {
		if _, e := NewRooted(context.Background(), tr, m, ns, tr.digest, b); e == nil {
			t.Fatalf("accepted invalid construction namespace %q", ns)
		}
	}
	if tr.opens != 0 {
		t.Fatal("invalid namespace reached helper")
	}
}
