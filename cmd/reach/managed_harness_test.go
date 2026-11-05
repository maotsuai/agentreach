package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/harnessprobe"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/transport"
)

func TestClaudeHookCommandIsIdenticalThroughSymlinkEntry(t *testing.T) {
	if os.Getenv("REACH_TEST_HOOK_ALIAS_HELPER") == "1" {
		command, err := claudeHookCommand()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(command)
		os.Exit(0)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the macOS/POSIX launcher uses symlink aliases")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(canonicalTempDir(t), "reach-hook-alias")
	if err := os.Symlink(resolved, alias); err != nil {
		t.Fatal(err)
	}
	want := transport.ShellQuote(resolved) + " hook"
	for _, entry := range []string{resolved, alias} {
		cmd := exec.Command(entry, "-test.run=^TestClaudeHookCommandIsIdenticalThroughSymlinkEntry$")
		cmd.Env = append(os.Environ(), "REACH_TEST_HOOK_ALIAS_HELPER=1")
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != want {
			t.Fatalf("entry=%q output=%q want=%q err=%v", entry, out, want, err)
		}
	}
}

func TestManagedHarnessArgumentsRejectConfigurationBypasses(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		for _, arg := range []string{"--force", "--allow-local-file-tools", "--mode=mirror", "--settings=untrusted", "--setting-sources=project", "--mcp-config=untrusted", "--tools=Read", "--plugin-dir=untrusted", "--danger-full-access", "--dangerously-bypass-approvals-and-sandbox", "-c", "--config=untrusted", "--cd=/", "--enable=local_shell"} {
			t.Run(harness+arg, func(t *testing.T) {
				if err := checkManagedArgs(harness, []string{arg}); err == nil {
					t.Fatalf("accepted %s %s", harness, arg)
				}
			})
		}
	}
	for _, args := range [][]string{{"--model", "--settings", "bad"}, {"--model="}, {"--model", ""}} {
		if err := checkManagedArgs("claude", args); err == nil {
			t.Fatalf("accepted ambiguous flag values %q", args)
		}
	}
	if err := checkManagedArgs("claude", []string{"-p", "a prompt", "--model", "test-model", "--output-format=json"}); err != nil {
		t.Fatal(err)
	}
	if err := checkManagedArgs("codex", []string{"exec", "--ephemeral", "--json", "a prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := checkManagedArgs("unverified", nil); err == nil {
		t.Fatal("accepted unknown harness")
	}
}

func TestManagedSeamNeverAllowsUnknownOrInconclusive(t *testing.T) {
	for _, version := range []string{"", "unknown", "dev-build"} {
		if err := requireManagedSeam(version, harnessprobe.Result{Verdict: harnessprobe.VerdictOK}); err == nil {
			t.Fatalf("accepted version %q", version)
		}
	}
	for _, verdict := range []string{harnessprobe.VerdictBypassed, harnessprobe.VerdictError, "inconclusive", ""} {
		if err := requireManagedSeam("1.2.3", harnessprobe.Result{Verdict: verdict}); err == nil {
			t.Fatalf("accepted verdict %q", verdict)
		}
	}
	if err := requireManagedSeam("0.148.0", harnessprobe.Result{Verdict: harnessprobe.VerdictOK}); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitManagedVerifyUsesTheLaunchSettings(t *testing.T) {
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	s := &session.Session{Name: "guarded", Managed: &session.Managed{ProjectID: "demo"}}
	opts, err := managedProbeOptions(s, "claude", "/approved/claude", "", "test-canary")
	if err != nil || opts.StrictClaudeSettings == "" || opts.CommandPrefix != "test-canary" || opts.BinaryPath != "/approved/claude" {
		t.Fatalf("probe did not use strict launch shape: %+v %v", opts, err)
	}
	if !reflect.DeepEqual(strictClaudeArgs(opts.StrictClaudeSettings), harnessprobe.ManagedClaudeArgs(opts.StrictClaudeSettings)) {
		t.Fatal("launch/probe settings diverged")
	}
	if _, err := managedProbeOptions(s, "kimi", "/unverified", "", ""); err == nil {
		t.Fatal("explicit verify admitted an unverified managed harness")
	}
}

func TestProjectSessionNameAndGuestArgumentBoundary(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range []string{"demo", strings.Repeat("a", 56), strings.Repeat("a", 57), strings.Repeat("a", 63), strings.Repeat("a", 64), strings.Repeat("a", 63) + "b"} {
		if !project.ValidID(id) {
			t.Fatalf("invalid fixture %s", id)
		}
		name := managedSessionName(id)
		if len(name) > 64 || len(name) == 0 || seen[name] || name != managedSessionName(id) {
			t.Fatalf("invalid/colliding session name %q", name)
		}
		seen[name] = true
	}
	if project.ValidID(strings.Repeat("a", 65)) {
		t.Fatal("accepted overlong project id")
	}
	if err := checkProjectSessionOverride([]string{"--", "app", "--session", "guest-option"}); err != nil {
		t.Fatalf("scanned Guest argv: %v", err)
	}
	for _, flag := range []string{"--session", "--session=other", "-session", "-session=other"} {
		if err := checkProjectSessionOverride([]string{flag}); err == nil {
			t.Fatalf("accepted %s", flag)
		}
	}
}

func TestDamagedOrFreshManagedSessionIsNeverReplaced(t *testing.T) {
	for _, raw := range []string{`{broken`, `{"name":"guarded","version":999,"target":{"kind":"local","workspace":"/tmp"}}`, `{"name":"guarded","version":2,"target":{"kind":"docker","container":"unapproved","workspace":"/workspace"},"mode":"exec","tier":"posix","managed":{"project_id":"absent","record_sha256":"invalid"}}`} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("REACH_HOME", canonicalTempDir(t))
			dir, err := session.Dir()
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "guarded.json")
			if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, fresh := range []bool{false, true} {
				_, err := ensureSession(context.Background(), &session.Target{Kind: session.KindLocal, Workspace: "/tmp"}, &sessionOptions{name: "guarded", fresh: fresh}, io.Discard, false)
				if err == nil {
					t.Fatal("replaced damaged or managed session")
				}
				got, _ := os.ReadFile(p)
				if string(got) != raw {
					t.Fatal("lost original session evidence")
				}
			}
		})
	}
}

