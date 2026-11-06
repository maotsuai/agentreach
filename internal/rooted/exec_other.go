//go:build !linux

package rooted

import (
	"fmt"
	"io"
)

func Exec(root, cwd, encoded string) error {
	return fmt.Errorf("unsupported: rooted launcher requires Linux")
}

// ReportCwd is available only in the Linux managed Guest.
func ReportCwd(_ string, _ io.Writer) error {
	return fmt.Errorf("unsupported: physical cwd reporting requires Linux")
}
