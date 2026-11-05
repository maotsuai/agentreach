package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bojieli/agentreach/internal/harnessprobe"
	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/session"
)

// Managed arguments are an allowlist, so a new harness option cannot silently
// introduce a new configuration source, executor or local command hook.
func checkManagedArgs(harness string, args []string) error {
	if harness != "claude" && harness != "codex" {
		return fmt.Errorf("policy_denied: unverified harness %s", harness)
	}
	values := map[string]bool{}
	flags := map[string]bool{}
	if harness == "claude" {
		for _, s := range []string{"--model", "--max-turns", "--output-format"} {
			values[s] = true
		}
		for _, s := range []string{"-p", "--print", "--verbose", "--no-session-persistence"} {
			flags[s] = true
		}
	} else {
		for _, s := range []string{"--model", "-m", "--color"} { //nolint:misspell // Codex's actual CLI flag.
			values[s] = true
		}
		for _, s := range []string{"--json", "--ephemeral", "--skip-git-repo-check"} {
			flags[s] = true
		}
	}
	printMode := false
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "cc://") || strings.HasPrefix(a, "cc+unix://") {
			return fmt.Errorf("policy_denied: managed Claude URI routing is unsupported")
		}
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		key, value, equal := strings.Cut(a, "=")
		if flags[key] && !equal {
			if key == "-p" || key == "--print" {
				printMode = true
			}
			continue
		}
		if !values[key] {
			return fmt.Errorf("policy_denied: managed %s does not allow argument %s", harness, key)
		}
		if !equal {
			i++
			if i >= len(args) {
				return fmt.Errorf("missing value for %s", key)
			}
			value = args[i] //nolint:gosec // i is checked against len(args) above.
		}
		if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "cc://") || strings.HasPrefix(value, "cc+unix://") {
			return fmt.Errorf("policy_denied: invalid managed argument value for %s", key)
		}
		if (key == "--model" || key == "-m") && !managedModel.MatchString(value) {
			return fmt.Errorf("policy_denied: unsupported managed model name")
		}
		if key == "--max-turns" && !managedTurns.MatchString(value) {
			return fmt.Errorf("policy_denied: invalid turn count")
		}
		if key == "--output-format" && value != "text" && value != "json" && value != "stream-json" {
			return fmt.Errorf("policy_denied: unsupported output format")
		}
		if key == "--color" && value != "always" && value != "never" && value != "auto" { //nolint:misspell // Codex uses --color.
			return fmt.Errorf("policy_denied: unsupported colour mode")
		}
	}
	if harness == "claude" {
		if len(positional) > 1 || len(positional) == 1 && !printMode {
			return fmt.Errorf("policy_denied: managed Claude accepts one print prompt only")
		}
	} else if len(positional) > 0 {
		if positional[0] != "exec" || len(positional) > 2 || len(positional) == 2 && (positional[1] == "resume" || positional[1] == "review") {
			return fmt.Errorf("policy_denied: managed Codex accepts interactive mode or exec with one prompt only")
		}
	}

	return nil
}

var managedModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
var managedTurns = regexp.MustCompile(`^[1-9][0-9]*$`)

var managedVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?$`)

func requireManagedSeam(version string, result harnessprobe.Result) error {
	if !managedVersion.MatchString(version) {
		return fmt.Errorf("seam_unverified: unknown harness version %q", version)
	}
	if result.Verdict != harnessprobe.VerdictOK {
		return fmt.Errorf("seam_unverified: %s: %s", result.Verdict, result.Detail)
	}
	return nil
}

func strictClaudeArgs(settings string) []string {
	return harnessprobe.ManagedClaudeArgs(settings)
}

func managedProbeOptions(s *session.Session, harness, bin, settings, task string) (harnessprobe.Options, error) {
	if harness != "claude" && harness != "codex" {
		return harnessprobe.Options{}, fmt.Errorf("policy_denied: %s is not a verified managed harness", harness)
	}
	if harness == "claude" && settings == "" {
		var err error
		settings, err = writeDenySettings(s.Name)
		if err != nil {
			return harnessprobe.Options{}, err
		}
	}
	return harnessprobe.Options{Harness: harness, SessionName: s.Name, BinaryPath: bin, EnsureShim: ensurePathShim, EnsureShellPrefix: ensureShim, StrictClaudeSettings: settings, CommandPrefix: task}, nil
}

func verifyManagedSeam(ctx context.Context, s *session.Session, harness, bin, settings, task string) (harnessprobe.Result, error) {
	if harness != "claude" && harness != "codex" {
		return harnessprobe.Result{}, fmt.Errorf("policy_denied: unsupported managed harness %s", harness)
	}
	if err := s.CheckManaged(ctx); err != nil {
		return harnessprobe.Result{}, err
	}
	bin, err := managedHarnessExecutable(s, bin)
	if err != nil {
		return harnessprobe.Result{}, err
	}
	path, err := managedSafePath(s)
	if err != nil {
		return harnessprobe.Result{}, err
	}
	before, err := os.ReadFile(bin)
	if err != nil {
		return harnessprobe.Result{}, err
	}
	fingerprint := sha256.Sum256(before)
	control, err := project.Home()
	if err != nil {
		return harnessprobe.Result{}, err
	}
	env := s.ManagedEnvironment(replaceEnv(managedHarnessEnv(os.Environ()), "PATH", path))
	env = replaceEnv(env, "PWD", control)
	version, err := harnessprobe.ManagedVersion(ctx, bin, control, env)
	if err != nil {
		return harnessprobe.Result{}, fmt.Errorf("seam_unverified: cannot determine %s version: %w", harness, err)
	}
	if !managedVersion.MatchString(version) {
		return harnessprobe.Result{}, fmt.Errorf("seam_unverified: unknown %s version %q", harness, version)
	}
	if after, err := os.ReadFile(bin); err != nil || sha256.Sum256(after) != fingerprint {
		return harnessprobe.Result{}, fmt.Errorf("seam_unverified: executable changed during version check")
	}
	opts, err := managedProbeOptions(s, harness, bin, settings, task)
	if err != nil {
		return harnessprobe.Result{}, err
	}
	opts.ManagedPath = path
	fmt.Fprintf(os.Stderr, "reach: strictly verifying %s %s for project %s (fresh probe)\n", harness, version, s.Managed.ProjectID)
	result := harnessprobe.Verify(ctx, opts)
	if err := requireManagedSeam(version, result); err != nil {
		return result, err
	}
	after, err := os.ReadFile(bin)
	if err != nil || sha256.Sum256(after) != fingerprint {
		return result, fmt.Errorf("seam_unverified: harness executable changed during verification")
	}
	return result, s.CheckManaged(ctx)
}

func strictSeam(ctx context.Context, s *session.Session, harness, bin, settings string) error {
	_, err := verifyManagedSeam(ctx, s, harness, bin, settings, "")
	return err
}

func managedHarnessEnv(env []string) []string {
	var out []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "CLAUDE_") {
			continue
		}
		out = append(out, e)
	}
	return harnessprobe.ManagedEnvironment(out)
}

func refuseUnverifiedManagedSession(s *session.Session, entry string) error {
	if s.Managed != nil {
		return fmt.Errorf("policy_denied: %s is not a verified managed project entrypoint", entry)
	}
	return nil
}

// Both the lookup spelling and resolved target must be outside every writable
// Guest bind. In particular, a trusted target does not make a Guest-owned
// symlink or one of its replaceable parent directories trusted.
func managedHarnessExecutable(s *session.Session, bin string) (string, error) {
	r, err := s.ManagedProject()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(bin) {
		return "", fmt.Errorf("policy_denied: managed executable must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", err
	}
	for _, p := range []string{bin, resolved} {
		if err := outsideWritableBinds(r, p); err != nil {
			return "", err
		}
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("policy_denied: managed executable is not a regular executable")
	}
	return resolved, nil
}
func outsideWritableBinds(r project.Record, p string) error {
	for _, m := range r.Spec.BindMounts {
		if m.ReadOnly {
			continue
		}
		inside, err := hostpath.Contains(m.Source, p)
		if err != nil {
			return fmt.Errorf("policy_denied: inspect Host executable path %s: %w", p, err)
		}
		if inside {
			return fmt.Errorf("policy_denied: Host executable/configuration path is Guest writable: %s", p)
		}
	}
	return nil
}
func managedSafePath(s *session.Session) (string, error) {
	r, err := s.ManagedProject()
	if err != nil {
		return "", err
	}
	var trusted []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if err := outsideWritableBinds(r, dir); err != nil {
			continue
		}
		if err := outsideWritableBinds(r, resolved); err != nil {
			continue
		}
		trusted = append(trusted, resolved)
	}
	if len(trusted) == 0 {
		return "", fmt.Errorf("policy_denied: no trusted Host PATH remains")
	}
	return strings.Join(trusted, string(os.PathListSeparator)), nil
}
func managedLaunchEnv(s *session.Session, env []string) ([]string, error) {
	path, err := managedSafePath(s)
	if err != nil {
		return nil, err
	}
	return s.ManagedEnvironment(replaceEnv(managedHarnessEnv(env), "PATH", path)), nil
}
