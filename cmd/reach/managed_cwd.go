package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/bojieli/agentreach/internal/hostpath"
)

var managedCwdName = regexp.MustCompile(`^claude-[0-9a-f]{4,64}-cwd$`)

type managedCwdRecord struct {
	root *os.Root
	name string
}

// Only the bookkeeping destination measured with Claude 2.1.220 is accepted.
// The directory is opened once; Root keeps every later operation anchored to
// it. Replacing a leaf never follows a symlink or truncates a hard-link target.
func openManagedCwdRecord(p string) (*managedCwdRecord, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || !managedCwdName.MatchString(filepath.Base(p)) {
		return nil, fmt.Errorf("policy_denied: unsupported managed cwd record")
	}
	parent, err := hostpath.Contains("/tmp", filepath.Dir(p))
	if err != nil || !parent {
		return nil, fmt.Errorf("policy_denied: cwd record must use the trusted temporary root")
	}
	a, err := os.Stat("/tmp")
	if err != nil {
		return nil, err
	}
	b, err := os.Stat(filepath.Dir(p))
	if err != nil || !os.SameFile(a, b) {
		return nil, fmt.Errorf("policy_denied: cwd record must be directly under the trusted temporary root")
	}
	root, err := os.OpenRoot("/tmp")
	if err != nil {
		return nil, err
	}
	r := &managedCwdRecord{root: root, name: filepath.Base(p)}
	if fi, err := root.Lstat(r.name); err == nil && !fi.Mode().IsRegular() || err != nil && !os.IsNotExist(err) {
		_ = root.Close()
		return nil, fmt.Errorf("policy_denied: unsafe cwd record leaf")
	}
	return r, nil
}
func (r *managedCwdRecord) write(cwd string) error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".reach-cwd-" + hex.EncodeToString(random[:])
	f, err := r.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = r.root.Remove(tmp) }()
	if _, err := f.WriteString(cwd + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return r.root.Rename(tmp, r.name)
}
