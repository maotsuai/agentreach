package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/bojieli/agentreach/internal/terminal"
	"io"
	"strings"
	"sync"

	"github.com/bojieli/agentreach/internal/policy"
)

// ToolNames is the complete model-visible capability surface.
var ToolNames = append([]string{"host_plan", "host_execute", "host_status"}, terminal.ToolNames...)

func tool(name, description string, properties map[string]any, required []string) any {
	return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
}

// Tools returns the fixed MCP catalogue.
func Tools() []any {
	s := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer", "minimum": 1}
	params := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"path": s, "service_id": s, "guest_port": integer, "host_port": map[string]any{"type": "integer", "minimum": 0}, "listen": s, "operation": s, "spec_digest": s}}
	return append([]any{
		tool("host_plan", "Plan a typed Host capability; this does not execute or approve it.", map[string]any{"capability": s, "target_id": s, "generation": integer, "params": params}, []string{"capability", "target_id", "generation", "params"}),
		tool("host_execute", "Execute an exact planned request with its operator approval, when required.", map[string]any{"request_id": s, "approval_id": s}, []string{"request_id"}),
		tool("host_status", "Read project policy counts or the exact status of a request/operation.", map[string]any{"request_id": s, "operation_id": s}, []string{}),
	}, terminal.Tools()...)
}

type call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	// MCP reserves _meta as transport metadata. It conveys no authority and
	// is deliberately never passed to a capability or included in its digest.
	Meta json.RawMessage `json:"_meta,omitempty"`
}
type executeArgs struct {
	RequestID  string `json:"request_id"`
	ApprovalID string `json:"approval_id,omitempty"`
}

// Call strictly decodes and routes a model request to the shared policy engine.
func (e *Executor) Call(ctx context.Context, name string, b json.RawMessage) (any, error) {
	if strings.HasPrefix(name, "terminal_") {
		if e.Terminal == nil {
			return nil, fmt.Errorf("unsupported: terminal manager unavailable")
		}
		q, err := terminal.DecodeRequest(b)
		if err != nil {
			return nil, err
		}
		return e.Terminal.Call(ctx, strings.TrimPrefix(name, "terminal_"), q)
	}
	switch name {
	case "host_plan":
		var h policy.HostRequest
		if err := policy.VerifyEncoded(b, &h); err != nil {
			return nil, err
		}
		return e.Store.Plan(ctx, h)
	case "host_execute":
		var a executeArgs
		if err := policy.VerifyEncoded(b, &a); err != nil {
			return nil, err
		}
		return e.Execute(ctx, a.RequestID, a.ApprovalID)
	case "host_status":
		var a struct {
			RequestID   string `json:"request_id,omitempty"`
			OperationID string `json:"operation_id,omitempty"`
		}
		if err := policy.VerifyEncoded(b, &a); err != nil {
			return nil, err
		}
		v, err := e.Store.Inspect()
		if err != nil {
			return nil, err
		}
		if a.RequestID != "" && a.OperationID != "" {
			return nil, fmt.Errorf("policy_denied: choose one status identity")
		}
		if a.OperationID != "" {
			for _, o := range v.Operations {
				if o.ID == a.OperationID {
					return o, nil
				}
			}
			return nil, fmt.Errorf("operation_unknown")
		}
		if a.RequestID != "" {
			for _, p := range v.Pending {
				if p.ID == a.RequestID {
					ops := []policy.Operation{}
					for _, o := range v.Operations {
						if o.RequestID == p.ID {
							ops = append(ops, o)
						}
					}
					return map[string]any{"request": p, "operations": ops}, nil
				}
			}
			return nil, fmt.Errorf("request_unknown")
		}
		return map[string]any{"binding": v.Binding, "policy_revision": v.Revision, "guest_allow": v.GuestAllow, "host": v.Host, "pending_count": len(v.Pending), "approval_count": len(v.Approvals), "operation_count": len(v.Operations), "event_count": len(v.Events)}, nil
	default:
		return nil, fmt.Errorf("policy_denied: unknown Host tool")
	}
}

// Serve is a bounded, sequential stdio MCP server. No operator methods exist.
func (e *Executor) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	// One lifetime per MCP connection, shared by pre-call checks and the
	// checks under the ledger lock. Only target guard failures latch.
	var guardMu sync.Mutex
	var guardFailure error
	var terminals *terminal.Manager
	ready := func(ctx context.Context) error {
		guardMu.Lock()
		defer guardMu.Unlock()
		if guardFailure == nil && terminals != nil {
			guardFailure = terminals.Failure()
		}
		if guardFailure == nil {
			guardFailure = e.Store.CheckTarget(ctx)
		}
		return guardFailure
	}
	connection := *e
	connection.Store = e.Store.WithTargetCheck(ready)
	if e.OpenTerminals != nil {
		var err error
		connection.Terminal, err = e.OpenTerminals(ready)
		if err != nil {
			return err
		}
	}
	terminals = connection.Terminal
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), policy.MaxRequest+1)
	enc := json.NewEncoder(out)
	initialized := false
	for scan.Scan() {
		var r struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := policy.VerifyEncoded(scan.Bytes(), &r); err != nil {
			return fmt.Errorf("invalid MCP frame: %w", err)
		}
		if r.JSONRPC != "2.0" {
			return fmt.Errorf("invalid MCP version")
		}
		if len(r.ID) == 0 {
			if r.Method == "notifications/initialized" && initialized {
				continue
			}
			return fmt.Errorf("unsupported MCP notification")
		}
		var value any
		var err error
		switch r.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string          `json:"protocolVersion"`
				Capabilities    json.RawMessage `json:"capabilities"`
				ClientInfo      json.RawMessage `json:"clientInfo"`
			}
			err = policy.VerifyEncoded(r.Params, &p)
			if initialized {
				err = fmt.Errorf("already initialized")
			}
			if err == nil {
				initialized = true
				value = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "reach-host", "version": "1"}}
			}
		case "ping":
			value = map[string]any{}
		case "tools/list":
			if !initialized {
				err = fmt.Errorf("not initialized")
			} else {
				value = map[string]any{"tools": Tools()}
			}
		case "tools/call":
			if !initialized {
				err = fmt.Errorf("not initialized")
				break
			}
			var c call
			err = policy.VerifyEncoded(r.Params, &c)
			if err == nil {
				var result any
				err = ready(ctx)
				if err == nil || strings.HasPrefix(c.Name, "terminal_") {
					result, err = connection.Call(ctx, c.Name, c.Arguments)
				}
				var msg string
				if err != nil {
					msg = err.Error()
					if v, ok := result.(terminal.Result); ok {
						b, _ := json.Marshal(map[string]any{"error": msg, "terminal": v})
						msg = string(b)
					}
					if op, ok := result.(policy.Operation); ok && op.ID != "" {
						b, _ := json.Marshal(map[string]any{"error": msg, "operation": op})
						msg = string(b)
					}
				} else {
					b, _ := json.Marshal(result)
					msg = string(b)
				}
				value = map[string]any{"content": []any{map[string]string{"type": "text", "text": msg}}, "isError": err != nil}
				err = nil
			}
		default:
			err = fmt.Errorf("unsupported MCP method")
		}
		response := map[string]any{"jsonrpc": "2.0", "id": r.ID}
		if err != nil {
			response["error"] = map[string]any{"code": -32602, "message": err.Error()}
		} else {
			response["result"] = value
		}
		if err = enc.Encode(response); err != nil {
			return err
		}
	}
	return scan.Err()
}
