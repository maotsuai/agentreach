package main

import (
	"context"
	"fmt"
	"github.com/bojieli/agentreach/internal/host"
	"github.com/bojieli/agentreach/internal/ports"
	"github.com/bojieli/agentreach/internal/service"
	"github.com/bojieli/agentreach/internal/session"
	"io"
	"os"
)

func cmdService(ctx context.Context, args []string, port bool) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: reach --project PROJECT service plan|start|stop|inspect|logs|list or port list|inspect (strict JSON stdin)")
	}
	s, e := session.Load(sessionNameFromEnv(""))
	if e != nil {
		return e
	}
	if s.Managed == nil {
		return fmt.Errorf("unsupported: managed project required")
	}
	b, e := io.ReadAll(io.LimitReader(os.Stdin, service.MaxFrame+1))
	if e != nil {
		return e
	}
	m, e := s.Services(s.CheckManaged)
	if e != nil {
		return e
	}
	var v any
	if port {
		p, e := s.Policy(s.CheckManaged)
		if e != nil {
			return e
		}
		x := host.Executor{Store: p, Services: m}
		v, e = x.Call(ctx, "port_"+args[0], b)
		if e != nil {
			return e
		}
		return printJSON(v)
	}
	q, e := service.Decode(b)
	if e != nil {
		return e
	}
	v, e = m.Call(ctx, args[0], q)
	if e != nil {
		if v != nil {
			_ = printJSON(map[string]any{"error": e.Error(), "service": v})
		}
		return e
	}
	return printJSON(v)
}
func cmdHostd(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "--session" && args[0] != "--stop-session" {
		return fmt.Errorf("policy_denied: fixed hostd --session required")
	}
	s, e := session.Load(args[1])
	if e != nil {
		return e
	}
	m, e := s.Services(s.CheckManaged)
	if e != nil {
		return e
	}
	if args[0] == "--stop-session" {
		return m.PortClient().Shutdown(ctx)
	}
	cfg := m.PortClient().Config
	// Each target observation is a new connection. A cancelled network client
	// must not poison every future stream in this long-lived manager.
	cfg.Resolve = func(ctx context.Context, id string, port uint16) (ports.Target, error) {
		fresh, e := s.Services(s.CheckManaged)
		if e != nil {
			return ports.Target{}, e
		}
		return fresh.Target(ctx, id, port)
	}
	cfg.ResolveIdentity = func(ctx context.Context, id string, port uint16) (ports.Target, error) {
		fresh, e := s.Services(s.CheckManaged)
		if e != nil {
			return ports.Target{}, e
		}
		return fresh.IdentityTarget(ctx, id, port)
	}
	return ports.New(cfg).Serve(ctx)
}
