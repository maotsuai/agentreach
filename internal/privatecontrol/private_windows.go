//go:build windows

package privatecontrol

import "os"

// Private refuses unsupported native Windows resource storage.
func Private(os.FileInfo) bool { return false }
