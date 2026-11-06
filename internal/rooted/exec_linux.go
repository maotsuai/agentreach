//go:build linux

package rooted

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/bojieli/agentreach/internal/state"
	"github.com/bojieli/agentreach/internal/transport"
)

// Exec is one-shot: only this helper process changes cwd, then exec replaces it.
func Exec(root, cwd, encoded string) error {
	if len(encoded) > 4<<20 {
		return fmt.Errorf("exec request exceeds limit")
	}
	b, e := base64.RawURLEncoding.DecodeString(encoded)
	if e != nil {
		return e
	}
	var req ExecRequest
	if e = state.Decode(b, &req); e != nil {
		return e
	}
	p, e := relative(cwd)
	if e != nil {
		return e
	}
	r, e := Open(root)
	if e != nil {
		return e
	}
	defer func() { _ = r.Close() }()
	if req.RootIdentity != "" && r.Identity != req.RootIdentity {
		return fmt.Errorf("identity_changed: launcher workspace root was replaced")
	}
	d, e := openRead(r.root, p)
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	i, e := d.Stat()
	if e != nil {
		return e
	}
	if !i.IsDir() {
		return fmt.Errorf("path_denied: cwd is not a directory")
	}
	if e = d.Chdir(); e != nil {
		return e
	}
	// Env exports and body are interpreted only after descriptor-anchored chdir succeeds.
	command := transport.BuildCommand(structRequest(req))
	return syscall.Exec("/bin/sh", []string{"sh", "-c", command}, os.Environ())
}

// ReportCwd reports the physical cwd without line loss or trusting shell PWD.
func ReportCwd(marker string, out io.Writer) error {
	if marker == "" || len(marker) > 256 || strings.ContainsAny(marker, "\r\n\x00") {
		return fmt.Errorf("invalid cwd report marker")
	}
	cwd, e := syscall.Getwd()
	if e != nil {
		return e
	}
	_, e = fmt.Fprintln(out, marker+base64.StdEncoding.EncodeToString([]byte(cwd)))
	return e
}
