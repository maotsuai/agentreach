//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd

package rooted

import (
	"fmt"
	"os"
)

func openRead(_ *os.Root, _ string) (*os.File, error) {
	return nil, fmt.Errorf("unsupported: rooted reads require Unix")
}
