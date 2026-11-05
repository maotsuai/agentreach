//go:build !windows

package hostpath

import (
	"fmt"
	"os"
	"syscall"
)

// PrivateFile rejects control leaves that could share contents with a Guest
// through a different hard-link spelling. Missing files may be created later.
func PrivateFile(p string) error {
	fi, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		return fmt.Errorf("control file is not an unshared regular file: %s", p)
	}
	return nil
}
