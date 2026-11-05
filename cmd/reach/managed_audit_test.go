package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/session"
)

func TestUnverifiedEntrypointsUseTheirParsedSession(t *testing.T) {
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	for _, name := range []string{"default", "project-fixture"} {
		s := &session.Session{Name: name, Target: &session.Target{Kind: session.KindLocal, Workspace: canonicalTempDir(t)}, Mode: session.ModeExec, Tier: reach.TierPOSIX}
		if name != "default" {
			s.Managed = &session.Managed{ProjectID: "fixture", RecordSHA256: "fixture"}
		}
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []string{"kimi", "goose", "gemini", "crush", "grok", "opencode", "env"} {
		for _, flag := range [][]string{{"-session=project-fixture"}, {"--session", "project-fixture"}, {"--session=project-fixture"}} {
			args := append([]string{entry}, flag...)
			if code := dispatch(context.Background(), args); code == 0 {
				t.Fatalf("accepted %q", args)
			}
		}
	}
	if err := cmdEnv(context.Background(), []string{"project-fixture"}); err == nil {
		t.Fatal("env positional bypass")
	}
	t.Setenv("REACH_SESSION", "project-fixture")
	if code := cmdGoose(context.Background(), []string{"--force", "--", "--session", "default"}); code == 0 {
		t.Fatal("harness tail overrode reach's session")
	}
	t.Setenv("REACH_SESSION", "")
	if err := cmdEnv(context.Background(), []string{"default"}); err != nil {
		t.Fatalf("legacy env: %v", err)
	}
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	t.Chdir(canonicalTempDir(t))
	if err := cmdOpencode(context.Background(), []string{"uninstall"}); err != nil {
		t.Fatalf("session-independent cleanup: %v", err)
	}
}

func TestManagedHarnessGrammarRejectsHostSubcommands(t *testing.T) {
	for _, tc := range []struct {
		h    string
		args []string
	}{
		{"claude", []string{"-p", "--model", "cc://dummy", "mcp", "get", "absent"}},
		{"claude", []string{"-p", "cc+unix://dummy"}},
		{"claude", []string{"-p", "mcp", "add-json", "name", "{}"}},
		{"claude", []string{"--model=cc://dummy", "-p", "prompt"}},
		{"codex", []string{"exec", "resume", "id"}},
		{"codex", []string{"exec", "review"}},
		{"codex", []string{"exec", "prompt", "extra"}},
	} {
		if err := checkManagedArgs(tc.h, tc.args); err == nil {
			t.Fatalf("accepted %s %q", tc.h, tc.args)
		}
	}
	for _, h := range []string{"claude", "codex"} {
		if err := checkManagedArgs(h, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := verifyManagedSeam(context.Background(), nil, "kimi", "/should/not/execute", "", ""); err == nil {
		t.Fatal("unsupported harness reached version execution")
	}
}

func managedPathFixture(t *testing.T) (*session.Session, string, string) {
	t.Helper()
	root := canonicalTempDir(t)
	t.Setenv("REACH_HOME", filepath.Join(root, "control"))
	guest, trusted := filepath.Join(root, "guest"), filepath.Join(root, "trusted")
	for _, p := range []string{guest, trusted} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := project.ReadSpec(filepath.Join("..", "..", "examples", "orbstack", "project.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec.BindMounts = []project.MountApproval{{Source: guest, Target: spec.Workspace.GuestPath}}
	r := project.Record{Spec: spec, Binding: project.Binding{Schema: 1, Revision: 1, Generation: 1, ContainerID: strings.Repeat("a", 64), ImageID: "image", DaemonID: "daemon", StartedAt: "now", SnapshotSHA256: strings.Repeat("b", 64)}}
	if err := project.Register(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s := &session.Session{Name: "path-test", Managed: session.Bind(r), Target: &session.Target{Kind: session.KindDocker, Container: r.Binding.ContainerID, Workspace: spec.Workspace.GuestPath}, Mode: session.ModeExec}
	return s, guest, trusted
}

func TestManagedExecutableRejectsGuestLookupAndAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Host executable policy is POSIX")
	}
	s, guest, trusted := managedPathFixture(t)
	safe, bad := filepath.Join(trusted, "codex"), filepath.Join(guest, "codex")
	for _, p := range []string{safe, bad} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	insideAlias, outsideAlias := filepath.Join(guest, "alias"), filepath.Join(trusted, "alias")
	if err := os.Symlink(safe, insideAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bad, outsideAlias); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{bad, insideAlias, outsideAlias} {
		if _, err := managedHarnessExecutable(s, p); err == nil {
			t.Fatalf("accepted Guest-controlled %s", p)
		}
	}
	if got, err := managedHarnessExecutable(s, safe); err != nil || got != safe {
		t.Fatalf("trusted executable: %s %v", got, err)
	}
	t.Chdir(guest)
	t.Setenv("PATH", ".:"+trusted+":"+guest+":/usr/bin:/bin")
	selected, err := lookHarnessPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedHarnessExecutable(s, selected); err == nil {
		t.Fatal("relative PATH precedence executed Guest program")
	}
	clean, err := managedSafePath(s)
	if err != nil || strings.Contains(clean, guest) || strings.Contains(clean, ".:") {
		t.Fatalf("untrusted PATH remained: %s %v", clean, err)
	}
}

func TestManagedCwdRecordRejectsArbitraryPathsAndNeverTruncatesLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Claude's measured POSIX temp record")
	}
	for _, p := range []string{filepath.Join(canonicalTempDir(t), "claude-1234-cwd"), "/tmp/other", "/tmp/claude-../escape-cwd"} {
		if r, err := openManagedCwdRecord(p); err == nil {
			r.root.Close()
			t.Fatalf("accepted %s", p)
		}
	}
	f, err := os.CreateTemp("/tmp", "reach-cwd-target-")
	if err != nil {
		t.Fatal(err)
	}
	target := f.Name()
	f.Close()
	defer os.Remove(target)
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join("/tmp", "claude-"+strings.Repeat("a", 32)+"-cwd")
	defer os.Remove(name)
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	if r, err := openManagedCwdRecord(name); err == nil {
		r.root.Close()
		t.Fatal("accepted symlink leaf")
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, name); err != nil {
		t.Fatal(err)
	}
	r, err := openManagedCwdRecord(name)
	if err != nil {
		t.Fatal(err)
	}
	defer r.root.Close()
	if err := r.write("/guest"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatalf("hardlink target changed: %q %v", b, err)
	}
	if b, err := os.ReadFile(name); err != nil || string(b) != "/guest\n" {
		t.Fatalf("record missing: %q %v", b, err)
	}
	// A replacement between validation and write also must not follow links.
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	if err := r.write("/second"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("racing symlink target changed")
	}
}

func TestManagedChildEntrypointsRefuseReusedLocalSession(t *testing.T) {
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	s := &session.Session{Name: "reused", Target: &session.Target{Kind: session.KindLocal, Workspace: canonicalTempDir(t)}, Mode: session.ModeExec, Tier: reach.TierPOSIX, Managed: &session.Managed{ProjectID: "original", RecordSHA256: "original-digest"}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := session.Remove(s.Name); err != nil {
		t.Fatal(err)
	}
	s.Managed = nil
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REACH_SESSION", s.Name)
	t.Setenv("REACH_MANAGED", "1")
	t.Setenv("REACH_MANAGED_PROJECT", "original")
	t.Setenv("REACH_MANAGED_RECORD", "original-digest")
	if code := cmdExecServer(context.Background(), nil); code == 0 {
		t.Fatal("old managed process opened a Host executor")
	}
	marker := filepath.Join(s.Target.Workspace, "host-canary")
	if code := runShellPrefix([]string{"touch " + marker}); code != exitTransportFailure {
		t.Fatal("old prefix dispatched to Host")
	}
	reply := hookDecide(context.Background(), []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"true"}}`))
	if reply.HookSpecificOutput == nil || reply.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatal("old managed hook accepted local replacement")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Host canary created")
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
