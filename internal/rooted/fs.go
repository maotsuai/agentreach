package rooted

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/workspace"
)

// FS holds one fixed directory descriptor for the complete helper lifetime.
type FS struct {
	root           *os.Root
	Name, Identity string
}

// Open acquires the approved directory instance and its filesystem identity.
func Open(name string) (*FS, error) {
	if len(name) > workspace.MaxPath || !strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "/" {
		return nil, fmt.Errorf("path_denied: invalid root")
	}
	r, e := os.OpenRoot(name)
	if e != nil {
		return nil, e
	}
	f, e := r.Open(".")
	if e != nil {
		_ = r.Close()
		return nil, e
	}
	defer func() { _ = f.Close() }()
	i, e := f.Stat()
	if e != nil {
		_ = r.Close()
		return nil, e
	}
	id, e := identity(i)
	if e != nil {
		_ = r.Close()
		return nil, e
	}
	return &FS{root: r, Name: name, Identity: id}, nil
}

// Close releases the root descriptor.
func (f *FS) Close() error { return f.root.Close() }
func relative(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path_denied: helper requires a root-relative path")
	}
	m := workspace.Mapper{Root: "/workspace"}
	r, e := m.Map(workspace.PathRequest{Operation: "helper", Namespace: workspace.Guest, Path: p})
	return r.Relative, e
}
func (f *FS) guest(p string) string { return path.Join(f.Name, p) }
func contentFile(i fs.FileInfo) error {
	if !i.Mode().IsRegular() {
		return fmt.Errorf("path_denied: content access requires a regular file")
	}
	return singleLink(i)
}
func (f *FS) read(p string, off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || n > MaxFrame {
		return nil, fmt.Errorf("invalid read bounds")
	}
	h, e := openRead(f.root, p)
	if e != nil {
		return nil, e
	}
	defer func() { _ = h.Close() }()
	i, e := h.Stat()
	if e != nil {
		return nil, e
	}
	if e = contentFile(i); e != nil {
		return nil, e
	}
	if _, e = h.Seek(off, io.SeekStart); e != nil {
		return nil, e
	}
	if n == 0 {
		n = MaxFrame + 1
	}
	b, e := io.ReadAll(io.LimitReader(h, n))
	if len(b) > MaxFrame {
		return nil, fmt.Errorf("read content limit exceeded")
	}
	return b, e
}
func (f *FS) write(p string, b []byte, mode fs.FileMode) error {
	if p == "." {
		return fmt.Errorf("path_denied: cannot replace workspace root")
	}
	if i, e := f.root.Lstat(p); e == nil {
		if i.Mode()&os.ModeSymlink != 0 {
			i, e = f.root.Stat(p)
			if e != nil {
				return e
			}
		}
		if e = contentFile(i); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	parent, e := f.root.OpenRoot(path.Dir(p))
	if e != nil {
		return e
	}
	defer func() { _ = parent.Close() }()
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	tmp := ".reach.tmp." + hex.EncodeToString(nonce)
	if mode == 0 {
		mode = 0644
	}
	h, e := parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if e != nil {
		return e
	}
	defer func() { _ = parent.Remove(tmp) }()
	_, e = h.Write(b)
	if e == nil {
		e = h.Chmod(mode.Perm())
	}
	if e == nil {
		e = h.Sync()
	}
	ce := h.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = parent.Rename(tmp, path.Base(p))
	}
	return e
}
func (f *FS) info(p string) (map[string]any, error) {
	i, e := f.root.Lstat(p)
	if e != nil {
		return nil, e
	}
	h := map[string]any{"name": path.Base(p), "path": f.guest(p), "size": i.Size(), "mode": uint32(i.Mode().Perm()), "mtime": i.ModTime().Unix(), "is_dir": i.IsDir(), "is_link": i.Mode()&os.ModeSymlink != 0}
	if i.Mode()&os.ModeSymlink != 0 {
		target, statErr := f.root.Stat(p)
		if statErr != nil {
			return nil, statErr
		}
		h["is_dir"] = target.IsDir()
		s, e := f.root.Readlink(p)
		if e != nil {
			return nil, e
		}
		h["link_target"] = s
	}
	return h, nil
}
func (f *FS) list(p string) ([]map[string]any, error) {
	h, e := openRead(f.root, p)
	if e != nil {
		return nil, e
	}
	defer func() { _ = h.Close() }()
	entries, e := h.ReadDir(MaxEntries + 1)
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, e
	}
	if len(entries) > MaxEntries {
		return nil, fmt.Errorf("directory entry limit exceeded")
	}
	out := make([]map[string]any, 0, len(entries))
	resultBytes := 0
	for _, i := range entries {
		m, e := f.info(path.Join(p, i.Name()))
		if e != nil {
			// Listing a directory may expose the name of a denied symlink, never its target.
			li, le := f.root.Lstat(path.Join(p, i.Name()))
			if le != nil {
				return nil, le
			}
			if li.Mode()&os.ModeSymlink == 0 {
				return nil, e
			}
			m = map[string]any{"name": i.Name(), "path": f.guest(path.Join(p, i.Name())), "is_link": true}
		}
		encoded, e := json.Marshal(m)
		if e != nil {
			return nil, e
		}
		resultBytes += len(encoded) + 1
		if resultBytes > MaxFrame-MaxHeader {
			return nil, fmt.Errorf("directory result byte limit exceeded")
		}
		out = append(out, m)
	}
	return out, nil
}
func (f *FS) canonical(p string) (string, error) {
	pending := strings.Split(p, "/")
	parts := []string{}
	links := 0
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		if c == "" || c == "." {
			continue
		}
		if c == ".." {
			if len(parts) == 0 {
				return "", fmt.Errorf("path_denied: symlink escapes root")
			}
			parts = parts[:len(parts)-1]
			continue
		}
		q := path.Join(append(append([]string{}, parts...), c)...)
		i, e := f.root.Lstat(q)
		if e != nil {
			return "", e
		}
		if i.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", fmt.Errorf("path_denied: too many symlinks")
			}
			s, e := f.root.Readlink(q)
			if e != nil {
				return "", e
			}
			if strings.HasPrefix(s, "/") {
				return "", fmt.Errorf("path_denied: absolute symlink")
			}
			pending = append(strings.Split(s, "/"), pending...)
			continue
		}
		parts = append(parts, c)
	}
	q := path.Join(parts...)
	if q == "" {
		q = "."
	}
	if _, e := f.root.Stat(q); e != nil {
		return "", e
	}
	return f.guest(q), nil
}

