//go:build linux || darwin || freebsd || openbsd || netbsd

package rooted

import (
	"fmt"
	"io/fs"
	"syscall"
)

func identity(i fs.FileInfo) (string, error) {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported: root identity unavailable")
	}
	return fmt.Sprintf("%d:%d", s.Dev, s.Ino), nil
}
func singleLink(i fs.FileInfo) error {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || s.Nlink != 1 {
		return fmt.Errorf("path_denied: multiply linked content file")
	}
	return nil
}
