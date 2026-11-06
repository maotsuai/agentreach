package rooted

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bojieli/agentreach/internal/reach"
)

func fixture(t *testing.T) (*FS, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	out := filepath.Join(base, "outside")
	for _, p := range []string{root, out} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	f, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, root, out
}
func TestRootedOperationsAndLinks(t *testing.T) {
	f, root, out := fixture(t)
	ctx := func(r Request, b []byte) error { _, _, e := f.Dispatch(r, b); return e }
	must := func(r Request, b []byte) {
		t.Helper()
		if e := ctx(r, b); e != nil {
			t.Fatalf("%+v: %v", r, e)
		}
	}
	must(Request{Op: "mkdir", Path: "子目录 with space"}, nil)
	must(Request{Op: "write", Path: "子目录 with space/one", Mode: 0640}, []byte("hello\nneedle\n"))
	b, e := f.read("子目录 with space/one", 6, 6)
	if e != nil || string(b) != "needle" {
		t.Fatalf("range %q %v", b, e)
	}
	must(Request{Op: "rename", From: "子目录 with space/one", To: "子目录 with space/two"}, nil)
	must(Request{Op: "copy", From: "子目录 with space", To: "copy", Recursive: true}, nil)
	h, _, e := f.Dispatch(Request{Op: "search", Path: ".", Search: &reach.SearchRequest{Pattern: "NEEDLE", IgnoreCase: true}}, nil)
	if e != nil || len(h["matches"].([]reach.Match)) != 2 {
		t.Fatalf("search %v %v", h, e)
	}
	h, _, e = f.Dispatch(Request{Op: "glob", Path: ".", Pattern: "two"}, nil)
	if e != nil || len(h["paths"].([]string)) != 2 {
		t.Fatalf("glob %v %v", h, e)
	}
	if e = os.WriteFile(filepath.Join(out, "canary"), []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink("../outside", filepath.Join(root, "escape")); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink("missing", filepath.Join(root, "dangling")); e != nil {
		t.Fatal(e)
	}
	for _, r := range []Request{{Op: "read", Path: "escape/canary"}, {Op: "write", Path: "escape/new"}, {Op: "stat", Path: "escape"}, {Op: "canonicalize", Path: "escape"}, {Op: "mkdir", Path: "escape/new"}, {Op: "rename", From: "copy/two", To: "escape/moved"}, {Op: "copy", From: "copy/two", To: "escape/new"}, {Op: "remove", Path: "."}, {Op: "rename", From: "copy", To: "."}, {Op: "read", Path: "dangling"}, {Op: "write", Path: "dangling"}, {Op: "read", Path: "../outside/canary"}} {
		if e = ctx(r, []byte("bad")); e == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	must(Request{Op: "remove", Path: "escape", Recursive: true}, nil)
	b, e = os.ReadFile(filepath.Join(out, "canary"))
	if e != nil || string(b) != "outside" {
		t.Fatal("remove followed symlink")
	}
	if e = os.Link(filepath.Join(out, "canary"), filepath.Join(root, "hardlink")); e != nil {
		t.Fatal(e)
	}
	for _, r := range []Request{{Op: "read", Path: "hardlink"}, {Op: "write", Path: "hardlink"}, {Op: "hash", Path: "hardlink"}, {Op: "copy", From: "hardlink", To: "new"}, {Op: "search", Path: "hardlink", Search: &reach.SearchRequest{Pattern: "outside"}}} {
		if e = ctx(r, []byte("bad")); e == nil {
			t.Fatalf("accepted multiply linked content: %+v", r)
		}
	}
	if e = os.Symlink("copy", filepath.Join(root, "internal")); e != nil {
		t.Fatal(e)
	}
	linkInfo, e := f.info("internal")
	if e != nil || linkInfo["is_dir"] != true {
		t.Fatalf("internal directory symlink metadata: %v %v", linkInfo, e)
	}
	s, e := f.canonical("internal/two")
	if e != nil || s != root+"/copy/two" {
		t.Fatalf("canonical %q %v", s, e)
	}
	must(Request{Op: "remove", Path: "copy", Recursive: true}, nil)
}
func TestRootedSymlinkSwapNoEscape(t *testing.T) {
	f, root, out := fixture(t)
	if e := os.Mkdir(root+"/safe", 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(out+"/canary", []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(root+"/safe/canary", []byte("inside"), 0600); e != nil {
		t.Fatal(e)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Symlink("safe", root+"/swap-new")
			_ = os.Rename(root+"/swap-new", root+"/swap")
			_ = os.Symlink("../outside", root+"/swap-new")
			_ = os.Rename(root+"/swap-new", root+"/swap")
		}
	}()
	for n := 0; n < 400; n++ {
		b, e := f.read("swap/canary", 0, 64)
		if e == nil && string(b) != "inside" {
			t.Fatalf("escaped read: %q", b)
		}
		_ = f.write("swap/new", []byte("inside"), 0600)
		_, _, _ = f.Dispatch(Request{Op: "rename", From: "swap/new", To: "swap/moved"}, nil)
	}
	close(stop)
	wg.Wait()
	for _, p := range []string{"new", "moved"} {
		if _, e := os.Stat(out + "/" + p); !os.IsNotExist(e) {
			t.Fatalf("escaped side effect %s: %v", p, e)
		}
	}
}
func frameFor(t *testing.T, r any, b []byte) []byte {
	t.Helper()
	h, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(len(h)))
	out = append(out, h...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(b)))
	return append(out, b...)
}
func response(t *testing.T, in io.Reader) map[string]any {
	t.Helper()
	var n uint32
	if e := binary.Read(in, binary.BigEndian, &n); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(in, b); e != nil {
		t.Fatal(e)
	}
	var h map[string]any
	if e := json.Unmarshal(b, &h); e != nil {
		t.Fatal(e)
	}
	if e := binary.Read(in, binary.BigEndian, &n); e != nil {
		t.Fatal(e)
	}
	if _, e := io.CopyN(io.Discard, in, int64(n)); e != nil {
		t.Fatal(e)
	}
	return h
}
func TestProtocolRequiresHandshakeAndFixedRoot(t *testing.T) {
	_, root, _ := fixture(t)
	input := append(frameFor(t, Request{ID: 1, Op: "write", Path: "denied"}, []byte("bad")), frameFor(t, Request{ID: 2, Op: "ping"}, nil)...)
	input = append(input, frameFor(t, Request{ID: 3, Op: "write", Path: "ok"}, []byte("yes"))...)
	var out bytes.Buffer
	e := Serve(root, "digest", bytes.NewReader(input), &out)
	if e != io.EOF {
		t.Fatal(e)
	}
	h := response(t, &out)
	if h["ok"] != false || !strings.Contains(h["error"].(string), "handshake") {
		t.Fatal(h)
	}
	h = response(t, &out)
	if h["capability"] != Capability || h["root"] != root || h["root_identity"] == "" || h["record_digest"] != "digest" {
		t.Fatal(h)
	}
	if h = response(t, &out); h["ok"] != true {
		t.Fatal(h)
	}
	if _, e = os.Stat(root + "/denied"); !os.IsNotExist(e) {
		t.Fatal("pre-handshake write")
	}
	if b, e := os.ReadFile(root + "/ok"); e != nil || string(b) != "yes" {
		t.Fatalf("write %q %v", b, e)
	}
	for _, raw := range []string{`{"id":1,"op":"ping","root":"/"}`, `{"id":1,"op":"ping","op":"write"}`} {
		var out bytes.Buffer
		h := binary.BigEndian.AppendUint32(nil, uint32(len(raw)))
		h = append(h, raw...)
		h = binary.BigEndian.AppendUint32(h, 0)
		if e = Serve(root, "digest", bytes.NewReader(h), &out); e == nil {
			t.Fatalf("accepted unknown/duplicate field %s", raw)
		}
	}
	over := binary.BigEndian.AppendUint32(nil, MaxHeader+1)
	if e = Serve(root, "digest", bytes.NewReader(over), io.Discard); e == nil {
		t.Fatal("oversized header accepted")
	}
}

func TestRootedContentLimitsUseActualReads(t *testing.T) {
	f, root, _ := fixture(t)
	h, e := os.Create(root + "/large")
	if e != nil {
		t.Fatal(e)
	}
	if e = h.Truncate(MaxFrame + 1); e != nil {
		t.Fatal(e)
	}
	_ = h.Close()
	for _, op := range []string{"read", "hash"} {
		if _, _, e = f.Dispatch(Request{Op: op, Path: "large"}, nil); e == nil {
			t.Fatalf("%s accepted over-limit content", op)
		}
	}
	// Search checks bytes actually consumed, including files whose size changes after stat.
	// Many short lines exceed the total scan budget without triggering the independent line limit.
	h, e = os.Create(root + "/search")
	if e != nil {
		t.Fatal(e)
	}
	chunk := bytes.Repeat([]byte("a\n"), 1<<19)
	for n := 0; n < 65; n++ {
		if _, e = h.Write(chunk); e != nil {
			t.Fatal(e)
		}
	}
	_ = h.Close()
	if _, _, e = f.Dispatch(Request{Op: "search", Path: "search", Search: &reach.SearchRequest{Pattern: "never-match"}}, nil); e == nil || !strings.Contains(e.Error(), "byte limit") {
		t.Fatalf("search did not enforce actual scan bound: %v", e)
	}
}

func TestTraversalRootSymlinkIsResolvedInsideRoot(t *testing.T) {
	f, root, _ := fixture(t)
	if e := os.Mkdir(root+"/dir", 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(root+"/dir/file", []byte("needle"), 0600); e != nil {
		t.Fatal(e)
	}
	for name, target := range map[string]string{"internal": "dir", "escape": "../outside"} {
		if e := os.Symlink(target, root+"/"+name); e != nil {
			t.Fatal(e)
		}
	}
	for _, op := range []string{"search", "glob"} {
		r := Request{Op: op, Path: "internal", Pattern: "*", Search: &reach.SearchRequest{Pattern: "needle"}}
		h, _, e := f.Dispatch(r, nil)
		if e != nil {
			t.Fatalf("internal %s: %v", op, e)
		}
		if op == "search" && len(h["matches"].([]reach.Match)) != 1 {
			t.Fatal(h)
		}
		if op == "glob" && len(h["paths"].([]string)) != 1 {
			t.Fatal(h)
		}
		r.Path = "escape"
		if _, _, e = f.Dispatch(r, nil); e == nil {
			t.Fatalf("%s accepted outside traversal root", op)
		}
	}
}
