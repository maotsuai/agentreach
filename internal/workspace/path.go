// Package workspace authorises managed protocol paths. It never accesses Guest files.
package workspace

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// MaxPath bounds a path before and after URI decoding.
const MaxPath = 16 << 10

// Namespace is selected by the trusted entrypoint, never by a tool request.
type Namespace string

// Path namespaces distinguish Guest paths from approved Host protocol aliases.
const (
	Guest    Namespace = "guest"
	Protocol Namespace = "protocol"
	FileURI  Namespace = "file-uri"
)

// PathRequest keeps both endpoints and optional cwd in one authorisation step.
type PathRequest struct {
	Operation  string
	Namespace  Namespace
	Path       string
	SecondPath string
	Cwd        string
}

// Result carries only approved root-relative and display paths.
type Result struct{ Relative, Guest, SecondRelative, SecondGuest, CwdRelative, CwdGuest string }

// Mapper contains the immutable Guest root and explicitly approved Host aliases.
type Mapper struct {
	Root        string
	HostAliases []string
}

// ValidateProtocolAliases rejects textually ambiguous Host/Guest namespaces.
// Equal roots are unambiguous; strict component containment in either direction
// is not. This validates trusted configuration, never a request-supplied flag.
func (m Mapper) ValidateProtocolAliases() error {
	if e := valid(m.Root); e != nil {
		return e
	}
	if m.Root == "/" || !path.IsAbs(m.Root) || path.Clean(m.Root) != m.Root {
		return fmt.Errorf("path_denied: invalid workspace root")
	}
	for _, alias := range m.HostAliases {
		if alias == "" {
			continue
		}
		if e := valid(alias); e != nil {
			return e
		}
		if !path.IsAbs(alias) || path.Clean(alias) != alias {
			return fmt.Errorf("path_denied: invalid trusted Host path alias")
		}
		if alias != m.Root && (alias == "/" || strings.HasPrefix(m.Root, alias+"/") || strings.HasPrefix(alias, m.Root+"/")) {
			return fmt.Errorf("path_denied: trusted Host alias %q and Guest root %q strictly contain one another", alias, m.Root)
		}
	}
	return nil
}

func valid(p string) error {
	if len(p) > MaxPath || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return fmt.Errorf("path_denied: invalid path encoding or length")
	}
	return nil
}

// DecodeURI decodes exactly once. Percent signs in the resulting filename are literal.
func DecodeURI(raw string) (string, error) {
	if err := valid(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.Path == "" || !strings.HasPrefix(u.Path, "/") {
		return "", fmt.Errorf("path_denied: expected a local absolute file URI without authority, query or fragment")
	}
	if err := valid(u.Path); err != nil {
		return "", err
	}
	return u.Path, nil
}
func within(root, p string) bool { return p == root || strings.HasPrefix(p, root+"/") }
func (m Mapper) one(ns Namespace, p string) (string, string, error) {
	if m.Root == "/" || !strings.HasPrefix(m.Root, "/") || path.Clean(m.Root) != m.Root {
		return "", "", fmt.Errorf("path_denied: invalid workspace root")
	}
	if ns == FileURI {
		var err error
		p, err = DecodeURI(p)
		if err != nil {
			return "", "", err
		}
		ns = Protocol
	}
	if ns != Guest && ns != Protocol {
		return "", "", fmt.Errorf("path_denied: unknown path namespace")
	}
	if err := valid(p); err != nil {
		return "", "", err
	}
	if p == "" {
		return "", "", fmt.Errorf("path_denied: empty path")
	}
	candidate := p
	if strings.HasPrefix(p, "/") {
		if ns == Protocol {
			// Match the uncleaned spelling: /approved/../outside must not acquire approval.
			best := ""
			for _, a := range m.HostAliases {
				if a != "/" && a != "" && within(a, p) && len(a) > len(best) {
					best = a
				}
			}
			if best != "" {
				candidate = m.Root + strings.TrimPrefix(p, best)
			}
		}
	} else {
		candidate = m.Root + "/" + p
	}
	clean := path.Clean(candidate)
	if !within(m.Root, clean) {
		return "", "", fmt.Errorf("path_denied: path is outside workspace")
	}
	// Reject escape-and-return spellings too; every .. must stay under the chosen root.
	rel := strings.TrimPrefix(candidate, m.Root)
	if candidate != m.Root && !strings.HasPrefix(rel, "/") {
		return "", "", fmt.Errorf("path_denied: path is outside workspace")
	}
	depth := 0
	for _, c := range strings.Split(rel, "/") {
		switch c {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return "", "", fmt.Errorf("path_denied: path traverses outside workspace")
			}
		default:
			depth++
		}
	}
	r := strings.TrimPrefix(clean, m.Root)
	r = strings.TrimPrefix(r, "/")
	if r == "" {
		r = "."
	}
	return r, clean, nil
}

// Map authorises all paths before returning any usable result.
func (m Mapper) Map(req PathRequest) (Result, error) {
	var r Result
	var err error
	if req.Namespace == Protocol || req.Namespace == FileURI {
		if e := m.ValidateProtocolAliases(); e != nil {
			return r, e
		}
	}
	if req.Operation == "cwd" && strings.ContainsAny(req.Path, "\r\n") {
		return r, fmt.Errorf("path_denied: managed cwd cannot contain CR or LF")
	}
	if req.Operation == "" {
		return r, fmt.Errorf("path_denied: missing operation")
	}
	r.Relative, r.Guest, err = m.one(req.Namespace, req.Path)
	if err != nil {
		return r, err
	}
	if req.SecondPath != "" {
		r.SecondRelative, r.SecondGuest, err = m.one(req.Namespace, req.SecondPath)
		if err != nil {
			return Result{}, err
		}
	}
	if req.Cwd != "" {
		r.CwdRelative, r.CwdGuest, err = m.one(req.Namespace, req.Cwd)
		if err != nil {
			return Result{}, err
		}
	}
	if req.Operation == "cwd" && strings.ContainsAny(r.Guest, "\r\n") || strings.ContainsAny(r.CwdGuest, "\r\n") {
		return Result{}, fmt.Errorf("path_denied: managed cwd cannot contain CR or LF")
	}
	if (req.Operation == "remove" || req.Operation == "rename") && r.Relative == "." {
		return Result{}, fmt.Errorf("path_denied: workspace root cannot be removed or renamed")
	}
	if req.Operation == "rename" && r.SecondRelative == "." {
		return Result{}, fmt.Errorf("path_denied: workspace root cannot be replaced")
	}
	return r, nil
}
