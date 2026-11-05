// Package state provides strict, atomic control-plane records. Reads never
// create directories and a corrupt record is never treated as an empty one.
package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Decode reads one unambiguous document and rejects unknown fields.
func Decode(data []byte, value any) error {
	// Reject duplicate keys as well as unknown ones. Otherwise two readers can
	// disagree about which authorization field a JSON object means.
	if err := CheckKeys(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON document")
	}
	return nil
}

// CheckKeys rejects ambiguous JSON without imposing a document's field schema.
func CheckKeys(data []byte) error {
	return uniqueKeys(json.NewDecoder(bytes.NewReader(data)))
}

func uniqueKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			k, ok := key.(string)
			// encoding/json matches struct fields case-insensitively. Treat its
			// aliases as duplicates so Managed:null cannot shadow managed:{}.
			folded := strings.ToLower(k)
			if !ok || seen[folded] {
				return fmt.Errorf("duplicate/invalid JSON key %v", key)
			}
			seen[folded] = true
		}
		if err := uniqueKeys(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

// Read decodes a regular control file without creating its parent directory.
func Read(path string, value any) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("state_corrupt: %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := Decode(data, value); err != nil {
		return fmt.Errorf("state_corrupt: %s: %w", path, err)
	}
	return nil
}

// Write replaces a record with a unique temporary file in the same directory.
// The caller owns directory creation and any required cross-process lock.
func Write(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".reach-state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

// Lock uses atomic directory creation, including on filesystems without flock.
// A stale lock is reported; it is never guessed to be safe to steal.
func Lock(ctx context.Context, path string) (func(), error) {
	for {
		if err := os.Mkdir(path, 0o700); err == nil {
			return func() { _ = os.Remove(path) }, nil
		} else if !os.IsExist(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("state_locked: %s: %w", path, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
