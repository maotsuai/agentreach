package terminal

// ToolNames is the fixed model-visible Guest terminal tool surface.
var ToolNames = []string{"terminal_create", "terminal_write", "terminal_read", "terminal_resize", "terminal_interrupt", "terminal_inspect", "terminal_list", "terminal_attach", "terminal_close"}

// Tools describes the same typed operations exposed by the operator CLI.
func Tools() []any {
	s := map[string]any{"type": "string"}
	id := map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"}
	cols := map[string]any{"type": "integer", "minimum": 1, "maximum": 500}
	rows := map[string]any{"type": "integer", "minimum": 1, "maximum": 300}
	specs := []struct {
		name, description string
		props             map[string]any
		required          []string
	}{
		{"terminal_create", "Create a persistent Guest text terminal; request_id is durable and must not be replayed with changed parameters.", map[string]any{"request_id": id, "cwd": s, "argv": map[string]any{"type": "array", "items": s, "minItems": 1, "maxItems": 128}, "cols": cols, "rows": rows}, []string{"request_id", "argv", "cols", "rows"}},
		{"terminal_write", "Send base64 data OR the Enter key using a durable input_id. Accepted means tmux received it, not that the application executed it.", map[string]any{"terminal_id": id, "input_id": id, "data": map[string]any{"type": "string", "contentEncoding": "base64"}, "key": map[string]any{"type": "string", "enum": []string{"Enter"}}}, []string{"terminal_id", "input_id"}},
		{"terminal_read", "Read original PTY bytes as base64 from a stable epoch/cursor. stdout and stderr are merged. Check gap and complete.", map[string]any{"terminal_id": id, "cursor": s, "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxRead}}, []string{"terminal_id", "max_bytes"}},
		{"terminal_resize", "Resize the verified Guest PTY.", map[string]any{"terminal_id": id, "cols": cols, "rows": rows}, []string{"terminal_id", "cols", "rows"}},
		{"terminal_interrupt", "Send interactive Ctrl-C to the verified Guest terminal.", map[string]any{"terminal_id": id, "signal": map[string]any{"type": "string", "enum": []string{"Ctrl-C"}}}, []string{"terminal_id"}},
	}
	out := []any{}
	for _, sp := range specs {
		out = append(out, map[string]any{"name": sp.name, "description": sp.description, "inputSchema": map[string]any{"type": "object", "properties": sp.props, "required": sp.required, "additionalProperties": false}})
	}
	for _, op := range []string{"inspect", "list", "attach", "close"} {
		props := map[string]any{}
		required := []string{}
		if op != "list" {
			props["terminal_id"] = id
			required = append(required, "terminal_id")
		}
		description := "Inspect a retained Guest terminal with a newly verified connection."
		if op == "close" {
			description = "Close exactly this terminal pane. Detached descendants are unconfirmed; early close can report an output gap."
		}
		if op == "list" {
			description = "List retained Host terminal identities without starting or recreating any backend."
		}
		out = append(out, map[string]any{"name": "terminal_" + op, "description": description, "inputSchema": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}})
	}
	return out
}
