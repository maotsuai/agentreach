package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/session"
)

// Configuration is generated exclusively from the trusted running binary and
// immutable session. No server command, argv or cwd comes from a model/repo.
func Configuration(s *session.Session, safePath string) (string, string, error) {
	if s.Managed == nil {
		return "", "", fmt.Errorf("policy_denied: managed bridge requires a binding")
	}
	r, e := s.ManagedProject()
	if e != nil {
		return "", "", e
	}
	base, e := project.Home()
	if e != nil {
		return "", "", e
	}
	self, e := os.Executable()
	if e != nil {
		return "", "", e
	}
	resolved, e := filepath.EvalSymlinks(self)
	if e != nil {
		return "", "", e
	}
	for _, p := range []string{self, resolved, base} {
		for _, m := range r.Spec.BindMounts {
			if m.ReadOnly {
				continue
			}
			yes, e := hostpath.Contains(m.Source, p)
			if e != nil || yes {
				return "", "", fmt.Errorf("policy_denied: bridge path overlaps writable bind")
			}
		}
	}
	if e = s.CheckControlPaths(); e != nil {
		return "", "", e
	}
	args := []string{"host-bridge", "--session", s.Name}
	env := map[string]string{"REACH_HOME": base, "REACH_SESSION": s.Name, "REACH_MANAGED": "1", "REACH_MANAGED_PROJECT": s.Managed.ProjectID, "REACH_MANAGED_RECORD": s.Managed.RecordSHA256, "PATH": safePath}
	b, e := json.Marshal(map[string]any{"mcpServers": map[string]any{"reach_host": map[string]any{"type": "stdio", "command": resolved, "args": args, "env": env}}})
	if e != nil {
		return "", "", e
	}
	a, _ := json.Marshal(args)
	tools, _ := json.Marshal(ToolNames)
	toml := "[mcp_servers.reach_host]\nenvironment_id = \"reach\"\nrequired = true\ndefault_tools_approval_mode = \"approve\"\ncommand = " + strconv.Quote(resolved) + "\nargs = " + string(a) + "\ncwd = " + strconv.Quote(base) + "\nenabled_tools = " + string(tools) + "\n[mcp_servers.reach_host.env]\n"
	for _, k := range []string{"REACH_HOME", "REACH_SESSION", "REACH_MANAGED", "REACH_MANAGED_PROJECT", "REACH_MANAGED_RECORD", "PATH"} {
		toml += k + " = " + strconv.Quote(env[k]) + "\n"
	}
	return string(b), toml, nil
}
