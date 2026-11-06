//go:build !windows

package terminal

import (
	"os"
	"syscall"
)

func private(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && fi.Mode().Perm()&0077 == 0 && (fi.IsDir() || st.Nlink == 1)
}
