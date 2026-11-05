package orbstack

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bojieli/agentreach/internal/runtime/docker"
)

// verifyControlHome accepts exactly one UID/home pair from dscacheutil. Other
// account fields are irrelevant; repeated identities or missing fields fail.
func verifyControlHome(output []byte, uid int, expected string) error {
	fields := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "uid" && key != "dir" {
			continue
		}
		if _, exists := fields[key]; exists {
			return docker.Fail("storage_unavailable", "ambiguous native account identity")
		}
		fields[key] = strings.TrimSpace(value)
	}
	if fields["uid"] != strconv.Itoa(uid) || fields["dir"] == "" {
		return docker.Fail("storage_unavailable", "native account UID/home is missing or mismatched")
	}
	home := fields["dir"]
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil || !filepath.IsAbs(home) || canonical != home || filepath.Clean(home) != home || home != expected {
		return docker.Fail("policy_denied", "control_home does not match the canonical native account home")
	}
	return nil
}
