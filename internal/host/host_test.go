package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/policy"
)

func TestMCPRuntimeFailureLatchesOnlyThisConnection(t *testing.T) {
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	enabled, failed := false, false
	checks := 0
	s := policy.New(base, policy.Binding{ProjectID: "test", RecordDigest: strings.Repeat("a", 64), Generation: 1}, func(context.Context) error {
		if enabled {
			checks++
			if !failed {
				failed = true
				return fmt.Errorf("storage_unavailable: transient fixture")
			}
		}
		return nil
	}, nil)
	if e = s.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = s.Set(context.Background(), "open_url", "allow"); e != nil {
		t.Fatal(e)
	}
	enabled = true
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}` + "\n"
	plan := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"host_plan","arguments":{"capability":"open_url","target_id":"exposure","generation":1,"params":{"path":"/"}}}}` + "\n"
	status := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"host_status","arguments":{}}}` + "\n"
	x := Executor{Store: s}
	var out bytes.Buffer
	if e = x.Serve(context.Background(), strings.NewReader(init+plan+status+plan), &out); e != nil {
		t.Fatal(e)
	}
	if checks != 1 || strings.Count(out.String(), `"isError":true`) != 3 {
		t.Fatal(out.String(), checks)
	}
	v, e := s.Inspect()
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Pending) != 0 {
		t.Fatal("retired connection created plan")
	}
	out.Reset()
	if e = x.Serve(context.Background(), strings.NewReader(init+plan), &out); e != nil {
		t.Fatal(e)
	}
	v, e = s.Inspect()
	if e != nil || len(v.Pending) != 1 {
		t.Fatal("new connection did not revalidate", e)
	}
}

func TestMCPPolicyDenialDoesNotLatchTarget(t *testing.T) {
	s, _ := setup(t)
	x := Executor{Store: s}
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"host_plan","arguments":{"capability":"arbitrary_host_shell","target_id":"exposure","generation":1,"params":{}}}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"host_plan","arguments":{"capability":"open_url","target_id":"exposure","generation":1,"params":{}}}}
`
	var out bytes.Buffer
	if e := x.Serve(context.Background(), strings.NewReader(in), &out); e != nil {
		t.Fatal(e)
	}
	v, e := s.Inspect()
	if e != nil || len(v.Pending) != 1 {
		t.Fatal(out.String(), e)
	}
}

