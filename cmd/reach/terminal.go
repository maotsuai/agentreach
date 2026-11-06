package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/bojieli/agentreach/internal/session"
	"github.com/bojieli/agentreach/internal/terminal"
)

func cmdTerminal(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: reach --project PROJECT terminal create|write|read|resize|interrupt|inspect|list|attach|close (strict JSON on stdin)")
	}
	s, e := session.Load(sessionNameFromEnv(""))
	if e != nil {
		return e
	}
	if s.Managed == nil {
		return fmt.Errorf("unsupported: persistent terminals require a managed project")
	}
	b, e := io.ReadAll(io.LimitReader(os.Stdin, terminal.MaxFrame+1))
	if e != nil {
		return e
	}
	q, e := terminal.DecodeRequest(b)
	if e != nil {
		return e
	}
	m, e := s.Terminals(s.CheckManaged)
	if e != nil {
		return e
	}
	v, e := m.Call(ctx, args[0], q)
	if e != nil {
		if v != nil {
			_ = printJSON(map[string]any{"error": e.Error(), "terminal": v})
		}
		return e
	}
	return printJSON(v)
}
