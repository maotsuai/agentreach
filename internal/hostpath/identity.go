// Package hostpath compares Host paths using filesystem identity, including on
// case-insensitive filesystems. It never guesses identity by folding strings.
package hostpath

import (
	"fmt"
	"os"
	"path/filepath"
)

// Canonical rejects symlink ancestors, including for a not-yet-created path.
// Missing suffixes are inspected through their nearest existing ancestor.
func Canonical(p string, missing bool) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("path must be absolute and clean: %s", p)
	}
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			if resolved != p {
				return fmt.Errorf("path has a symlink ancestor: %s", p)
			}
			return nil
		}
		if !missing || !os.IsNotExist(err) || filepath.Dir(p) == p {
			return err
		}
		// A dangling symlink is not a missing directory.
		if _, statErr := os.Lstat(p); statErr == nil {
			return fmt.Errorf("unresolvable path: %s", p)
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		p = filepath.Dir(p)
	}
}

// Contains reports whether root is the same object as p or an ancestor of p.
// A missing root cannot contain an existing object; a missing p is compared
// through all its existing ancestors. Other inspection errors fail closed.
func Contains(root, p string) (bool, error) {
	r, err := os.Stat(root)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if os.IsNotExist(err) {
		return false, Canonical(root, true)
	}
	for {
		f, err := os.Stat(p)
		if err == nil && os.SameFile(r, f) {
			return true, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false, nil
		}
		p = parent
	}
}

// Overlaps checks both directions, including hard-linked regular-file binds.
func Overlaps(a, b string) (bool, error) {
	if yes, err := Contains(a, b); yes || err != nil {
		return yes, err
	}
	return Contains(b, a)
}
