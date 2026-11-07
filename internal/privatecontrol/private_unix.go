//go:build !windows

package privatecontrol

import (
	"os"
	"syscall"
)

// Private requires current ownership, private permissions and an unshared leaf.
func Private(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && fi.Mode().Perm()&0077 == 0 && (fi.IsDir() || st.Nlink == 1)
}
