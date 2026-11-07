//go:build !windows

package session

import (
	"os/exec"
	"syscall"
)

func detachHostManager(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
