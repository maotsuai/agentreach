//go:build windows

package terminal

import "os"

func private(os.FileInfo) bool { return false }
