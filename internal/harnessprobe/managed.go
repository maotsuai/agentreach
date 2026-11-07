package harnessprobe

import (
	"github.com/bojieli/agentreach/internal/service"
	"github.com/bojieli/agentreach/internal/terminal"
	"strings"
)

// ManagedClaudeArgs is shared by strict probes and the real managed launcher.
// Only the controlled settings may supply hooks, and only Bash is available.
func ManagedClaudeArgs(settings string, bridge ...string) []string {
	config := `{"mcpServers":{}}`
	if len(bridge) > 0 {
		config = bridge[0]
	}
	args := []string{"--setting-sources", "", "--settings", settings, "--tools", "Bash", "--strict-mcp-config", "--mcp-config", config}
	if len(bridge) > 0 {
		args = append(args, "--allowedTools", "mcp__reach_host__host_plan", "mcp__reach_host__host_execute", "mcp__reach_host__host_status")
		for _, name := range append(append([]string{}, terminal.ToolNames...), service.ToolNames...) {
			args = append(args, "mcp__reach_host__"+name)
		}
	}
	return args
}

// ManagedEnvironment removes known shell/Node injection and workspace routing
// overrides. The harness-specific builder controls auth and configuration keys.
func ManagedEnvironment(env []string) []string {
	var clean []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		switch key {
		case "REACH_MANAGED", "REACH_MANAGED_PROJECT", "REACH_MANAGED_RECORD", "REACH_IN_SHELL_SHIM", "BASH_ENV", "ENV", "NODE_OPTIONS", "NODE_PATH", "REACH_EXEC_WORKSPACE", "ZDOTDIR", "SHELLOPTS", "BASHOPTS", "CDPATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH":
			continue
		}
		clean = append(clean, e)
	}
	return append(clean, "REACH_MANAGED=1")
}