func TestManagedHookAndPrefixFailuresCannotAllowOrExecuteLocally(t *testing.T) {
	t.Setenv("REACH_HOME", canonicalTempDir(t))
	t.Setenv("REACH_SESSION", "guarded")
	t.Setenv("REACH_MANAGED", "1")
	t.Setenv(claudeProjectDirEnv, canonicalTempDir(t))
	for _, raw := range []string{`{broken`, `{}`, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"true"}}`} {
		r := hookDecide(context.Background(), []byte(raw))
		if r.HookSpecificOutput == nil || r.HookSpecificOutput.PermissionDecision != "deny" {
			t.Fatalf("missing managed state returned allow/empty: %+v", r)
		}
	}
	marker := filepath.Join(canonicalTempDir(t), "must-not-run")
	if code := runShellPrefix([]string{"touch " + marker}); code != exitTransportFailure {
		t.Fatalf("unbound managed callback returned %d", code)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("spoofed callback reached Host shell")
	}
	// Managed session metadata itself enables the strict path even when the
	// auxiliary environment marker is absent.
	s := &session.Session{Name: "guarded", Target: &session.Target{Kind: session.KindDocker, Container: "absent", Workspace: "/workspace"}, Mode: session.ModeExec, Tier: reach.TierPOSIX, Managed: &session.Managed{ProjectID: "absent", RecordSHA256: "invalid"}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REACH_MANAGED", "")
	r := hookDecide(context.Background(), []byte(`{malformed`))
	if r.HookSpecificOutput == nil || r.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatal("managed malformed hook produced an empty allow")
	}
	if code := runShellPrefix([]string{"touch " + marker}); code != exitTransportFailure {
		t.Fatal("managed spoofed callback was accepted")
	}
}
