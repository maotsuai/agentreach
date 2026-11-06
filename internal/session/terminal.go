package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/rooted"
	"github.com/bojieli/agentreach/internal/state"
	"github.com/bojieli/agentreach/internal/terminal"
	"github.com/bojieli/agentreach/internal/transport"
	"github.com/bojieli/agentreach/internal/workspace"
)

// Terminals creates a fresh connection while retaining the immutable project and backend identities.
func (s *Session) Terminals(check func(context.Context) error) (*terminal.Manager, error) {
	r, e := s.ManagedProject()
	if e != nil {
		return nil, e
	}
	base, e := project.Home()
	if e != nil {
		return nil, e
	}
	p, e := s.Policy(check)
	if e != nil {
		return nil, e
	}
	b, _ := json.Marshal(struct{ Daemon, Container, StartedAt, Record string }{r.Binding.DaemonID, r.Binding.ContainerID, r.Binding.StartedAt, s.Managed.RecordSHA256})
	sum := sha256.Sum256(b)
	var m *terminal.Manager
	cfg := terminal.Config{Base: base, Binding: policy.Binding{ProjectID: s.Managed.ProjectID, RecordDigest: s.Managed.RecordSHA256, Generation: r.Binding.Generation}, Runtime: hex.EncodeToString(sum[:]), Policy: p, Check: check, Paths: s.CheckControlPaths}
	cfg.Dispatch = func(ctx context.Context, q terminal.GuestRequest) (terminal.Observation, error) {
		var o terminal.Observation
		b, e := json.Marshal(q)
		if e != nil {
			return o, e
		}
		if len(b) > terminal.MaxFrame {
			return o, fmt.Errorf("request_budget: terminal internal frame")
		}
		t, e := s.TransportGuard(m.Ready)
		if e != nil {
			return o, e
		}
		defer func() { _ = t.Close() }()
		stream, e := t.Open(ctx, rooted.HelperPath+" terminal-call")
		if e != nil {
			return o, e
		}
		defer func() { _ = stream.Close() }()
		// Drain diagnostics concurrently so a failing helper cannot deadlock its protocol output.
		errorsDone := make(chan []byte, 1)
		go func() {
			v, _ := io.ReadAll(io.LimitReader(stream.Stderr, terminal.MaxFrame+1))
			_, _ = io.Copy(io.Discard, stream.Stderr)
			errorsDone <- v
		}()
		if _, e = stream.Stdin.Write(b); e != nil {
			return o, e
		}
		if e = stream.Stdin.Close(); e != nil {
			return o, e
		}
		data, e := io.ReadAll(io.LimitReader(stream.Stdout, terminal.MaxFrame+1))
		if e != nil {
			return o, e
		}
		if len(data) > terminal.MaxFrame {
			return o, fmt.Errorf("request_budget: terminal response")
		}
		code, e := stream.Wait()
		diagnostic := <-errorsDone
		if e != nil {
			return o, e
		}
		if code != 0 {
			cause := fmt.Errorf("terminal helper exit %d: %s", code, diagnostic)
			if strings.HasPrefix(strings.TrimSpace(string(diagnostic)), "reach-helper: identity_changed:") {
				return o, &terminal.BackendIdentityError{Cause: cause}
			}
			return o, cause
		}
		if e = state.Decode(data, &o); e != nil {
			return o, e
		}
		return o, terminal.ValidateObservation(o)
	}
	cfg.Launch = func(ctx context.Context, cwd string, argv []string, id string) (string, error) {
		mapper, e := s.PathMapper()
		if e != nil {
			return "", e
		}
		if cwd == "" {
			cwd = mapper.Root
		}
		mapped, e := mapper.Map(workspace.PathRequest{Operation: "cwd", Namespace: workspace.Protocol, Path: cwd})
		if e != nil {
			return "", e
		}
		t, e := s.TransportGuard(m.Ready)
		if e != nil {
			return "", e
		}
		defer func() { _ = t.Close() }()
		sel, e := s.FileOpsGuard(ctx, t, workspace.Guest, m.Ready)
		if e != nil {
			return "", e
		}
		defer func() { _ = sel.Ops.Close() }()
		data, e := json.Marshal(argv)
		if e != nil {
			return "", e
		}
		command := "exec " + transport.ShellQuote(rooted.HelperPath) + " terminal-pane " + transport.ShellQuote(id) + " " + transport.ShellQuote(base64.RawURLEncoding.EncodeToString(data))
		return s.Command(reach.ExecRequest{Command: command, Dir: mapped.Guest})
	}
	m = terminal.New(cfg)
	return m, nil
}
