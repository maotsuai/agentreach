package harnessprobe

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/session"
)

func TestManagedEnvironmentScrubsRoutingAndInjectionOverrides(t *testing.T) {
	input := []string{"HOME=/trusted", "PATH=/bin", "REACH_IN_SHELL_SHIM=1", "BASH_ENV=/untrusted", "ENV=/untrusted", "NODE_OPTIONS=--require=untrusted", "REACH_EXEC_WORKSPACE=/untrusted", "REACH_MANAGED=0", "REACH_SESSION=approved", "CLAUDE_CODE_SHELL_PREFIX=/controlled/prefix"}
	want := []string{"HOME=/trusted", "PATH=/bin", "REACH_SESSION=approved", "CLAUDE_CODE_SHELL_PREFIX=/controlled/prefix", "REACH_MANAGED=1"}
	if got := ManagedEnvironment(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}

func TestManagedVersionUsesTrustedCwdAndCleanEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher fixture")
	}
	trusted, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guest, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(trusted, "harness")
	script := "#!/bin/sh\n[ \"$PWD\" = " + trusted + " ] || exit 30\n[ -z \"${NODE_OPTIONS+x}${NODE_PATH+x}${BASH_ENV+x}\" ] || exit 31\n[ ! -f ./guest-marker ] || exit 32\nprintf '1.2.3\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guest, "guest-marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(guest)
	env := ManagedEnvironment([]string{"PATH=/usr/bin:/bin", "NODE_OPTIONS=--require=./guest.js", "NODE_PATH=.", "BASH_ENV=./guest.sh"})
	if v, err := ManagedVersion(context.Background(), bin, trusted, env); err != nil || v != "1.2.3" {
		t.Fatalf("version=%q err=%v", v, err)
	}
}

func TestManagedProbeCwdTracksClaudeSessionCwd(t *testing.T) {
	t.Setenv("REACH_HOME", t.TempDir())
	if _, err := session.Dir(); err != nil {
		t.Fatal(err)
	}
	s := &session.Session{Name: "cwd", Target: &session.Target{Workspace: "/workspace"}}
	if err := s.SetCwd("/tmp"); err != nil {
		t.Fatal(err)
	}
	if got := managedProbeCwd(s, HarnessClaudeCode); got != "/tmp" {
		t.Fatalf("Claude expected stale workspace: %s", got)
	}
	if got := managedProbeCwd(s, HarnessCodex); got != "/workspace" {
		t.Fatalf("Codex lost workspace mapping: %s", got)
	}
	for _, e := range ManagedEnvironment([]string{"REACH_MANAGED_PROJECT=spoof", "REACH_MANAGED_RECORD=spoof"}) {
		if strings.Contains(e, "spoof") {
			t.Fatal("inherited binding override")
		}
	}
}