func TestMCPExecuteFailureRetainsUnknownOperation(t *testing.T) {
	s, b := setup(t)
	p, e := s.Plan(context.Background(), policy.HostRequest{Capability: "open_url", TargetID: "exposure", Generation: 1, Params: policy.HostParams{Path: "/"}})
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.Approve(context.Background(), p.ID, p.Digest, policy.DefaultTTL)
	if e != nil {
		t.Fatal(e)
	}
	x := Executor{Store: s}
	x.Resolve = func(context.Context, string) (Exposure, error) {
		return Exposure{b, "exposure", 1, true, "127.0.0.1", 8080}, nil
	}
	launched := 0
	x.launch = func(context.Context, string) error { launched++; return io.ErrClosedPipe }
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}`
	args, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "host_execute", "arguments": map[string]string{"request_id": p.ID, "approval_id": a.ID}}})
	var out bytes.Buffer
	if e = x.Serve(context.Background(), strings.NewReader(init+"\n"+string(args)+"\n"), &out); e != nil {
		t.Fatal(e)
	}
	v, e := s.Inspect()
	if e != nil {
		t.Fatal(e)
	}
	if launched != 1 || len(v.Operations) != 1 || v.Operations[0].Outcome != "outcome_unknown" {
		t.Fatal("invalid dispatch fixture")
	}
	var response struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if e = json.Unmarshal([]byte(lines[1]), &response); e != nil {
		t.Fatal(e)
	}
	var body struct {
		Error     string           `json:"error"`
		Operation policy.Operation `json:"operation"`
	}
	if e = json.Unmarshal([]byte(response.Result.Content[0].Text), &body); e != nil {
		t.Fatal(e)
	}
	if !response.Result.IsError || body.Operation.ID != v.Operations[0].ID || body.Operation.Outcome != "outcome_unknown" || !strings.Contains(body.Error, io.ErrClosedPipe.Error()) || !strings.Contains(body.Error, "outcome_unknown") {
		t.Fatal(response.Result.Content[0].Text)
	}
}

func setup(t *testing.T) (*policy.Store, policy.Binding) {
	t.Helper()
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	b := policy.Binding{ProjectID: "test", RecordDigest: strings.Repeat("a", 64), Generation: 1}
	s := policy.New(base, b, func(context.Context) error { return nil }, nil)
	if e = s.Init(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = s.Set(context.Background(), "open_url", "approval"); e != nil {
		t.Fatal(e)
	}
	return s, b
}

func TestTypedExecutorApprovalAndGeneration(t *testing.T) {
	for _, mode := range []string{"ok", "foreign", "generation", "unhealthy", "non_loopback", "changed", "production"} {
		t.Run(mode, func(t *testing.T) {
			s, b := setup(t)
			p, e := s.Plan(context.Background(), policy.HostRequest{Capability: "open_url", TargetID: "exposure", Generation: 3, Params: policy.HostParams{Path: "/hello world"}})
			if e != nil {
				t.Fatal(e)
			}
			a, e := s.Approve(context.Background(), p.ID, p.Digest, policy.DefaultTTL)
			if e != nil {
				t.Fatal(e)
			}
			x := Executor{Store: s}
			calls, launches := 0, 0
			x.Resolve = func(context.Context, string) (Exposure, error) {
				calls++
				ex := Exposure{b, "exposure", 3, true, "127.0.0.1", 8123}
				switch mode {
				case "foreign":
					ex.Binding.ProjectID = "other"
				case "generation":
					ex.Generation++
				case "unhealthy":
					ex.Healthy = false
				case "non_loopback":
					ex.Address = "0.0.0.0"
				case "changed":
					if calls > 1 {
						ex.Port++
					}
				}
				return ex, nil
			}
			if mode == "production" {
				x.Resolve = nil
			}
			x.launch = func(_ context.Context, u string) error {
				launches++
				if u != "http://127.0.0.1:8123/hello%20world" {
					t.Fatal(u)
				}
				return nil
			}
			_, e = x.Execute(context.Background(), p.ID, a.ID)
			if mode == "ok" {
				if e != nil || launches != 1 {
					t.Fatalf("launch=%d %v", launches, e)
				}
				_, e = x.Execute(context.Background(), p.ID, a.ID)
				if e == nil {
					t.Fatal("replay")
				}
			} else if e == nil || launches != 0 {
				t.Fatalf("launch=%d %v", launches, e)
			}
		})
	}
}

func TestMCPOnlyFixedToolsAndStrictArguments(t *testing.T) {
	s, _ := setup(t)
	x := Executor{Store: s}
	var in bytes.Buffer
	frames := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}},
	}
	for i, c := range []string{"host_plan", "approve", "revoke", "host_shell", "host_execute"} {
		args := map[string]any{"argv": []string{"touch", "canary"}}
		frames = append(frames, map[string]any{"jsonrpc": "2.0", "id": i + 3, "method": "tools/call", "params": map[string]any{"name": c, "arguments": args}})
	}
	for _, f := range frames {
		b, _ := json.Marshal(f)
		fmt.Fprintln(&in, string(b))
	}
	var out bytes.Buffer
	if e := x.Serve(context.Background(), &in, &out); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 7 {
		t.Fatal(out.String())
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if e := json.Unmarshal([]byte(lines[1]), &listed); e != nil {
		t.Fatal(e)
	}
	if len(listed.Result.Tools) != len(ToolNames) {
		t.Fatal(lines[1])
	}
	for i, n := range ToolNames {
		if listed.Result.Tools[i].Name != n {
			t.Fatal(lines[1])
		}
	}
	for _, line := range lines[2:] {
		if !strings.Contains(line, `"isError":true`) {
			t.Fatal(line)
		}
	}
	if e := x.Serve(context.Background(), strings.NewReader(strings.Repeat("x", policy.MaxRequest+1)), &out); e == nil {
		t.Fatal("oversize MCP accepted")
	}
}

func TestUnimplementedDriverRejectsBeforeIntent(t *testing.T) {
	s, _ := setup(t)
	if e := s.Set(context.Background(), "port_expose", "allow"); e != nil {
		t.Fatal(e)
	}
	p, e := s.Plan(context.Background(), policy.HostRequest{Capability: "port_expose", TargetID: "service", Generation: 1, Params: policy.HostParams{ServiceID: "service", GuestPort: 3000, Listen: "127.0.0.1"}})
	if e != nil {
		t.Fatal(e)
	}
	x := Executor{Store: s}
	_, e = x.Execute(context.Background(), p.ID, "")
	if e == nil || !strings.Contains(e.Error(), "unsupported") {
		t.Fatal(e)
	}
	v, e := s.Inspect()
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Operations) != 0 {
		t.Fatal("unsupported action consumed intent")
	}
}
