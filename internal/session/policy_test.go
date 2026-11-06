package session

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
)

func effectFixture(t *testing.T) (*Session, *policy.Store, string) {
	t.Helper()
	root := canonicalTempDir(t)
	t.Setenv("REACH_HOME", filepath.Join(root, "control"))
	guest := filepath.Join(root, "guest")
	if e := os.Mkdir(guest, 0700); e != nil {
		t.Fatal(e)
	}
	spec, e := project.ReadSpec(filepath.Join("..", "..", "examples", "orbstack", "project.example.json"))
	if e != nil {
		t.Fatal(e)
	}
	spec.BindMounts = []project.MountApproval{{Source: guest, Target: spec.Workspace.GuestPath}}
	r := project.Record{Spec: spec, Binding: project.Binding{Schema: 1, Revision: 1, Generation: 1, ContainerID: strings.Repeat("a", 64), ImageID: "image", DaemonID: "daemon", StartedAt: "now", SnapshotSHA256: strings.Repeat("b", 64)}}
	if e = project.Register(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	s := &Session{Name: "effect-test", Managed: Bind(r), Target: &Target{Kind: KindDocker, Container: r.Binding.ContainerID, Workspace: spec.Workspace.GuestPath}, Mode: ModeExec}
	p, e := s.Policy(func(context.Context) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	return s, p, guest
}

func TestGuestDispatchedUnknownRetainsIdentityAndCause(t *testing.T) {
	s, p, guest := effectFixture(t)
	ctx := context.Background()
	check := func(context.Context) error { return nil }
	for _, kind := range []string{"exec", "process_start", "stdin", "write", "mkdir", "remove", "rename", "copy"} {
		e := s.guestEffect(check)(ctx, kind, "dummy-payload", func() error {
			if e := os.WriteFile(filepath.Join(guest, kind), []byte("effect happened"), 0600); e != nil {
				t.Fatal(e)
			}
			return io.ErrClosedPipe
		})
		v, loadErr := p.Inspect()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		op := v.Operations[len(v.Operations)-1]
		if !errors.Is(e, io.ErrClosedPipe) || !strings.Contains(e.Error(), "outcome_unknown") || !strings.Contains(e.Error(), op.ID) || op.Outcome != "outcome_unknown" {
			t.Fatalf("%s: op=%+v error=%v", kind, op, e)
		}
	}
	if e := p.Set(ctx, "guest", "deny"); e != nil {
		t.Fatal(e)
	}
	dispatched := false
	e := s.guestEffect(check)(ctx, "write", "payload", func() error { dispatched = true; return nil })
	if e == nil || !strings.Contains(e.Error(), "policy_denied") || strings.Contains(e.Error(), "outcome_unknown") || dispatched {
		t.Fatalf("pre-intent denial changed: %v", e)
	}
}

func TestGuestResultFailureRetainsDispatchCause(t *testing.T) {
	s, p, _ := effectFixture(t)
	base, e := project.Home()
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(base, "security")
	defer os.Chmod(dir, 0700)
	e = s.guestEffect(func(context.Context) error { return nil })(context.Background(), "write", "dummy", func() error {
		if e := os.Chmod(dir, 0500); e != nil {
			t.Fatal(e)
		}
		return io.ErrClosedPipe
	})
	if e == nil || !errors.Is(e, io.ErrClosedPipe) || !strings.Contains(e.Error(), "outcome_unknown") {
		t.Fatal(e)
	}
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	v, e := p.Inspect()
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Operations) != 1 || v.Operations[0].Outcome != "intent" {
		t.Fatal("result failure lost durable unknown intent")
	}
}

func TestUnmanagedDispatchErrorIsUnchanged(t *testing.T) {
	s := &Session{}
	e := s.guestEffect(func(context.Context) error { t.Fatal("unmanaged guard"); return nil })(context.Background(), "write", "payload", func() error { return io.ErrClosedPipe })
	if e != io.ErrClosedPipe {
		t.Fatal(e)
	}
}
