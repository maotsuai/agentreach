package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bojieli/agentreach/internal/host"
	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/recovery"
	"github.com/bojieli/agentreach/internal/runtime/orbstack"
	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/transport"
)

// Claude 2.1.220 prefixes MCP subprocesses as local callbacks too. Admit only
// this generated exact command, and call the typed handler without a shell.
func claudeBridgeCommand(s *session.Session) (string, error) {
	self, e := selfPath()
	if e != nil {
		return "", e
	}
	self, e = managedHarnessExecutable(s, self)
	if e != nil {
		return "", e
	}
	return transport.ShellQuote(self) + " host-bridge --session " + transport.ShellQuote(s.Name), nil
}

func operatorPolicy(id string) (*policy.Store, error) {
	if os.Getenv("REACH_MANAGED") == "1" {
		return nil, fmt.Errorf("policy_denied: operator policy entry is unavailable to managed children")
	}
	r, e := project.Load(id)
	if e != nil {
		return nil, e
	}
	base, e := project.Home()
	if e != nil {
		return nil, e
	}
	b := session.Bind(r)
	check := func(ctx context.Context) error {
		current, e := project.Load(id)
		if e != nil {
			return e
		}
		if *session.Bind(current) != *b {
			return fmt.Errorf("identity_changed: project binding changed")
		}
		return orbstack.New(r).Check(ctx)
	}
	paths := func() error {
		s := &session.Session{Name: project.SessionNameFor(r), Managed: b}
		if e := s.CheckControlPaths(); e != nil {
			return e
		}
		return outsideWritableBinds(r, base)
	}
	return policy.New(project.ControlBase(base, r), policy.Binding{ProjectID: id, RecordDigest: b.RecordSHA256, Generation: r.Binding.Generation}, check, paths), nil
}

func cmdPolicy(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: reach policy init|inspect|set|approve|revoke <project> ...")
	}
	p, e := operatorPolicy(args[1])
	if e != nil {
		return e
	}
	switch args[0] {
	case "init":
		if len(args) != 2 {
			break
		}
		if e = p.Init(ctx); e != nil {
			return e
		}
		v, e := p.Inspect()
		if e != nil {
			return e
		}
		return printJSON(v)
	case "inspect":
		if len(args) != 2 {
			break
		}
		v, e := p.Inspect()
		if e != nil {
			return e
		}
		return printJSON(v)
	case "set":
		if len(args) != 4 {
			break
		}
		if e = p.Set(ctx, args[2], args[3]); e != nil {
			return e
		}
		v, e := p.Inspect()
		if e != nil {
			return e
		}
		return printJSON(v)
	case "approve":
		if len(args) != 4 && len(args) != 5 {
			break
		}
		ttl := policy.DefaultTTL
		if len(args) == 5 {
			ttl, e = time.ParseDuration(args[4])
			if e != nil {
				return e
			}
		}
		a, e := p.Approve(ctx, args[2], args[3], ttl)
		if e != nil {
			return e
		}
		return printJSON(a)
	case "revoke":
		if len(args) != 3 {
			break
		}
		if e = p.Revoke(ctx, args[2]); e != nil {
			return e
		}
		return printJSON(map[string]string{"status": "revoked", "approval_id": args[2]})
	}
	return fmt.Errorf("usage: reach policy init|inspect <project>; set <project> <capability|guest> allow|approval|deny; approve <project> <request_id> <exact_digest> [TTL]; revoke <project> <approval_id>")
}

func cmdHost(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: reach host plan|execute|status <project> (JSON on stdin)")
	}
	id := args[1]
	p, e := operatorPolicy(id)
	if e != nil {
		return e
	}
	b := []byte("{}")
	if args[0] != "status" {
		b, e = io.ReadAll(io.LimitReader(os.Stdin, policy.MaxRequest+1))
		if e != nil {
			return e
		}
	}
	x := host.Executor{Store: p}
	if args[0] == "execute" {
		x.Runtime = recovery.Executor{Controller: recovery.New(), ProjectID: id}
		v, err := p.Inspect()
		if err != nil {
			return err
		}
		var q struct {
			RequestID  string `json:"request_id"`
			ApprovalID string `json:"approval_id"`
		}
		if err := policy.VerifyEncoded(b, &q); err != nil {
			return err
		}
		needServices := false
		for _, pending := range v.Pending {
			if pending.ID == q.RequestID {
				needServices = pending.Request.Host.Capability != "runtime_change"
				break
			}
		}
		if needServices {
			s, e := managedTerminalSession(ctx, id, false)
			if e != nil {
				return e
			}
			m, e := s.Services(s.CheckManaged)
			if e != nil {
				return e
			}
			x.Services = m
			x.Resolve = host.ServiceResolver(m)
		}
	}
	v, e := x.Call(ctx, "host_"+args[0], b)
	if e != nil {
		if op, ok := v.(policy.Operation); ok && op.ID != "" {
			_ = printJSON(map[string]any{"error": e.Error(), "operation": op})
		}
		return e
	}
	return printJSON(v)
}

func cmdHostBridge(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "--session" {
		return fmt.Errorf("policy_denied: fixed host-bridge session required")
	}
	s, e := session.Load(args[1])
	if e != nil {
		return e
	}
	if s.Managed == nil {
		return fmt.Errorf("policy_denied: managed bridge required")
	}
	if e = s.CheckManaged(ctx); e != nil {
		return e
	}
	p, e := s.Policy(s.CheckManaged)
	if e != nil {
		return e
	}
	x := host.Executor{Store: p, OpenTerminals: s.Terminals, OpenServices: s.Services}
	return x.Serve(ctx, os.Stdin, os.Stdout)
}
