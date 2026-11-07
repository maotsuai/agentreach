package privatecontrol

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStrictPrivateRoundTrip(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	f := File{Root: root, Path: filepath.Join(root, "new", "state.json"), Max: 4096}
	var out struct {
		N uint64 `json:"n"`
	}
	if e := f.Read(&out); !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Dir(f.Path)); !os.IsNotExist(e) {
		t.Fatal("read created storage")
	}
	unlock, e := f.Lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	in := struct {
		N uint64 `json:"n"`
	}{^uint64(0)}
	if e = f.Save(in); e != nil {
		t.Fatal(e)
	}
	if e = f.Read(&out); e != nil || out != in {
		t.Fatal(out, e)
	}
	b, e := os.ReadFile(f.Path)
	if e != nil {
		t.Fatal(e)
	}
	for i := range b {
		if b[i] == 'f' {
			b[i] = 'e'
			break
		}
	}
	if e = os.WriteFile(f.Path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = f.Read(&out); e == nil {
		t.Fatal("corrupt record accepted")
	}
}
func TestRejectLinksAndPermissions(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if e := os.WriteFile(target, []byte("private"), 0600); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(root, "record")
	f := File{Root: root, Path: p, Max: 4096}
	if e := os.Symlink(target, p); e != nil {
		t.Fatal(e)
	}
	if e := f.Save(struct{}{}); e == nil {
		t.Fatal("symlink accepted")
	}
	_ = os.Remove(p)
	if e := os.Link(target, p); e != nil {
		t.Fatal(e)
	}
	if e := f.Save(struct{}{}); e == nil {
		t.Fatal("hardlink accepted")
	}
	_ = os.Remove(p)
	if e := os.WriteFile(p, []byte("{}"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := f.Save(struct{}{}); e == nil {
		t.Fatal("public record accepted")
	}
}
