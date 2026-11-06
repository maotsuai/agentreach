//go:build linux || darwin || freebsd || openbsd || netbsd

package rooted

import (
	"os"
	"syscall"
)

// Nonblocking open allows the descriptor type check to reject FIFOs without
// waiting for another process. Root still performs all pathname resolution.
func openRead(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
