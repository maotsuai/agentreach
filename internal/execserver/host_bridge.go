package execserver

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/bojieli/agentreach/internal/host"
	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/transport"
)

// Codex 0.148 requires a registered local executor for local stdio MCP. Managed
// sessions deliberately exclude that executor. Its fixed MCP configuration uses
// the reach executor instead, whose exact generated argv is a typed callback.
// No program, shell, cwd or environment from this request is executed on Host.
func (s *Server) hostBridgeStream(p execParams) (transport.Stream, bool, error) {
	if s.sess.Managed == nil || len(p.Argv) < 2 || p.Argv[1] != "host-bridge" {
		return transport.Stream{}, false, nil
	}
	self, e := os.Executable()
	if e != nil {
		return transport.Stream{}, true, e
	}
	self, e = filepath.EvalSymlinks(self)
	if e != nil {
		return transport.Stream{}, true, e
	}
	if p.Argv[0] != self {
		return transport.Stream{}, false, nil
	}
	base, e := project.Home()
	if e != nil {
		return transport.Stream{}, true, e
	}
	if len(p.Argv) != 4 || p.Argv[2] != "--session" || p.Argv[3] != s.sess.Name || p.Cwd != pathToURI(base) || p.Tty || !p.PipeStdin {
		return transport.Stream{}, true, fmt.Errorf("policy_denied: fixed Host MCP callback shape changed")
	}
	if p.Env["REACH_HOME"] != base || p.Env["REACH_SESSION"] != s.sess.Name || p.Env["REACH_MANAGED"] != "1" || p.Env["REACH_MANAGED_PROJECT"] != s.sess.Managed.ProjectID || p.Env["REACH_MANAGED_RECORD"] != s.sess.Managed.RecordSHA256 {
		return transport.Stream{}, true, fmt.Errorf("policy_denied: fixed Host MCP binding changed")
	}
	for key := range p.Env {
		switch key {
		case "REACH_HOME", "REACH_SESSION", "REACH_MANAGED", "REACH_MANAGED_PROJECT", "REACH_MANAGED_RECORD", "PATH":
		default:
			return transport.Stream{}, true, fmt.Errorf("policy_denied: unexpected Host MCP environment key")
		}
	}
	if e = s.sess.CheckControlPaths(); e != nil {
		return transport.Stream{}, true, e
	}
	r, e := s.sess.ManagedProject()
	if e != nil {
		return transport.Stream{}, true, e
	}
	for _, m := range r.Spec.BindMounts {
		if !m.ReadOnly {
			inside, err := hostpath.Contains(m.Source, self)
			if err != nil || inside {
				return transport.Stream{}, true, fmt.Errorf("policy_denied: callback executable is Guest writable")
			}
		}
	}
	store, e := s.sess.Policy(s.runtimeReady)
	if e != nil {
		return transport.Stream{}, true, e
	}
	s.mu.Lock()
	if s.hostBridgeActive {
		s.mu.Unlock()
		return transport.Stream{}, true, fmt.Errorf("request_budget: fixed Host MCP stream is already active")
	}
	s.hostBridgeActive = true
	s.mu.Unlock()
	input, in := io.Pipe()
	out, output := io.Pipe()
	stderr, errors := io.Pipe()
	done := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	closeStream := func() error {
		once.Do(func() {
			cancel()
			_ = in.Close()
			_ = input.Close()
			_ = out.Close()
			_ = output.Close()
			_ = stderr.Close()
			_ = errors.Close()
		})
		return nil
	}
	go func() {
		x := host.Executor{Store: store}
		err := x.Serve(ctx, input, output)
		code := 0
		if err != nil {
			code = 1
			_, _ = fmt.Fprintln(errors, "fixed Host MCP connection closed:", err)
		}
		_ = output.Close()
		_ = errors.Close()
		_ = input.Close()
		s.mu.Lock()
		s.hostBridgeActive = false
		s.mu.Unlock()
		done <- code
		cancel()
	}()
	return transport.Stream{Stdin: in, Stdout: out, Stderr: stderr, Wait: func() (int, error) { return <-done, nil }, Close: closeStream}, true, nil
}
