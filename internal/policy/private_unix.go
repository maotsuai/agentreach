//go:build !windows

package policy

import (
	"os"
	"syscall"
)

func private(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && fi.Mode().Perm()&0o077 == 0 && (fi.IsDir() || st.Nlink == 1)
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
