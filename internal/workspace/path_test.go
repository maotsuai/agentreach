package workspace

import (
	"strings"
	"testing"
)

func TestManagedPathMatrix(t *testing.T) {
	m := Mapper{Root: "/workspace/project", HostAliases: []string{"/Volumes/work/project", "/var/control", "/private/var/control"}}
	tests := []struct {
		name, p string
		ns      Namespace
		want    string
		bad     bool
	}{
		{"relative", "子目录/with space.txt", Guest, "子目录/with space.txt", false}, {"root", ".", Guest, ".", false}, {"guest", "/workspace/project/a/../b", Guest, "b", false}, {"shared alias", "/Volumes/work/project/sub/a", Protocol, "sub/a", false}, {"raw control", "/var/control/a", FileURI, "", true}, {"canonical control", "file:///private/var/control/a%20b", FileURI, "a b", false}, {"single decode", "file:///workspace/project/%252e%252e/literal", FileURI, "%2e%2e/literal", false},
		{"traversal", "../outside", Guest, "", true}, {"absolute escape", "/etc/passwd", Guest, "", true}, {"prefix collision", "/workspace/project-other/f", Guest, "", true}, {"host collision", "/Volumes/work/project-other/f", Protocol, "", true}, {"alias escape", "/var/control/../outside", Protocol, "", true}, {"escape return", "/workspace/project/../project/f", Guest, "", true}, {"authority", "file://localhost/workspace/project/f", FileURI, "", true}, {"remote", "file://server/workspace/project/f", FileURI, "", true}, {"query", "file:///workspace/project/f?x", FileURI, "", true}, {"empty query", "file:///workspace/project/f?", FileURI, "", true}, {"fragment", "file:///workspace/project/f#", FileURI, "", true}, {"bad escape", "file:///workspace/project/%XX", FileURI, "", true}, {"nul", "file:///workspace/project/%00", FileURI, "", true}, {"encoding", "file:///workspace/project/%ff", FileURI, "", true}, {"long", strings.Repeat("a", MaxPath+1), Guest, "", true}, {"empty", "", Guest, "", true}, {"unknown ns", "x", Namespace("trusted"), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, e := m.Map(PathRequest{Operation: "read", Namespace: tt.ns, Path: tt.p})
			if (e != nil) != tt.bad || e == nil && r.Relative != tt.want {
				t.Fatalf("result=%+v error=%v", r, e)
			}
		})
	}
	// Guest-owned never gains a Host project alias.
	if _, e := (Mapper{Root: m.Root}).Map(PathRequest{Operation: "read", Namespace: Protocol, Path: "/Volumes/work/project/a"}); e == nil {
		t.Fatal("invented guest-owned mirror")
	}
	for _, r := range []PathRequest{{Operation: "rename", Namespace: Guest, Path: "a", SecondPath: "../outside"}, {Operation: "copy", Namespace: Guest, Path: "a", SecondPath: "/etc/passwd"}, {Operation: "remove", Namespace: Guest, Path: "."}, {Operation: "rename", Namespace: Guest, Path: "a", SecondPath: "."}, {Operation: "read", Namespace: Guest, Path: "a", Cwd: "/tmp"}} {
		if _, e := m.Map(r); e == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}

func TestCwdRejectsLineBreaksWithoutRestrictingFileNames(t *testing.T) {
	m := Mapper{Root: "/workspace/project"}
	for _, p := range []string{"dir\nname", "dir\rname"} {
		if _, e := m.Map(PathRequest{Operation: "cwd", Namespace: Guest, Path: p}); e == nil {
			t.Fatal("cwd line break accepted")
		}
		if _, e := m.Map(PathRequest{Operation: "write", Namespace: Guest, Path: p}); e != nil {
			t.Fatal("file line break rejected")
		}
	}
}

func TestCwdLineBreakValidationAfterURIDecode(t *testing.T) {
	m := Mapper{Root: "/workspace/project"}
	for _, r := range []PathRequest{{Operation: "cwd", Namespace: FileURI, Path: "file:///workspace/project/dir%0Aname"}, {Operation: "read", Namespace: FileURI, Path: "file:///workspace/project/a", Cwd: "file:///workspace/project/dir%0Dname"}} {
		if _, e := m.Map(r); e == nil {
			t.Fatalf("accepted cwd: %+v", r)
		}
	}
}

func TestProtocolAliasContract(t *testing.T) {
	for _, tt := range []struct {
		name, root, alias string
		bad               bool
	}{{"host-parent", "/A/guest", "/A", true}, {"host-child", "/A", "/A/host", true}, {"equal", "/A", "/A", false}, {"sibling", "/A", "/A-other", false}, {"reverse sibling", "/A-other", "/A", false}, {"disjoint", "/guest/project", "/host/project", false}, {"root alias", "/guest/project", "/", true}} {
		t.Run(tt.name, func(t *testing.T) {
			m := Mapper{Root: tt.root, HostAliases: []string{tt.alias}}
			e := m.ValidateProtocolAliases()
			if (e != nil) != tt.bad {
				t.Fatalf("validation=%v", e)
			}
			_, e = m.Map(PathRequest{Operation: "write", Namespace: Protocol, Path: tt.alias + "/note"})
			if (e != nil) != tt.bad {
				t.Fatalf("mapping=%v", e)
			}
			if !tt.bad {
				first, e := m.Map(PathRequest{Operation: "canonicalize", Namespace: Protocol, Path: tt.alias + "/note"})
				if e != nil {
					t.Fatal(e)
				}
				again, e := m.Map(PathRequest{Operation: "read", Namespace: FileURI, Path: "file://" + first.Guest})
				if e != nil || again.Guest != first.Guest {
					t.Fatalf("Guest URI unstable: %+v %v", again, e)
				}
			}
		})
	}
}
