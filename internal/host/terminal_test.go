package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/policy"
	"github.com/bojieli/agentreach/internal/terminal"
)

func TestTerminalMCPUnknownPreservesOperationAndNeverReplays(t *testing.T) {
	for _, guardFault := range []bool{false, true} {
		t.Run(fmt.Sprint(guardFault), func(t *testing.T) {
			base, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			b := policy.Binding{ProjectID: "terminal", RecordDigest: strings.Repeat("a", 64), Generation: 1}
			failNext := false
			check := func(context.Context) error {
				if failNext {
					failNext = false
					return fmt.Errorf("storage_unavailable: injected once")
				}
				return nil
			}
			p := policy.New(base, b, check, nil)
			if e = p.Init(context.Background()); e != nil {
				t.Fatal(e)
			}
			writes := 0
			var saved terminal.Observation
			inspectFault := false
			cfg := terminal.Config{Base: base, Binding: b, Runtime: strings.Repeat("b", 64), Policy: p, Check: check, Paths: func() error { return nil }, Launch: func(context.Context, string, []string, string) (string, error) { return "rooted", nil }}
			cfg.Dispatch = func(_ context.Context, q terminal.GuestRequest) (terminal.Observation, error) {
				if q.Op == "create" {
					q.Identity.Server = "boot:123:456"
					q.Identity.Session = "$0"
					q.Identity.Pane = "%0"
					saved = terminal.Observation{Identity: q.Identity, State: "running"}
				}
				if q.Op == "inspect" && inspectFault {
					failNext = true
					inspectFault = false
				}
				if q.Op == "write" {
					writes++
					return saved, fmt.Errorf("response lost")
				}
				return saved, nil
			}
			m := terminal.New(cfg)
			request, _ := terminal.NewID()
			v, e := m.Call(context.Background(), "create", terminal.Request{RequestID: request, Argv: []string{"python3"}, Cols: 80, Rows: 24})
			if e != nil {
				t.Fatal(e)
			}
			id := v.(terminal.Result).TerminalID
			input, _ := terminal.NewID()
			x := Executor{Store: p, OpenTerminals: func(ready func(context.Context) error) (*terminal.Manager, error) {
				copyConfig := cfg
				copyConfig.Check = ready
				return terminal.New(copyConfig), nil
			}}
			init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}` + "\n"
			frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "terminal_write", "arguments": terminal.Request{TerminalID: id, InputID: input, Key: "Enter"}}})
			status := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"host_status","arguments":{}}}` + "\n"
			inspectFault = guardFault
			var out bytes.Buffer
			if e = x.Serve(context.Background(), strings.NewReader(init+string(frame)+"\n"+status+string(frame)+"\n"), &out); e != nil {
				t.Fatal(e)
			}
			if guardFault {
				if writes != 0 || strings.Count(out.String(), `"isError":true`) != 3 {
					t.Fatal(writes, out.String())
				}
			} else {
				if writes != 1 || !strings.Contains(out.String(), "outcome_unknown") || !strings.Contains(out.String(), "operation_id") || !strings.Contains(out.String(), `\"terminal\"`) {
					t.Fatal(writes, out.String())
				}
			}
		})
	}
}

func TestBackendIdentityFailureRetiresWholeMCPConnection(t *testing.T) {
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	binding := policy.Binding{ProjectID: "backend", RecordDigest: strings.Repeat("a", 64), Generation: 1}
	check := func(context.Context) error { return nil }
	p := policy.New(base, binding, check, nil)
	if e = p.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	var saved terminal.Observation
	bad := false
	cfg := terminal.Config{Base: base, Binding: binding, Runtime: strings.Repeat("b", 64), Policy: p, Check: check, Paths: func() error { return nil }, Launch: func(context.Context, string, []string, string) (string, error) { return "rooted", nil }}
	cfg.Dispatch = func(_ context.Context, q terminal.GuestRequest) (terminal.Observation, error) {
		if q.Op == "create" {
			q.Identity.Server = "boot:123:456"
			q.Identity.Session = "$0"
			q.Identity.Pane = "%0"
			saved = terminal.Observation{Identity: q.Identity, State: "running"}
		}
		o := saved
		if bad && q.Op == "inspect" {
			bad = false
			o.Identity.Nonce, _ = terminal.NewID()
		}
		return o, nil
	}
	m := terminal.New(cfg)
	id, _ := terminal.NewID()
	created, e := m.Call(context.Background(), "create", terminal.Request{RequestID: id, Argv: []string{"python3"}, Cols: 80, Rows: 24})
	if e != nil {
		t.Fatal(e)
	}
	tid := created.(terminal.Result).TerminalID
	x := Executor{Store: p, OpenTerminals: func(ready func(context.Context) error) (*terminal.Manager, error) {
		copyConfig := cfg
		copyConfig.Check = ready
		return terminal.New(copyConfig), nil
	}}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}` + "\n"
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "terminal_inspect", "arguments": map[string]string{"terminal_id": tid}}})
	status := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"host_status","arguments":{}}}` + "\n"
	bad = true
	var out bytes.Buffer
	if e = x.Serve(context.Background(), strings.NewReader(initialize+string(frame)+"\n"+status+string(frame)+"\n"), &out); e != nil {
		t.Fatal(e)
	}
	if strings.Count(out.String(), `"isError":true`) != 3 {
		t.Fatal(out.String())
	}
	out.Reset()
	if e = x.Serve(context.Background(), strings.NewReader(initialize+string(frame)+"\n"), &out); e != nil || strings.Contains(out.String(), `"isError":true`) {
		t.Fatal(out.String(), e)
	}
}
