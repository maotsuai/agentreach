package orbstack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAccountHomeRejectsAmbiguousOrSpoofedIdentity(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	valid := fmt.Sprintf("name: developer\nuid: %d\ngid: 20\ndir: %s\nshell: /bin/zsh\n\n", uid, home)
	t.Setenv("HOME", t.TempDir())
	if err := verifyControlHome([]byte(valid), uid, home); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", valid + valid, strings.Replace(valid, fmt.Sprintf("uid: %d", uid), "uid: 999999", 1), strings.Replace(valid, "dir: "+home, "dir: /missing", 1), strings.Replace(valid, "dir: "+home, "dir: "+home+"/../alias", 1)} {
		if err := verifyControlHome([]byte(invalid), uid, home); err == nil {
			t.Fatalf("accepted invalid native record %q", invalid)
		}
	}
	if err := verifyControlHome([]byte(valid), uid, t.TempDir()); err == nil {
		t.Fatal("accepted a different configured control home")
	}
}

func TestAccountHomeFailurePreventsDockerDispatch(t *testing.T) {
	f := setup(t)
	f.g.CheckControlHome = func(context.Context, string) error { return fmt.Errorf("native account unavailable") }
	if err := f.g.Check(context.Background()); err == nil || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}
