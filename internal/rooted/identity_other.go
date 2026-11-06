//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd

package rooted

import (
	"fmt"
	"io/fs"
)

func identity(fs.FileInfo) (string, error) {
	return "", fmt.Errorf("unsupported: rooted helper requires Unix identity")
}
func singleLink(fs.FileInfo) error { return fmt.Errorf("unsupported: link identity unavailable") }
