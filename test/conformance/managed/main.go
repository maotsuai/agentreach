// Command managed exercises the actual managed launcher with real harness
// binaries and a localhost scripted model. Build it in the Linux development
// container, including the Darwin cross-build; it never calls a paid provider.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bojieli/agentreach/internal/harnessprobe"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/transport"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	reachPath := flag.String("reach", "", "absolute cross-built reach binary")
	projectID := flag.String("project", "", "registered managed test project")
	harness := flag.String("harness", "", "codex or claude")
	binary := flag.String("binary", "", "absolute real harness binary")
	mode := flag.String("mode", "shell", "shell, shell-cwd-race, patch, patch-update, patch-delete, patch-move, patch-outside or patch-move-outside")
	guestCwd := flag.String("guest-cwd", "", "expected Guest cwd for the shell case (default project workspace)")
	flag.Parse()
	if !filepath.IsAbs(*reachPath) || !filepath.IsAbs(*binary) || *projectID == "" || *harness != "codex" && *harness != "claude" || !validMode(*mode, *harness) {
		return fmt.Errorf("require absolute --reach/--binary, --project, --harness codex|claude and a supported --mode")
	}
	stateHome := os.Getenv("REACH_HOME")
	if !filepath.IsAbs(stateHome) {
		return fmt.Errorf("REACH_HOME must explicitly name the isolated test state")
	}
	r, err := project.Load(*projectID)
	if err != nil {
		return err
	}
	if *guestCwd == "" {
		*guestCwd = r.Spec.Workspace.GuestPath
	}
	if !path.IsAbs(*guestCwd) || path.Clean(*guestCwd) != *guestCwd {
		return fmt.Errorf("--guest-cwd must be an absolute clean Guest path")
	}
	realBytes, err := os.ReadFile(*binary)
	if err != nil {
		return err
	}
	fmt.Printf("harness=%s mode=%s binary_sha256=%x\n", *harness, *mode, sha256.Sum256(realBytes))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	home, err := os.MkdirTemp("", "reach-managed-launch-conformance-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()
	binDir := filepath.Join(home, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		return err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	marker := "REACH_LAUNCH_" + hex.EncodeToString(nonce[:])
	testCase := launchCase(*mode, r.Spec.Workspace.GuestPath, *guestCwd, marker)
	if testCase.violation != "" {
		fmt.Printf("guest_body_violation_canary=%s\n", testCase.violation)
	}
	dialect := harnessprobe.DialectResponses
	if *harness == "claude" {
		dialect = harnessprobe.DialectAnthropic
	}
	mock := harnessprobe.StartMock(marker, dialect)
	defer mock.Close()
	if testCase.patch != "" {
		mock.SetPatch(testCase.patch)
	} else {
		mock.SetCommand(testCase.command)
		if *mode == "shell-cwd-race" && *harness == "codex" {
			mock.SetCwd(*guestCwd)
		}
	}
	// The inherited environment and real auth/config are deliberately absent.
	// ControlHome comes from the approved project, not this temporary HOME.
	env := []string{"PATH=" + binDir + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "REACH_HOME=" + stateHome, "REACH_HOST_ONLY_CANARY=dummy-host-value", "LANG=C", "LC_ALL=C", "DO_NOT_TRACK=1"}
	if *harness == "codex" {
		sourceHome := filepath.Join(home, "empty-codex-source")
		if err := os.Mkdir(sourceHome, 0o700); err != nil {
			return err
		}
		env = append(env, "CODEX_HOME="+sourceHome, "OPENAI_API_KEY=dummy-offline-conformance")
		// The production argument allowlist stays strict. This test-only
		// executable prepends provider flags before reach's argv. A strict
		// readiness probe supplies its own later provider flags, so it still
		// uses its own localhost mock. The final replacement reaches this mock.
		args := []string{"-c", `model_providers.reach_conformance.name="reach conformance"`, "-c", fmt.Sprintf("model_providers.reach_conformance.base_url=%q", mock.BaseURL()), "-c", `model_providers.reach_conformance.wire_api="responses"`, "-c", `model_providers.reach_conformance.requires_openai_auth=false`, "-c", `model_providers.reach_conformance.supports_websockets=false`, "-c", `model_provider="reach_conformance"`}
		words := []string{transport.ShellQuote(*binary)}
		for _, arg := range args {
			words = append(words, transport.ShellQuote(arg))
		}
		invoke := "exec " + strings.Join(words, " ")
		script := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = -a ]; then " + invoke + " \"$@\"; fi\ndone\n" + invoke + " -a never \"$@\"\n"
		if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o700); err != nil {
			return err
		}
	} else {
		if err := os.Symlink(*binary, filepath.Join(binDir, "claude")); err != nil {
			return err
		}
		env = append(env, "ANTHROPIC_API_KEY=dummy-offline-conformance", "ANTHROPIC_BASE_URL="+mock.BaseURL(), "CLAUDE_TELEMETRY_OPT_OUT=1")
	}
	args := []string{"--project", *projectID, *harness}
	if *harness == "codex" {
		args = append(args, "exec", "--ephemeral", "--skip-git-repo-check", "--model", "gpt-5.4")
	} else {
		args = append(args, "--print", "--no-session-persistence", "--model", "reach-mock")
	}
	args = append(args, "Follow the tool-call instructions exactly.")
	if testCase.setup != "" {
		setup := exec.CommandContext(ctx, *reachPath, "--project", *projectID, "exec", "--", testCase.setup)
		setup.Dir, setup.Env = home, env
		if output, err := setup.CombinedOutput(); err != nil {
			return fmt.Errorf("prepare Guest patch fixture: %w: %s", err, output)
		}
	}
	cmd := exec.CommandContext(ctx, *reachPath, args...)
	cmd.Dir, cmd.Env = home, env
	output, runErr := cmd.CombinedOutput()
	fmt.Printf("launcher_output:\n%s\n", output)
	if runErr != nil {
		return fmt.Errorf("actual launcher failed: %w", runErr)
	}
	result, observed := mock.Result()
	fmt.Printf("model_tool_result:\n%s\n", result)
	if !observed {
		return fmt.Errorf("actual launcher never returned the scripted tool output")
	}
	if !strings.Contains(result, testCase.wantResult) {
		return fmt.Errorf("scripted tool result does not contain %q", testCase.wantResult)
	}
	if testCase.wantFailure && !strings.Contains(result, "Exit code: 1") {
		return fmt.Errorf("scripted negative tool case did not report failure")
	}
	// Independently read through the registered project's real CLI entrypoint.
	read := exec.CommandContext(ctx, *reachPath, "--project", *projectID, "exec", "--", testCase.verify)
	if testCase.readPath != "" {
		// The raced directory instance can be renamed after safe acquisition.
		// FileOps readback stays rooted without reopening that mutable cwd.
		read = exec.CommandContext(ctx, *reachPath, "--project", *projectID, "fs", "read", testCase.readPath)
	}
	read.Dir, read.Env = home, env
	data, err := read.Output()
	if err != nil {
		return fmt.Errorf("Guest readback failed: %w", err)
	}
	if string(data) != testCase.content {
		return fmt.Errorf("Guest readback mismatch: %q", data)
	}
	for _, file := range testCase.hostAbsent {
		if r.Spec.Workspace.Mode == "shared-project" && approvedSharedProjection(r.Spec.Workspace.HostPath, r.Spec.Workspace.GuestPath, file) {
			continue
		}
		if _, err := os.Lstat(file); !os.IsNotExist(err) {
			if err != nil {
				return fmt.Errorf("inspect Host canary %s: %w", file, err)
			}
			return fmt.Errorf("Host canary path unexpectedly exists: %s", file)
		}
	}
	if testCase.patch != "" {
		sessions, broken, err := session.List()
		if err != nil {
			return fmt.Errorf("cannot inspect isolated Host sessions: %w", err)
		}
		if len(broken) != 0 {
			return fmt.Errorf("isolated Host state contains %d unreadable sessions", len(broken))
		}
		checked := false
		for _, s := range sessions {
			if s.Managed != nil && s.Managed.ProjectID == *projectID {
				for _, name := range testCase.controlAbsent {
					hostFile := filepath.Join(stateHome, "codex-home", s.Name, name)
					if _, err := os.Lstat(hostFile); !os.IsNotExist(err) {
						return fmt.Errorf("native patch reached its Host control cwd or that path is unreadable: %s", hostFile)
					}
				}
				checked = true
			}
		}
		if !checked {
			return fmt.Errorf("managed session was not created by the actual launcher")
		}
		fmt.Println("host_control_cwd_canary_absent=true")
	}
	fmt.Printf("PASS actual_launcher=true mode=%s unapproved_host_canaries_absent=true\n", *mode)
	return nil
}
