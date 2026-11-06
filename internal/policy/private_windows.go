package policy

import (
	"fmt"
	"os"
)

func private(os.FileInfo) bool { return false }
func syncDir(string) error {
	return fmt.Errorf("unsupported: native managed policy requires Darwin or Linux")
}
