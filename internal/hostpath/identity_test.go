package hostpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityAncestryMissingPathsAndHardlinks(t *testing.T) {
	root := canonicalTempDir(t)
	dir := filepath.Join(root, "source")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "hardlink")
	if err := os.Link(file, alias); err != nil {
		t.Skipf("filesystem lacks hardlinks: %v", err)
	}
	for _, pair := range [][2]string{{dir, file}, {dir, filepath.Join(dir, "missing", "control")}, {file, alias}, {alias, file}} {
		if yes, err := Overlaps(pair[0], pair[1]); err != nil || !yes {
			t.Fatalf("missed overlap %q: %v", pair, err)
		}
	}
	for _, pair := range [][2]string{{dir, filepath.Join(root, "other")}, {file, filepath.Join(dir, "file-other")}} {
		if yes, err := Overlaps(pair[0], pair[1]); err != nil || yes {
			t.Fatalf("false overlap %q: %v", pair, err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := Canonical(filepath.Join(link, "not-created"), true); err == nil {
		t.Fatal("accepted symlink ancestor")
	}
	if err := Canonical(filepath.Join(root, "not-created", "control"), true); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCaseAliasIdentity(t *testing.T) {
	root := os.Getenv("REACH_TEST_CASE_ALIAS_ROOT")
	if root == "" {
		t.Skip("requires an explicit native case-insensitive external directory")
	}
	upper := filepath.Join(filepath.Dir(root), strings.ToUpper(filepath.Base(root)))
	a, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(upper)
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("test volume is not case insensitive: %v", err)
	}
	for _, pair := range [][2]string{{root, upper}, {root, filepath.Join(upper, "missing-control")}, {upper, filepath.Join(root, "missing-control")}} {
		if yes, err := Overlaps(pair[0], pair[1]); err != nil || !yes {
			t.Fatalf("case alias bypass %q: %v", pair, err)
		}
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestCanonicalMissingSuffixMayAppearOrDisappearConcurrently(t *testing.T) {
	root := canonicalTempDir(t)
	p := filepath.Join(root, "transient.lock")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if e := os.Mkdir(p, 0700); e != nil {
				t.Error(e)
				return
			}
			if e := os.Remove(p); e != nil {
				t.Error(e)
				return
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for range 2000 {
		if e := Canonical(p, true); e != nil {
			t.Fatal(e)
		}
	}
}