// walk visits only descriptor-rooted names and never follows directory symlinks.
func (f *FS) walk(p string, visit func(string, fs.FileInfo) error) error {
	count := 0
	var walk func(string, int) error
	walk = func(q string, depth int) error {
		if depth > 128 {
			return fmt.Errorf("walk depth limit exceeded")
		}
		if len(f.guest(q)) > workspace.MaxPath {
			return fmt.Errorf("walk path length limit exceeded")
		}
		count++
		if count > MaxEntries {
			return fmt.Errorf("walk entry limit exceeded")
		}
		i, e := f.root.Lstat(q)
		if e != nil {
			return e
		}
		if depth == 0 && i.Mode()&os.ModeSymlink != 0 {
			i, e = f.root.Stat(q)
			if e != nil {
				return e
			}
		}
		if e = visit(q, i); e != nil {
			return e
		}
		if !i.IsDir() {
			return nil
		}
		h, e := openRead(f.root, q)
		if e != nil {
			return e
		}
		list, e := h.ReadDir(MaxEntries + 1)
		_ = h.Close()
		if e != nil && !errors.Is(e, io.EOF) {
			return e
		}
		if len(list) > MaxEntries {
			return fmt.Errorf("walk entry limit exceeded")
		}
		for _, entry := range list {
			if e = walk(path.Join(q, entry.Name()), depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	return walk(p, 0)
}
func (f *FS) search(p string, r reach.SearchRequest) ([]reach.Match, error) {
	if len(r.Pattern) > 16<<10 || len(r.Glob) > 4096 {
		return nil, fmt.Errorf("search expression limit exceeded")
	}
	pat := r.Pattern
	if r.Literal {
		pat = regexp.QuoteMeta(pat)
	}
	if r.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, e := regexp.Compile(pat)
	if e != nil {
		return nil, e
	}
	if r.Glob != "" {
		if _, e = path.Match(r.Glob, ""); e != nil {
			return nil, e
		}
	}
	limit := r.MaxResults
	if limit <= 0 {
		limit = 1000
	}
	if limit > MaxEntries {
		return nil, fmt.Errorf("search result limit exceeded")
	}
	out := []reach.Match{}
	total := int64(0)
	resultBytes := 0
	e = f.walk(p, func(q string, i fs.FileInfo) error {
		if !i.Mode().IsRegular() {
			return nil
		}
		if len(out) >= limit {
			return nil
		}
		if r.Glob != "" {
			match, _ := path.Match(r.Glob, path.Base(q))
			if !match {
				return nil
			}
		}
		h, e := openRead(f.root, q)
		if e != nil {
			return e
		}
		defer func() { _ = h.Close() }()
		i, e = h.Stat()
		if e != nil {
			return e
		}
		if e = contentFile(i); e != nil {
			return e
		}
		remaining := int64(MaxFrame) - total
		limited := &io.LimitedReader{R: h, N: remaining + 1}
		scan := bufio.NewScanner(limited)
		scan.Buffer(make([]byte, 4096), 1<<20)
		line := 0
		for scan.Scan() {
			line++
			if re.Match(scan.Bytes()) {
				match := reach.Match{Path: f.guest(q), Line: line, Text: scan.Text()}
				encoded, e := json.Marshal(match)
				if e != nil {
					return e
				}
				resultBytes += len(encoded) + 1
				if resultBytes > MaxFrame-MaxHeader {
					return fmt.Errorf("search result byte limit exceeded")
				}
				out = append(out, match)
				if len(out) >= limit {
					break
				}
			}
		}
		total += remaining + 1 - limited.N
		if total > MaxFrame {
			return fmt.Errorf("search byte limit exceeded")
		}
		return scan.Err()
	})
	return out, e
}
func (f *FS) copy(src, dst string, recursive bool) error {
	if i, e := f.root.Lstat(src); e != nil {
		return e
	} else if i.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsupported: copy of symlinks")
	}
	if src == "." || dst == "." || src == dst || strings.HasPrefix(dst, src+"/") {
		return fmt.Errorf("path_denied: invalid copy endpoints")
	}
	type item struct {
		src, dst string
		mode     fs.FileMode
		dir      bool
	}
	items := []item{}
	if i, e := f.root.Stat(dst); e == nil && i.IsDir() {
		dst = path.Join(dst, path.Base(src))
	}
	if strings.HasPrefix(dst, src+"/") {
		return fmt.Errorf("path_denied: recursive copy into source")
	}
	// Authorize every source and destination before creating any directory/file.
	e := f.walk(src, func(q string, i fs.FileInfo) error {
		if i.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path_denied: copy of symlinks is unsupported")
		}
		if i.IsDir() && !recursive {
			return fmt.Errorf("copy requires recursive for directory")
		}
		if !i.IsDir() {
			if e := contentFile(i); e != nil {
				return e
			}
		}
		d := dst + strings.TrimPrefix(q, src)
		if _, e := relative(d); e != nil {
			return e
		}
		if old, e := f.root.Lstat(d); e == nil {
			if old.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("path_denied: copy destination symlink")
			}
			if !old.IsDir() {
				if e = contentFile(old); e != nil {
					return e
				}
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		items = append(items, item{q, d, i.Mode().Perm(), i.IsDir()})
		return nil
	})
	if e != nil {
		return e
	}
	for _, i := range items {
		if i.dir {
			e = f.root.MkdirAll(i.dst, i.mode)
		} else {
			var b []byte
			b, e = f.read(i.src, 0, 0)
			if e == nil {
				e = f.write(i.dst, b, i.mode)
			}
		}
		if e != nil {
			return e
		}
	}
	return nil
}

// Dispatch accepts only root-relative operation paths, never a replacement root.
func (f *FS) Dispatch(r Request, payload []byte) (map[string]any, []byte, error) {
	if r.Op == "ping" {
		return nil, nil, nil
	}
	p, e := relative(r.Path)
	if r.Op == "rename" || r.Op == "copy" {
		p, e = relative(r.From)
	}
	if e != nil {
		return nil, nil, e
	}
	var to string
	if r.Op == "rename" || r.Op == "copy" {
		to, e = relative(r.To)
		if e != nil {
			return nil, nil, e
		}
	}
	switch r.Op {
	case "read":
		b, e := f.read(p, r.Offset, r.Limit)
		return nil, b, e
	case "write":
		return nil, nil, f.write(p, payload, fs.FileMode(r.Mode))
	case "stat":
		i, e := f.info(p)
		return map[string]any{"info": i}, nil, e
	case "list":
		l, e := f.list(p)
		return map[string]any{"entries": l}, nil, e
	case "mkdir":
		m := fs.FileMode(r.Mode).Perm()
		if m == 0 {
			m = 0755
		}
		return nil, nil, f.root.MkdirAll(p, m)
	case "remove":
		if p == "." {
			return nil, nil, fmt.Errorf("path_denied: cannot remove root")
		}
		if r.Recursive {
			return nil, nil, f.root.RemoveAll(p)
		}
		return nil, nil, f.root.Remove(p)
	case "rename":
		if p == "." || to == "." {
			return nil, nil, fmt.Errorf("path_denied: cannot rename root")
		}
		return nil, nil, f.root.Rename(p, to)
	case "copy":
		return nil, nil, f.copy(p, to, r.Recursive)
	case "canonicalize":
		s, e := f.canonical(p)
		return map[string]any{"path": s}, nil, e
	case "hash":
		h, e := openRead(f.root, p)
		if e != nil {
			return nil, nil, e
		}
		defer func() { _ = h.Close() }()
		i, e := h.Stat()
		if e != nil {
			return nil, nil, e
		}
		if e = contentFile(i); e != nil {
			return nil, nil, e
		}
		sum := sha256.New()
		if n, copyErr := io.Copy(sum, io.LimitReader(h, MaxFrame+1)); copyErr != nil {
			return nil, nil, copyErr
		} else if n > MaxFrame {
			return nil, nil, fmt.Errorf("hash byte limit exceeded")
		}
		return map[string]any{"digest": hex.EncodeToString(sum.Sum(nil))}, nil, nil
	case "search":
		if r.Search == nil {
			return nil, nil, fmt.Errorf("missing search")
		}
		l, e := f.search(p, *r.Search)
		return map[string]any{"matches": l}, nil, e
	case "glob":
		if len(r.Pattern) > 4096 {
			return nil, nil, fmt.Errorf("glob expression limit exceeded")
		}
		if _, e = path.Match(r.Pattern, ""); e != nil {
			return nil, nil, e
		}
		l := []string{}
		resultBytes := 0
		e = f.walk(p, func(q string, i fs.FileInfo) error {
			if !i.Mode().IsRegular() {
				return nil
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(q, p), "/")
			ok, _ := path.Match(r.Pattern, rel)
			if !strings.Contains(r.Pattern, "/") {
				ok, _ = path.Match(r.Pattern, path.Base(q))
			}
			if ok {
				s := f.guest(q)
				encoded, e := json.Marshal(s)
				if e != nil {
					return e
				}
				resultBytes += len(encoded) + 1
				if resultBytes > MaxFrame-MaxHeader {
					return fmt.Errorf("glob result byte limit exceeded")
				}
				l = append(l, s)
			}
			return nil
		})
		return map[string]any{"paths": l}, nil, e
	}
	return nil, nil, fmt.Errorf("unsupported: unknown rooted operation")
}
