//go:build !linux

// Package terminalguest implements the fixed Linux tmux/collector helper modes.
package terminalguest

import (
	"fmt"
	"io"
)

// Call is unavailable outside the Linux Guest.
func Call(io.Reader, io.Writer) error {
	return fmt.Errorf("unsupported: terminal helper requires Linux")
}

// Collect is unavailable outside the Linux Guest.
func Collect(string) error { return fmt.Errorf("unsupported: terminal helper requires Linux") }

// Pane is unavailable outside the Linux Guest.
func Pane(string, string) error { return fmt.Errorf("unsupported: terminal helper requires Linux") }

// Exited is unavailable outside the Linux Guest.
func Exited(string, string) error { return fmt.Errorf("unsupported: terminal helper requires Linux") }
