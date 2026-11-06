// Package rooted implements the fixed-root Guest helper protocol.
package rooted

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/state"
)

// Version is the fixed-root wire protocol version.
const Version = 1

// Capability identifies the required managed containment contract.
const Capability = "managed-rooted-v1"

// HelperPath is prepared explicitly by the operator, never installed by managed dispatch.
const HelperPath = "/usr/local/bin/reach-helper"

// MaxFrame bounds one content frame.
const MaxFrame = 64 << 20

// MaxHeader bounds metadata before allocation.
const MaxHeader = 64 << 10

// MaxEntries bounds traversal and result counts.
const MaxEntries = 10000

// Request contains operation parameters; the root is fixed at process start.
type Request struct {
	ID        uint32               `json:"id"`
	Op        string               `json:"op"`
	Path      string               `json:"path,omitempty"`
	From      string               `json:"from,omitempty"`
	To        string               `json:"to,omitempty"`
	Offset    int64                `json:"offset,omitempty"`
	Limit     int64                `json:"limit,omitempty"`
	Mode      int                  `json:"mode,omitempty"`
	Recursive bool                 `json:"recursive,omitempty"`
	Pattern   string               `json:"pattern,omitempty"`
	Search    *reach.SearchRequest `json:"search,omitempty"`
}

func readFrame(in io.Reader) (Request, []byte, error) {
	var r Request
	var n uint32
	if e := binary.Read(in, binary.BigEndian, &n); e != nil {
		return r, nil, e
	}
	if n > MaxHeader {
		return r, nil, fmt.Errorf("request header exceeds limit")
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(in, b); e != nil {
		return r, nil, e
	}
	if !utf8.Valid(b) {
		return r, nil, fmt.Errorf("invalid request UTF-8")
	}
	if e := state.Decode(b, &r); e != nil {
		return r, nil, e
	}
	if e := binary.Read(in, binary.BigEndian, &n); e != nil {
		return r, nil, e
	}
	if n > MaxFrame {
		return r, nil, fmt.Errorf("request payload exceeds limit")
	}
	b = make([]byte, n)
	_, e := io.ReadFull(in, b)
	return r, b, e
}
func writeFrame(out io.Writer, h map[string]any, p []byte) error {
	b, e := json.Marshal(h)
	if e != nil {
		return e
	}
	if len(b) > MaxFrame || len(p) > MaxFrame {
		return fmt.Errorf("response exceeds frame limit")
	}
	if e = binary.Write(out, binary.BigEndian, uint32(len(b))); e != nil {
		return e
	}
	if _, e = out.Write(b); e != nil {
		return e
	}
	if e = binary.Write(out, binary.BigEndian, uint32(len(p))); e != nil {
		return e
	}
	_, e = out.Write(p)
	return e
}

// Serve serves one immutable root binding and requires a handshake before operations.
func Serve(root, digest string, stdin io.Reader, stdout io.Writer) error {
	f, e := Open(root)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	in := bufio.NewReaderSize(stdin, 64<<10)
	out := bufio.NewWriterSize(stdout, 64<<10)
	ready := false
	for {
		r, p, e := readFrame(in)
		if e != nil {
			return e
		}
		var h map[string]any
		var b []byte
		if !ready && r.Op != "ping" {
			e = fmt.Errorf("unsupported: rooted handshake required")
		} else {
			h, b, e = f.Dispatch(r, p)
		}
		if h == nil {
			h = map[string]any{}
		}
		if e != nil {
			kind := "error"
			if errors.Is(e, os.ErrNotExist) {
				kind = "notfound"
			}
			h = map[string]any{"kind": kind, "error": e.Error()}
			b = nil
		}
		h["id"] = r.ID
		h["ok"] = e == nil
		h["capability"] = Capability
		h["protocol"] = Version
		h["root"] = root
		h["root_identity"] = f.Identity
		h["record_digest"] = digest
		h["version"] = reach.Version
		if r.Op == "ping" && e == nil {
			ready = true
		}
		if e = writeFrame(out, h, b); e != nil {
			return e
		}
		if e = out.Flush(); e != nil {
			return e
		}
	}
}
