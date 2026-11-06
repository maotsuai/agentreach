package main

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bojieli/agentreach/internal/transport"
)

// Each case scripts one actual harness tool call. Fixture preparation and
// independent readback use Guest commands, never a substitute filesystem RPC.
type conformanceCase struct {
	patch         string
	command       string
	setup         string
	verify        string
	content       string
	wantResult    string
	wantFailure   bool
	hostAbsent    []string
	controlAbsent []string
	violation     string
	readPath      string
}

func validMode(mode, harness string) bool {
	if mode == "shell" || mode == "shell-cwd-race" {
		return true
	}
	if harness != "codex" {
		return false
	}
	switch mode {
	case "patch", "patch-update", "patch-delete", "patch-move", "patch-outside", "patch-move-outside":
		return true
	}
	return false
}

func launchCase(mode, root, cwd, marker string) conformanceCase {
	name := strings.ToLower(marker) + ".txt"
	file := path.Join(root, name)
	outside := "/tmp/agentreach-outside-" + strings.ToLower(marker)
	q := transport.ShellQuote
	c := conformanceCase{
		content:       marker + "\n",
		wantResult:    "Success. Updated the following files:",
		hostAbsent:    []string{file, outside},
		controlAbsent: []string{name, "moved-" + name},
		verify:        "cat " + q(file),
	}
	seed := "printf '%s\\n' " + q("before-"+marker) + " > " + q(file)
	update := "@@\n-before-" + marker + "\n+" + marker + "\n"
	switch mode {
	case "shell":
		c.wantResult = marker + "\n"
		c.command = "test -z \"${REACH_HOST_ONLY_CANARY+x}\" && test \"$(pwd -P)\" = " + q(cwd) + " && printf '%s\\n' " + q(marker) + " > " + q(file) + " && cat " + q(file) + " && hostname"
	case "shell-cwd-race":
		// The driver continuously swaps the persisted cwd with an escaping
		// symlink. Safe rejection is allowed; any command body that starts
		// outside the root leaves an independently observable Guest canary.
		c.wantResult = marker + "\n"
		c.violation = outside
		c.readPath = file
		c.command = "case \"$(pwd -P)\" in " + q(root) + "|" + q(root) + "/*) ;; *) printf invalid-cwd > " + q(outside) + "; exit 91 ;; esac; test -z \"${REACH_HOST_ONLY_CANARY+x}\" && printf '%s\\n' " + q(marker) + " > " + q(file) + " && cat " + q(file) + " && hostname"
	case "patch":
		c.patch = "*** Add File: " + name + "\n+" + marker + "\n"
	case "patch-update":
		c.setup = seed
		c.patch = "*** Update File: " + name + "\n" + update
	case "patch-delete":
		c.setup = seed
		c.patch = "*** Delete File: " + name + "\n"
		c.verify = absentCommand(file)
		c.content = ""
	case "patch-move":
		c.setup = seed
		moved := path.Join(root, "moved-"+name)
		c.patch = "*** Update File: " + name + "\n*** Move to: moved-" + name + "\n" + update
		c.verify = absentCommand(file) + " && cat " + q(moved)
		c.hostAbsent = append(c.hostAbsent, moved)
	case "patch-outside":
		c.patch = "*** Add File: " + outside + "\n+" + marker + "\n"
		c.verify = absentCommand(outside)
		c.content = ""
		// Codex 0.148.0 hides the backend error category in native patch
		// results. Require its actual failed-write result for this exact
		// target, then independently verify Guest and Host absence.
		c.wantResult = "Failed to write file " + outside
		c.wantFailure = true
	case "patch-move-outside":
		c.setup = seed
		c.patch = "*** Update File: " + name + "\n*** Move to: " + outside + "\n" + update
		c.verify = absentCommand(outside) + " && cat " + q(file)
		c.content = "before-" + marker + "\n"
		c.wantResult = "Failed to write file " + outside
		c.wantFailure = true
	}
	if c.patch != "" {
		c.patch = "*** Begin Patch\n" + c.patch + "*** End Patch\n"
	}
	return c
}

func absentCommand(file string) string {
	q := transport.ShellQuote(file)
	return "test ! -e " + q + " && test ! -L " + q
}

// A shared bind may deliberately use the same Host and Guest path. Its
// approved projection is expected on the Host; control-cwd canaries remain
// separate and must still be absent.
func approvedSharedProjection(hostRoot, guestRoot, file string) bool {
	if file != guestRoot && !strings.HasPrefix(file, guestRoot+"/") {
		return false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(file, guestRoot), "/")
	expected := filepath.Join(hostRoot, filepath.FromSlash(rel))
	actualInfo, actualErr := os.Stat(file)
	expectedInfo, expectedErr := os.Stat(expected)
	return actualErr == nil && expectedErr == nil && os.SameFile(actualInfo, expectedInfo)
}
