package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/session"
)

func printJSON(value any) error { return json.NewEncoder(os.Stdout).Encode(value) }
func cmdProject(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: reach project register <config.json> | inspect <id>")
	}
	switch args[0] {
	case "register":
		s, err := project.ReadSpec(args[1])
		if err != nil {
			return err
		}
		g := orbstack.New(project.Record{Spec: s})
		binding, err := g.Capture(ctx)
		if err != nil {
			return err
		}
		r := project.Record{Spec: s, Binding: binding}
		if err := project.Register(ctx, r); err != nil {
			return err
		}
		return printJSON(r)
	case "inspect":
		r, err := project.Load(args[1])
		if err != nil {
			return err
		}
		return printJSON(r)
	default:
		return fmt.Errorf("unknown project operation %q", args[0])
	}
}
func cmdRuntime(ctx context.Context, args []string) error {
	if len(args) != 2 || (args[0] != "health" && args[0] != "inspect") {
		return fmt.Errorf("usage: reach runtime inspect|health <id>")
	}
	r, err := project.Load(args[1])
	if err != nil {
		return err
	}
	g := orbstack.New(r)
	if err := g.Check(ctx); err != nil {
		return err
	}
	if args[0] == "inspect" {
		c, err := g.Docker.Inspect(ctx, r.Binding.ContainerID)
		if err != nil {
			return err
		}
		return printJSON(c)
	}
	return printJSON(map[string]any{"status": "ready", "project_id": r.Spec.ID, "binding": r.Binding})
}

func managedSession(ctx context.Context, id string) (*session.Session, error) {
	r, err := project.Load(id)
	if err != nil {
		return nil, err
	}
	name := managedSessionName(id)
	if s, err := session.Load(name); err == nil {
		if s.Managed == nil || *s.Managed != *session.Bind(r) {
			return nil, fmt.Errorf("identity_changed: existing session is not this registered project")
		}
		return s, s.CheckManaged(ctx)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	s := &session.Session{Name: name, Target: &session.Target{Kind: session.KindDocker, Container: r.Binding.ContainerID, Workspace: r.Spec.Workspace.GuestPath, Raw: "docker://" + r.Binding.ContainerID + r.Spec.Workspace.GuestPath}, Mode: session.ModeExec, Tier: reach.TierPOSIX, Timeout: 2 * time.Minute, Created: time.Now(), Managed: session.Bind(r)}
	if err := s.ProbeManaged(ctx, r); err != nil {
		return nil, err
	}
	if err := s.Save(); err != nil {
		return nil, err
	}
	if err := s.SetCwd(s.Target.Workspace); err != nil {
		return nil, err
	}
	return s, nil
}
func runProject(ctx context.Context, args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: reach --project <id> claude|codex|exec|fs|terminal [args...]")
		return 2
	}
	if args[1] != "claude" && args[1] != "codex" && args[1] != "exec" && args[1] != "fs" && args[1] != "terminal" && args[1] != "service" && args[1] != "port" {
		fmt.Fprintln(os.Stderr, "policy_denied: managed projects support only claude, codex, exec, fs and terminal")
		return 2
	}
	if err := checkProjectSessionOverride(args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var s *session.Session
	var err error
	if args[1] == "terminal" || args[1] == "service" || args[1] == "port" {
		s, err = managedTerminalSession(ctx, args[0], len(args) > 2 && (args[2] == "create" || args[2] == "plan" || args[2] == "start"))
	} else {
		s, err = managedSession(ctx, args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "reach:", err)
		return 1
	}
	if err := os.Setenv("REACH_SESSION", s.Name); err != nil {
		fmt.Fprintln(os.Stderr, "reach:", err)
		return 1
	}
	for _, e := range s.ManagedEnvironment(nil) {
		k, v, _ := strings.Cut(e, "=")
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return dispatch(ctx, args[1:]) //nolint:gosec // len(args) >= 2 is enforced at entry.
}

func checkProjectSessionOverride(args []string) error {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--session" || a == "-session" || strings.HasPrefix(a, "--session=") || strings.HasPrefix(a, "-session=") {
			return fmt.Errorf("policy_denied: --project cannot override --session")
		}
	}
	return nil
}

func managedSessionName(id string) string {
	name := "project-" + id
	if len(name) <= 64 {
		return name
	}
	h := sha256.Sum256([]byte(id))
	return name[:47] + "-" + hex.EncodeToString(h[:8])
}

// Terminal observations must reach their manager even when a saved runtime generation is lost.
// The manager performs the same guard before any Guest dispatch and can retain a typed lost result.
func managedTerminalSession(ctx context.Context, id string, bootstrap bool) (*session.Session, error) {
	r, e := project.Load(id)
	if e != nil {
		return nil, e
	}
	s, e := session.Load(managedSessionName(id))
	if errors.Is(e, os.ErrNotExist) {
		if !bootstrap {
			return nil, fmt.Errorf("resource_lost: no managed session; create a terminal first")
		}
		return managedSession(ctx, id)
	}
	if e != nil {
		return nil, e
	}
	if s.Managed == nil || *s.Managed != *session.Bind(r) {
		return nil, fmt.Errorf("identity_changed: terminal session binding")
	}
	return s, nil
}
