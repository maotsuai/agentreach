package service

// ToolNames is the complete fixed P4 model-visible service catalogue.
var ToolNames = []string{"service_plan", "service_start", "service_stop", "service_inspect", "service_logs", "service_list", "port_list", "port_inspect"}

// Tools describes the same typed operations implemented by the CLI.
func Tools() []any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer", "minimum": 1}
	obj := func(p map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false}
	}
	array := func(v any) map[string]any { return map[string]any{"type": "array", "items": v} }
	volume := obj(map[string]any{"Name": str, "Driver": str, "CreatedAt": str, "Options": map[string]any{"type": []string{"object", "null"}, "additionalProperties": str}, "Labels": map[string]any{"type": []string{"object", "null"}, "additionalProperties": str}}, "Name", "Driver", "CreatedAt")
	spec := obj(map[string]any{"schema": integer, "project_id": str, "driver": map[string]any{"type": "string", "enum": []string{"sidecar", "workspace"}}, "image": str, "argv": array(str), "cwd": str, "env": map[string]any{"type": "object", "additionalProperties": str}, "mounts": array(obj(map[string]any{"type": str, "source": str, "target": str, "read_only": map[string]any{"type": "boolean"}, "volume": volume}, "type", "source", "target", "read_only")), "network_id": str, "user": str, "resources": obj(map[string]any{"memory": integer, "nano_cpus": integer, "pids": integer}, "memory", "nano_cpus", "pids"), "health": obj(map[string]any{"protocol": str, "port": integer, "path": str, "timeout_ms": integer}, "protocol", "port", "timeout_ms"), "restart": str, "log_bytes": integer, "guest_ports": array(integer), "publish": array(obj(map[string]any{"guest_port": integer, "host_port": integer, "listen": str}, "guest_port", "host_port", "listen"))}, "schema", "project_id", "driver", "argv", "cwd", "env", "mounts", "network_id", "user", "resources", "health", "restart", "log_bytes", "guest_ports", "publish")
	out := []any{}
	add := func(name, description string, schema any) {
		out = append(out, map[string]any{"name": name, "description": description, "inputSchema": schema})
	}
	add("service_plan", "Validate and retain a sidecar specification and exact Host approval plan. Does not start it.", obj(map[string]any{"request_id": str, "spec": spec}, "request_id", "spec"))
	add("service_start", "Start a non-interactive workspace service under Guest policy with a durable request_id. Sidecars require service_plan and host_execute.", obj(map[string]any{"request_id": str, "spec": spec}, "request_id", "spec"))
	add("service_stop", "Stop the supervised workspace service backend; detached descendants are unconfirmed. Sidecar stop/restart requires an exact service_change Host plan and approval.", obj(map[string]any{"service_id": str}, "service_id"))
	add("service_inspect", "Observe actual service identity, process/readiness and retained operation; running alone is not ready.", obj(map[string]any{"service_id": str}, "service_id"))
	add("service_logs", "Read bounded service output. Workspace logs use an epoch cursor; Docker sidecars use a bounded tail.", obj(map[string]any{"service_id": str, "cursor": str, "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxRead}}, "service_id", "max_bytes"))
	add("service_list", "List bounded retained service metadata; inspect supplies the complete specification.", obj(map[string]any{}))
	add("port_list", "Read verified fixed loopback publications and Host proxy listeners; never starts a manager.", obj(map[string]any{}))
	add("port_inspect", "Inspect one same-project exposure. Use its ID/generation for an approved open_url; cannot connect to arbitrary endpoints.", obj(map[string]any{"exposure_id": str}, "exposure_id"))
	return out
}
