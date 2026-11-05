package harnessprobe

import "strings"

// ManagedClaudeArgs is shared by strict probes and the real managed launcher.
// Only the controlled settings may supply hooks, and only Bash is available.
func ManagedClaudeArgs(settings string) []string {
	return []string{"--setting-sources", "", "--settings", settings, "--tools", "Bash", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
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
