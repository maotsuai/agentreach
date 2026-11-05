package hostpath

import (
	"fmt"
	"os"
	"syscall"
)

// PrivateFile verifies a control leaf's link count using its actual handle.
func PrivateFile(p string) error {
	fi, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("control file is not regular: %s", p)
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("control file has hard links: %s", p)
	}
	return nil
}
