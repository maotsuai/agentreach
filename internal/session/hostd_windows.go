//go:build windows

package session

import "os/exec"

func detachHostManager(*exec.Cmd) {}
